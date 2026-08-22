package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/common/limiter"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	ModelRequestRateLimitCountMark        = "MRRL"
	ModelRequestRateLimitSuccessCountMark = "MRRLS"
	modelRateLimitTimeFormat              = "2006-01-02T15:04:05.000Z"
)

// 检查Redis中的请求限制
// retryAfter is the seconds until the oldest recorded request slides out of the
// window (the real wait), 0 when allowed or unknown (caller falls back to the
// full window).
func checkRedisRateLimit(ctx context.Context, rdb *redis.Client, key string, maxCount int, duration int64) (bool, int64, error) {
	// 如果maxCount为0，表示不限制
	if maxCount == 0 {
		return true, 0, nil
	}

	// 获取当前计数
	length, err := rdb.LLen(ctx, key).Result()
	if err != nil {
		return false, 0, err
	}

	// 如果未达到限制，允许请求
	if length < int64(maxCount) {
		return true, 0, nil
	}

	// 检查时间窗口
	oldTimeStr, _ := rdb.LIndex(ctx, key, -1).Result()
	oldTime, err := time.Parse(modelRateLimitTimeFormat, oldTimeStr)
	if err != nil {
		return false, 0, err
	}

	nowTimeStr := time.Now().UTC().Format(modelRateLimitTimeFormat)
	nowTime, err := time.Parse(modelRateLimitTimeFormat, nowTimeStr)
	if err != nil {
		return false, 0, err
	}
	// 如果在时间窗口内已达到限制，拒绝请求
	subTime := nowTime.Sub(oldTime).Seconds()
	if int64(subTime) < duration {
		rdb.Expire(ctx, key, time.Duration(duration)*time.Second)
		remaining := duration - int64(subTime)
		if remaining < 1 {
			remaining = 1
		}
		return false, remaining, nil
	}

	return true, 0, nil
}

// 记录Redis请求
func recordRedisRequest(ctx context.Context, rdb *redis.Client, key string, maxCount int, duration int64) {
	// 如果maxCount为0，不记录请求
	if maxCount == 0 {
		return
	}

	now := time.Now().UTC().Format(modelRateLimitTimeFormat)
	rdb.LPush(ctx, key, now)
	rdb.LTrim(ctx, key, 0, int64(maxCount-1))
	rdb.Expire(ctx, key, time.Duration(duration)*time.Second)
}

// Redis限流处理器
func redisRateLimitHandler(duration int64, totalMaxCount, successMaxCount int) gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := strconv.Itoa(c.GetInt("id"))
		ctx := context.Background()
		rdb := common.RDB

		// 1. 检查成功请求数限制
		successKey := fmt.Sprintf("rateLimit:%s:%s", ModelRequestRateLimitSuccessCountMark, userId)
		allowed, retryAfter, err := checkRedisRateLimit(ctx, rdb, successKey, successMaxCount, duration)
		if err != nil {
			fmt.Println("failed to check success request rate limit:", err.Error())
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
			return
		}
		if !allowed {
			if retryAfter <= 0 {
				retryAfter = duration
			}
			c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, i18n.T(c, "rate_limit.reached", map[string]any{"Minutes": setting.ModelRequestRateLimitDurationMinutes, "Count": successMaxCount}))
			return
		}

		//2.检查总请求数限制并记录总请求（当totalMaxCount为0时会自动跳过，使用令牌桶限流器
		if totalMaxCount > 0 {
			totalKey := fmt.Sprintf("rateLimit:%s", userId)
			// 初始化
			tb := limiter.New(ctx, rdb)
			allowed, err = tb.Allow(
				ctx,
				totalKey,
				limiter.WithCapacity(int64(totalMaxCount)*duration),
				limiter.WithRate(int64(totalMaxCount)),
				limiter.WithRequested(duration),
			)

			if err != nil {
				fmt.Println("failed to check total request rate limit:", err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
				return
			}

			if !allowed {
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, i18n.T(c, "rate_limit.total_reached", map[string]any{"Minutes": setting.ModelRequestRateLimitDurationMinutes, "Count": totalMaxCount}))
			}
		}

		// 4. 处理请求
		c.Next()

		// 5. 如果请求成功，记录成功请求
		if c.Writer.Status() < 400 {
			recordRedisRequest(ctx, rdb, successKey, successMaxCount, duration)
		}
	}
}

