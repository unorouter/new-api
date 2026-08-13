package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

func init() {
	// Feed the admin-editable channel-fault keyword list into the types package
	// (which does the error reclassification but cannot import this package).
	types.ChannelFaultKeywordsProvider = func() []string { return ChannelFaultKeywords }
}

var DemoSiteEnabled = false
var SelfUseModeEnabled = false
var ShowOriginalPriceEnabled = false

var AutomaticDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
}

// ChannelFaultKeywords: upstream error fragments that mean THIS channel is at
// fault (dead key, drained upstream wallet, exhausted free quota) rather than the
// client's request. Unlike AutomaticDisableKeywords (disable only), a match here
// on a 400/403 ALSO reclassifies the error to a channel fault so the SAME request
// fails over to a healthy sibling. Admin-editable option (standard OptionMap
// flow: these are the code defaults, a saved DB row overrides them); matched
// case-insensitively.
var ChannelFaultKeywords = []string{
	"api key not valid",
	"api key expired",
	"api_key_invalid",
	"external billing pre-consume: insufficient balance",
	"用户额度不足",
	"剩余额度",
	// Alibaba Bailian/DashScope free-tier quota exhausted (Stop-on-Exhaust).
	"the free quota has been exhausted",
	"免费额度已用尽",
	// Upstream reseller (runs new-api) whose org/owner wallet is drained.
	"organization and owner combined quota is zero",
	"insufficient credits",
	"please top up your balance",
	// A spent plan/daily allowance rather than a per-second rate limit: it cannot
	// clear on its own, so every further request is a wasted failover hop. A bare
	// capacity 429 carries none of these and stays rate-gated.
	"token plan limit exhausted",
	"tokens per day limit exceeded",
	// Google free tier: a spent DAILY allowance, not a per-second rate limit, so it
	// cannot clear on its own and every further request is a wasted failover hop.
	"you exceeded your current quota",
	"generate_content_free_tier_requests",
	// Google returns a suspended/revoked key as 429, which reads as an ordinary
	// rate limit and keeps the channel in rotation serving nothing.
	"has been suspended",
	"consumer 'api_key",
}

func keywordsToString(kw []string) string {
	return strings.Join(kw, "\n")
}

func keywordsFromString(s string) []string {
	out := []string{}
	for _, k := range strings.Split(s, "\n") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

func AutomaticDisableKeywordsToString() string {
	return keywordsToString(AutomaticDisableKeywords)
}

func AutomaticDisableKeywordsFromString(s string) {
	AutomaticDisableKeywords = keywordsFromString(s)
}

func ChannelFaultKeywordsToString() string {
	return keywordsToString(ChannelFaultKeywords)
}

func ChannelFaultKeywordsFromString(s string) {
	ChannelFaultKeywords = keywordsFromString(s)
}
