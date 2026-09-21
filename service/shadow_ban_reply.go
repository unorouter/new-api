package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ShadowBanRetryAfter is the wait a shadow-banned account is told, shaped like
// the limiter's own sliding window: the first attempt sees the full window, an
// attempt made after part of it has passed sees the rest, and every attempt
// renews the window. A client that waits exactly what it was told and retries
// lands on a fresh full window, so the countdown reads as a real per-account
// limit that it keeps missing by a hair rather than a flat, fixed number that
// gives the ban away. Without Redis the window is flat.
func ShadowBanRetryAfter(userId int, modelName string, windowSeconds int64) int64 {
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	if !common.RedisEnabled || userId <= 0 {
		return windowSeconds
	}
	ctx := context.Background()
	key := fmt.Sprintf("shadow_ban_seen:%d:%s", userId, modelName)
	now := time.Now().Unix()
	prev, err := common.RDB.GetSet(ctx, key, strconv.FormatInt(now, 10)).Result()
	common.RDB.Expire(ctx, key, time.Duration(windowSeconds)*time.Second)
	if err != nil {
		return windowSeconds
	}
	last, parseErr := strconv.ParseInt(prev, 10, 64)
	if parseErr != nil {
		return windowSeconds
	}
	remaining := windowSeconds - (now - last)
	if remaining < 1 || remaining > windowSeconds {
		return windowSeconds
	}
	return remaining
}
