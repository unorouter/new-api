package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

var freeAbuseLimiter common.InMemoryRateLimiter
var freeAbuseDayLimiter common.InMemoryRateLimiter
var freeAbuseErrLimiter common.InMemoryRateLimiter

// freeModelSets tracks, per user, the distinct free models seen within the current
// window on this instance (single-instance fallback when Redis is off).
var freeModelSets = struct {
	sync.Mutex
	m map[int]map[string]int64
}{m: make(map[int]map[string]int64)}

// freeMediaErrModelSets mirrors freeModelSets but for distinct FAILING free media
// models per user per window (single-instance fallback when Redis is off).
var freeMediaErrModelSets = struct {
	sync.Mutex
	m map[int]map[string]int64
}{m: make(map[int]map[string]int64)}

const freeAbuseWindowSeconds = 60
const freeAbuseDaySeconds = 86400
const freeAbuseErrWindowSeconds = 3600

// TrackFreeModelUsage records a single free-model request for a user and auto-sets
// BlockFreeWhenNoQuota when the user's balance is non-positive AND either signal
// trips: the per-minute request count exceeds FreeAbuseMaxPerMinute, or the number
// of DISTINCT free models hit in the window exceeds FreeAbuseMaxDistinctModels
// (fast model-switching = scraping). No-op when the global auto-block setting is
// disabled. The flag clears automatically on the next quota top-up
// (see model.IncreaseUserQuota).
func TrackFreeModelUsage(userId int, userQuota int, modelName string) {
	setting := operation_setting.GetQuotaSetting()
	if !setting.EnableFreeAbuseAutoBlock {
		return
	}
	maxPerMin := setting.FreeAbuseMaxPerMinute

	over := maxPerMin > 0 && recordFreeUsage(userId, maxPerMin)

	maxDistinct := setting.FreeAbuseMaxDistinctModels
	tooManyModels := maxDistinct > 0 && modelName != "" &&
		recordDistinctFreeModel(userId, modelName) > maxDistinct

	// Slow-but-relentless scraper: paces under the per-minute limits but racks up
	// thousands of free requests per DAY. Per-minute windows never catch it; the
	// daily counter does.
	maxPerDay := setting.FreeAbuseMaxPerDay
	overDaily := maxPerDay > 0 &&
		recordWindowedUsage("freeAbuseDay", &freeAbuseDayLimiter, userId, maxPerDay, freeAbuseDaySeconds)

	if (!over && !tooManyModels && !overDaily) || userQuota > 0 {
		return
	}

	gopool.Go(func() {
		autoBlockUser(userId)
	})
}

// TrackFreeModelError records a FAILED free-model request. A user hammering a
// disabled or nonexistent model retries relentlessly (a human gives up), so a
// high hourly error count at non-positive balance is a strong bot signal. Blocks
// when FreeAbuseMaxErrorsPerHour is exceeded. The caller must exclude transient
// upstream infra faults (5xx/429/timeout) before calling: those are our capacity
// failing, not abuse, and would otherwise auto-block heavy legit users during an
// upstream outage.
//
// isMedia marks image/audio/video requests. Catalog-probe scrapers scan many
// distinct free MEDIA models that mostly 404/400 (no legit user fires 3+ failing
// image gens back-to-back), so a separate, much smaller distinct-failing-media
// threshold catches the probe shape regardless of volume - it trips well under
// the per-minute/per-day/hourly-error counts a paced scraper stays beneath.
// Text models are exempt (users legitimately try several chat models). No-op when
// disabled or quota > 0.
func TrackFreeModelError(userId int, userQuota int, modelName string, isMedia bool) {
	if userId <= 0 || userQuota > 0 {
		return
	}
	setting := operation_setting.GetQuotaSetting()
	if !setting.EnableFreeAbuseAutoBlock {
		return
	}

	block := false

	maxErr := setting.FreeAbuseMaxErrorsPerHour
	if maxErr > 0 && recordWindowedUsage("freeAbuseErr", &freeAbuseErrLimiter, userId, maxErr, freeAbuseErrWindowSeconds) {
		block = true
	}

	maxMediaErr := setting.FreeAbuseMaxMediaErrModels
	if isMedia && maxMediaErr > 0 && modelName != "" &&
		recordDistinctMediaErrModel(userId, modelName) > maxMediaErr {
		block = true
	}

	if !block {
		return
	}
	gopool.Go(func() {
		autoBlockUser(userId)
	})
}

