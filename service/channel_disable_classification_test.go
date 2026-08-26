package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
)

func mk403(msg string) *types.NewAPIError {
	return types.NewOpenAIError(errors.New(msg), types.ErrorCodeBadResponseStatusCode, http.StatusForbidden)
}

func TestForbiddenDisableMap(t *testing.T) {
	common.AutomaticDisableChannelEnabled = true
	// Match production: the DB option, not the 401-only code default.
	if err := operation_setting.AutomaticDisableStatusCodesFromString("400-406,410,422,429,500,502-504,520-521,523-530"); err != nil {
		t.Fatal(err)
	}
	shouldDisable := map[string]string{
		"drained wallet (gptnb)":  "user [1924] quota [814] preConsumedQuota [113605] is not enough",
		"drained wallet (holdai)": "预扣费额度失败, 用户剩余额度: $0.448134",
		"no group access":         "无权访问 vip_1_azure 分组",
		"bare 403":                "bad response status code 403",
		"trial expired":           "Your 24-hour unverified experience period has ended.",
		"auth failed":             "Forbidden: authentication failed.",
		"plan gated":              "Your plan does not include the requested model.",
		"workers free":            "AiError: Model @cf/moonshotai/kimi-k2.6 is not available on the Workers Free plan",
	}
	neverDisable := map[string]string{
		"cn content audit": "内容审计命中风险规则，请调整输入后重试",
		"safe guard":       "This request was blocked by safe guard policy.",
		"usage policy":     "Your request was rejected because it violates our usage policy.",
		"usage guidelines": `{"code":"permission-denied","error":"Content violates usage guidelines."}`,
		"image size limit": `{"message": "Due to heavy demand, for requests over 844x844 or over 50 steps"}`,
	}
	// An upstream running new-api quotes OUR wording back, and the same sentence
	// means either "one of our end users is banned there" (channel healthy) or
	// "our account is banned there" (channel dead). Nothing in the text tells them
	// apart, so this layer flags it and the caller's failure-RATE guard decides:
	// a channel still serving others survives on its successes.
	rateGuardDecides := map[string]string{
		"our banned user":   "User has been banned (request id: X)",
		"our disabled chan": "This channel has been disabled (request id: X)",
		"our model busy":    "This model is busy right now (free providers hit their rate limit).",
	}
	for name, msg := range shouldDisable {
		assert.True(t, ShouldDisableChannel(mk403(msg)), "SHOULD disable: %s", name)
	}
	for name, msg := range neverDisable {
		assert.False(t, ShouldDisableChannel(mk403(msg)), "must NOT disable: %s", name)
	}
	for name, msg := range rateGuardDecides {
		assert.True(t, ShouldDisableChannel(mk403(msg)),
			"must reach the rate guard rather than being excluded here: %s", name)
	}
}

func mk(code int, msg string) *types.NewAPIError {
	return types.NewOpenAIError(errors.New(msg), types.ErrorCodeBadResponseStatusCode, code)
}

// Every shape below is a real production message from the 30-day error map.
func TestModerationNeverDisablesRegardlessOfStatus(t *testing.T) {
	common.AutomaticDisableChannelEnabled = true
	if err := operation_setting.AutomaticDisableStatusCodesFromString("400-406,410,422,429,500,502-504,520-521,523-530"); err != nil {
		t.Fatal(err)
	}
	moderation := []struct {
		code int
		msg  string
	}{
		{500, "sensitive words detected"},
		{500, "sensitive_words_detected"},
		{500, "request blocked by Google Gemini (PROHIBITED_CONTENT): content is prohibited"},
		{500, `{"code":"cyber_policy","message":"This content was flagged for possible"}`},
		{503, "Content moderation is temporarily unavailable."},
		{502, "Upstream error from AtlasCloud: Input data may contain inappropriate content."},
		{502, "Upstream error from Alibaba: Output data may contain inappropriate content."},
		{502, "Invalid prompt: your prompt was flagged as potentially violating our usage policy"},
		{429, "Output data may contain inappropriate content."},
		{429, "内容审计命中风险规则，请调整输入后重试"},
		{429, "The model output could not be generated. This output contains sensitive"},
		{400, "Input data may contain inappropriate content."},
		{403, `{"code":"permission-denied","error":"Content violates usage guidelines."}`},
	}
	for _, c := range moderation {
		assert.False(t, ShouldDisableChannel(mk(c.code, c.msg)),
			"moderation must not disable: [%d] %s", c.code, c.msg)
	}
	// The transient/dead codes this fix must NOT have neutered.
	stillDisable := []struct {
		code int
		msg  string
	}{
		{504, `Post "https://x.com/v1": http: timeout awaiting response headers`},
		{502, "upstream streamed an empty response (no content/tool calls)"},
		{403, "预扣费额度失败, 用户剩余额度: $0.40"},
		{403, "无权访问 vip_1_azure 分组"},
		{500, "Internal server error"},
	}
	for _, c := range stillDisable {
		assert.True(t, ShouldDisableChannel(mk(c.code, c.msg)),
			"should still disable: [%d] %s", c.code, c.msg)
	}
}
