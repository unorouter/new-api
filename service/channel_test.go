package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A live empty-stream fault that failed over cleanly must NOT auto-disable the
// channel (ErrOptionWithSkipDisable), while the same channel:empty_response code
// without that option still disables (the scheduled autotest path). Guards the
// split that keeps healthy free-tier channels from being false-banned on one
// transient empty stream.
func TestShouldDisableChannelSkipDisable(t *testing.T) {
	prev := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = prev })

	skip := types.NewOpenAIError(errors.New("upstream streamed an empty response"),
		types.ErrorCodeChannelEmptyResponse, http.StatusBadGateway,
		types.ErrOptionWithSkipDisable())
	assert.False(t, ShouldDisableChannel(skip), "skip-disable empty-response must not disable")

	disable := types.NewOpenAIError(errors.New("upstream streamed an empty response"),
		types.ErrorCodeChannelEmptyResponse, http.StatusBadGateway)
	assert.True(t, ShouldDisableChannel(disable), "channel:empty_response without skip-disable must still disable")
}

// Empty-response disable is rate-based: a dead channel (high empty share, or the
// absolute floor with zero successes) disables, while a healthy high-volume channel
// survives scattered empties so one heavy user cannot cascade the whole pool.
func TestRecordEmptyResponseFailureRate(t *testing.T) {
	const ch = -42
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	m := operation_setting.GetMonitorSetting()
	prevRate, prevMin, prevFloor := m.EmptyResponseRateThreshold, m.EmptyResponseMinSamples, m.EmptyResponseAbsoluteFloor
	m.EmptyResponseRateThreshold, m.EmptyResponseMinSamples, m.EmptyResponseAbsoluteFloor = 0.5, 8, 5
	t.Cleanup(func() {
		common.RedisEnabled = prevRedis
		m.EmptyResponseRateThreshold, m.EmptyResponseMinSamples, m.EmptyResponseAbsoluteFloor = prevRate, prevMin, prevFloor
	})

	reset := func() { emptyResponseFailCounts.Delete(ch); channelSuccessCounts.Delete(ch) }
	feedEmpties := func(n int) bool {
		var last bool
		for i := 0; i < n; i++ {
			last = RecordEmptyResponseFailure(ch)
		}
		return last
	}
	feedSuccess := func(n int) {
		for i := 0; i < n; i++ {
			RecordChannelSuccess(ch)
		}
	}

	t.Run("dead channel trips the absolute floor (no successes)", func(t *testing.T) {
		reset()
		assert.False(t, feedEmpties(4), "below floor must not disable")
		assert.True(t, RecordEmptyResponseFailure(ch), "5th empty hits floor -> disable")
	})

	t.Run("flapping channel disables on rate", func(t *testing.T) {
		reset()
		feedSuccess(5)
		require.False(t, feedEmpties(4), "4 empty / 5 ok = 44% over 9 samples -> keep")
		assert.True(t, RecordEmptyResponseFailure(ch), "5 empty / 5 ok = 50% over 10 -> disable")
	})

	t.Run("healthy high-volume channel survives scattered empties", func(t *testing.T) {
		reset()
		feedSuccess(570)
		assert.False(t, feedEmpties(4), "4 empty / 570 ok = 0.7% and below floor -> keep")
	})

	t.Run("thin sample does not disable", func(t *testing.T) {
		reset()
		feedSuccess(3)
		assert.False(t, feedEmpties(2), "2 empty / 3 ok: total 5 < min 8 and empties < floor -> keep")
	})
}

