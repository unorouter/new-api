package limiter

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useLimiterMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()

	previousInstance := instance

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())

	instance = nil

	t.Cleanup(func() {
		_ = redisClient.Close()
		instance = previousInstance
	})

	return redisServer, redisClient
}

func TestAllowFallsBackToEvalAfterScriptFlush(t *testing.T) {
	ctx := context.Background()
	_, redisClient := useLimiterMiniRedis(t)

	redisLimiter := New(ctx, redisClient)

	allowed, err := redisLimiter.Allow(
		ctx,
		"rateLimit:test-before-flush",
		WithCapacity(60),
		WithRate(60),
		WithRequested(60),
	)
	require.NoError(t, err)
	assert.True(t, allowed)

	exists, err := redisClient.ScriptExists(ctx, redisLimiter.limitScriptSHA).Result()
	require.NoError(t, err)
	require.Len(t, exists, 1)
	assert.True(t, exists[0])

	require.NoError(t, redisClient.ScriptFlush(ctx).Err())

	exists, err = redisClient.ScriptExists(ctx, redisLimiter.limitScriptSHA).Result()
	require.NoError(t, err)
	require.Len(t, exists, 1)
	assert.False(t, exists[0])

	allowed, err = redisLimiter.Allow(
		ctx,
		"rateLimit:test-after-flush",
		WithCapacity(60),
		WithRate(60),
		WithRequested(60),
	)
	require.NoError(t, err)
	assert.True(t, allowed)

	exists, err = redisClient.ScriptExists(ctx, redisLimiter.limitScriptSHA).Result()
	require.NoError(t, err)
	require.Len(t, exists, 1)
	assert.True(t, exists[0])
}
