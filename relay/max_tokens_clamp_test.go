package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
)

// The clamp exists because clients that size max output off the CONTEXT window
// send values no provider accepts, and the upstream 400 is retried across every
// sibling channel before it surfaces.
func TestApplyOutputLimit(t *testing.T) {
	ptr := func(v uint) *uint { return &v }
	cases := []struct {
		name         string
		limit        int
		maxTokens    *uint
		wantMaxToken *uint
	}{
		{"clamps a context-sized value", 65536, ptr(958318), ptr(65536)},
		{"clamps one over the cap", 65536, ptr(65537), ptr(65536)},
		{"leaves the cap itself", 65536, ptr(65536), ptr(65536)},
		{"leaves a value under the cap", 65536, ptr(8192), ptr(8192)},
		{"no metadata leaves the request alone", 0, ptr(958318), ptr(958318)},
		{"implausible limit is ignored", 1, ptr(8192), ptr(8192)},
		{"unset field stays unset", 65536, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &dto.GeneralOpenAIRequest{MaxTokens: tc.maxTokens}
			applyOutputLimit(tc.limit, req)
			if tc.wantMaxToken == nil {
				assert.Nil(t, req.MaxTokens)
				return
			}
			assert.Equal(t, *tc.wantMaxToken, *req.MaxTokens)
		})
	}
}

// Both fields carry the cap depending on the client dialect, so both must clamp.
func TestApplyOutputLimitClampsCompletionTokens(t *testing.T) {
	v := uint(958318)
	req := &dto.GeneralOpenAIRequest{MaxCompletionTokens: &v}
	applyOutputLimit(65536, req)
	assert.Equal(t, uint(65536), *req.MaxCompletionTokens)
}

// A request that omits max_tokens still gets one, because Anthropic requires the
// field. One number shared by the whole family was the wrong answer: it
// truncated every model allowing more than 8192 and overshot the one allowing
// 4096, and truncation surfaces as finish_reason=length with no error at all.
func TestResolveDefaultMaxTokens(t *testing.T) {
	pinned := map[string]int{"default": 8192, "claude-pinned": 16384}
	cases := []struct {
		name         string
		model        string
		publishedCap int
		want         int
	}{
		{"published cap beats the shared fallback", "claude-fable-5", 128000, 128000},
		{"a smaller published cap is honored", "claude-haiku", 4096, 4096},
		{"an entry naming the model outranks the cap", "claude-pinned", 128000, 16384},
		{"no metadata falls back to the shared value", "claude-unknown", 0, 8192},
		{"implausible metadata cannot become the default", "claude-odd", 1, 8192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveDefaultMaxTokens(tc.model, pinned, tc.publishedCap))
		})
	}
}
