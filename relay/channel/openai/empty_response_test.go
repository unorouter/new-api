package openai

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenAIResponseHasOutput gates the live empty-response auto-disable. These cases
// are real upstream shapes captured from Bailian/OpenAI so a normal reply is never
// misread as empty, and a genuinely empty reply is.
func TestOpenAIResponseHasOutput(t *testing.T) {
	parse := func(body string) *dto.OpenAITextResponse {
		var r dto.OpenAITextResponse
		require.NoError(t, common.Unmarshal([]byte(body), &r))
		return &r
	}

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			"plain content",
			`{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello! How can I"},"finish_reason":"length"}]}`,
			true,
		},
		{
			// Completed thinking turn where reasoning IS the answer (Qwen-style):
			// finish_reason=stop, so reasoning-only counts as real output.
			"reasoning only, completed (qwen thinking)",
			`{"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"Thinking..."},"finish_reason":"stop"}]}`,
			true,
		},
		{
			// Ran out of budget mid-reasoning, never produced an answer (GLM with
			// thinking left on): finish_reason=length + empty content = blank to
			// the user, so this is empty and must auto-disable.
			"reasoning only, truncated (glm thinking budget exhausted)",
			`{"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"Analyzing the request..."},"finish_reason":"length"}]}`,
			false,
		},
		{
			"tool call only",
			`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			true,
		},
		{
			"no choices (dead channel / quota wall)",
			`{"id":"x","object":"chat.completion","choices":[],"usage":{"prompt_tokens":9,"total_tokens":9}}`,
			false,
		},
		{
			"choice with blank content, no reasoning, no tools",
			`{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, OpenAIResponseHasOutput(parse(tc.body)))
		})
	}
}

// streamHadOutput is the streaming twin of the above. It exists because
// responseTextBuilder also accumulates reasoning (it feeds billing), so the old
// builder-emptiness test could only ever catch a totally silent upstream and was
// blind to a stream that spent its whole budget on reasoning_content.
func TestStreamHadOutput(t *testing.T) {
	// Each case is the sequence of SSE data chunks an upstream sent.
	cases := []struct {
		name      string
		chunks    []string
		want      bool
		wantBuilt string // responseTextBuilder must keep feeding billing unchanged
	}{
		{
			"plain content",
			[]string{`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`},
			true, "Hello",
		},
		{
			// Qwen-style: reasoning IS the answer and the turn completed cleanly.
			"reasoning only, finish_reason=stop",
			[]string{`{"choices":[{"index":0,"delta":{"reasoning_content":"Thinking..."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`},
			true, "Thinking...",
		},
		{
			// GLM burned the budget on reasoning and never answered.
			"reasoning only, finish_reason=length",
			[]string{`{"choices":[{"index":0,"delta":{"reasoning_content":"Analyzing..."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`},
			false, "Analyzing...",
		},
		{
			// The upstream stopped mid-reasoning and never sent a finish_reason at
			// all. Blank to the reader, so empty - absent is not "stop".
			"reasoning only, no finish_reason",
			[]string{`{"choices":[{"index":0,"delta":{"reasoning_content":"Analyzing..."}}]}`},
			false, "Analyzing...",
		},
		{
			// The regression this fix targets: reasoning, then a single stray
			// character of content. Whitespace-only content is not an answer.
			"reasoning then whitespace-only content",
			[]string{`{"choices":[{"index":0,"delta":{"reasoning_content":"Analyzing..."}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"\n"}}]}`},
			false, "Analyzing...\n",
		},
		{
			"tool call only",
			[]string{`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`},
			true, "f{}",
		},
		{
			"no usable data at all (silent upstream)",
			[]string{`{"choices":[{"index":0,"delta":{}}]}`},
			false, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var builder strings.Builder
			var stats StreamOutputStats
			var toolCount int
			for _, chunk := range tc.chunks {
				require.NoError(t, processTokenData(
					relayconstant.RelayModeChatCompletions, chunk, &builder, &toolCount, &stats))
			}

			assert.Equal(t, tc.want, streamHadOutput(&stats, toolCount))
			// Billing invariant: reasoning must still reach the builder, which is
			// what ResponseText2Usage bills when the upstream sends no usage block.
			// Dropping it here would silently under-bill every reasoning model.
			assert.Equal(t, tc.wantBuilt, builder.String())
		})
	}
}

// A content_filter finish with no text is blank for the reader: it must fail over
// like any other empty reply (Vertex safety blocks stream a lone "\n" and were
// billed as a 200), but it is the content being refused, not the lane being
// dead, so it never counts toward the channel's empty-response disable.
func TestEmptyResponseContentFilterFailsOverWithoutDisable(t *testing.T) {
	opts := emptyResponseOptions(nil, true)
	apiErr := types.NewOpenAIError(errors.New(emptyResponseMessage(true)),
		types.ErrorCodeChannelEmptyResponse, http.StatusTooManyRequests, opts...)
	assert.True(t, types.IsSkipDisableError(apiErr))
	assert.NotEqual(t, emptyResponseMessage(true), emptyResponseMessage(false))
	assert.Contains(t, emptyResponseMessage(true), "blocked this content")
}

func TestOaiStreamHandlerContentFilterBlankFailsOver(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	ms := operation_setting.GetMonitorSetting()
	oldFlag := ms.DisableOnEmptyResponse
	ms.DisableOnEmptyResponse = true
	t.Cleanup(func() { ms.DisableOnEmptyResponse = oldFlag })

	// Vertex shape: role opener, a whitespace-only delta, then content_filter.
	sse := `data: {"id":"chatcmpl-v","object":"chat.completion.chunk","created":100,"model":"glm-4.7","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"choices":[{"index":0,"delta":{"content":"\n"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":3000,"completion_tokens":1,"total_tokens":3001}}

data: [DONE]

`
	c, recorder, resp, info := newStreamTestContext(t, sse)
	_, apiErr := OaiStreamHandler(c, info, resp)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeChannelEmptyResponse, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.True(t, types.IsSkipDisableError(apiErr), "content-filter empties must not disable the lane")
	assert.Empty(t, recorder.Body.String(), "nothing may be committed to the client before failover")
}
