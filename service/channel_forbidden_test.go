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
		"cn content audit":  "内容审计命中风险规则，请调整输入后重试",
		"safe guard":        "This request was blocked by safe guard policy.",
		"usage policy":      "Your request was rejected because it violates our usage policy.",
		"usage guidelines":  `{"code":"permission-denied","error":"Content violates usage guidelines."}`,
		"image size limit":  `{"message": "Due to heavy demand, for requests over 844x844 or over 50 steps"}`,
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
}
