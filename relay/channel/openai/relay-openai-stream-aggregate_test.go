package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The force-upstream-stream path rewrites a client's stream=false into an
// upstream SSE call and rebuilds one JSON body from the chunks, so a bug here
// is silent: the client still gets a 200 with a plausible-looking body. These
// exercise the real handler rather than an extracted copy of its loop.

func runAggregate(t *testing.T, sse string) (*dto.OpenAITextResponse, *httptest.ResponseRecorder) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	// The scanner builds a ticker from this; zero panics with "non-positive interval".
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, resp, info := newStreamTestContext(t, sse)
	usage, apiErr := OaiStreamToJsonHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	var out dto.OpenAITextResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	return &out, recorder
}

func TestAggregateJoinsTextChunksInOrder(t *testing.T) {
	sse := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":100,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}

data: {"choices":[{"index":0,"delta":{"content":", "}}]}

data: {"choices":[{"index":0,"delta":{"content":"world"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	require.Len(t, out.Choices, 1)
	assert.Equal(t, "chatcmpl-1", out.Id)
	assert.Equal(t, "assistant", out.Choices[0].Message.Role)
	assert.Equal(t, "Hello, world", out.Choices[0].Message.StringContent())
	assert.Equal(t, "stop", out.Choices[0].FinishReason)
}

// Reasoning must stay in its own field: folding it into content ships raw
// chain-of-thought to every client that reads only `content`.
func TestAggregateKeepsReasoningSeparateFromContent(t *testing.T) {
	sse := `data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking "}}]}

data: {"choices":[{"index":0,"delta":{"reasoning_content":"hard"}}]}

data: {"choices":[{"index":0,"delta":{"content":"answer"}}]}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	require.Len(t, out.Choices, 1)
	assert.Equal(t, "answer", out.Choices[0].Message.StringContent())
	require.NotNil(t, out.Choices[0].Message.ReasoningContent)
	assert.Equal(t, "thinking hard", *out.Choices[0].Message.ReasoningContent)
}

// Arguments arrive split at arbitrary points, including mid-JSON-token, so the
// only correct reassembly is raw concatenation in arrival order.
func TestAggregateReassemblesToolCallArgumentsSplitMidJson(t *testing.T) {
	sse := `data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cit"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"y\":\"Berlin\"}"}}]}}]}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	require.Len(t, out.Choices, 1)
	calls := out.Choices[0].Message.ParseToolCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "call_a", calls[0].ID)
	assert.Equal(t, "function", calls[0].Type)
	assert.Equal(t, "get_weather", calls[0].Function.Name)
	assert.Equal(t, `{"city":"Berlin"}`, calls[0].Function.Arguments)
	// No explicit finish_reason arrived, so tool calls must imply it.
	assert.Equal(t, "tool_calls", out.Choices[0].FinishReason)
}

// Two calls interleaved across chunks must stay separate and keep the order
// upstream streamed them, since the client matches results back by position.
func TestAggregateKeepsMultipleToolCallsSeparateAndOrdered(t *testing.T) {
	sse := `data: {"id":"chatcmpl-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"first","arguments":"{"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"second","arguments":"{"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\":1}"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"b\":2}"}}]}}]}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	calls := out.Choices[0].Message.ParseToolCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "first", calls[0].Function.Name)
	assert.Equal(t, `{"a":1}`, calls[0].Function.Arguments)
	assert.Equal(t, "second", calls[1].Function.Name)
	assert.Equal(t, `{"b":2}`, calls[1].Function.Arguments)
}

func TestAggregateKeepsChoicesSeparateWhenNGreaterThanOne(t *testing.T) {
	sse := `data: {"id":"chatcmpl-5","choices":[{"index":0,"delta":{"role":"assistant","content":"first"}}]}

data: {"choices":[{"index":1,"delta":{"role":"assistant","content":"second"}}]}

data: {"choices":[{"index":0,"delta":{"content":"-a"}},{"index":1,"delta":{"content":"-b"}}]}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	require.Len(t, out.Choices, 2)
	assert.Equal(t, 0, out.Choices[0].Index)
	assert.Equal(t, "first-a", out.Choices[0].Message.StringContent())
	assert.Equal(t, 1, out.Choices[1].Index)
	assert.Equal(t, "second-b", out.Choices[1].Message.StringContent())
}

// Keep-alive comments and malformed frames are normal on a long upstream hold;
// they must be skipped rather than abort the aggregation.
func TestAggregateIgnoresKeepAliveAndUnparseableFrames(t *testing.T) {
	sse := `data: {"id":"chatcmpl-6","choices":[{"index":0,"delta":{"role":"assistant","content":"kept"}}]}

data: not-json-at-all

data:

data: {"choices":[{"index":0,"delta":{"content":"-still-kept"}}]}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	require.Len(t, out.Choices, 1)
	assert.Equal(t, "kept-still-kept", out.Choices[0].Message.StringContent())
}

// Upstream usage is authoritative for billing, so it must survive rather than
// be replaced by the text-length estimate.
func TestAggregatePrefersUpstreamUsage(t *testing.T) {
	sse := `data: {"id":"chatcmpl-7","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}

data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}

data: [DONE]

`
	out, _ := runAggregate(t, sse)

	assert.Equal(t, 11, out.Usage.PromptTokens)
	assert.Equal(t, 22, out.Usage.CompletionTokens)
}

// a6 truncating the connection is the exact failure this feature exists to
// survive, so a stream that stops without [DONE] must still return what it got.
func TestAggregateReturnsPartialContentWhenStreamEndsWithoutDone(t *testing.T) {
	sse := `data: {"id":"chatcmpl-8","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}

`
	out, _ := runAggregate(t, sse)

	require.Len(t, out.Choices, 1)
	assert.Equal(t, "partial", out.Choices[0].Message.StringContent())
}
