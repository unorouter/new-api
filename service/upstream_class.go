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
	markers []string
	class   UpstreamClass
	// userMessage replaces the upstream's own text on its way to the client. Every
	// rule carries one: the sentence a customer reads is part of deciding what an
	// upstream state means, not an afterthought, and a marker added without one
	// silently ships somebody else's wording (often in Chinese) to our users.
	userMessage string
}

// Ordered: the first rule whose marker is in the message wins. Every marker is
// lowercase; the message is lowercased before matching.
//
// Matched on the text alone, never on which upstream sent it. These sentences
// say what happened ("circuit breaker cooling", "raised its price", "exceeds the
// processable range") and the right action follows from that meaning wherever it
// comes from, so scoping them to a host would only narrow a correct rule.
//
// Reseller platforms wrap every merchant state in one error type and the only
// stable discriminator is the 原因 sentence. Their statuses lie: "pinned merchant
// busy/cooling" and "merchant rejects the request" arrive as 400, which the
// generic rules read as a malformed request and never fail over.
var upstreamRules = []upstreamRule{
	// Reseller, platform-wide protective throttle: every lane on it answers the same 503
	// for the next seconds, so counting it per lane mass-disables healthy merchants.
	{markers: []string{"平台正在进行保护性限流"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true, Provider: true}, userMessage: "The provider is throttling every request right now. Nothing is wrong with your request or your balance. Try again in a moment."},
	// Reseller, request over this merchant's context budget: another merchant serves it.
	{markers: []string{"超过了可处理范围"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, ContextCap: true}, userMessage: "This request is longer than the provider could accept. Shorten the conversation or lower max_tokens, then retry."},
	// Reseller, merchant rate limited.
	{markers: []string{"该商家上游正在限流"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}, userMessage: "The providers for this model are rate limited right now. Nothing is used up on your side. Try again in a little while."},
	// Reseller, the merchant is fused on their side for a cooldown: every further try
	// during it fails, so the lane leaves rotation now and the probe brings it back.
	{markers: []string{"熔断冷却"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "The provider put this route into a cooldown. We have taken it out of rotation, so retry and another provider will pick it up."},
	// Reseller, lane-fatal states: capability blocked, price raised and the pin
	// suspended, our token refused, model gone from the merchant.
	{markers: []string{"暂时无法访问该商家的上游能力", "上调了价格", "账号或令牌状态不允许", "未开放或找不到该模型"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider can no longer serve this model. We have removed it from rotation, so retry and another provider will take it."},
	// Reseller, pinned merchant busy or cooling (masked as 400 a third of the time)
	// and merchant rejecting the request's parameters or capability: a sibling
	// merchant serves the same body, and a lane doing this all day is dead.
	// Cooled as well as counted: the body says the merchant is busy or cooling, so
	// sending it the next request immediately just buys another failure. A healthy
	// lane sheds the strike on its next success, so only real bursts bite.
	{markers: []string{"您固定的商家当前处于繁忙", "该商家拒绝了本次请求", "该商家上游返回了错误"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "The provider is busy or refused this request. Retrying usually lands on a different provider."},
	// Reseller, the merchant's own wallet or plan cannot fund the call ("请充值").
	// Intermittent in practice: over 24h lane 4591 answered it 43 times spread
	// across six hours while serving 792 requests, and 4615 17 times against 1,937.
	// Pulled at once, both flapped through the retest; counted, the guard pulls only
	// the lanes where it is the majority answer (4569: 91 against 15).
	// "您已超过输入 tokens 配额" is the same state metered differently: it fires on
	// prompts as small as 2 tokens (548 rows average 2,310 against an 18,054
	// baseline), so it is the merchant's allowance, not this request's size.
	{markers: []string{"可用额度不足", "您已超过输入 tokens 配额"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "This provider ran out of credit on their side. That is on us, not you: retry and the request goes to another provider."},
	// Reseller, states the platform itself calls durable: the merchant's upstream
	// balance is gone "and will not recover soon", or our account there is banned.
	{markers: []string{"上游账户余额不足", "账号处于封禁状态"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider can no longer take requests from us. We have taken it out of rotation, so please retry."},
	// Reseller, platform-wide faults every lane on the host answers together (auth
	// database degraded, CPU admission, no channel found, platform concurrency):
	// 290 rows across 55 lanes in three minutes on 2026-09-16. Counted per lane
	// they read as 55 broken merchants.
	{markers: []string{"认证数据库降级队列已满", "cpu 使用率超过平台准入阈值", "平台当前没有找到满足", "请求过多、并发过高"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true, Provider: true}, userMessage: "The provider platform is having trouble right now, not your request. Try again in a moment."},
	// Reseller, this merchant's channel cannot speak the request's protocol, and
	// the relay layer between us returned a broken reply (arrives as 404, 502, 400
	// or 520, so no status default covers every form): a sibling serves it and a
	// lane doing it all day is dead.
	{markers: []string{"协议能力与本次请求不匹配", "上游服务、网络链路或代理返回异常响应"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "The provider could not handle this request. Retry and it will go to a different one."},

	// Any host, the name did not resolve: nothing this lane serves can be reached
	// until the record is back, so it leaves rotation at the first failure instead
	// of waiting for the rate guard. a6api.com rotates its CNAME between backends
	// and landed on one with no record on 2026-09-16: 2,000 failures in seven
	// minutes on lanes that stayed enabled throughout. Only the lane that saw the
	// error is pulled, and the disabled-channel retest brings it back on its own.
	{markers: []string{"no such host", "server misbehaving"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "We could not reach that provider at all. It has left rotation, so please retry."},
	// Any host, the TLS handshake could not be trusted: nothing may be sent through
	// it, and the answer is never to skip verification (an untrusted certificate and
	// an interception look identical from here). a6api.com began serving a cert from
	// a private "Cyber Inc." CA at 21:23 UTC on 2026-09-15 and every lane on it
	// failed. Same treatment as an unresolvable name: the lane leaves rotation now
	// and the retest brings it back when the chain verifies again.
	{markers: []string{"certificate signed by unknown authority", "failed to verify certificate", "certificate has expired", "certificate is not valid for any names"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "We could not open a trusted connection to that provider, so your request was never sent. It has left rotation, please retry."},
	// Any host, the far end aborted the handshake itself (internal error, handshake
	// failure, unrecognized name): a7 emitted these alongside the bad certificate
	// while it was down for maintenance. Nothing can be sent over a handshake that
	// never completed, so the lane leaves rotation and the retest returns it.
	{markers: []string{"remote error: tls:"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "The provider dropped the connection before it was established. It has left rotation, please retry."},

	// Any host, rate limits and capacity: fail over, count nothing. AI Horde alone
	// produced 190k of these in a week; each one disabled a lane the probe
	// re-enabled five minutes later.
	{markers: []string{"per 1 second", "parallel requests (", "rate limit reached", "rate limit exceeded", "resource has been exhausted", "temporarily overloaded", "this model is busy right now", "rate_limit_exceeded", "并发上限", "总请求数限制"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}, userMessage: "This model is rate limited right now. Nothing is used up on your side. Try again in a few moments."},
	// Any host, the lane cannot serve this model's requests at all (unsupported
	// parameter, wrong model id, audio model behind a chat route): deterministic
	// for this lane only, so fail over AND let the rate guard pull it.
	{markers: []string{"does not support the requested parameter", "input should be '"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure}, userMessage: "The provider rejected one of this request's parameters. Retry to reach a different provider, or drop the unusual parameter."},
	// Any host, the client called an audio model on the chat route: every sibling
	// answers the same, and the lane did nothing wrong.
	// Any host, the upstream's content filter ended the conversation. Arrives as 400
	// and 502 in equal measure across 11 lanes, so the status defaults counted half
	// of it against lanes that did nothing wrong. The prompt caused it, not the lane.
	{markers: []string{"检测到该会话包含敏感信息"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "The provider's content filter closed this conversation. Rephrasing the last message usually clears it."},
	{markers: []string{"does not support chat completions"}, class: UpstreamClass{Known: true, Failover: false, Count: CountNone}, userMessage: "This model cannot be called from the chat completions endpoint."},
}

// matchUpstreamRule returns the first rule whose marker appears in the message,
// which is the single place the ordered table is walked: the class an error gets
// and the sentence the user reads must never come from two different matchers.
func matchUpstreamRule(msg string) *upstreamRule {
	for i := range upstreamRules {
		for _, marker := range upstreamRules[i].markers {
			if strings.Contains(msg, marker) {
				return &upstreamRules[i]
			}
		}
	}
	return nil
}

// A shared filter is one every sibling fronts, so the refusal is final and saying
// "try again" would be a lie. A per-upstream filter has already cost the request a
// walk through the pool by the time the user sees this, so every provider we hold
// refused it.
const (
	sharedFilterModerationMessage = "The provider's content filter rejected this prompt. Every provider for this model uses the same filter, so retrying will not change the answer. Rephrasing usually does."
	upstreamModerationMessage     = "Every provider we tried refused this prompt on content grounds. Rephrasing usually clears it."
)

// UpstreamUserMessage is the sentence to show the customer in place of the
// upstream's own error text. Upstreams write their errors for their own users, in
// their own language: 7,180 of ours, 26% of everyone active, read raw Chinese in
// the last nine days because whatever the upstream wrote went straight through.
//
// The raw text is not lost. Both error-log writers run before the relay rewrites
// the message, so triage still reads exactly what the upstream said.
func UpstreamUserMessage(err *types.NewAPIError) (string, bool) {
	if err == nil || err.GetErrorType() == types.ErrorTypeNewAPIError {
		return "", false
	}
	// Moderation first: its markers are deployment config rather than rules here,
	// and a refusal is about the prompt, never about the lane that answered.
	if types.IsSharedFilterModerationError(err) {
		return sharedFilterModerationMessage, true
	}
	if types.IsUpstreamModerationError(err) {
		return upstreamModerationMessage, true
	}
	if rule := matchUpstreamRule(strings.ToLower(err.Error())); rule != nil && rule.userMessage != "" {
		return rule.userMessage, true
	}
	return "", false
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
func ClassifyUpstreamError(err *types.NewAPIError) UpstreamClass {
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
	if rule := matchUpstreamRule(strings.ToLower(err.Error())); rule != nil {
		return rule.class
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
