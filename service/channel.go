package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

// Disabling on the FIRST empty upstream response false-bans healthy channels on
// transient reseller hiccups, but never disabling lets a dead-but-enabled channel
// spam errors forever (the autotest cron only probes DISABLED channels, so live
// disable is the only removal path). Decision is rate-based: a channel is disabled
// once its empty responses are a large enough SHARE of its traffic in the window,
// so a high-volume healthy channel is not false-banned by scattered empties while
// one heavy user cannot cascade the whole pool. A fully dead channel (no successes)
// still trips the absolute floor. Both counters live in Redis so they aggregate
// across swarm replicas; an in-process map is the single-instance fallback.
const emptyResponseCounterTTL = 10 * time.Minute

var emptyResponseFailCounts sync.Map // channelId -> emptyResponseWindow, fallback when Redis is unavailable
var channelSuccessCounts sync.Map    // channelId -> emptyResponseWindow, fallback when Redis is unavailable
var channelFailureCounts sync.Map    // channelId -> emptyResponseWindow, fallback when Redis is unavailable
var channelFailureStreaks sync.Map   // channelId -> int, fallback when Redis is unavailable

type emptyResponseWindow struct {
	count int
	start time.Time
}

func emptyResponseCounterKey(channelId int) string {
	return fmt.Sprintf("empty_response_fail:%d", channelId)
}

func channelSuccessCounterKey(channelId int) string {
	return fmt.Sprintf("channel_success:%d", channelId)
}

func channelFailureCounterKey(channelId int) string {
	return fmt.Sprintf("channel_fail:%d", channelId)
}

func channelFailureStreakKey(channelId int) string {
	return fmt.Sprintf("channel_fail_streak:%d", channelId)
}

// bumpWindowCounter increments a fixed-window Redis counter, arming the TTL on the
// first hit so the count self-expires, and returns the new value. Falls back to an
// in-process fixed window when Redis is unavailable.
func bumpWindowCounter(key string, fallback *sync.Map, id any) int {
	if common.RedisEnabled {
		ctx := context.Background()
		n, err := common.RDB.Incr(ctx, key).Result()
		if err == nil {
			if n == 1 {
				common.RDB.Expire(ctx, key, emptyResponseCounterTTL)
			}
			return int(n)
		}
		common.SysError("window counter Redis incr failed: " + err.Error())
	}
	now := time.Now()
	next := emptyResponseWindow{count: 1, start: now}
	if v, ok := fallback.Load(id); ok {
		prev := v.(emptyResponseWindow)
		if now.Sub(prev.start) < emptyResponseCounterTTL {
			next = emptyResponseWindow{count: prev.count + 1, start: prev.start}
		}
	}
	fallback.Store(id, next)
	return next.count
}

// readWindowCounter returns the current value of a fixed-window counter without
// mutating it (Redis GET, or the live in-process window).
func readWindowCounter(key string, fallback *sync.Map, id any) int {
	if common.RedisEnabled {
		ctx := context.Background()
		n, err := common.RDB.Get(ctx, key).Int()
		if err == nil {
			return n
		}
		return 0 // redis.Nil (no key) or parse error -> treat as empty window
	}
	if v, ok := fallback.Load(id); ok {
		w := v.(emptyResponseWindow)
		if time.Since(w.start) < emptyResponseCounterTTL {
			return w.count
		}
	}
	return 0
}

// RecordChannelSuccess bumps the per-channel success counter in the same window as
// the empty-response counter, feeding the rate-based disable decision.
// bumpFailureStreak increments the consecutive-failure counter and returns the new
// value. Unlike the window counters this has no fixed window: it is cleared by the
// next success, so its value is the length of the current unbroken failure run. The
// TTL only stops an abandoned streak from living forever.
func bumpFailureStreak(channelId int) int {
	if common.RedisEnabled {
		ctx := context.Background()
		n, err := common.RDB.Incr(ctx, channelFailureStreakKey(channelId)).Result()
		if err == nil {
			if n == 1 {
				common.RDB.Expire(ctx, channelFailureStreakKey(channelId), emptyResponseCounterTTL)
			}
			return int(n)
		}
		common.SysError("failure streak Redis incr failed: " + err.Error())
	}
	next := 1
	if v, ok := channelFailureStreaks.Load(channelId); ok {
		if prev, ok := v.(int); ok {
			next = prev + 1
		}
	}
	channelFailureStreaks.Store(channelId, next)
	return next
}

func resetFailureStreak(channelId int) {
	if common.RedisEnabled {
		common.RDB.Del(context.Background(), channelFailureStreakKey(channelId))
	}
	channelFailureStreaks.Delete(channelId)
}

func RecordChannelSuccess(channelId int) {
	bumpWindowCounter(channelSuccessCounterKey(channelId), &channelSuccessCounts, channelId)
	// Any success breaks the run, so the streak must not survive it.
	resetFailureStreak(channelId)
}

