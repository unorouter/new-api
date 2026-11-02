package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/go-redis/redis/v8"
)

// Fair share for congested free models. A thin free lane is shared by thousands
// of accounts, and per-account limits cannot divide it: a farm of accounts that
// each stay under every limit still takes most of the lane while real users see
// "all providers are busy". While a model is congested, each account's attempts
// over the window are compared with the lane's attempts divided by its active
// accounts, scaled by how much the account has proven; an account above its
// share is answered with the ordinary rate-limit reply instead of an upstream
// call. A model that is not congested limits nobody.
//
// Attempts, not successes, are counted: a retry against a busy lane takes a
// lane slot as surely as an answer does, and counting it is what stops retry
// storms from paying off.

const freeFairShareBucketSeconds = 300

// Congestion is judged on the two most recent buckets, about ten minutes.
const freeFairShareCongestionBuckets = 2

func freeFairShareKey(kind string, modelName string, bucket int64) string {
	return fmt.Sprintf("ffs:%s:%s:%d", kind, modelName, bucket)
}

// FreeFairShareWeightPct is the share an account may take, in percent of an
// even split: money spent counts most, then a bound login, and an account with
// neither scales like every other unverified threshold.
func FreeFairShareWeightPct(spent bool, hasIdentity bool, verified bool) int {
	setting := operation_setting.GetQuotaSetting()
	switch {
	case spent:
		return setting.FreeFairSharePaidPct
	case hasIdentity, verified:
		return 100
	default:
		return setting.FreeAbuseUnverifiedPct
	}
}

// FreeFairShareAdmit reports whether a free request may go upstream. It answers
// false only in enforce mode, on a congested model, for an account above its
// share; observe mode logs that verdict and admits.
func FreeFairShareAdmit(userId int, modelName string, weightPct int) bool {
	setting := operation_setting.GetQuotaSetting()
	if setting.FreeFairShareMode <= 0 || !common.RedisEnabled || userId <= 0 || modelName == "" {
		return true
	}
	ctx := context.Background()
	now := time.Now().Unix() / freeFairShareBucketSeconds
	windowBuckets := max(int64(setting.FreeFairShareWindowMinutes*60/freeFairShareBucketSeconds), freeFairShareCongestionBuckets)

	pipe := common.RDB.Pipeline()
	recentAttempts := make([]*redis.StringCmd, 0, freeFairShareCongestionBuckets)
	recentFailures := make([]*redis.StringCmd, 0, freeFairShareCongestionBuckets)
	for b := now - freeFairShareCongestionBuckets + 1; b <= now; b++ {
		recentAttempts = append(recentAttempts, pipe.Get(ctx, freeFairShareKey("n", modelName, b)))
		recentFailures = append(recentFailures, pipe.Get(ctx, freeFairShareKey("f", modelName, b)))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		common.SysError("free fair share congestion read failed: " + err.Error())
		return true
	}
	attempts, failures := sumCounters(recentAttempts), sumCounters(recentFailures)
	if attempts < int64(setting.FreeFairShareMinAttempts) || failures*100 < attempts*int64(setting.FreeFairShareCongestedPct) {
		return true
	}

	member := strconv.Itoa(userId)
	pipe = common.RDB.Pipeline()
	userAttempts := make([]*redis.StringCmd, 0, windowBuckets)
	totalAttempts := make([]*redis.StringCmd, 0, windowBuckets)
	activeKeys := make([]string, 0, windowBuckets)
	for b := now - windowBuckets + 1; b <= now; b++ {
		userAttempts = append(userAttempts, pipe.HGet(ctx, freeFairShareKey("u", modelName, b), member))
		totalAttempts = append(totalAttempts, pipe.Get(ctx, freeFairShareKey("n", modelName, b)))
		activeKeys = append(activeKeys, freeFairShareKey("a", modelName, b))
	}
	activeCmd := pipe.PFCount(ctx, activeKeys...)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		common.SysError("free fair share window read failed: " + err.Error())
		return true
	}
	used := sumCounters(userAttempts)
	active := max(activeCmd.Val(), 1)
	fair := float64(sumCounters(totalAttempts)) / float64(active)
	allowed := max(fair*float64(setting.FreeFairShareBurstPct)/100*float64(weightPct)/100, float64(setting.FreeFairShareFloor))
	if float64(used) < allowed {
		return true
	}

	enforce := setting.FreeFairShareMode >= 2
	// One line per account and model per window: a throttled client retries
	// every few seconds and would otherwise flood the log.
	if fresh, err := common.RedisSetNX(fmt.Sprintf("ffs:log:%d:%s", userId, modelName), "1", 10*time.Minute); err == nil && fresh {
		verdict := "would throttle"
		if enforce {
			verdict = "throttled"
		}
		common.SysLog(fmt.Sprintf("free fair share: %s user %d on %s (used %d, allowed %.1f, fair %.1f, active %d, weight %d%%, congestion %d/%d)",
			verdict, userId, modelName, used, allowed, fair, active, weightPct, failures, attempts))
	}
	return !enforce
}

// RecordFreeFairShareAttempt counts one admitted free request against its
// account and its model, and whether it failed for lack of capacity.
func RecordFreeFairShareAttempt(userId int, modelName string, capacityFailure bool) {
	setting := operation_setting.GetQuotaSetting()
	if setting.FreeFairShareMode <= 0 || !common.RedisEnabled || userId <= 0 || modelName == "" {
		return
	}
	ctx := context.Background()
	bucket := time.Now().Unix() / freeFairShareBucketSeconds
	ttl := time.Duration(setting.FreeFairShareWindowMinutes*60+freeFairShareBucketSeconds) * time.Second
	userKey := freeFairShareKey("u", modelName, bucket)
	totalKey := freeFairShareKey("n", modelName, bucket)
	activeKey := freeFairShareKey("a", modelName, bucket)
	pipe := common.RDB.Pipeline()
	pipe.HIncrBy(ctx, userKey, strconv.Itoa(userId), 1)
	pipe.Incr(ctx, totalKey)
	pipe.PFAdd(ctx, activeKey, userId)
	pipe.Expire(ctx, userKey, ttl)
	pipe.Expire(ctx, totalKey, ttl)
	pipe.Expire(ctx, activeKey, ttl)
	if capacityFailure {
		failKey := freeFairShareKey("f", modelName, bucket)
		pipe.Incr(ctx, failKey)
		pipe.Expire(ctx, failKey, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		common.SysError("free fair share write failed: " + err.Error())
	}
}

func sumCounters(cmds []*redis.StringCmd) int64 {
	var total int64
	for _, cmd := range cmds {
		if n, err := cmd.Int64(); err == nil {
			total += n
		}
	}
	return total
}