// 内存限流处理器
func memoryRateLimitHandler(duration int64, totalMaxCount, successMaxCount int) gin.HandlerFunc {
	inMemoryRateLimiter.Init(inMemoryCleanupHorizon)

	return func(c *gin.Context) {
		userId := strconv.Itoa(c.GetInt("id"))
		totalKey := ModelRequestRateLimitCountMark + userId
		successKey := ModelRequestRateLimitSuccessCountMark + userId

		// 1. 检查总请求数限制（当totalMaxCount为0时跳过）
		if totalMaxCount > 0 && !inMemoryRateLimiter.Request(totalKey, totalMaxCount, duration) {
			c.Status(http.StatusTooManyRequests)
			c.Abort()
			return
		}

		// 2. 检查成功请求数限制
		// 使用一个临时key来检查限制，这样可以避免实际记录
		checkKey := successKey + "_check"
		if !inMemoryRateLimiter.Request(checkKey, successMaxCount, duration) {
			c.Status(http.StatusTooManyRequests)
			c.Abort()
			return
		}

		// 3. 处理请求
		c.Next()

		// 4. 如果请求成功，记录到实际的成功请求计数中
		if c.Writer.Status() < 400 {
			inMemoryRateLimiter.Request(successKey, successMaxCount, duration)
		}
	}
}

const (
	perModelRateLimitKeyMark      = "perModelRateLimitKey"
	perModelRateLimitMaxMark      = "perModelRateLimitMax"
	perModelRateLimitDurationMark = "perModelRateLimitDuration"
	perModelRateLimitMemberMark   = "perModelRateLimitMember"
	// In-memory cleanup horizon must cover the largest per-model window (Redis
	// unaffected); entries idle longer than this are evicted.
	inMemoryCleanupHorizon = 24 * time.Hour
)

// discountedDuration shortens a rate-limit window by pct percent, never below one
// second. Out-of-band values are clamped, not rejected: this runs per request, so
// a bad setting degrades to a sane window instead of failing the call.
func discountedDuration(duration int64, pct int) int64 {
	pct = types.ClampFreeRateLimitWindowPct(pct)
	if pct == 0 || duration <= 0 {
		return duration
	}
	discounted := duration * int64(100-pct) / 100
	if discounted < 1 {
		return 1
	}
	return discounted
}

