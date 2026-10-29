package helper

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"

	"github.com/bytedance/gopkg/util/gopool"
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

// keepaliveWriter lets the padding goroutine and the relay handlers share one
// response: every write goes through mu, and the handler's first write or status
// ends the padding for good.
type keepaliveWriter struct {
	gin.ResponseWriter
	mu    sync.Mutex
	owned bool
	sent  int
	// The status the handler meant once padding had already committed a 200, so
	// middleware that reads Status() still sees a failure as one.
	status int
}

func (w *keepaliveWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.owned = true
	if w.sent > 0 {
		w.status = code
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *keepaliveWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status != 0 {
		return w.status
	}
	return w.ResponseWriter.Status()
}

func (w *keepaliveWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.owned = true
	w.ResponseWriter.WriteHeaderNow()
}

func (w *keepaliveWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.owned = true
	return w.ResponseWriter.Write(data)
}

func (w *keepaliveWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.owned = true
	return w.ResponseWriter.WriteString(s)
}

func (w *keepaliveWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.Flush()
}

func (w *keepaliveWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *keepaliveWriter) pad(c *gin.Context) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.owned {
		return false
	}
	if w.sent == 0 {
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := w.ResponseWriter.Write([]byte(" ")); err != nil {
		return false
	}
	w.ResponseWriter.Flush()
	w.sent++
	c.Set(jsonKeepaliveBytesKey, w.sent)
	return true
}

// StartJSONKeepalive pads a JSON reply from the moment the request arrives, not
// from the first upstream byte: a silent upstream or a failover chain is exactly
// the wait the edge cuts. The returned stop must run before the handler returns.
func StartJSONKeepalive(c *gin.Context) (stop func()) {
	w := &keepaliveWriter{ResponseWriter: c.Writer}
	c.Writer = w
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("json keepalive goroutine panic: %v", r))
			}
			wg.Done()
		}()
		runJSONKeepalive(c, w, done)
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

func runJSONKeepalive(c *gin.Context, w *keepaliveWriter, done <-chan struct{}) {
	started := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if started.IsZero() {
		started = time.Now()
	}
	timer := time.NewTimer(max(time.Until(started.Add(jsonKeepaliveAfter)), 0))
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-done:
			return
		case <-c.Request.Context().Done():
			return
		}
		if !w.pad(c) {
			return
		}
		timer.Reset(jsonKeepaliveInterval)
	}
}

// OnlyJSONKeepaliveWritten reports that the client has received nothing but
// keepalive padding, so a JSON error body can still follow it.
func OnlyJSONKeepaliveWritten(c *gin.Context) bool {
	sent := c.GetInt(jsonKeepaliveBytesKey)
	return sent > 0 && c.Writer.Size() == sent
}
