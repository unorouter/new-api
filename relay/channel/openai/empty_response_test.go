package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAIResponseHasOutput gates the live empty-response auto-disable. These cases
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
			assert.Equal(t, tc.want, openAIResponseHasOutput(parse(tc.body)))
		})
	}
}