// recordWindowedUsage increments a per-user counter under keyPrefix for the given
// window and reports whether it now exceeds max. Redis-backed (cross-instance)
// with an in-memory sliding-window fallback, mirroring recordFreeUsage.
func recordWindowedUsage(keyPrefix string, mem *common.InMemoryRateLimiter, userId int, max int, windowSeconds int) bool {
	if common.RedisEnabled {
		ctx := context.Background()
		key := fmt.Sprintf("%s:user:%d", keyPrefix, userId)
		count, err := common.RDB.Incr(ctx, key).Result()
		if err != nil {
			common.SysLog(keyPrefix + " counter incr failed: " + err.Error())
			return false
		}
		if count == 1 {
			common.RDB.Expire(ctx, key, time.Duration(windowSeconds)*time.Second)
		}
		return count > int64(max)
	}
	key := fmt.Sprintf("%s:user:%d", keyPrefix, userId)
	mem.Init(time.Duration(windowSeconds) * time.Second)
	allowed := mem.Request(key, max, int64(windowSeconds))
	return !allowed
}

// recordDistinctFreeModel adds modelName to the user's per-window model set and
// returns the current distinct-model count. Uses a Redis SET when enabled
// (cross-instance), otherwise an in-memory per-model sliding window whose
// saturation count approximates the distinct set on a single instance.
func recordDistinctFreeModel(userId int, modelName string) int {
	return recordDistinctModelSet("freeAbuseModels", &freeModelSets, userId, modelName)
}

// recordDistinctMediaErrModel tracks distinct FAILING free media models per user
// per window under its own key, so it never mixes with the all-request distinct
// set above.
func recordDistinctMediaErrModel(userId int, modelName string) int {
	return recordDistinctModelSet("freeAbuseMediaErrModels", &freeMediaErrModelSets, userId, modelName)
}

// recordDistinctModelSet adds modelName to the user's per-window set under
// keyPrefix and returns the current distinct count. Uses a Redis SET when enabled
// (cross-instance), otherwise the given in-memory per-model sliding window whose
// live entry count approximates the distinct set on a single instance.
func recordDistinctModelSet(keyPrefix string, sets *struct {
	sync.Mutex
	m map[int]map[string]int64
}, userId int, modelName string) int {
	if common.RedisEnabled {
		ctx := context.Background()
		key := fmt.Sprintf("%s:user:%d", keyPrefix, userId)
		count, err := common.RDB.SAdd(ctx, key, modelName).Result()
		if err != nil {
			common.SysLog(keyPrefix + " model-set add failed: " + err.Error())
			return 0
		}
		if count > 0 {
			// (re)arm the window expiry on first membership change
			common.RDB.Expire(ctx, key, freeAbuseWindowSeconds*time.Second)
		}
		total, err := common.RDB.SCard(ctx, key).Result()
		if err != nil {
			return 0
		}
		return int(total)
	}

	// In-memory fallback: per-user map of model -> last-seen unix ts. Prune entries
	// older than the window, add this model, return the live distinct count.
	now := time.Now().Unix()
	sets.Lock()
	defer sets.Unlock()
	models := sets.m[userId]
	if models == nil {
		models = make(map[string]int64)
		sets.m[userId] = models
	}
	for name, ts := range models {
		if now-ts >= freeAbuseWindowSeconds {
			delete(models, name)
		}
	}
	models[modelName] = now
	return len(models)
}

// recordFreeUsage increments the per-user request counter for the current window
// and reports whether the count has exceeded maxPerMin. Uses Redis when enabled
// (cross-instance), otherwise an in-memory sliding window (single instance).
func recordFreeUsage(userId int, maxPerMin int) bool {
	if common.RedisEnabled {
		ctx := context.Background()
		key := fmt.Sprintf("freeAbuse:user:%d", userId)
		count, err := common.RDB.Incr(ctx, key).Result()
		if err != nil {
			common.SysLog("free abuse counter incr failed: " + err.Error())
			return false
		}
		if count == 1 {
			common.RDB.Expire(ctx, key, freeAbuseWindowSeconds*time.Second)
		}
		return count > int64(maxPerMin)
	}

	// In-memory fallback: Request returns false once the window is saturated.
	key := fmt.Sprintf("freeAbuse:user:%d", userId)
	freeAbuseLimiter.Init(freeAbuseWindowSeconds * time.Second)
	allowed := freeAbuseLimiter.Request(key, maxPerMin, freeAbuseWindowSeconds)
	return !allowed
}

func autoBlockUser(userId int) {
	s, err := model.GetUserSetting(userId, false)
	if err == nil && s.BlockFreeWhenNoQuota {
		return // already blocked
	}
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return
	}
	ns := user.GetSetting()
	if ns.BlockFreeWhenNoQuota {
		return
	}
	ns.BlockFreeWhenNoQuota = true
	user.SetSetting(ns)
	if err := user.Update(false); err != nil {
		common.SysLog(fmt.Sprintf("failed to auto-block user %d for free-model abuse: %s", userId, err.Error()))
		return
	}
	model.RecordLog(userId, model.LogTypeManage, "auto-blocked free models for user due to free-model abuse with zero balance")
}
