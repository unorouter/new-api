package helper

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// Cloudflare answers 524 once the origin has been silent for 125 s (measured on
// our zone; Pro plans cannot raise it), while the upstream keeps generating and
// the answer is billed. A client that asked for one JSON reply hears nothing
// until the upstream finishes, so a long request gets a space every so often:
// leading whitespace is valid JSON and the edge timer restarts on any byte.
const (
	jsonKeepaliveAfter    = 60 * time.Second
	jsonKeepaliveInterval = 20 * time.Second
	jsonKeepaliveBytesKey = "json_keepalive_bytes"
)

// runJSONKeepalive is timed from the request start, not this attempt, so a
// failover that began at 50 s still pads before the edge gives up.
func runJSONKeepalive(c *gin.Context, ctx context.Context, stopChan <-chan bool, writeMutex *sync.Mutex) {
	started := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if started.IsZero() {
		started = time.Now()
	}
	wait := max(time.Until(started.Add(jsonKeepaliveAfter)), 0)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		case <-stopChan:
			return
		case <-c.Request.Context().Done():
			return
		}
		ok := func() bool {
			writeMutex.Lock()
			defer writeMutex.Unlock()
			return writeJSONKeepalive(c)
		}()
		if !ok {
			return
		}
		timer.Reset(jsonKeepaliveInterval)
	}
}

func writeJSONKeepalive(c *gin.Context) bool {
	sent := c.GetInt(jsonKeepaliveBytesKey)
	// Something other than our padding is already out: the reply has started.
	if c.Writer.Written() && c.Writer.Size() != sent {
		return false
	}
	if sent == 0 {
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
	}
	ExtendWriteDeadline(c)
	if _, err := c.Writer.Write([]byte(" ")); err != nil {
		return false
	}
	c.Writer.Flush()
	c.Set(jsonKeepaliveBytesKey, sent+1)
	return true
}

// OnlyJSONKeepaliveWritten reports that the client has received nothing but
// keepalive padding, so a JSON error body can still follow it.
func OnlyJSONKeepaliveWritten(c *gin.Context) bool {
	sent := c.GetInt(jsonKeepaliveBytesKey)
	return sent > 0 && c.Writer.Size() == sent
}
