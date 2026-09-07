package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

// Shared status page cache. Payloads live in Redis, one copy for every gateway pod,
// wrapped in an envelope that records when they were built; the in-process map stays
// in front as a read-through layer so a hot key never re-parses a megabyte of JSON
// per request. Freshness is the envelope age against the window's TTL, not the Redis
// expiry: a stale entry is still served while exactly one pod (Redis lock) rebuilds
// it in the background, so no public request waits for an aggregation once the key
// exists at all. Without Redis the behaviour is the old one: per-pod memory and a
// blocking singleflight build on a miss.

const (
	statusCachePrefix = "status_page:"
	statusLockPrefix  = "status_page_lock:"
)

type statusEnvelope struct {
	At   int64           `json:"at"`
	Data json.RawMessage `json:"data"`
}

// Keys this pod is already refreshing, so a burst of stale reads spawns one refresh.
var statusRefreshing sync.Map

// statusRedisKeep is how long Redis holds a payload. It exceeds the freshness TTL on
// purpose: the stale copy is what gets served during the next rebuild.
func statusRedisKeep(ttl time.Duration) time.Duration {
	keep := 3 * ttl
	if keep < 10*time.Minute {
		keep = 10 * time.Minute
	}
	return keep
}

func statusLockTTL(ttl time.Duration) time.Duration {
	lock := ttl / 2
	if lock < 45*time.Second {
		lock = 45 * time.Second
	}
	return lock
}

// cachedStatus serves key from memory, then Redis, and builds it otherwise.
func cachedStatus[T any](key string, ttl time.Duration, build func() (T, error)) (T, error) {
	if v, ok := statusPageCacheGet(key); ok {
		if t, ok := v.(T); ok {
			return t, nil
		}
	}
	if common.RedisEnabled {
		if env, ok := statusRedisGet(key); ok {
			var t T
			if err := json.Unmarshal(env.Data, &t); err == nil {
				age := time.Since(time.Unix(env.At, 0))
				if age < ttl {
					statusPageCacheSetTTL(key, t, ttl-age)
					return t, nil
				}
				statusRefreshAsync(key, ttl, build)
				return t, nil
			}
		}
	}
	built, err, _ := statusPageGroup.Do(key, func() (any, error) {
		if v, ok := statusPageCacheGet(key); ok {
			return v, nil
		}
		t, err := build()
		if err != nil {
			return nil, err
		}
		statusStore(key, ttl, t)
		return t, nil
	})
	var zero T
	if err != nil {
		return zero, err
	}
	t, ok := built.(T)
	if !ok {
		return zero, fmt.Errorf("status cache entry %s has an unexpected type", key)
	}
	return t, nil
}

func statusStore[T any](key string, ttl time.Duration, v T) {
	statusPageCacheSetTTL(key, v, ttl)
	if !common.RedisEnabled {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		common.SysLog("status cache marshal failed for " + key + ": " + err.Error())
		return
	}
	env, err := json.Marshal(statusEnvelope{At: time.Now().Unix(), Data: data})
	if err != nil {
		return
	}
	if err := common.RedisSet(statusCachePrefix+key, string(env), statusRedisKeep(ttl)); err != nil {
		common.SysLog("status cache redis set failed for " + key + ": " + err.Error())
	}
}

func statusRedisGet(key string) (statusEnvelope, bool) {
	var env statusEnvelope
	raw, err := common.RedisGet(statusCachePrefix + key)
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			common.SysLog("status cache redis get failed for " + key + ": " + err.Error())
		}
		return env, false
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return env, false
	}
	return env, true
}

// statusRefreshAsync rebuilds a stale key in the background; the Redis lock makes
// the rebuild cluster-wide singular, the local map makes it per-pod singular.
func statusRefreshAsync[T any](key string, ttl time.Duration, build func() (T, error)) {
	if _, loaded := statusRefreshing.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	go func() {
		defer statusRefreshing.Delete(key)
		statusRefreshSync(key, ttl, build)
	}()
}

// statusRefreshSync rebuilds key now if no other pod holds the lock. Returns
// whether this pod did the build.
func statusRefreshSync[T any](key string, ttl time.Duration, build func() (T, error)) bool {
	if common.RedisEnabled {
		won, err := common.RedisSetNX(statusLockPrefix+key, "1", statusLockTTL(ttl))
		if err != nil {
			common.SysLog("status cache lock failed for " + key + ": " + err.Error())
		} else if !won {
			return false
		}
		defer func() { _ = common.RedisDel(statusLockPrefix + key) }()
	}
	v, err := build()
	if err != nil {
		common.SysLog("status cache refresh failed for " + key + ": " + err.Error())
		return false
	}
	statusStore(key, ttl, v)
	return true
}
