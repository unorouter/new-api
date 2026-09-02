package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const claudeResponsesStreamFixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":7,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

// A Responses client saw response.created and response.completed twice: the
// handler finalized its own converter state and then also ran the chat-format
// trailer, which replayed a second, never-fed state plus a [DONE].
func TestClaudeResponsesStreamHandlerEmitsOneTerminalSequence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "claude-resp-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(claudeResponsesStreamFixture)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		RelayFormat:        types.RelayFormatOpenAIResponses,
		IsStream:           true,
		ShouldIncludeUsage: true,
		DisablePing:        true,
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "claude-test"},
	}

	usage, apiErr := ClaudeResponsesStreamHandler(c, resp, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	assert.Equal(t, 1, strings.Count(body, "event: response.created"), body)
	assert.Equal(t, 1, strings.Count(body, "event: response.completed"), body)
	assert.Equal(t, 0, strings.Count(body, "event: response.in_progress"), body)
	assert.NotContains(t, body, "[DONE]")
	assert.Contains(t, body, `"delta":"Hel"`)
	assert.Contains(t, body, `"delta":"lo"`)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(body), "}"), "response.completed must be the last frame:\n%s", body)
	assert.Contains(t, body[strings.LastIndex(body, "event: "):], "response.completed")

	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 5, usage.CompletionTokens)
}
