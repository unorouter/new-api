package service

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
)

func TestValidUsage(t *testing.T) {
	cases := []struct {
		name  string
		usage *dto.Usage
		want  bool
	}{
		{"nil", nil, false},
		{"unreported", &dto.Usage{}, false},
		// A reverse-engineered upstream that reports a constant 1 would otherwise
		// bill every request, however large, as two tokens.
		{"stub placeholder", &dto.Usage{PromptTokens: 1, CompletionTokens: 1}, false},
		{"real counts", &dto.Usage{PromptTokens: 42, CompletionTokens: 7}, true},
		{"prompt only", &dto.Usage{PromptTokens: 42}, true},
		{"completion only", &dto.Usage{CompletionTokens: 7}, true},
		// One side stubbed is still worth trusting: the other carries a real count.
		{"stub prompt, real completion", &dto.Usage{PromptTokens: 1, CompletionTokens: 7}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ValidUsage(tc.usage))
		})
	}
}

func TestIsStubTokenCount(t *testing.T) {
	assert.True(t, IsStubTokenCount(0))
	assert.True(t, IsStubTokenCount(1))
	assert.False(t, IsStubTokenCount(2))
}
