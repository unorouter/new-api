package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
)

// Image upstreams frequently report no usage. The handler used to stamp a
// literal 1, so a prompt of any length billed as one token and per-channel cost
// was meaningless. Counting locally must produce a real count for a real
// prompt, and must not itself look like the stub it replaces.
func TestImagePromptTokensAreCountedNotStubbed(t *testing.T) {
	const model = "cogview-4-250304"

	cases := []struct {
		name   string
		prompt string
	}{
		{"typical prompt", "A photorealistic portrait of a fisherman mending nets at dawn"},
		{"short prompt", "a cat"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := service.CountTextToken(tc.prompt, model)
			assert.False(t, service.IsStubTokenCount(got),
				"a real prompt must not count as a stub placeholder")
			assert.Greater(t, got, 1, "prompt must cost more than the old hardcoded 1")
		})
	}

	// An empty prompt has nothing to charge for and stays stub-shaped, so the
	// TotalTokens fallback must not be tricked into reporting a phantom cost.
	assert.True(t, service.IsStubTokenCount(service.CountTextToken("", model)))
}
