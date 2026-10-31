package service

import (
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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
	// Shared marks a state every lane of the upstream is in at once (a guest
	// throttle metered across all exits): pulling the lane that answered changes
	// nothing except shrinking the pool the retry walks, so it is never disabled
	// for it.
	Shared bool
	// Window skips the lane for this long with no strike: a quota that refills on
	// the clock is spent, not failing, so the doubling Cooldown ladder is wrong for it.
	Window time.Duration
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
	// The upstream's own backend crashed and said so at HTTP 400 (a reseller on 2026-10-12:
	// "Database error: (...UndefinedColumn) column users.discord_id does not exist"):
	// nothing about the request is wrong, a sibling serves it, and the SQL stays out
	// of the customer's reply.
	{markers: []string{"database error:", "undefinedcolumn"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "The provider's own server had an internal error. Nothing is wrong with your request: retry and another provider will pick it up."},
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
	// "您已超过输入 tokens 配额" is the same state metered differently: it fires on
	// prompts as small as 2 tokens, so it is the merchant's allowance, not this
	// request's size. Pulled on the first answer, and the whole host cools: on
	// 2026-09-25 our own balance at a reseller ran out and a counted rule kept every lane on it in
	// rotation for five hours while each request walked several of them. A lane that
	// only flapped comes back through the scheduled retest.
	{markers: []string{"可用额度不足", "您已超过输入 tokens 配额", "account quota is insufficient"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true, Provider: true}, userMessage: "This provider ran out of credit on their side. That is on us, not you: retry and the request goes to another provider."},
	// The upstream balance is gone (a reseller adds "and will not recover soon"),
	// or our account there is banned.
	{markers: []string{"上游账户余额不足", "账号处于封禁状态", "insufficient balance", "no credits available", "may have insufficient balance", "insufficient free or premium credits", "balance is exhausted (0 tokens remaining)", "credits remaining, not enough to fulfill this request"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider can no longer take requests from us. We have taken it out of rotation, so please retry."},
	// Reseller, platform-wide faults every lane on the host answers together (auth
	// database degraded, CPU admission, no channel found, platform concurrency):
	// 290 rows across 55 lanes in three minutes on 2026-09-16. Counted per lane
	// they read as 55 broken merchants.
	{markers: []string{"认证数据库降级队列已满", "cpu 使用率超过平台准入阈值", "平台当前没有找到满足", "请求过多、并发过高", "请求体内存保护已触发", "平台公共依赖暂时不可用"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true, Provider: true}, userMessage: "The provider platform is having trouble right now, not your request. Try again in a moment."},
	// Reseller, this merchant's channel cannot speak the request's protocol, and
	// the relay layer between us returned a broken reply (arrives as 404, 502, 400
	// or 520, so no status default covers every form): a sibling serves it and a
	// lane doing it all day is dead.
	{markers: []string{"协议能力与本次请求不匹配", "上游服务、网络链路或代理返回异常响应", "模型请求失败，请稍后重试或更换模型", "当前请求未成功", "对话失败"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "The provider could not handle this request. Retry and it will go to a different one."},
	// The platform already paused a merchant that kept failing this model, or every
	// candidate it could route to failed: the lane is cooling, not dead.
	{markers: []string{"持续失败，平台已暂停", "智能路由候选均请求失败", "所有回退分组均不可用"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "The provider paused this route after repeated failures. Retry and another provider will pick it up."},
	{markers: []string{"负载已饱和", "该模型当前访问量过大", "请求暂时未能完成，请稍后重试", "当前模型服务暂不可用", "请等待其他对话生成完毕", "the selected model is temporarily unavailable", "this model is currently experiencing high demand", "worker local total request limit reached", "upstream connection failed before the request could be processed"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}, userMessage: "The provider is overloaded right now. Retry and another provider will pick it up."},
	// A relay running new-api answers this when none of its own channels serves the
	// model for that group: the lane is empty, not refusing the request.
	{markers: []string{"的可用渠道不存在", "的可用管道不存在", "无可用渠道", "no available channel for model"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "This provider has no capacity for that model right now. Retry and another provider will pick it up."},
	{markers: []string{"等待响应超时", "上游响应超时", "未接收到上游响应内容", "upstream failed: no completion within"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "The provider took too long to answer. Retry and another provider will pick it up."},
	{markers: []string{"sensitive words detected", "内容不符合安全策略", "请求内容未通过安全检查"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "This provider refused the prompt on content grounds. Retrying may reach one that accepts it; otherwise rephrase it."},
	{markers: []string{"免费点数不足", "预扣费额度失败", "failed to pre-consume quota", "当前商户余额不足"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true, Provider: true}, userMessage: "This provider ran out of credit on their side. That is on us, not you: retry and the request goes to another provider."},
	{markers: []string{"的价格尚未由管理员配置", "该商家渠道已暂停、下架或不存在"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider can no longer serve this model. We have removed it from rotation, so retry and another provider will take it."},
	{markers: []string{"智能路由流式响应中途失败"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure}, userMessage: "The provider dropped the response partway through. Please send the request again."},
	{markers: []string{"请求参数不正确，请检查请求体", "请求未能完成，请检查请求参数", "限制小数点", "image_url格式错误", "当前模型或商家不支持请求中的能力", "上游渠道拒绝了图片请求", "inference request is invalid ("}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "This provider rejected one of the request's parameters. Retry and it will go to a provider that accepts it."},
	{markers: []string{"当前会话被上游安全策略封锁"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "The provider's safety policy blocked this conversation. Retry to reach another provider, or start a new chat."},
	{markers: []string{"提示词或参考图此前已被上游内容审核拒绝", "提示词安全审计拒绝了本次请求"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "This provider refused the prompt on content grounds. Retrying may reach one that accepts it; otherwise rephrase it."},
	{markers: []string{"免费请求额度已用完", "额度已用尽"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider's free allowance is used up until it resets. It has left rotation, so please retry and another provider will take it."},
	{markers: []string{"api key 无效", "上游认证失败、密钥无效或账号被限制"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider's credentials stopped working. It has left rotation, so please retry."},

	// Strict schema hosts refuse request shapes other lanes take: non-text
	// content parts, or echoed reasoning_content. The lane is healthy, only this
	// request does not fit it, so a sibling serves it and nothing is counted.
	// One reseller pool answers a PDF file part with a bare 11101 of this text.
	{markers: []string{"type is invalid, allowed values: ['text']", "extra inputs are not permitted", "invalid request parameters. please check and retry"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "This provider cannot take part of your request (an image, file or reasoning block). Retry and it will go to a provider that accepts it."},

	// Any host, the route or model is simply not there: a Go mux "404 page not
	// found" from a relay whose handler is gone, or a gateway naming the model as
	// nonexistent. Deterministic for this lane, and the failure-RATE guard never
	// trips on it while the lane still serves other requests: one free lane answered it 106
	// times against 700 successes on 2026-09-21 and stayed enabled all day. The
	// disabled-channel retest brings the lane back once the route answers again.
	{markers: []string{"404 page not found", "please use an exact model id", "` does not exist", ":free not supported"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider no longer serves that model. It has left rotation, so please retry."},
	// A retired model: NVIDIA answers 410 "has reached its end of life on <date>
	// and is no longer available". Nothing about it clears, and read as an
	// unknown error it counted nothing: three lanes failed 18,600 requests in ten
	// hours on 2026-09-21 and stayed enabled.
	{markers: []string{"reached its end of life", "has been retired", "has been deprecated and is no longer", "has been deprecated. please use one of the available models"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider has retired that model. It has left rotation, so please retry."},
	// Google's answer to a deleted or revoked key comes as a 400, so nothing counted it: one lane passed
	// it to users 5,142 times in 24h to 2026-09-28 and that lane stayed enabled.
	{markers: []string{"api key not found. please pass a valid api key", "api key not valid. please pass a valid api key"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider's credentials stopped working. It has left rotation, so please retry."},
	// A reseller refusing a model outright answers 403, which neither failed over nor counted: both
	// gpt-6-astra lanes of one reseller returned it 253 times in 3h on 2026-09-30 while five other lanes served.
	{markers: []string{"拒绝，服务暂时不可用，请联系管理员"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider stopped serving that model. It has left rotation, so please retry."},
	// xAI refuses a stale Grok CLI identity with 426 until the relay updates it: two relays'
	// grok lanes answered only this from 2026-09-30 20:39, 0 successes, and stayed enabled.
	{markers: []string{"grok cli version"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider's upstream client is out of date. It has left rotation, so please retry."},
	// A free allowance spent until its daily or monthly reset fails every request until then, and a
	// sole lane is never pulled on rate: one sole lane stayed listed through 367 of these in two days.
	{markers: []string{"free tier limit reached", "too many tokens per day", "daily free quota for the model", "today's free-model token quota", "free-models-per-day", "api calls / month", "reached your monthly usage limit", "you exceeded your current quota"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "This provider's free allowance is used up until it resets. It has left rotation, so please retry and another provider will take it."},
	// A demo site fronted by our proxy shards caps the messages per conversation:
	// the customer's long chat is refused, the lane is fine. Its 502 read as a
	// lane failure disabled ten of those lanes on 2026-09-21 at half their traffic.
	{markers: []string{"demo is limited to"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "This provider caps the number of messages in one conversation. Start a new conversation, or retry and another provider will take it."},
	// The same demo caps the LAST user message at 6000 characters (the system
	// prompt and earlier turns are not counted), so it is not a context ceiling:
	// learning one from the whole prompt would route every long chat past a lane
	// that serves them. Arrives as 400, which alone never fails over.
	{markers: []string{"prompt too long (max "}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone}, userMessage: "This provider limits the length of a single message. Shorten your last message, or retry and another provider will take it."},
	// One upstream's per minute cap on one model (3021) is one budget for every caller
	// of the account behind a lane, and it refills each minute. On the doubling
	// Cooldown all eight of its glm-5.3 lanes sat out ten minutes at a time and the
	// model left the catalog (2026-09-21); one window per hit follows the cap.
	{markers: []string{"inference request per min rate reached"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Shared: true, Window: time.Minute}, userMessage: "This model is at its provider's per minute limit right now. Nothing is used up on your side. Try again in a minute."},
	// Per key and per address caps that refill each minute: free tiers metered per key
	// (5 to 6 RPM) or per address (100 a minute per exit, relayed by our proxy), and
	// free keys allowed one request in flight 7s apart (that cap is a 403). The
	// markers that name an upstream come from MINUTE_WINDOW_MARKERS. The generic rate limit rule below would double the
	// sit out up to ten minutes.
	{markers: append([]string{"free tier rate limit (", "account per-minute rate limit exceeded ("}, operation_setting.MinuteWindowMarkers...), class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: time.Minute}, userMessage: "This model is at its provider's per minute limit right now. Nothing is used up on your side. Try again in a minute."},
	// One upstream admits signed-in and paid accounts first; an anonymous key at peak gets
	// this for minutes at a time, and its text carried its signup link and model
	// list straight to our users (2026-10-04, glm-5.3-flash:free).
	{markers: []string{"at concurrency capacity. signed-in and paid accounts are admitted first"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: 5 * time.Minute}, userMessage: "This model's provider is full right now. Nothing is used up on your side. Retry and another provider will take it, or try again in a few minutes."},
	// A free key capped per hour across all its free ids (a 403) disabled six of
	// eight lanes within minutes of their first sync (2026-09-26). Markers from
	// QUARTER_HOUR_WINDOW_MARKERS.
	// An image horde puts an address into timeout for a few minutes after a burst;
	// every request until then is refused, and the lane is fine afterwards.
	{markers: []string{"due to abuse prevention, your ip address has been put into timeout"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: 5 * time.Minute}, userMessage: "This model's provider is full right now. Nothing is used up on your side. Retry and another provider will take it, or try again in a few minutes."},
	// Our own proxy shards announce an exhausted hourly allowance this way.
	{markers: []string{"shard exit at rph limit"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: 15 * time.Minute}, userMessage: "This model is at its provider's hourly limit right now. Nothing is used up on your side. Retry and another provider will take it."},
	{markers: operation_setting.QuarterHourWindowMarkers, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: 15 * time.Minute}, userMessage: "This model is at its provider's hourly limit right now. Nothing is used up on your side. Retry and another provider will take it."},
	// A free lane whose upstream keeps answering that free capacity is limited
	// and paid credits lift it: a 429 in words, but one lane failed 583
	// requests against 15 successes all day, so it counts toward the guard.
	{markers: []string{"add credits for higher, more stable limits"}, class: UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}, userMessage: "This model is at capacity right now. Nothing is used up on your side. Try again in a few moments."},
	// Any host, the name did not resolve: nothing this lane serves can be reached
	// until the record is back, so it leaves rotation at the first failure instead
	// of waiting for the rate guard. a reseller host rotates its CNAME between backends
	// and landed on one with no record on 2026-09-16: 2,000 failures in seven
	// minutes on lanes that stayed enabled throughout. Only the lane that saw the
	// error is pulled, and the disabled-channel retest brings it back on its own.
	{markers: []string{"no such host", "server misbehaving"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "We could not reach that provider at all. It has left rotation, so please retry."},
	// Any host, the TLS handshake could not be trusted: nothing may be sent through
	// it, and the answer is never to skip verification (an untrusted certificate and
	// an interception look identical from here). a reseller host began serving a cert from
	// a private "Cyber Inc." CA at 21:23 UTC on 2026-09-15 and every lane on it
	// failed. Same treatment as an unresolvable name: the lane leaves rotation now
	// and the retest brings it back when the chain verifies again.
	{markers: []string{"certificate signed by unknown authority", "failed to verify certificate", "certificate has expired", "certificate is not valid for any names"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "We could not open a trusted connection to that provider, so your request was never sent. It has left rotation, please retry."},
	// Any host, the far end aborted the handshake itself (internal error, handshake
	// failure, unrecognized name): that host emitted these alongside the bad certificate
	// while it was down for maintenance. Nothing can be sent over a handshake that
	// never completed, so the lane leaves rotation and the retest returns it.
	{markers: []string{"remote error: tls:"}, class: UpstreamClass{Known: true, Failover: true, DisableNow: true}, userMessage: "The provider dropped the connection before it was established. It has left rotation, please retry."},

	// Our own lane proxies: the shard's slots are full or the
	// upstream handed back empty frames, which is what a web guest throttle
	// looks like from inside the tunnel. A capacity state of the shard, not a
	// broken lane: 108 of these reached users in five minutes on 2026-09-21 while
	// the generic 503 default counted every one and pulled the lane.
	// Gemini web answers a guest 405 on about one call in seven per shard while
	// busier shards see none; three in a row pulled half the gemini lanes on 2026-10-30.
	{markers: []string{"upstream returned an empty answer", "free model capacity exhausted", "upstream returned http 405"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}, userMessage: "This model is at capacity right now. Nothing is used up on your side. Try again in a few moments."},
	// Full slots refill as answers finish; on the rate gate they pulled most lanes
	// of two shard series on 2026-10-30 while those shards were serving.
	{markers: []string{"shard busy, retry on another shard", "shard exit at concurrent limit"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: 30 * time.Second}, userMessage: "This model is at capacity right now. Nothing is used up on your side. Try again in a few moments."},
	// Any host, rate limits and capacity: fail over, count nothing. One host alone
	// produced 190k of these in a week; each one disabled a lane the probe
	// re-enabled five minutes later.
	// A web guest throttle. As DisableNow it held 166 of 167 lanes of a series off on
	// 2026-10-30 while 15 of 53 shards answered; the needed run of passing probes
	// rarely lands at that rate. The proxy now answers it in under a second, so a
	// short window fails over at once without the 2026-09-23 first byte stalls.
	{markers: []string{"请求过于频繁", "当前访客资源不足"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Window: 3 * time.Minute}, userMessage: "This model is rate limited right now. Nothing is used up on your side. Try again in a few moments."},
	{markers: []string{"per 1 second", "parallel requests (", "rate limit reached", "rate limit exceeded", "resource has been exhausted", "temporarily overloaded", "this model is busy right now", "rate_limit_exceeded", "并发上限", "总请求数限制", "上游返回频率限制", "上游文件上传额度已达到限制"}, class: UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}, userMessage: "This model is rate limited right now. Nothing is used up on your side. Try again in a few moments."},
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
	{markers: []string{"does not support chat completions", "or is not supported for generatecontent", "model was not found. make sure you are using the correct endpoint"}, class: UpstreamClass{Known: true, Failover: false, Count: CountNone}, userMessage: "This model cannot be called from the chat completions endpoint."},
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
	upstreamModerationMessage     = "Every provider we tried refused this prompt on content grounds. Free providers often run a stricter filter than paid ones, so rephrasing or another model usually clears it."
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
	// A provider notice served as the answer (replyRules): no retest prompt
	// reproduces it reliably, so the lane leaves on the first one.
	if err.GetErrorCode() == types.ErrorCodeChannelCannedReply {
		return UpstreamClass{Known: true, Failover: true, DisableNow: true}
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
	case err.StatusCode == 402:
		// An upstream's 402 is its own wallet or pin, never the customer's balance,
		// which this gateway settles itself before any upstream call. Spent money does
		// not come back on a retry, so the lane leaves until the retest passes.
		return UpstreamClass{Known: true, Failover: true, DisableNow: true}
	case err.StatusCode == 410:
		// Gone is permanent by definition: the route or model will not return.
		return UpstreamClass{Known: true, Failover: true, DisableNow: true}
	case err.StatusCode == 413:
		return UpstreamClass{Known: true, Failover: true, Count: CountNone, ContextCap: true}
	case err.StatusCode == 429:
		return UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}
	case err.StatusCode >= 500 && err.StatusCode <= 504, err.StatusCode >= 520 && err.StatusCode <= 530:
		return UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}
	}
	return UpstreamClass{}
}
