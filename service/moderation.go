package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// Prompt moderation gate. Screens a generation prompt before dispatch across
// text + image + video surfaces. Vendor-neutral and PLUGGABLE: an ordered chain
// of providers (setting.ModerationProviders) is tried in priority order. The
// first provider that returns a clean decision (allow OR deny) wins; a provider
// that fails operationally (rate-limit, timeout, bad response) falls through to
// the next. If every provider fails operationally, the fail-open/closed policy
// (setting.ModerationFailOpen) decides.
//
// All providers converge on the same deny semantics: ModerationDenyError ->
// ErrPromptDenied, so the gate, the 400 response, and the user-visible usage-log
// row are identical regardless of which provider produced the decision.

const moderationTimeout = 5 * time.Second

var (
	ErrPromptDenied      = errors.New("moderation.prompt_rejected")
	ErrModerationFailure = errors.New("moderation.unavailable")
)

// ModerationDenyError carries the category (and, for score-based providers, the
// score/threshold) that triggered a denial so callers can surface a specific
// reason in the user-facing log. Satisfies errors.Is(err, ErrPromptDenied).
type ModerationDenyError struct {
	Category  string
	Score     float64
	Threshold float64
}

func (e *ModerationDenyError) Error() string { return ErrPromptDenied.Error() }
func (e *ModerationDenyError) Is(target error) bool {
	return target == ErrPromptDenied
}

// ModerationDenyReason returns a human-readable reason for a denial so the user
// can adjust the prompt, or "" if the error is not a moderation denial. Handles
// both score-based providers (category + numeric score + threshold) and
// decision-based providers (a bare decision word, no numeric score).
func ModerationDenyReason(err error) string {
	var denyErr *ModerationDenyError
	if !errors.As(err, &denyErr) {
		return ""
	}
	if denyErr.Score > 0 || denyErr.Threshold > 0 {
		return fmt.Sprintf(
			"Inappropriate prompt: blocked by content moderation for \"%s\" (scored %.2f, blocks at %.2f). Reword that part of the prompt and retry.",
			denyErr.Category, denyErr.Score, denyErr.Threshold,
		)
	}
	// Decision-based providers return no category, so there is nothing specific to name.
	// The bare decision word ("deny"/"flag") meant nothing to users and read like a
	// provider fault, so it is not surfaced.
	return "Inappropriate prompt: blocked by content moderation. Reword the prompt and retry."
}

// moderationOutcome is a provider's verdict. A non-nil error from screen() means
// an OPERATIONAL failure (fall through to the next provider). When the error is
// nil: denied==true means blocked (Category/Score/Threshold populated for the
// user-facing reason); denied==false means allowed.
type moderationOutcome struct {
	denied    bool
	category  string
	score     float64
	threshold float64
}

type moderationProvider interface {
	name() string
	enabled() bool
	screen(ctx context.Context, prompt string) (moderationOutcome, error)
}

// Moderation surfaces. The provider chain is chosen per surface so text and
// image/video can route to different backends (e.g. Creem's prompt-moderation API
// is built for image/video, not chat text).
const (
	ModerationSurfaceText  = "text"
	ModerationSurfaceImage = "image"
	ModerationSurfaceVideo = "video"
)

// ModerationExempt reports whether the request's user carries the admin-granted
// exemption. Mirrors the UnlimitedFreeModels grant: the ONLY bypass is the explicit
// per-user flag, so every exemption stays visible in the user's setting and
// revocable, rather than being inferred from role or balance.
func ModerationExempt(c *gin.Context) bool {
	if c == nil {
		return false
	}
	s, ok := common.GetContextKeyType[types.UserSetting](c, constant.ContextKeyUserSetting)
	return ok && s.ModerationExempt
}

