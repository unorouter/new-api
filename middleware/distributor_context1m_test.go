package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The 1M-context alias is published lowercase (model ids are lowercased at sync)
// but Anthropic documents the window as "1M", so clients configure it either way.
// An unnormalized "[1M]" misses the model entirely AND skips the param_override
// that appends the context-1m beta header, so the request would silently route
// without the larger window even if the name resolved.
func TestNormalizeContext1MSuffix(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"uppercase", "claude-opus-4.8[1M]", "claude-opus-4.8[1m]"},
		{"mixed", "claude-opus-4.8[1m]", "claude-opus-4.8[1m]"},
		{"already lowercase", "claude-opus-4.8[1m]", "claude-opus-4.8[1m]"},
		{"no suffix untouched", "claude-opus-4.8", "claude-opus-4.8"},
		{"empty", "", ""},
		{"suffix only is not a model", "[1M]", "[1M]"},
		// A model whose own name ends in these characters must not be rewritten.
		{"unrelated bracket suffix", "some-model[2m]", "some-model[2m]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeContext1MSuffix(c.model))
		})
	}
}
