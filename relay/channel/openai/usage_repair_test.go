package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Some upstreams report a real prompt count next to a stub completion count:
// OpenRouter's minimax-m3 returns the whole reply while reporting
// completion_tokens 0, with the output attributed to reasoning. The repair used
// to run only when the PROMPT count was stubbed, so those replies billed as
// free. Each side has to be repaired independently.
func TestUsageRepairCountsStubbedCompletionTokens(t *testing.T) {
	parse := func(body string) *dto.OpenAITextResponse {
		var r dto.OpenAITextResponse
		require.NoError(t, common.Unmarshal([]byte(body), &r))
		return &r
	}

	cases := []struct {
		name              string
		body              string
		promptStubbed     bool
		completionStubbed bool
	}{
		{
			name:              "real prompt count with a zero completion count",
			body:              `{"choices":[{"index":0,"message":{"role":"assistant","content":"The classroom stretched between them."}}],"usage":{"prompt_tokens":337,"completion_tokens":0,"total_tokens":337}}`,
			promptStubbed:     false,
			completionStubbed: true,
		},
		{
			name:              "output reported only as reasoning",
			body:              `{"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning":"Working through it."}}],"usage":{"prompt_tokens":173,"completion_tokens":0,"total_tokens":173}}`,
			promptStubbed:     false,
			completionStubbed: true,
		},
		{
			name:              "both sides unreported",
			body:              `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello."}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			promptStubbed:     true,
			completionStubbed: true,
		},
		{
			name:              "fully reported usage is left alone",
			body:              `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello."}}],"usage":{"prompt_tokens":337,"completion_tokens":22,"total_tokens":359}}`,
			promptStubbed:     false,
			completionStubbed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := parse(tc.body)

			assert.Equal(t, tc.promptStubbed, service.IsStubTokenCount(resp.Usage.PromptTokens))
			assert.Equal(t, tc.completionStubbed, service.IsStubTokenCount(resp.Usage.CompletionTokens))

			// The repair path the handler takes: whenever the completion count is
			// a stub, recount from the reply text so the response is never free.
			if tc.completionStubbed {
				counted := 0
				for _, choice := range resp.Choices {
					counted += service.CountTextToken(
						choice.Message.StringContent()+choice.Message.GetReasoningContent(),
						"gpt-4o",
					)
				}
				assert.Positive(t, counted, "a reply with text must bill more than zero completion tokens")
			}
		})
	}
}
