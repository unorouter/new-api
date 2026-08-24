package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every wording below is copied from the production logs table.
func TestSamplerValueFaultsDoNotFailOver(t *testing.T) {
	cases := []string{
		"Error from provider (Console): Upstream request failed: [1210] The temperature parameter is illegal.：限制数值范围[0,1]",
		"[1210][temperature参数非法：限制数值范围[0,1]][20260822073150938]",
		"The request is invalid: temperature参数非法：限制数值范围[0,1]. Please check the request body",
		"The temperature parameter is illegal.：限制小数点[2]位",
		"Error from provider (Console Go): Upstream request failed: [invalid_request_error] invalid temperature: only 1 is allowed for this model",
		"field temperature invalid",
		"field Temperature invalid, should be in [0.0, 1.0]",
		"Model does not support request parameter value supplied: 'temperature' must be in the range [0.0 and 1.0]",
		"'$.body.input.temperature' value must be less or equal than 1;",
		"top_p must be in (0, 1], got 0.0. (parameter=top_p, value=0.0)",
		"Validation: Top_k must be null, -1, or greater than or equal to 1",
		"AiError: Bad input: Error: oneOf at '/' not met, 0 matches: '/top_k' must be <= 50",
		`{"detail":[{"type":"less_than_equal","loc":["body","temperature"],"msg":"Input should be less than or equal to 1"}]}`,
	}
	for _, msg := range cases {
		err := NewOpenAIError(errors.New(msg), ErrorCodeBadResponse, 400)
		assert.True(t, IsInvalidParamError(err), "sampler value fault: %s", msg)
		assert.False(t, IsTransientUpstream400(err), "must not failover: %s", msg)
	}
}

// A sibling that accepts the field, or has a roomier ceiling, really can serve
// these, so they must keep failing over.
func TestUnsupportedFieldAndTokenCeilingStillFailOver(t *testing.T) {
	cases := []string{
		`Invalid JSON payload received. Unknown name "top_k": Cannot find field.`,
		"top_k: property 'top_k' is unsupported",
		"top_k sampling is not enabled for this model",
		"Unrecognized request argument supplied: top_k",
		`feature 'extra arguments: {"top_k":60}' is not currently supported`,
		"Invalid request parameters (invalid argument) (upstream: Penalty is not enabled for this model)",
		"`max_tokens` must be less than or equal to `4096`, the maximum value for `max_tokens`",
		"field MaxTokens invalid, should be in [1, 384000]",
		"too many tokens: max tokens must be less than or equal to 4096",
	}
	for _, msg := range cases {
		err := NewOpenAIError(errors.New(msg), ErrorCodeBadResponse, 400)
		assert.False(t, IsInvalidParamError(err), "must stay retryable: %s", msg)
	}
}

func TestTransientWrapperAloneStaysTransient(t *testing.T) {
	err := NewOpenAIError(
		errors.New("Error from provider (Console): Upstream request failed: endpoint is degraded"),
		ErrorCodeBadResponse, 400)

	assert.False(t, IsInvalidParamError(err))
	assert.True(t, IsTransientUpstream400(err))
}