// AssertPromptAllowed screens a generation prompt through the provider chain
// configured for the given surface ("text", "image", or "video"). No-op when the
// prompt is empty. Returns ErrPromptDenied (a *ModerationDenyError) when a provider
// blocks, and applies the fail-open/closed policy only when every enabled provider
// fails operationally. Callers gate this behind their own per-surface enable flag.
func AssertPromptAllowed(ctx context.Context, surface, prompt string) error {
	if prompt == "" {
		return nil
	}

	var enabled []moderationProvider
	for _, p := range orderedModerationProviders(surface) {
		if p.enabled() {
			enabled = append(enabled, p)
		}
	}
	if len(enabled) == 0 {
		return moderationOperationalFailure(fmt.Sprintf("no moderation provider configured for %s", surface))
	}

	prompt = truncateModerationInput(prompt)

	var lastOpErr error
	for _, p := range enabled {
		outcome, err := p.screen(ctx, prompt)
		if err != nil {
			common.SysError(fmt.Sprintf("moderation provider %s failed (%s): %s; trying next", p.name(), surface, err.Error()))
			lastOpErr = err
			continue
		}
		if outcome.denied {
			common.SysLog(fmt.Sprintf("moderation denied by %s (%s): category=%s score=%.3f threshold=%.3f", p.name(), surface, outcome.category, outcome.score, outcome.threshold))
			return &ModerationDenyError{Category: outcome.category, Score: outcome.score, Threshold: outcome.threshold}
		}
		return nil
	}

	return moderationOperationalFailure(fmt.Sprintf("all %s moderation providers failed (last: %v)", surface, lastOpErr))
}

// orderedModerationProviders maps the surface's configured provider list (a
// comma-separated priority list) to provider instances, ignoring unknown tokens
// and de-duping. Text uses setting.ModerationProvidersText (OpenAI only by
// default); image/video use setting.ModerationProvidersMedia (OpenAI then Creem).
func orderedModerationProviders(surface string) []moderationProvider {
	var spec, fallback string
	if surface == ModerationSurfaceText {
		spec, fallback = setting.ModerationProvidersText, "openai"
	} else {
		spec, fallback = setting.ModerationProvidersMedia, "openai,creem"
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = fallback
	}
	seen := map[string]bool{}
	result := make([]moderationProvider, 0, 2)
	for _, token := range strings.Split(spec, ",") {
		name := strings.ToLower(strings.TrimSpace(token))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "openai":
			result = append(result, openAIModerationProvider{})
		case "creem":
			result = append(result, creemModerationProvider{})
		}
	}
	return result
}

// moderationOperationalFailure logs an all-providers-down failure and returns nil
// (allow) when ModerationFailOpen is set, or ErrModerationFailure (block)
// otherwise. Operational failures are distinct from content denials, which always
// block regardless of this setting.
func moderationOperationalFailure(detail string) error {
	if setting.ModerationFailOpen {
		common.SysError(fmt.Sprintf("%s; failing open (allowing request)", detail))
		return nil
	}
	common.SysError(fmt.Sprintf("%s; failing closed (blocking request)", detail))
	return ErrModerationFailure
}

// truncateModerationInput keeps the TAIL of the prompt up to
// ModerationMaxInputChars (the newest turn is what needs screening). Rune-safe so
// multibyte characters are never split. 0 or negative disables truncation.
func truncateModerationInput(prompt string) string {
	maxChars := setting.ModerationMaxInputChars
	if maxChars <= 0 {
		return prompt
	}
	runes := []rune(prompt)
	if len(runes) <= maxChars {
		return prompt
	}
	return string(runes[len(runes)-maxChars:])
}

// ---- OpenAI omni-moderation provider ----

type openAIModerationProvider struct{}

func (openAIModerationProvider) name() string  { return "openai" }
func (openAIModerationProvider) enabled() bool { return setting.ModerationApiKey != "" }

type moderationRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type moderationResponse struct {
	Results []struct {
		Flagged        bool               `json:"flagged"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
}

func (openAIModerationProvider) screen(ctx context.Context, prompt string) (moderationOutcome, error) {
	apiKey := setting.ModerationApiKey

	body, err := common.Marshal(moderationRequest{Model: setting.ModerationModel, Input: prompt})
	if err != nil {
		return moderationOutcome{}, fmt.Errorf("marshal failed: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, moderationTimeout)
	defer cancel()

	url := strings.TrimRight(setting.ModerationBaseUrl, "/") + "/v1/moderations"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return moderationOutcome{}, fmt.Errorf("request build failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	// One quick retry on 429 (rate limit) to ride out short bursts before falling
	// through to the next provider.
	if err == nil && resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		select {
		case <-reqCtx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		retryReq, retryErr := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		if retryErr == nil {
			retryReq.Header.Set("Authorization", "Bearer "+apiKey)
			retryReq.Header.Set("Content-Type", "application/json")
			resp, err = http.DefaultClient.Do(retryReq)
		}
	}
	if err != nil {
		return moderationOutcome{}, fmt.Errorf("call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return moderationOutcome{}, fmt.Errorf("http %d", resp.StatusCode)
	}

	var parsed moderationResponse
	if err := common.DecodeJson(resp.Body, &parsed); err != nil {
		return moderationOutcome{}, fmt.Errorf("decode failed: %w", err)
	}
	if len(parsed.Results) == 0 {
		return moderationOutcome{}, errors.New("no results")
	}

	if category, score, threshold, blocked := evaluateModeration(parsed.Results[0].CategoryScores); blocked {
		return moderationOutcome{denied: true, category: category, score: score, threshold: threshold}, nil
	}
	return moderationOutcome{}, nil
}

// evaluateModeration applies per-category thresholds. Returns the first category
// whose score reaches its threshold (or the default when unlisted), along with the
// threshold that was applied.
func evaluateModeration(scores map[string]float64) (string, float64, float64, bool) {
	thresholds := moderationThresholds()
	for category, score := range scores {
		threshold, ok := thresholds[category]
		if !ok {
			threshold = setting.ModerationDefaultThreshold
		}
		if score >= threshold {
			return category, score, threshold, true
		}
	}
	return "", 0, 0, false
}

func moderationThresholds() map[string]float64 {
	thresholds := map[string]float64{}
	if setting.ModerationCategoryThresholds == "" {
		return thresholds
	}
	if err := common.UnmarshalJsonStr(setting.ModerationCategoryThresholds, &thresholds); err != nil {
		common.SysError(fmt.Sprintf("moderation thresholds parse failed: %s; using default for all categories", err.Error()))
		return map[string]float64{}
	}
	return thresholds
}

// ---- Creem moderation provider (fallback) ----

const creemModerationURL = "https://api.creem.io/v1/moderation/prompt"

type creemModerationProvider struct{}

func (creemModerationProvider) name() string  { return "creem" }
func (creemModerationProvider) enabled() bool { return setting.CreemApiKey != "" }

type creemModerationRequest struct {
	Prompt string `json:"prompt"`
}

type creemModerationResponse struct {
	Decision string `json:"decision"`
}

func (creemModerationProvider) screen(ctx context.Context, prompt string) (moderationOutcome, error) {
	body, err := common.Marshal(creemModerationRequest{Prompt: prompt})
	if err != nil {
		return moderationOutcome{}, fmt.Errorf("marshal failed: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, moderationTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, creemModerationURL, bytes.NewReader(body))
	if err != nil {
		return moderationOutcome{}, fmt.Errorf("request build failed: %w", err)
	}
	req.Header.Set("x-api-key", setting.CreemApiKey)
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return moderationOutcome{}, fmt.Errorf("call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return moderationOutcome{}, fmt.Errorf("http %d", resp.StatusCode)
	}

	var parsed creemModerationResponse
	if err := common.DecodeJson(resp.Body, &parsed); err != nil {
		return moderationOutcome{}, fmt.Errorf("decode failed: %w", err)
	}

	switch parsed.Decision {
	case "allow":
		return moderationOutcome{}, nil
	case "flag", "deny":
		// Raw decision word as the category; no score/threshold from Creem.
		return moderationOutcome{denied: true, category: parsed.Decision}, nil
	default:
		return moderationOutcome{}, fmt.Errorf("unexpected decision %q", parsed.Decision)
	}
}
