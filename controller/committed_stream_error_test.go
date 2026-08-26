package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// A committed stream that fails mid-flight used to end with no [DONE] and no
// error, so the client could only report that the stream stopped while the cause
// (an Alibaba moderation 502, say) existed solely in our logs.
func TestIsCommittedSSE(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	newCtx := func(contentType string) *gin.Context {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		if contentType != "" {
			c.Writer.Header().Set("Content-Type", contentType)
		}
		return c
	}

	t.Run("openai sse stream accepts an error chunk", func(t *testing.T) {
		assert.True(t, isCommittedSSE(newCtx("text/event-stream"), types.RelayFormatOpenAI))
	})

	t.Run("charset suffix still counts", func(t *testing.T) {
		assert.True(t, isCommittedSSE(newCtx("text/event-stream; charset=utf-8"), types.RelayFormatOpenAI))
	})

	t.Run("committed non-stream response gets nothing appended", func(t *testing.T) {
		assert.False(t, isCommittedSSE(newCtx("application/json"), types.RelayFormatOpenAI))
	})

	t.Run("claude stream is left alone: its events are all named", func(t *testing.T) {
		assert.False(t, isCommittedSSE(newCtx("text/event-stream"), types.RelayFormatClaude))
	})

	t.Run("missing content-type is not assumed to be a stream", func(t *testing.T) {
		assert.False(t, isCommittedSSE(newCtx(""), types.RelayFormatOpenAI))
	})

	t.Run("a stream already closed by [DONE] takes no error chunk", func(t *testing.T) {
		c := newCtx("text/event-stream")
		helper.Done(c)
		assert.False(t, isCommittedSSE(c, types.RelayFormatOpenAI))
	})
}

// The success path sends [DONE] itself, and the error defer can fire after it, so
// the terminator has to survive being called twice.
func TestDoneIsIdempotent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	helper.Done(c)
	helper.Done(c)
	helper.Done(c)

	assert.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"))
	assert.True(t, helper.StreamDoneSent(c))
}