// RecordEmptyResponseFailure bumps the empty-response counter for a channel and
// reports whether it has reached the disable decision. Rate-based: disable when
// empties are >= EmptyResponseRateThreshold of total traffic over at least
// EmptyResponseMinSamples requests, OR empties alone reach EmptyResponseAbsoluteFloor
// (a fully dead channel with no successes). Fixed window: TTL is armed on the first
// empty, so counts self-expire and no success-path reset is needed.
func RecordEmptyResponseFailure(channelId int) bool {
	empties := bumpWindowCounter(emptyResponseCounterKey(channelId), &emptyResponseFailCounts, channelId)
	successes := readWindowCounter(channelSuccessCounterKey(channelId), &channelSuccessCounts, channelId)

	m := operation_setting.GetMonitorSetting()
	floor := m.EmptyResponseAbsoluteFloor
	if floor <= 0 {
		floor = 5
	}
	// The floor is the DEAD-channel signal, so it only applies while the channel has
	// answered nothing. Applied unconditionally it pulls a busy channel for its
	// ordinary share of empties: a slow upstream returns a few every window no
	// matter how much it is serving, and at a floor of 3-5 that is minutes of
	// traffic, not a fault. Once there are successes the rate below decides.
	if empties >= floor && successes == 0 {
		return true
	}
	if m.EmptyResponseMinSamples <= 0 || m.EmptyResponseRateThreshold <= 0 {
		return false // rate disabled/unconfigured: floor is the only trigger
	}
	total := empties + successes
	if total < m.EmptyResponseMinSamples {
		return false
	}
	return float64(empties)/float64(total) >= m.EmptyResponseRateThreshold
}

// transientCapacityCodes are channel:* faults that report a saturated or stalled
// upstream, not a dead credential. They carry a 429 so the reason survives
// Cloudflare and the chat frontends that discard 5xx bodies, which would
// otherwise read as a spent quota here and disable the channel on first sight.
// They stay rate-gated: a shard that stalls one request while serving the rest
// must not be pulled.
var transientCapacityCodes = map[types.ErrorCode]struct{}{
	types.ErrorCodeChannelResponseTimeExceeded: {},
	types.ErrorCodeChannelEmptyResponse:        {},
	"channel:stream_timeout_no_response":       {},
}

func isTransientCapacityCode(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	_, exists := transientCapacityCodes[err.GetErrorCode()]
	return exists
}

// IsCredentialFault reports whether an error means the channel's credential is
// dead rather than the upstream having a bad moment. These bypass the failure-rate
// window and disable on first sight: waiting out a window on a revoked key only
// serves users errors for requests that cannot succeed.
func IsCredentialFault(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.StatusCode == http.StatusUnauthorized {
		return true
	}
	// A 429 is normally capacity and must stay rate-gated, but a drained wallet or
	// spent free quota is reclassified to a channel:* code upstream of here and
	// cannot clear on its own, so it parks immediately like any dead credential.
	if isTransientCapacityCode(err) {
		return false
	}
	return (err.StatusCode == http.StatusForbidden || err.StatusCode == http.StatusTooManyRequests) &&
		types.IsChannelError(err)
}

// RecordChannelFailure bumps the per-channel failure counter and reports whether the
// channel has failed enough of its recent traffic to be disabled. Rate first: a
// channel is only pulled when failures are a large SHARE of its window, so the
// busiest lanes are not banned for the scattered 429s that come with carrying the
// most traffic. The absolute floor applies only once a channel has piled up failures
// with no successes to show for them, which is what a genuinely dead lane looks like.
func RecordChannelFailure(channelId int) bool {
	failures := bumpWindowCounter(channelFailureCounterKey(channelId), &channelFailureCounts, channelId)
	successes := readWindowCounter(channelSuccessCounterKey(channelId), &channelSuccessCounts, channelId)

	m := operation_setting.GetMonitorSetting()
	// An unbroken run of failures is the cheapest strong signal that the upstream is
	// down, and it is independent of the window counts - a trickle spread thin never
	// reaches a count floor.
	//
	// It is NOT evidence on a channel that is still serving. Concurrency makes a
	// "streak" only mean N failures landed with no success BETWEEN them, which a
	// slow upstream produces constantly: a minute-long GLM completion overlaps
	// several others, so three timeouts in flight together read as a run while the
	// channel answers everything else. That pulled 308 working glm channels and
	// left 3 to carry the load. Once the window holds real successes the rate gate
	// below is the honest measure, so the streak only decides when there is nothing
	// to compare against.
	streak := bumpFailureStreak(channelId)
	streakFloor := m.ChannelFailureStreakFloor
	if streakFloor <= 0 {
		streakFloor = 3
	}
	if streak >= streakFloor && successes == 0 {
		return true
	}
	// A channel with no successes at all has nothing to compute a rate against, so a
	// small floor is the only signal. It must be reachable inside one fixed counter
	// window: a dead channel on a low-traffic model may only fail a few times per
	// window and would otherwise stay enabled forever, serving every request an error.
	if successes == 0 {
		deadFloor := m.ChannelFailureDeadFloor
		if deadFloor <= 0 {
			deadFloor = 5
		}
		return failures >= deadFloor
	}
	floor := m.ChannelFailureAbsoluteFloor
	if floor <= 0 {
		floor = 20
	}
	if failures < floor {
		return false
	}
	if m.ChannelFailureMinSamples <= 0 || m.ChannelFailureRateThreshold <= 0 {
		return false
	}
	total := failures + successes
	if total < m.ChannelFailureMinSamples {
		return false
	}
	return float64(failures)/float64(total) >= m.ChannelFailureRateThreshold
}

