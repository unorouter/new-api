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
