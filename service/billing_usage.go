package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	usageBillingPathLocal              = "local"
	usageBillingPathUpstream           = "upstream"
	usageBillingPathOpenAI             = "billing-usage-openai"
	usageBillingPathOpenAIEstimated    = "billing-usage-openai-estimated"
	usageBillingPathAnthropic          = "billing-usage-anthropic"
	usageBillingPathAnthropicEstimated = "billing-usage-anthropic-estimated"
	usageBillingPathGemini             = "billing-usage-gemini"
	usageBillingPathGeminiEstimated    = "billing-usage-gemini-estimated"
)

func effectiveBillingUsage(usage *dto.Usage) *dto.Usage {
	if billingUsage, ok := usageFromBillingUsage(usage); ok {
		return billingUsage
	}
	return usage
}

func usageBillingPathForLog(isLocalCountTokens bool, usage *dto.Usage) string {
	effectiveUsage, ok := usageFromBillingUsage(usage)
	if !ok {
		if isLocalCountTokens {
			return usageBillingPathLocal
		}
		return usageBillingPathUpstream
	}

	switch effectiveUsage.UsageSemantic {
	case dto.BillingUsageSemanticOpenAI:
		if usage.BillingUsage.Estimated {
			return usageBillingPathOpenAIEstimated
		}
		return usageBillingPathOpenAI
	case dto.BillingUsageSemanticAnthropic:
		if usage.BillingUsage.Estimated {
			return usageBillingPathAnthropicEstimated
		}
		return usageBillingPathAnthropic
	case dto.BillingUsageSemanticGemini:
		if usage.BillingUsage.Estimated {
			return usageBillingPathGeminiEstimated
		}
		return usageBillingPathGemini
	}

	return usageBillingPathUpstream
}

func appendUsageBillingPathForLog(other *model.LogOther, isLocalCountTokens bool, usage *dto.Usage) {
	if other == nil {
		return
	}
	other.SetAdmin("usage_billing_path", usageBillingPathForLog(isLocalCountTokens, usage))
}

func usageFromBillingUsage(usage *dto.Usage) (*dto.Usage, bool) {
	if usage == nil || usage.BillingUsage == nil {
		return nil, false
	}
	return usage.BillingUsage.CanonicalUsage()
}

// Above this an upstream's report is not tokenizer drift (1.1 to 1.5x, a hidden
// system prompt, caching) but a padded bill: a7 6870 billed 478k for a 37k prompt.
const (
	promptInflationFactor = 3
	promptInflationSlack  = 2000
	// The most a channel override lifts a client's max_tokens (utim/rdx Claude lanes set 2000).
	completionOverrideSlack = 4000
)

