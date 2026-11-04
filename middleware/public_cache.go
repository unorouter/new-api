package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

type publicCacheWriter struct {
	gin.ResponseWriter
	c       *gin.Context
	browser string
	cdn     string
	applied bool
}

func (w *publicCacheWriter) apply() {
	if w.applied {
		return
	}
	w.applied = true
	h := w.ResponseWriter.Header()
	if h.Get("Cache-Control") != "" {
		return
	}
	if _, authed := w.c.Get("id"); authed || h.Get("Set-Cookie") != "" {
		h.Set("Cache-Control", "private, no-store")
		return
	}
	if w.ResponseWriter.Status() != http.StatusOK {
		return
	}
	h.Set("Cache-Control", w.browser)
	h.Set("CDN-Cache-Control", w.cdn)
}

func (w *publicCacheWriter) WriteHeaderNow() {
	w.apply()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *publicCacheWriter) Write(b []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(b)
}

func (w *publicCacheWriter) WriteString(s string) (int, error) {
	w.apply()
	return w.ResponseWriter.WriteString(s)
}

// Must sit after TryUserAuth, so an identified caller is never handed the shared copy.
// Browsers get max-age=0: one that logs in must not reuse an anonymous body.
func PublicCache(seconds int) gin.HandlerFunc {
	browser := fmt.Sprintf("public, max-age=0, s-maxage=%d", seconds)
	cdn := fmt.Sprintf("public, max-age=%d, stale-while-revalidate=%d, stale-if-error=86400", seconds, seconds*5)
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Next()
			return
		}
		c.Writer = &publicCacheWriter{ResponseWriter: c.Writer, c: c, browser: browser, cdn: cdn}
		c.Next()
	}
}