// perModelRateLimit enforces a per-user, per-model success-count cap for the
// configured `:free` models. Returns false when the request was blocked (already
// aborted). Paid/small models (not in the map) return true unchanged. On allow it
// stashes the key/max so the post-handler records the request only on success.
func perModelRateLimit(c *gin.Context) bool {
	if !setting.HasModelRateLimits() {
		return true
	}
	// The ONLY exemption is the explicit per-user UnlimitedFreeModels grant.
	// No role or balance bypass: infrastructure accounts (guest-token owner,
	// autotest) get the grant instead, so every exemption is visible in the
	// user's setting and revocable from the admin drawer.
	userSetting, hasUserSetting := common.GetContextKeyType[types.UserSetting](c, constant.ContextKeyUserSetting)
	if hasUserSetting && userSetting.UnlimitedFreeModels {
		return true
	}
	// A full 100% window discount is no wait at all. Handled here rather than in
	// discountedDuration, which floors at one second and so could never express it.
	if hasUserSetting &&
		types.ClampFreeRateLimitWindowPct(userSetting.FreeRateLimitWindowPct) >= types.MaxFreeRateLimitWindowPct {
		return true
	}
	var mr ModelRequest
	if err := common.UnmarshalBodyReusable(c, &mr); err != nil || mr.Model == "" {
		return true
	}
	_, successMaxCount, windowMinutes, found := setting.GetModelRateLimit(mr.Model)
	if !found || successMaxCount <= 0 {
		return true
	}

	if windowMinutes <= 0 {
		windowMinutes = setting.ModelRequestRateLimitDurationMinutes
	}
	duration := int64(windowMinutes * 60)
	// Per-user discount (the server-tag perk). Shortens the WAIT rather than
	// raising the count, because most free models sit at 1 request per window
	// and a percentage off 1 is still 1.
	if hasUserSetting {
		duration = discountedDuration(duration, userSetting.FreeRateLimitWindowPct)
	}
	userId := strconv.Itoa(c.GetInt("id"))

	allowed := true
	// Real remaining wait from the limiter (0 = unknown -> full window fallback).
	var retryAfter int64
	// MODELW is a distinct namespace from the retired MODEL list keys: a ZADD
	// against a leftover list would fail WRONGTYPE.
	key := fmt.Sprintf("rateLimit:MODELW:%s:%s", userId, mr.Model)
	member := ""
	if common.RedisEnabled {
		// The slot is claimed HERE, not after the upstream answers. A check that
		// only records on completion leaves the whole upstream latency (17-56s on
		// free lanes) as a window where every concurrent request sees an empty
		// bucket, which let one account run 5 "1/min" requests in 8 seconds.
		member = fmt.Sprintf("%d:%s", time.Now().UnixNano(), common.GetRandomString(8))
		ctx := context.Background()
		ok, remaining, err := limiter.New(ctx, common.RDB).
			Reserve(ctx, key, successMaxCount, time.Duration(duration)*time.Second, member)
		if err == nil {
			allowed = ok
			retryAfter = int64(remaining.Seconds())
			if !ok && remaining > 0 && retryAfter < 1 {
				retryAfter = 1
			}
		}
	} else {
		// Already race-free: Request() checks and records under one mutex.
		key = fmt.Sprintf("rateLimit:MODEL:%s:%s", userId, mr.Model)
		inMemoryRateLimiter.Init(inMemoryCleanupHorizon)
		allowed = inMemoryRateLimiter.Request(key+"_check", successMaxCount, duration)
	}

	if !allowed {
		paidName := strings.TrimSuffix(mr.Model, ":free")
		if retryAfter <= 0 {
			retryAfter = duration
		}
		c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
		c.Header("X-RateLimit-Limit", strconv.Itoa(successMaxCount))
		c.Header("X-RateLimit-Remaining", "0")
		c.Header("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix()+retryAfter, 10))
		// Report the window actually enforced, not the configured one: a
		// discounted account limited to 45s would otherwise be told "every 1 min".
		windowLabel := fmt.Sprintf("%v min", duration/60)
		if duration%60 != 0 {
			windowLabel = fmt.Sprintf("%vs", duration)
		}
		msg := fmt.Sprintf("Too many requests. The free tier allows %v request(s) every %v per account on %v - nothing is used up, retry in %vs. The paid %v has no per-minute limit.",
			successMaxCount, windowLabel, mr.Model, retryAfter, paidName)
		// Surface the rejection in the usage logs (aborting here skips the
		// relay's own error logging entirely). DB guard keeps unit tests DB-free.
		if model.DB != nil {
			group := c.GetString("group")
			// Rate limiting runs before the distributor selects a channel, so attach
			// the channel the request would have been routed to for log attribution.
			channelId := 0
			if ch := model.GetChannelForLog(group, mr.Model, c.Request.URL.Path); ch != nil {
				channelId = ch.Id
			}
			model.RecordErrorLog(c, c.GetInt("id"), channelId, mr.Model,
				c.GetString("token_name"), msg, c.GetInt("token_id"), 0, false,
				group, 0, map[string]interface{}{
					"status_code": http.StatusTooManyRequests,
					"retry_after": retryAfter,
				})
		}
		abortWithOpenAiMessage(c, http.StatusTooManyRequests, msg)
		return false
	}

	c.Set(perModelRateLimitKeyMark, key)
	c.Set(perModelRateLimitMaxMark, successMaxCount)
	c.Set(perModelRateLimitDurationMark, duration)
	c.Set(perModelRateLimitMemberMark, member)
	return true
}

// settlePerModelRequest finalizes the slot taken at admission: a failed upstream
// call releases it, so failures stay free without letting concurrent requests
// through. A success needs no write, because the reservation IS the record.
func settlePerModelRequest(c *gin.Context) {
	key := c.GetString(perModelRateLimitKeyMark)
	maxCount := c.GetInt(perModelRateLimitMaxMark)
	if key == "" || maxCount <= 0 {
		return
	}
	failed := c.Writer.Status() >= 400
	if common.RedisEnabled {
		member := c.GetString(perModelRateLimitMemberMark)
		if failed && member != "" {
			ctx := context.Background()
			_ = limiter.New(ctx, common.RDB).Release(ctx, key, member)
		}
		return
	}
	if failed {
		return
	}
	duration := c.GetInt64(perModelRateLimitDurationMark)
	if duration <= 0 {
		duration = int64(setting.ModelRequestRateLimitDurationMinutes * 60)
	}
	inMemoryRateLimiter.Request(key, maxCount, duration)
}

// ModelRequestRateLimit 模型请求限流中间件
func ModelRequestRateLimit() func(c *gin.Context) {
	return func(c *gin.Context) {
		// 在每个请求时检查是否启用限流
		if !setting.ModelRequestRateLimitEnabled {
			c.Next()
			return
		}

		if !perModelRateLimit(c) {
			return
		}
		defer settlePerModelRequest(c)

		// 计算限流参数
		duration := int64(setting.ModelRequestRateLimitDurationMinutes * 60)
		totalMaxCount := setting.ModelRequestRateLimitCount
		successMaxCount := setting.ModelRequestRateLimitSuccessCount

		// 获取分组
		group := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		if group == "" {
			group = common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		}

		//获取分组的限流配置
		groupTotalCount, groupSuccessCount, found := setting.GetGroupRateLimit(group)
		if found {
			totalMaxCount = groupTotalCount
			successMaxCount = groupSuccessCount
		}

		// 根据存储类型选择并执行限流处理器
		if common.RedisEnabled {
			redisRateLimitHandler(duration, totalMaxCount, successMaxCount)(c)
		} else {
			memoryRateLimitHandler(duration, totalMaxCount, successMaxCount)(c)
		}
	}
}