// capInflatedUsage bills our own count when an upstream reports more than the
// request could have used: a text-only prompt far past our estimate, cache writes
// that with the rest of the input outgrow it, or output past the request's
// max_tokens, which bounds visible and reasoning output alike at every vendor.
// The original report is left untouched for the log.
func capInflatedUsage(info *relaycommon.RelayInfo, usage *dto.Usage) *dto.Usage {
	if info == nil || usage == nil {
		return usage
	}
	openAISemantic := usage.UsageSemantic == "" || usage.UsageSemantic == dto.BillingUsageSemanticOpenAI
	anthropicSemantic := usage.UsageSemantic == dto.BillingUsageSemanticAnthropic
	if !openAISemantic && !anthropicSemantic {
		return usage
	}
	capped := *usage
	report := relaycommon.UsageCap{}
	estimate := info.GetEstimatePromptTokens()
	allowance := estimate*promptInflationFactor + promptInflationSlack
	checkInput := info.RequestTextOnly && estimate > 0
	if checkInput && openAISemantic && usage.PromptTokens > allowance {
		report.PromptBilled, report.PromptCapped = usage.PromptTokens, estimate
		capped.PromptTokens = estimate
		capped.PromptTokensDetails.CachedTokens = min(capped.PromptTokensDetails.CachedTokens, estimate)
		capped.PromptCacheHitTokens = min(capped.PromptCacheHitTokens, estimate)
	}
	// A cache write never exceeds the prompt it stores, so writes that push the
	// whole input past the allowance are padding (a7 6693 billed 345k writes on 7k chats).
	if writes := capped.PromptTokensDetails.CacheCreationTokensTotal(); checkInput && writes > 0 {
		claimed := capped.PromptTokens + writes
		if anthropicSemantic {
			claimed += capped.PromptTokensDetails.CachedTokens
		}
		if claimed > allowance {
			kept := max(0, writes-(claimed-allowance))
			report.CacheWriteBilled, report.CacheWriteCapped = writes, kept
			capCacheWrites(&capped, kept)
		}
	}
	// Headroom for channel overrides that raise max_tokens on the way upstream.
	if limit := max(info.RequestMaxTokens*2, info.RequestMaxTokens+completionOverrideSlack); info.RequestMaxTokens > 0 && usage.CompletionTokens > limit {
		report.CompletionBilled, report.CompletionCapped = usage.CompletionTokens, limit
		capped.CompletionTokens = limit
		capped.CompletionTokenDetails.ReasoningTokens = min(capped.CompletionTokenDetails.ReasoningTokens, limit)
	}
	if report == (relaycommon.UsageCap{}) {
		return usage
	}
	if openAISemantic {
		capped.TotalTokens = capped.PromptTokens + capped.CompletionTokens
	}
	info.UsageCap = &report
	return &capped
}

func capCacheWrites(usage *dto.Usage, kept int) {
	d := &usage.PromptTokensDetails
	d.CacheWriteTokens = min(d.CacheWriteTokens, kept)
	d.CachedCreationTokens = min(d.CachedCreationTokens, kept)
	d.CacheCreationInputTokens = min(d.CacheCreationInputTokens, kept)
	usage.ClaudeCacheCreation1hTokens = min(usage.ClaudeCacheCreation1hTokens, kept)
	usage.ClaudeCacheCreation5mTokens = min(usage.ClaudeCacheCreation5mTokens, kept-usage.ClaudeCacheCreation1hTokens)
}

// A lane whose input bill is clamped this often in the window over-reports on
// purpose, not by tokenizer drift; each clamp already billed our count.
const (
	usageInflationStrikeLimit = 5
	usageInflationWindow      = time.Hour
)

var usageInflationStrikes = struct {
	sync.Mutex
	hits map[int][]time.Time
}{hits: map[int][]time.Time{}}

func noteUsageInflation(c *gin.Context, info *relaycommon.RelayInfo) {
	if info == nil || info.ChannelMeta == nil || info.UsageCap == nil ||
		(info.UsageCap.PromptCapped == 0 && info.UsageCap.CacheWriteBilled == 0) {
		return
	}
	now := time.Now()
	usageInflationStrikes.Lock()
	recent := usageInflationStrikes.hits[info.ChannelId][:0]
	for _, at := range usageInflationStrikes.hits[info.ChannelId] {
		if now.Sub(at) < usageInflationWindow {
			recent = append(recent, at)
		}
	}
	recent = append(recent, now)
	pull := len(recent) >= usageInflationStrikeLimit
	if pull {
		delete(usageInflationStrikes.hits, info.ChannelId)
	} else {
		usageInflationStrikes.hits[info.ChannelId] = recent
	}
	usageInflationStrikes.Unlock()
	if !pull {
		return
	}
	reason := fmt.Sprintf("upstream over-reported input usage %d times within %s; billed on our own count", usageInflationStrikeLimit, usageInflationWindow)
	DisableChannel(*types.NewChannelError(info.ChannelId, info.ChannelType, common.GetContextKeyString(c, constant.ContextKeyChannelName),
		info.ChannelIsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), common.GetContextKeyBool(c, constant.ContextKeyChannelAutoBan)), reason)
}