// Live auto-disable for ordinary upstream faults is rate-based, so the busiest
// channels are not banned for the transient 429/5xx that come with carrying the
// most traffic. A channel with no successes at all still parks on the floor.
func TestRecordChannelFailureRate(t *testing.T) {
	const ch = -43
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	m := operation_setting.GetMonitorSetting()
	prevRate, prevMin, prevFloor, prevDead, prevStreak := m.ChannelFailureRateThreshold, m.ChannelFailureMinSamples, m.ChannelFailureAbsoluteFloor, m.ChannelFailureDeadFloor, m.ChannelFailureStreakFloor
	m.ChannelFailureRateThreshold, m.ChannelFailureMinSamples, m.ChannelFailureAbsoluteFloor, m.ChannelFailureDeadFloor, m.ChannelFailureStreakFloor = 0.5, 10, 20, 5, 3
	t.Cleanup(func() {
		common.RedisEnabled = prevRedis
		m.ChannelFailureRateThreshold, m.ChannelFailureMinSamples, m.ChannelFailureAbsoluteFloor, m.ChannelFailureDeadFloor, m.ChannelFailureStreakFloor = prevRate, prevMin, prevFloor, prevDead, prevStreak
	})

	reset := func() {
		channelFailureCounts.Delete(ch)
		channelSuccessCounts.Delete(ch)
		channelFailureStreaks.Delete(ch)
	}
	feedSuccess := func(n int) {
		for i := 0; i < n; i++ {
			RecordChannelSuccess(ch)
		}
	}
	// Interleaves a success after every failure, so the run never becomes a streak.
	// Models scattered faults on a channel that is otherwise serving traffic.
	feedScattered := func(n int) bool {
		var last bool
		for i := 0; i < n; i++ {
			last = RecordChannelFailure(ch, false)
			RecordChannelSuccess(ch)
		}
		return last
	}
	feedConsecutive := func(n int) bool {
		var last bool
		for i := 0; i < n; i++ {
			last = RecordChannelFailure(ch, false)
		}
		return last
	}

	// The regression this whole mechanism exists for: a real prod channel served
	// 3768 requests against 176 rate-limit faults (4.5%) and was pulled from
	// rotation 176 times under single-error disabling.
	t.Run("busiest channel survives its rate-limit share", func(t *testing.T) {
		reset()
		feedSuccess(3768)
		assert.False(t, feedScattered(176), "176 scattered fail / 3768+ ok = 4.5% -> keep")
	})

	// Interleaved 1:1 traffic can never reach a 50% failure share, so this feeds
	// two failures per success: enough to cross the rate bar without ever building
	// a 3-run streak, proving the rate gate still works on its own.
	t.Run("genuinely failing channel disables on rate", func(t *testing.T) {
		reset()
		feedSuccess(10)
		trip := false
		for i := 0; i < 40 && !trip; i++ {
			RecordChannelFailure(ch, false)
			trip = RecordChannelFailure(ch, false)
			if !trip {
				RecordChannelSuccess(ch)
			}
		}
		fails, oks := ChannelFailureWindow(ch)
		assert.True(t, trip, "2 fail per 1 ok crosses 50% past the floor -> disable (fail=%d ok=%d)", fails, oks)
	})

	// The regression the dead floor exists for: a dead paid channel on a
	// low-traffic model failed 1-6 times per minute with zero successes for hours,
	// but never accumulated 20 failures inside one fixed 10-minute counter window,
	// so the absolute floor was unreachable and it served errors forever. With the
	// streak floor at 3 the dead floor is now only reachable when successes exist.
	t.Run("dead channel with no successes trips the streak floor first", func(t *testing.T) {
		reset()
		assert.False(t, feedConsecutive(2), "below streak floor -> keep")
		assert.True(t, RecordChannelFailure(ch, false), "3rd consecutive failure -> disable")
	})

	// The fishx outage: three channels sat at fail=4 ok=0 on 502s, one short of the
	// dead floor, and stayed in rotation serving errors.
	t.Run("provider outage trips on the streak, not the window count", func(t *testing.T) {
		reset()
		assert.True(t, feedConsecutive(3), "3 consecutive 502s with no success -> disable")
	})

	// The incident this guards: a slow upstream runs several minute-long requests at
	// once, so timeouts land back-to-back with no success BETWEEN them and read as a
	// run even though the channel is serving everything else. Disabling on that
	// pulled 308 working glm channels and left 3 carrying the load.
	t.Run("a serving channel survives a concurrent timeout run", func(t *testing.T) {
		reset()
		feedSuccess(20)
		assert.False(t, feedConsecutive(5), "streak on a channel with successes -> keep, rate decides")
	})

	t.Run("a success breaks the streak", func(t *testing.T) {
		reset()
		require.False(t, feedConsecutive(2), "2 consecutive -> keep")
		feedSuccess(1)
		assert.False(t, feedConsecutive(2), "streak restarted by the success -> keep")
	})

	t.Run("a scattered burst below the floor never disables", func(t *testing.T) {
		reset()
		feedSuccess(2)
		assert.False(t, feedScattered(19), "19 fail / 21 ok under the floor -> keep")
	})

	// A capacity 429 is soft: it must skip every short-run gate, because a few
	// concurrent rate limits are not a dead lane. This is the churn the soft path
	// exists to avoid.
	t.Run("soft failures skip the streak and dead floors", func(t *testing.T) {
		reset()
		// Streak floor is 3 and the dead floor 5, so a hard failure would have been
		// pulled here; a run of capacity 429s must not be.
		for i := 1; i < 20; i++ {
			require.False(t, RecordChannelFailure(ch, true), "soft failure %d of 19, below the absolute floor -> keep", i)
		}
		// ...but a lane that only ever fails is still dead, and the rate gate says so
		// once the absolute floor is reached. This is the 13080 case: 5,548 failures,
		// zero successes, enabled the whole time.
		assert.True(t, RecordChannelFailure(ch, true), "20th soft failure at 100% -> disable")
	})

	// The regression: a7-3304 served a paying user at 59% failure for hours because
	// soft failures were never counted at all, so no gate could ever see them.
	t.Run("soft failures still reach the rate gate", func(t *testing.T) {
		reset()
		feedSuccess(10)
		trip := false
		for i := 0; i < 40 && !trip; i++ {
			RecordChannelFailure(ch, true)
			trip = RecordChannelFailure(ch, true)
			if !trip {
				RecordChannelSuccess(ch)
			}
		}
		fails, oks := ChannelFailureWindow(ch)
		assert.True(t, trip, "sustained 2 soft fail per 1 ok crosses 50% past the floor -> disable (fail=%d ok=%d)", fails, oks)
	})
}

