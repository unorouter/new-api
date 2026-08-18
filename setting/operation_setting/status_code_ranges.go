package operation_setting

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type StatusCodeRange struct {
	Start int
	End   int
}

var AutomaticDisableStatusCodeRanges = []StatusCodeRange{{Start: 401, End: 401}}

// Default behavior matches legacy hardcoded retry rules in controller/relay.go shouldRetry:
// retry for 1xx, 3xx, 4xx(except 400/408), 5xx(except 504/524), and no retry for 2xx.
var AutomaticRetryStatusCodeRanges = []StatusCodeRange{
	{Start: 100, End: 199},
	{Start: 300, End: 399},
	{Start: 401, End: 407},
	{Start: 409, End: 499},
	{Start: 500, End: 503},
	{Start: 505, End: 523},
	{Start: 525, End: 599},
}

var alwaysSkipRetryStatusCodes = map[int]struct{}{
	504: {},
	524: {},
}

// alwaysSkipRetryCodes lists upstream/internal error codes that are inherently
// terminal: retrying against another channel will produce the same outcome
// because the rejection is content/policy driven rather than a per-channel
// transient failure. Keys MUST be lowercase; IsAlwaysSkipRetryCode lowercases
// the lookup so upstream casing variations still match.
var alwaysSkipRetryCodes = map[types.ErrorCode]struct{}{
	types.ErrorCodeBadResponseBody: {},
	// Zhipu / GLM input moderation
	"data_inspection_failed": {},
	// OpenAI / Azure OpenAI content moderation
	"content_filter":               {},
	"content_policy_violation":     {},
	"responsibleaipolicyviolation": {},
	// Google Gemini safety stops
	"safety":     {},
	"recitation": {},
	// Google Gemini malformed-request rejection: deterministic, the same payload
	// fails on every channel, so do not retry across the pool or auto-ban channels.
	"invalid_argument": {},
	// Anthropic / generic policy refusals
	"policy_violation": {},
	// Request exceeds the model context window / payload too large.
	// Deterministic: the same oversized request fails on every channel.
	"tokens_limit_reached":    {},
	"context_length_exceeded": {},
	"context_too_long":        {},
	"request_too_large":       {},
	// Local/user quota exhaustion; never an upstream/channel fault. Only the
	// namespaced local codes belong here. The bare "insufficient_quota" is
	// intentionally omitted: OpenAI-format upstreams (e.g. W&B Inference) echo it
	// when THEIR account quota is drained, which is a per-channel fault that must
	// fail over to a sibling and auto-disable the dead channel.
	"insufficient_user_quota":  {},
	"local:insufficient_quota": {},
	// Local request-build failures (we could not convert/marshal the request for
	// this model, e.g. "model does not support image generation", AWS/volcengine
	// request encode failures). Deterministic: every channel of the model fails
	// identically, so do not retry the pool. Most emit ErrOptionWithSkipRetry()
	// per-site already; listing them here is the safety net for any that do not.
	"convert_request_failed": {},
	"bad_request_body":       {},
	// NOTE: "pre_consume_token_quota_failed" is intentionally NOT listed. The local
	// pre-consume failures (billing_session.go, pre_consume_quota.go) already attach
	// ErrOptionWithSkipRetry() per-error, so they skip retry via IsSkipRetryError
	// before this map is consulted. Listing the bare code here ALSO matched the
	// identical code emitted by an upstream reseller (e.g. cent) when OUR account
	// there is short of balance, which is a per-channel transient fault that MUST
	// fail over to a sibling channel. Keeping it out lets that case retry.
}

func AutomaticDisableStatusCodesToString() string {
	return statusCodeRangesToString(AutomaticDisableStatusCodeRanges)
}

func AutomaticDisableStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	AutomaticDisableStatusCodeRanges = ranges
	return nil
}

func ShouldDisableByStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(AutomaticDisableStatusCodeRanges, code)
}

func AutomaticRetryStatusCodesToString() string {
	return statusCodeRangesToString(AutomaticRetryStatusCodeRanges)
}

func AutomaticRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	AutomaticRetryStatusCodeRanges = ranges
	return nil
}

func IsAlwaysSkipRetryStatusCode(code int) bool {
	_, exists := alwaysSkipRetryStatusCodes[code]
	return exists
}

func IsAlwaysSkipRetryCode(errorCode types.ErrorCode) bool {
	if _, exists := alwaysSkipRetryCodes[errorCode]; exists {
		return true
	}
	// Upstream providers vary in casing (e.g. Azure "ResponsibleAIPolicyViolation",
	// Gemini "SAFETY"). Normalize to lowercase for a deterministic match.
	lower := types.ErrorCode(strings.ToLower(string(errorCode)))
	_, exists := alwaysSkipRetryCodes[lower]
	return exists
}

func ShouldRetryByStatusCode(code int) bool {
	if IsAlwaysSkipRetryStatusCode(code) {
		return false
	}
	return shouldMatchStatusCodeRanges(AutomaticRetryStatusCodeRanges, code)
}

func statusCodeRangesToString(ranges []StatusCodeRange) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == r.End {
			parts = append(parts, strconv.Itoa(r.Start))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	return strings.Join(parts, ",")
}

func shouldMatchStatusCodeRanges(ranges []StatusCodeRange, code int) bool {
	if code < 100 || code > 599 {
		return false
	}
	for _, r := range ranges {
		if code < r.Start {
			return false
		}
		if code <= r.End {
			return true
		}
	}
	return false
}

func ParseHTTPStatusCodeRanges(input string) ([]StatusCodeRange, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	input = strings.NewReplacer("，", ",").Replace(input)
	segments := strings.Split(input, ",")

	var ranges []StatusCodeRange
	var invalid []string

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		r, err := parseHTTPStatusCodeToken(seg)
		if err != nil {
			invalid = append(invalid, seg)
			continue
		}
		ranges = append(ranges, r)
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid http status code rules: %s", strings.Join(invalid, ", "))
	}
	if len(ranges) == 0 {
		return nil, nil
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})

	merged := []StatusCodeRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}

	return merged, nil
}

func parseHTTPStatusCodeToken(token string) (StatusCodeRange, error) {
	token = strings.TrimSpace(token)
	token = strings.ReplaceAll(token, " ", "")
	if token == "" {
		return StatusCodeRange{}, fmt.Errorf("empty token")
	}

	if strings.Contains(token, "-") {
		parts := strings.Split(token, "-")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return StatusCodeRange{}, fmt.Errorf("invalid range token: %s", token)
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range start: %s", token)
		}
		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range end: %s", token)
		}
		if start > end {
			return StatusCodeRange{}, fmt.Errorf("range start > end: %s", token)
		}
		if start < 100 || end > 599 {
			return StatusCodeRange{}, fmt.Errorf("range out of bounds: %s", token)
		}
		return StatusCodeRange{Start: start, End: end}, nil
	}

	code, err := strconv.Atoi(token)
	if err != nil {
		return StatusCodeRange{}, fmt.Errorf("invalid status code: %s", token)
	}
	if code < 100 || code > 599 {
		return StatusCodeRange{}, fmt.Errorf("status code out of bounds: %s", token)
	}
	return StatusCodeRange{Start: code, End: code}, nil
}
