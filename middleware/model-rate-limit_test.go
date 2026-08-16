package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetModelRateLimit(t *testing.T) {
	require.NoError(t, setting.UpdateModelRequestRateLimitModelsByJSONString(`{"kimi-k2.6:free":[0,20],"glm-4.5-flash:free":[5,15],"z-image:free":[0,1,60]}`))

	total, success, window, found := setting.GetModelRateLimit("kimi-k2.6:free")
	assert.True(t, found)
	assert.Equal(t, 0, total)
	assert.Equal(t, 20, success)
	assert.Equal(t, 0, window) // 2-tuple -> no custom window

	_, _, window, found = setting.GetModelRateLimit("z-image:free")
	assert.True(t, found)
	assert.Equal(t, 60, window) // 3-tuple carries the window

	_, _, _, found = setting.GetModelRateLimit("kimi-k2.6") // paid twin not in map
	assert.False(t, found)

	_, _, _, found = setting.GetModelRateLimit("gpt-5.5")
	assert.False(t, found)
}

// A model with a custom windowMinutes gets an independent bucket + longer
// Retry-After than the 1-min default.
func TestPerModelRateLimitCustomWindow(t *testing.T) {
	require.NoError(t, setting.UpdateModelRequestRateLimitModelsByJSONString(`{"slow-model:free":[0,1,60]}`))
	setting.ModelRequestRateLimitDurationMinutes = 1
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = prevRedis })
	require.NoError(t, i18n.Init())

	c1, _ := newPerModelCtx(920001, common.RoleCommonUser, 0, types.UserSetting{}, "slow-model:free")
	assert.True(t, perModelRateLimit(c1))
	c2, w := newPerModelCtx(920001, common.RoleCommonUser, 0, types.UserSetting{}, "slow-model:free")
	assert.False(t, perModelRateLimit(c2))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	// 60-min window -> Retry-After well above the 60s the default window would give.
	ra, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	assert.Greater(t, ra, 60)
}

// newPerModelCtx builds a POST ctx with the model body + user identity the
// per-model limiter reads. recorder returned for status/header assertions.
func newPerModelCtx(userId, role, quota int, userSetting types.UserSetting, model string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"` + model + `"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", userId)
	c.Set(string(constant.ContextKeyUserRole), role)
	c.Set(string(constant.ContextKeyUserQuota), quota)
	c.Set(string(constant.ContextKeyUserSetting), userSetting)
	c.Set(string(constant.ContextKeyUserCreatedAt), time.Now().Unix()-30*86400)
	c.Set(string(constant.ContextKeyUserUsedQuota), 10_000_000)
	return c, w
}

// Contract: nobody bypasses the per-model free limits by balance anymore; only
// admins and users with the per-user UnlimitedFreeModels grant are exempt.
func TestPerModelRateLimitBypassSemantics(t *testing.T) {
	require.NoError(t, setting.UpdateModelRequestRateLimitModelsByJSONString(`{"limited-model:free":[0,1]}`))
	setting.ModelRequestRateLimitDurationMinutes = 1
	// RedisEnabled defaults true; no Redis in tests -> force the in-memory limiter.
	prevRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = prevRedis })
	// The blocked path renders an i18n 429 message.
	require.NoError(t, i18n.Init())

	run := func(userId, role, quota int, s types.UserSetting) (first, second bool, w2 *httptest.ResponseRecorder) {
		c1, _ := newPerModelCtx(userId, role, quota, s, "limited-model:free")
		first = perModelRateLimit(c1)
		c2, w := newPerModelCtx(userId, role, quota, s, "limited-model:free")
		second = perModelRateLimit(c2)
		return first, second, w
	}

	t.Run("zero-balance user hits the limit", func(t *testing.T) {
		first, second, w2 := run(910001, common.RoleCommonUser, 0, types.UserSetting{})
		assert.True(t, first)
		assert.False(t, second)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
		assert.NotEmpty(t, w2.Header().Get("Retry-After"))
	})

	t.Run("funded user no longer bypasses", func(t *testing.T) {
		first, second, w2 := run(910002, common.RoleCommonUser, 5_000_000, types.UserSetting{})
		assert.True(t, first)
		assert.False(t, second)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})

	t.Run("unlimited-free grant bypasses", func(t *testing.T) {
		first, second, _ := run(910003, common.RoleCommonUser, 0, types.UserSetting{UnlimitedFreeModels: true})
		assert.True(t, first)
		assert.True(t, second)
	})

	t.Run("admin no longer bypasses (guest token abuse)", func(t *testing.T) {
		first, second, w2 := run(910004, common.RoleAdminUser, 0, types.UserSetting{})
		assert.True(t, first)
		assert.False(t, second)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})
}

// Regression: the limiter used to only CHECK on admission and record after the
// upstream answered, so every request that arrived during a slow call saw an
// empty bucket. One account ran five "1/min" requests in eight seconds that way.
// Concurrent callers must now collapse to exactly one admission, and a failed
// request must give its slot back so failures stay free.
func TestPerModelRateLimitRedisConcurrency(t *testing.T) {
	require.NoError(t, setting.UpdateModelRequestRateLimitModelsByJSONString(`{"limited-model:free":[0,1]}`))
	setting.ModelRequestRateLimitDurationMinutes = 1
	require.NoError(t, i18n.Init())

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	prevRedis, prevRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, rdb
	t.Cleanup(func() { common.RedisEnabled, common.RDB = prevRedis, prevRDB })

	// The bug was a GAP, not a data race: admission passed while an earlier
	// request was still in flight (nothing recorded until it finished). So the
	// test admits one request and, WITHOUT settling it, checks that the next
	// caller is refused - which is exactly what record-on-completion allowed.
	t.Run("a request in flight holds the slot", func(t *testing.T) {
		inFlight, _ := newPerModelCtx(920001, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
		require.True(t, perModelRateLimit(inFlight))

		for i := 0; i < 4; i++ {
			c, w := newPerModelCtx(920001, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
			assert.False(t, perModelRateLimit(c), "request %d slipped through while one was in flight", i)
			assert.Equal(t, http.StatusTooManyRequests, w.Code)
		}
	})

	t.Run("parallel arrivals collapse to one admission", func(t *testing.T) {
		const parallel = 8
		var wg sync.WaitGroup
		var admitted atomic.Int32
		for i := 0; i < parallel; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, _ := newPerModelCtx(920004, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
				if perModelRateLimit(c) {
					admitted.Add(1)
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int32(1), admitted.Load())
	})

	t.Run("a failed request releases its slot", func(t *testing.T) {
		c1, _ := newPerModelCtx(920002, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
		require.True(t, perModelRateLimit(c1))
		c1.Writer.WriteHeader(http.StatusBadGateway)
		settlePerModelRequest(c1)

		c2, _ := newPerModelCtx(920002, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
		assert.True(t, perModelRateLimit(c2), "an upstream failure must not consume the user's slot")
	})

	t.Run("a successful request keeps its slot", func(t *testing.T) {
		c1, _ := newPerModelCtx(920003, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
		require.True(t, perModelRateLimit(c1))
		settlePerModelRequest(c1)

		c2, w2 := newPerModelCtx(920003, common.RoleCommonUser, 0, types.UserSetting{}, "limited-model:free")
		assert.False(t, perModelRateLimit(c2))
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
		assert.NotEmpty(t, w2.Header().Get("Retry-After"))
	})
}