// ChannelFailureWindow returns the channel's current failure and success counts,
// for logging why a disable decision went the way it did.
func ChannelFailureWindow(channelId int) (failures int, successes int) {
	return readWindowCounter(channelFailureCounterKey(channelId), &channelFailureCounts, channelId),
		readWindowCounter(channelSuccessCounterKey(channelId), &channelSuccessCounts, channelId)
}

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string, opts ...model.ChannelStatusChangeOpt) {
	fails, oks := ChannelFailureWindow(channelError.ChannelId)
	common.SysLog(fmt.Sprintf("Channel \"%s\" (#%d) encountered an error, preparing to disable (window fail=%d ok=%d). Reason: %s", channelError.ChannelName, channelError.ChannelId, fails, oks, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("Channel \"%s\" (#%d) does not have auto-disable enabled, skipping disable operation", channelError.ChannelName, channelError.ChannelId))
		return
	}

	success := model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason, opts...)
	if success && operation_setting.GetMonitorSetting().ChannelStatusNotifyEnabled {
		subject := fmt.Sprintf("Channel \"%s\" (#%d) has been disabled", channelError.ChannelName, channelError.ChannelId)
		content := fmt.Sprintf("Channel \"%s\" (#%d) has been disabled. Reason: %s", channelError.ChannelName, channelError.ChannelId, reason)
		NotifyRootUser(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), subject, content)
	}
}

func EnableChannel(channelId int, usingKey string, channelName string, opts ...model.ChannelStatusChangeOpt) {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "", opts...)
	if success && operation_setting.GetMonitorSetting().ChannelStatusNotifyEnabled {
		subject := fmt.Sprintf("Channel \"%s\" (#%d) has been enabled", channelName, channelId)
		content := fmt.Sprintf("Channel \"%s\" (#%d) has been enabled", channelName, channelId)
		NotifyRootUser(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content)
	}
}

func ShouldDisableChannel(err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	if err == nil {
		return false
	}
	// An explicitly non-disabling fault (e.g. an empty stream that failed over cleanly):
	// never auto-ban; a genuinely dead channel is still caught by the scheduled autotest.
	if types.IsSkipDisableError(err) {
		return false
	}
	if types.IsChannelError(err) {
		return true
	}
	// A local new_api_error (our own sensitive-word filter, request-build failure,
	// quota gate, etc.) never reached upstream, so the channel is blameless. Never
	// auto-ban a healthy channel for a request WE rejected locally.
	if err.GetErrorType() == types.ErrorTypeNewAPIError {
		return false
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	// Content/policy-driven rejections (safety, content_filter, invalid_argument,
	// etc.) are deterministic, not per-channel faults. Skipping retry without
	// skipping disable would still auto-ban a healthy channel on a bad request.
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) {
		return false
	}
	// Not the channel's fault - never auto-ban a healthy channel, even when the
	// status code is in the disable ranges and the upstream's error code did not
	// normalize into alwaysSkipRetryCodes:
	//   - deterministic upstream 400/415/422/451: request-side fault, fails the same
	//     on every channel;
	//   - per-upstream content moderation (400/422): caused by the client's prompt,
	//     fails over to a less strict sibling;
	//   - transient upstream 400 ("degraded", "retry later", "try again"): capacity
	//     blip, fails over to a sibling and the channel recovers on its own.
	if types.IsDeterministicUpstreamError(err) ||
		types.IsUpstreamModerationError(err) ||
		types.IsSharedFilterModerationError(err) ||
		types.IsInvalidParamError(err) ||
		types.IsTransientUpstream400(err) {
		return false
	}
	// "User has been banned" from an upstream that runs new-api is ambiguous, and
	// the wording is identical either way: it means one END USER of ours tripped
	// their rules (channel healthy - banning it would let a single banned user walk
	// the whole pool down), or OUR ACCOUNT there is banned (channel permanently
	// dead). Only behaviour separates them, so fall through to the caller's
	// failure-RATE guard instead of returning false here: a channel still serving
	// others keeps its successes and survives the rate gate, while a dead account
	// trips the zero-success floor. Excluding it outright left 3 cent channels
	// enabled on 4,289 errors and zero successes.
	// A 403 reaching here is a channel-side fault in ~90% of production cases (a
	// drained upstream wallet, a group the key cannot access, an expired trial, a
	// plan that does not cover the model), and none of those clear on their own.
	// ShouldDisableByStatusCode already returns true for it; the caller's
	// failure-RATE guard is what keeps a one-off refusal from pulling a busy lane.
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
