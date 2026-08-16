package limiter

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

//go:embed lua/rate_limit.lua
var rateLimitScript string

//go:embed lua/sliding_window_reserve.lua
var slidingWindowReserveScript string

//go:embed lua/sliding_window_release.lua
var slidingWindowReleaseScript string

type RedisLimiter struct {
	client           *redis.Client
	limitScriptSHA   string
	reserveScriptSHA string
	releaseScriptSHA string
}

var (
	instance *RedisLimiter
	once     sync.Once
)

func New(ctx context.Context, r *redis.Client) *RedisLimiter {
	once.Do(func() {
		load := func(name, script string) string {
			sha, err := r.ScriptLoad(ctx, script).Result()
			if err != nil {
				common.SysLog(fmt.Sprintf("Failed to load %s script: %v", name, err))
			}
			return sha
		}
		instance = &RedisLimiter{
			client:           r,
			limitScriptSHA:   load("rate limit", rateLimitScript),
			reserveScriptSHA: load("sliding window reserve", slidingWindowReserveScript),
			releaseScriptSHA: load("sliding window release", slidingWindowReleaseScript),
		}
	})

	return instance
}

// eval runs a preloaded script, falling back to a full EVAL when Redis has lost
// its script cache (restart/FLUSH) so admission keeps working without a redeploy.
func (rl *RedisLimiter) eval(ctx context.Context, sha, script string, keys []string, args ...any) *redis.Cmd {
	if sha != "" {
		cmd := rl.client.EvalSha(ctx, sha, keys, args...)
		if err := cmd.Err(); err == nil || !strings.Contains(err.Error(), "NOSCRIPT") {
			return cmd
		}
	}
	return rl.client.Eval(ctx, script, keys, args...)
}

// Reserve claims a slot in a sliding window BEFORE the work runs, so concurrent
// in-flight requests cannot all pass a check that only records on completion.
// The member it returns must be handed to Release when the work fails, which is
// what keeps failures free without reopening that gap. The window doubles as a
// lease: a crashed caller's reservation ages out instead of leaking a slot.
func (rl *RedisLimiter) Reserve(ctx context.Context, key string, max int, window time.Duration, member string) (bool, time.Duration, error) {
	res, err := rl.eval(ctx, rl.reserveScriptSHA, slidingWindowReserveScript,
		[]string{key}, max, window.Milliseconds(), member).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("reserve failed: %w", err)
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("reserve returned %d values, want 2", len(res))
	}
	allowed, _ := res[0].(int64)
	retryMs, _ := res[1].(int64)
	return allowed == 1, time.Duration(retryMs) * time.Millisecond, nil
}

func (rl *RedisLimiter) Release(ctx context.Context, key, member string) error {
	return rl.eval(ctx, rl.releaseScriptSHA, slidingWindowReleaseScript,
		[]string{key}, member).Err()
}

func (rl *RedisLimiter) Allow(ctx context.Context, key string, opts ...Option) (bool, error) {
	// 默认配置
	config := &Config{
		Capacity:  10,
		Rate:      1,
		Requested: 1,
	}

	// 应用选项模式
	for _, opt := range opts {
		opt(config)
	}

	// 执行限流
	result, err := rl.client.EvalSha(
		ctx,
		rl.limitScriptSHA,
		[]string{key},
		config.Requested,
		config.Rate,
		config.Capacity,
	).Int()

	if err != nil {
		return false, fmt.Errorf("rate limit failed: %w", err)
	}
	return result == 1, nil
}

// Config 配置选项模式
type Config struct {
	Capacity  int64
	Rate      int64
	Requested int64
}

type Option func(*Config)

func WithCapacity(c int64) Option {
	return func(cfg *Config) { cfg.Capacity = c }
}

func WithRate(r int64) Option {
	return func(cfg *Config) { cfg.Rate = r }
}

func WithRequested(n int64) Option {
	return func(cfg *Config) { cfg.Requested = n }
}