// Credential faults must bypass the failure-rate window: a revoked key cannot
// recover, so waiting out a window only serves users errors.
func TestIsCredentialFault(t *testing.T) {
	assert.True(t, IsCredentialFault(types.NewOpenAIError(errors.New("unauthorized"),
		types.ErrorCodeChannelInvalidKey, http.StatusUnauthorized)), "401 is a dead credential")

	assert.True(t, IsCredentialFault(types.NewOpenAIError(errors.New("key revoked"),
		types.ErrorCodeChannelInvalidKey, http.StatusForbidden)), "403 channel:* is a dead credential")

	assert.False(t, IsCredentialFault(types.NewOpenAIError(errors.New("rate limited"),
		types.ErrorCodeRateLimitExceeded, http.StatusTooManyRequests)), "capacity 429 is transient")

	// A drained upstream wallet surfaces as 429 on some resellers but cannot clear
	// on its own, so it parks immediately instead of waiting out the failure window.
	assert.True(t, IsCredentialFault(types.NewOpenAIError(errors.New("Insufficient credits. Please top up your balance"),
		types.ErrorCodeChannelInvalidKey, http.StatusTooManyRequests)), "drained-quota 429 is a dead credential")

	assert.False(t, IsCredentialFault(types.NewOpenAIError(errors.New("bad gateway"),
		types.ErrorCodeBadResponse, http.StatusBadGateway)), "502 is transient")

	assert.False(t, IsCredentialFault(nil), "nil error is not a credential fault")

	// A stalled or empty upstream carries a 429 so the reason survives Cloudflare
	// and the chat frontends that discard 5xx bodies. It is still capacity, so it
	// must stay rate-gated rather than parking a shard that serves most requests.
	for _, code := range []types.ErrorCode{
		types.ErrorCodeChannelResponseTimeExceeded,
		types.ErrorCodeChannelEmptyResponse,
		"channel:stream_timeout_no_response",
	} {
		assert.False(t, IsCredentialFault(types.NewOpenAIError(errors.New("upstream saturated"),
			code, http.StatusTooManyRequests)), string(code)+" 429 is capacity, not a dead credential")
	}
}

