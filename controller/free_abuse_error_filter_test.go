package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
)

// A content-filter refusal is a 400, so isTransientInfraError never covers it.
// Before this filter existed those refusals counted toward the hourly free-error
// budget and auto-blocked real users: one upstream serves every free lane and its
// filter rejects ordinary roleplay often enough to cross the threshold in one
// sitting. Nine accounts with 113 to 3638 successful requests were blocked that
// way, every one of them at ~40 rejections in an hour.
func TestModerationRejectionsAreNotCountedAsFreeModelAbuse(t *testing.T) {
	// Verbatim from the production log of a blocked account (user 23831).
	const chatglmReject = "非常抱歉，我目前无法提供你需要的具体信息，如果你有其他的问题或者需要查找其他信息，我非常乐意帮助你。 (shared-filter moderation: input_sensitive/REJECT)"

	for _, tc := range []struct {
		name    string
		err     *types.NewAPIError
		exempt  bool
		comment string
	}{
		{
			name:   "chatglm shared filter reject",
			err:    types.NewOpenAIError(errors.New(chatglmReject), types.ErrorCodeBadResponse, http.StatusBadRequest),
			exempt: true,
		},
		{
			name:   "generic content policy reject",
			err:    types.NewOpenAIError(errors.New("your request was rejected by our content policy"), types.ErrorCodeBadResponse, http.StatusBadRequest),
			exempt: true,
		},
		{
			name:   "bedrock guardrail reject",
			err:    types.NewOpenAIError(errors.New("Blocked by guardrail policy"), types.ErrorCodeBadResponse, http.StatusBadRequest),
			exempt: true,
		},
		{
			name:   "model not found still counts",
			err:    types.NewOpenAIError(errors.New("the model does not exist"), types.ErrorCodeModelNotFound, http.StatusNotFound),
			exempt: false,
		},
		{
			name:   "plain bad request still counts",
			err:    types.NewOpenAIError(errors.New("invalid request payload"), types.ErrorCodeBadResponse, http.StatusBadRequest),
			exempt: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.exempt, isModerationRejection(tc.err))
		})
	}
}

// Infra faults were already exempt and must stay exempt: an overloaded free model
// is our capacity failing, not the user misbehaving.
func TestTransientInfraErrorsStayExempt(t *testing.T) {
	for _, status := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		err := types.NewOpenAIError(errors.New("upstream unavailable"), types.ErrorCodeBadResponse, status)
		assert.True(t, isTransientInfraError(err), "status %d must be exempt", status)
	}
}
