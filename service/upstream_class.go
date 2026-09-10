package service

import (
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// CountMode says what an upstream fault contributes to the channel's failure
// window: nothing (a rate limit is not evidence the lane is broken), or a failure
// the rate guard weighs like any other.
type CountMode int

const (
	CountFailure CountMode = iota
	CountNone
)

// UpstreamClass is the gateway's reading of one upstream error text. Known is
// false when no rule matched and the generic status-code rules apply.
type UpstreamClass struct {
	Known      bool
	Failover   bool
	Count      CountMode
	DisableNow bool
	// Provider marks a fault every lane of the host shares right now.
	Provider bool
	// Cooldown skips the lane for a growing period even when the failure still
	// counts: a flapping upstream is passed over, a dead one still trips the guard.
	Cooldown bool
	// ContextCap marks a prompt the lane cannot take: its size becomes the lane's
	// learned ceiling and bigger prompts route past it.
	ContextCap bool
}

type upstreamRule struct {
	host    string // substring of the channel host, "" matches every host
	markers []string
	class   UpstreamClass
}

// Ordered: the first rule whose host matches and whose marker is in the message
// wins. Every marker is lowercase; the message is lowercased before matching.
//
// marketplace wraps every merchant state in type fixed_merchant_unavailable and the
// only stable discriminator is the 原因 sentence. Its statuses lie: "pinned
// merchant busy/cooling" and "merchant rejects the request" arrive as 400, which
// the generic rules read as a malformed request and never fail over.
var upstreamRules = []upstreamRule{
	// marketplace, platform-wide protective throttle: every a6 lane answers the same 503
	// for the next seconds, so counting it per lane mass-disables healthy merchants.
	{host: "marketplace.example", markers: []string{"平台正在进行保护性限流"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true, Provider: true}},
	// marketplace, request over this merchant's context budget: another merchant serves it.
	{host: "marketplace.example", markers: []string{"超过了可处理范围"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, ContextCap: true}},
	// marketplace, merchant rate limited.
	{host: "marketplace.example", markers: []string{"该商家上游正在限流"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}},
	// marketplace, the merchant is fused on their side for a cooldown: every further try
	// during it fails, so the lane leaves rotation now and the probe brings it back.
	{host: "marketplace.example", markers: []string{"熔断冷却"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}},
	// marketplace, lane-fatal states: capability blocked, price raised and the pin
	// suspended, our token refused, model gone from the merchant.
	{host: "marketplace.example", markers: []string{"暂时无法访问该商家的上游能力", "上调了价格", "账号或令牌状态不允许", "未开放或找不到该模型"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}},
	// marketplace, pinned merchant busy or cooling (masked as 400 a third of the time)
	// and merchant rejecting the request's parameters or capability: a sibling
	// merchant serves the same body, and a lane doing this all day is dead.
	{host: "marketplace.example", markers: []string{"您固定的商家当前处于繁忙", "该商家拒绝了本次请求", "该商家上游返回了错误"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure}},

	// Any host, rate limits and capacity: fail over, count nothing. AI Horde alone
	// produced 190k of these in a week; each one disabled a lane the probe
	// re-enabled five minutes later.
	{markers: []string{"per 1 second", "parallel requests (", "rate limit reached", "rate limit exceeded", "resource has been exhausted", "temporarily overloaded", "this model is busy right now", "rate_limit_exceeded"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}},
	// Any host, the lane cannot serve this model's requests at all (unsupported
	// parameter, wrong model id, audio model behind a chat route): deterministic
	// for this lane only, so fail over AND let the rate guard pull it.
	{markers: []string{"does not support the requested parameter", "input should be '"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure}},
	// Any host, the client called an audio model on the chat route: every sibling
	// answers the same, and the lane did nothing wrong.
	{markers: []string{"does not support chat completions"}, class: UpstreamClass{Known: true, Failover: false, Count: CountNone}},
}

func upstreamHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return strings.ToLower(baseURL)
	}
	return strings.ToLower(u.Host)
}

// ClassifyUpstreamError reads the upstream's error text for the states the
// status code does not carry. Local errors (our own filters, quota, request
// build) are never upstream faults and return Known false.
func ClassifyUpstreamError(baseURL string, err *types.NewAPIError) UpstreamClass {
	if err == nil {
		return UpstreamClass{}
	}
	// Our own first-byte deadline on the lane: the upstream was slow, not broken,
	// and the largest error class of all (176k a week) must fail over and cool.
	if err.GetErrorCode() == types.ErrorCodeChannelResponseTimeExceeded || err.GetErrorCode() == types.ErrorCodeChannelEmptyResponse {
		return UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}
	}
	if err.GetErrorType() == types.ErrorTypeNewAPIError {
		return UpstreamClass{}
	}
	host := upstreamHost(baseURL)
	msg := strings.ToLower(err.Error())
	for _, rule := range upstreamRules {
		if rule.host != "" && !strings.Contains(host, rule.host) {
			continue
		}
		for _, marker := range rule.markers {
			if strings.Contains(msg, marker) {
				return rule.class
			}
		}
	}
	// Status defaults for texts no rule names. Every upstream 429 is a limit of
	// some kind (rpm, tpm, shards, daily quota): nothing about the lane is broken.
	// A 5xx is capacity or a real fault; the rate guard tells them apart over the
	// window, the cooldown keeps the lane out of rotation while it decides.
	switch {
	case err.StatusCode == 413:
		return UpstreamClass{Known: true, Failover: true, Count: CountNone, ContextCap: true}
	case err.StatusCode == 429:
		return UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}
	case err.StatusCode >= 500 && err.StatusCode <= 504, err.StatusCode >= 520 && err.StatusCode <= 530:
		return UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}
	}
	return UpstreamClass{}
}