// Request-side upstream faults must never auto-ban the channel, even when their
// status code sits in the configured disable ranges: the same request fails
// identically elsewhere (deterministic) or a laxer sibling accepts the prompt
// (moderation). A transient capacity 400 is the exception: it reports the
// CHANNEL's condition, so it must reach the caller's failure-rate guard, which
// spares a lane that is still serving and pulls one emitting nothing else.
func TestShouldDisableChannelSparesNonChannelFaults(t *testing.T) {
	prev := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	prevRanges := operation_setting.AutomaticDisableStatusCodeRanges
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{{Start: 400, End: 406}}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = prev
		operation_setting.AutomaticDisableStatusCodeRanges = prevRanges
	})

	cases := []struct {
		name      string
		err       *types.NewAPIError
		rateGated bool
	}{
		{"transient capacity 400", types.NewOpenAIError(
			errors.New("The current model cannot be routed at the moment, please try again later"),
			types.ErrorCodeBadResponse, http.StatusBadRequest), true},
		{"flapping reseller model-lost 400", types.NewOpenAIError(
			errors.New("Model name not specified, model name cannot be empty"),
			types.ErrorCodeBadResponse, http.StatusBadRequest), true},
		{"upstream moderation 400", types.NewOpenAIError(
			errors.New("Input data may contain inappropriate content"),
			types.ErrorCodeBadResponse, http.StatusBadRequest), false},
		{"deterministic malformed 400", types.NewOpenAIError(
			errors.New("missing required field messages"),
			types.ErrorCodeBadResponse, http.StatusBadRequest), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.rateGated, ShouldDisableChannel(tc.err))
		})
	}
}

// The upstream's text, not its status, decides failover and counting, and the
// text alone: a 400-masked "pinned merchant busy" fails over, a platform
// throttle counts nothing, a fused merchant disables now, and a generic rate
// limit is never a fault. These sentences mean the same thing whoever sends
// them, so no rule is scoped to a host. Unknown text falls back to the
// status-code rules.
func TestClassifyUpstreamError(t *testing.T) {
	mk := func(msg string, status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New(msg), types.ErrorCodeBadResponse, status)
	}
	cases := []struct {
		name string
		err  *types.NewAPIError
		want UpstreamClass
	}{
		{"marketplace pinned busy masked as 400", mk("固定商家当前不可用。 原因：您固定的商家当前处于繁忙、冷却、不可用或能力不匹配状态。", 400), UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}},
		{"marketplace platform throttle", mk("平台当前繁忙，请稍后重试。 原因：平台正在进行保护性限流或资源保护。", 503), UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true, Provider: true}},
		{"marketplace merchant fused", mk("固定商家当前不可用。 原因：该商家渠道近期连续失败，正处于熔断冷却保护中。", 503), UpstreamClass{Known: true, Failover: true, DisableNow: true}},
		{"marketplace price raised", mk("原因：该商家上调了价格，您的固定关系已被暂停", 402), UpstreamClass{Known: true, Failover: true, DisableNow: true}},
		{"horde rate limit", mk(`{"message":"2 per 1 second"}`, 429), UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}},
		{"any 429 is a limit", mk("shard exit at rph limit", 429), UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}},
		{"5xx counts and cools", mk("This model is currently experiencing high demand", 503), UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}},
		{"empty response cools", types.NewOpenAIError(errors.New("upstream streamed an empty response"), types.ErrorCodeChannelEmptyResponse, http.StatusBadGateway), UpstreamClass{Known: true, Failover: true, Count: CountNone, Cooldown: true}},
		{"unsupported parameter is lane-dead", mk("Model kinfra-text-embedding-4b does not support the requested parameter", 400), UpstreamClass{Known: true, Failover: true, Count: CountFailure}},
		{"audio model on the chat route is the caller's fault", mk("The model `whisper-large-v3-turbo` does not support chat completions", 400), UpstreamClass{Known: true, Failover: false, Count: CountNone}},
		{"unknown 504 counts and cools", mk("bad response status code 504", 504), UpstreamClass{Known: true, Failover: true, Count: CountFailure, Cooldown: true}},
		{"unknown 400 stays generic", mk("invalid model", 400), UpstreamClass{}},
		{"marketplace context over the merchant cap", mk("请求内容过大。 原因：本次上下文、附件或输出预算超过了可处理范围。", 413), UpstreamClass{Known: true, Failover: true, Count: CountNone, ContextCap: true}},
		{"any 413 teaches the cap", mk("request entity too large", 413), UpstreamClass{Known: true, Failover: true, Count: CountNone, ContextCap: true}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ClassifyUpstreamError(tc.err), tc.name)
	}
	local := types.NewError(errors.New("原因：平台正在进行保护性限流"), types.ErrorCodeGenRelayInfoFailed)
	assert.Equal(t, UpstreamClass{}, ClassifyUpstreamError(local), "local errors are never upstream classes")
}
