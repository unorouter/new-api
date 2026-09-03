package middleware

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// 2026-09-03: 36k abandoned streams OOMed cloudflared and the gateway. Concurrency, not
// rate, is what ran out; per-IP limits never trigger against a thousand rotated IPs.
type inflightScope struct {
	name  string
	limit func() int
	cur   atomic.Int64
}

var (
	InflightRelay  = &inflightScope{name: "relay", limit: func() int { return common.InflightLimitRelay }}
	InflightPublic = &inflightScope{name: "public", limit: func() int { return common.InflightLimitPublic }}
)

func (s *inflightScope) tryAcquire(limit int64) bool {
	for {
		cur := s.cur.Load()
		if cur >= limit {
			return false
		}
		if s.cur.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (s *inflightScope) release() { s.cur.Add(-1) }

// InflightLimit rejects with 429 once the scope holds limit requests. The slot is released
// when the handler returns, which for a stream is after it ends: this bounds how many
// streams exist, it does not shorten them.
func InflightLimit(scope *inflightScope) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := int64(scope.limit())
		if limit <= 0 {
			c.Next()
			return
		}
		if !scope.tryAcquire(limit) {
			rejectInflight(c, scope)
			return
		}
		defer scope.release()
		c.Next()
	}
}

func rejectInflight(c *gin.Context, scope *inflightScope) {
	c.Header("Retry-After", "2")
	msg := "too many concurrent requests, retry shortly"
	if scope != InflightRelay {
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "message": msg})
		c.Abort()
		return
	}
	path := c.Request.URL.Path
	if strings.Contains(path, "/mj") {
		abortWithMidjourneyMessage(c, http.StatusTooManyRequests, http.StatusTooManyRequests, msg)
		return
	}
	apiErr := types.NewErrorWithStatusCode(errors.New(msg), "inflight_limit_reached", http.StatusTooManyRequests)
	if strings.HasPrefix(path, "/v1/messages") {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": apiErr.ToClaudeError()})
	} else {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": apiErr.ToOpenAIError()})
	}
	c.Abort()
}

func inflightCounts() map[string]int64 {
	return map[string]int64{
		InflightRelay.name:  InflightRelay.cur.Load(),
		InflightPublic.name: InflightPublic.cur.Load(),
	}
}
