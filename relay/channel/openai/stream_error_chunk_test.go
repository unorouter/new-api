package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStreamTestContext builds an OpenAI-format SSE stream context driving
// OaiStreamHandler, mirroring newImageTestContext in image_stream_test.go.
func newStreamTestContext(t *testing.T, sse string) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(sse)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
	}
	info.RelayMode = relayconstant.RelayModeChatCompletions
	return c, recorder, resp, info
}

// TestOaiStreamHandlerInterceptsMidStreamError covers the fork's mid-stream error
// interception: an upstream {"error":{...}} SSE chunk (Aliyun content moderation)
// must NOT forward to the client and must surface as a NewAPIError that flows
// through the masking + logging + failover path.
func TestOaiStreamHandlerInterceptsMidStreamError(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	const moderationChunk = `data: {"error":{"message":"Output data may contain inappropriate content. For details, see: https://help.aliyun.com/zh/model-studio/error-code#inappropriate-content","type":"data_inspection_failed","code":"data_inspection_failed"},"id":"chatcmpl-435da115"}`
	const contentChunk = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"}}]}`

	t.Run("error before any content: failover, masked, not forwarded", func(t *testing.T) {
		sse := strings.Join([]string{
			`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			``,
			moderationChunk,
			``,
		}, "\n")
		c, rec, resp, info := newStreamTestContext(t, sse)

		_, apiErr := OaiStreamHandler(c, info, resp)

		require.NotNil(t, apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
		assert.True(t, types.IsUpstreamModerationError(apiErr), "moderation 400 must classify as upstream moderation (failover, no disable)")
		assert.False(t, types.IsSkipRetryError(apiErr), "nothing committed -> must be retryable (failover)")

		masked := apiErr.MaskSensitiveErrorWithStatusCode()
		assert.NotContains(t, masked, "help.aliyun.com", "URL must be masked in the DB-logged message")

		body := rec.Body.String()
		assert.NotContains(t, body, "help.aliyun.com", "raw upstream URL must not leak to the client")
		assert.NotContains(t, body, "data_inspection_failed", "raw error chunk must not be forwarded")
	})

	t.Run("error after content committed: skip retry, content kept, error not forwarded", func(t *testing.T) {
		sse := strings.Join([]string{
			contentChunk,
			``,
			moderationChunk,
			``,
		}, "\n")
		c, rec, resp, info := newStreamTestContext(t, sse)

		_, apiErr := OaiStreamHandler(c, info, resp)

		require.NotNil(t, apiErr)
		assert.True(t, types.IsSkipRetryError(apiErr), "bytes already committed -> skip retry (no double-send)")

		body := rec.Body.String()
		assert.Contains(t, body, "Hi", "already-streamed content stays on the wire")
		assert.NotContains(t, body, "data_inspection_failed", "error chunk must not be forwarded even after content")
	})

	const reasoningChunk = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"thinking it over"}}]}`
	const busyChunk = `data: {"error":{"message":"pinned merchant busy","type":"upstream_error","code":"fixed_merchant_unavailable"}}`
	const toolChunk = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`
	const timeoutGuardChunk = `data: {"id":"chatcmpl-timeout-guard","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":1000,"total_tokens":2000}}`
	for _, tc := range []struct {
		name          string
		chunks        []string
		wantSkipRetry bool
	}{
		{"error after reasoning only fails over", []string{reasoningChunk, busyChunk}, false},
		{"error after whitespace content and reasoning fails over", []string{reasoningChunk, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"\n"}}]}`, busyChunk}, false},
		{"error after answer text is a truncation", []string{reasoningChunk, contentChunk, busyChunk}, true},
		{"error after a tool call is a truncation", []string{reasoningChunk, toolChunk, busyChunk}, true},
		{"timeout guard after reasoning only fails over", []string{reasoningChunk, timeoutGuardChunk}, false},
		{"timeout guard after answer text is a truncation", []string{contentChunk, timeoutGuardChunk}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec, resp, info := newStreamTestContext(t, strings.Join(tc.chunks, "\n\n")+"\n\n")

			_, apiErr := OaiStreamHandler(c, info, resp)

			require.NotNil(t, apiErr)
			assert.Equal(t, tc.wantSkipRetry, types.IsSkipRetryError(apiErr))
			assert.Equal(t, tc.wantSkipRetry, types.IsStreamTruncatedError(apiErr))
			assert.NotContains(t, rec.Body.String(), "[DONE]", "the next attempt or the error writer ends the stream")
		})
	}

	t.Run("legit content_filter refusal is not hijacked", func(t *testing.T) {
		sse := strings.Join([]string{
			contentChunk,
			``,
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n")
		c, rec, resp, info := newStreamTestContext(t, sse)

		_, apiErr := OaiStreamHandler(c, info, resp)

		require.Nil(t, apiErr, "a content_filter finish_reason has no top-level error and must pass through")
		assert.Contains(t, rec.Body.String(), "Hi")
	})

	t.Run("clean stream forwards all chunks", func(t *testing.T) {
		sse := strings.Join([]string{
			contentChunk,
			``,
			`data: [DONE]`,
			``,
		}, "\n")
		c, rec, resp, info := newStreamTestContext(t, sse)

		_, apiErr := OaiStreamHandler(c, info, resp)

		require.Nil(t, apiErr)
		assert.Contains(t, rec.Body.String(), "Hi")
	})
}
