package common

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/semaphore"
)

// Converting a request for an upstream and retrying it costs about 15 times the body in
// heap: on 2026-10-10 one 91 MB upload with 53 images, retried after an upstream 413,
// took a 2 GiB pod down with 26 other requests. Bodies above the threshold reserve their
// size from a per-pod pool before any of that starts.
const largeBodyThreshold = 8 << 20

const keyLargeBodyReserved = "key_large_body_reserved"

var ErrLargeBodyBusy = errors.New("too many large requests in progress on this server, retry shortly")

var (
	largeBodyOnce sync.Once
	largeBodyPool *semaphore.Weighted
	largeBodyCap  int64
)

func largeBodyBudget() (*semaphore.Weighted, int64) {
	largeBodyOnce.Do(func() {
		largeBodyCap = int64(constant.LargeBodyBudgetMB) << 20
		if largeBodyCap > 0 {
			largeBodyPool = semaphore.NewWeighted(largeBodyCap)
		}
	})
	return largeBodyPool, largeBodyCap
}

func reserveLargeBody(c *gin.Context, size int64) error {
	pool, capBytes := largeBodyBudget()
	if pool == nil || size <= largeBodyThreshold {
		return nil
	}
	if _, held := c.Get(keyLargeBodyReserved); held {
		return nil
	}
	weight := min(size, capBytes)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	if err := pool.Acquire(ctx, weight); err != nil {
		return ErrLargeBodyBusy
	}
	c.Set(keyLargeBodyReserved, weight)
	return nil
}

func releaseLargeBody(c *gin.Context) {
	v, held := c.Get(keyLargeBodyReserved)
	if !held || v == nil {
		return
	}
	if weight, ok := v.(int64); ok && weight > 0 {
		largeBodyPool.Release(weight)
	}
	c.Set(keyLargeBodyReserved, nil)
}
