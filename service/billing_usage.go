package service

import (
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
// request could have used: a text-only prompt far past our estimate, or output
// past the request's max_tokens, which bounds visible and reasoning output alike
// at every vendor. Only OpenAI-semantic usage, where prompt tokens include the
// cached share; the original report is left untouched for the log.
func capInflatedUsage(info *relaycommon.RelayInfo, usage *dto.Usage) *dto.Usage {
	if info == nil || usage == nil {
		return usage
	}
	if usage.UsageSemantic != "" && usage.UsageSemantic != dto.BillingUsageSemanticOpenAI {
		return usage
	}
	capped := *usage
	report := relaycommon.UsageCap{}
	estimate := info.GetEstimatePromptTokens()
	if info.RequestTextOnly && estimate > 0 &&
		usage.PromptTokens > estimate*promptInflationFactor+promptInflationSlack {
		report.PromptBilled, report.PromptCapped = usage.PromptTokens, estimate
		capped.PromptTokens = estimate
		capped.PromptTokensDetails.CachedTokens = min(capped.PromptTokensDetails.CachedTokens, estimate)
		capped.PromptCacheHitTokens = min(capped.PromptCacheHitTokens, estimate)
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
	capped.TotalTokens = capped.PromptTokens + capped.CompletionTokens
	info.UsageCap = &report
	return &capped
}
