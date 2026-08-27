package controller

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/go-fuego/fuego"
)

var completionRatioMetaOptionKeys = []string{
	"ModelPrice",
	"ModelRatio",
	"CompletionRatio",
	"CacheRatio",
	"CreateCacheRatio",
	"ImageRatio",
	"AudioRatio",
	"AudioCompletionRatio",
}

func isPaymentComplianceOptionKey(key string) bool {
	return strings.HasPrefix(key, "payment_setting.compliance_")
}

func isPositiveOptionValue(value string) bool {
	intValue, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil {
		return intValue > 0
	}
	floatValue, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && floatValue > 0
}

func collectModelNamesFromOptionValue(raw string, modelNames map[string]struct{}) {
	if strings.TrimSpace(raw) == "" {
		return
	}

	var parsed map[string]any
	if err := common.UnmarshalJsonStr(raw, &parsed); err != nil {
		return
	}

	for modelName := range parsed {
		modelNames[modelName] = struct{}{}
	}
}

func buildCompletionRatioMetaValue(optionValues map[string]string) string {
	modelNames := make(map[string]struct{})
	for _, key := range completionRatioMetaOptionKeys {
		collectModelNamesFromOptionValue(optionValues[key], modelNames)
	}

	meta := make(map[string]ratio_setting.CompletionRatioInfo, len(modelNames))
	for modelName := range modelNames {
		meta[modelName] = ratio_setting.GetCompletionRatioInfo(modelName)
	}

	jsonBytes, err := common.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

func GetOptions(c fuego.ContextNoBody) (*dto.Response[[]*model.Option], error) {
	var options []*model.Option
	optionValues := make(map[string]string)
	common.OptionMapRWMutex.Lock()
	for k, v := range common.OptionMap {
		if k == "theme.frontend" {
			continue
		}
		value := common.Interface2String(v)
		isSensitiveKey := strings.HasSuffix(k, "Token") ||
			strings.HasSuffix(k, "Secret") ||
			strings.HasSuffix(k, "Key") ||
			strings.HasSuffix(k, "Password") ||
			strings.HasSuffix(k, "secret") ||
			strings.HasSuffix(k, "api_key") ||
			strings.HasSuffix(k, "password")
		if isSensitiveKey {
			continue
		}
		options = append(options, &model.Option{
			Key:   k,
			Value: value,
		})
		for _, optionKey := range completionRatioMetaOptionKeys {
			if optionKey == k {
				optionValues[k] = value
				break
			}
		}
	}
	common.OptionMapRWMutex.Unlock()
	options = append(options, &model.Option{
		Key:   "CompletionRatioMeta",
		Value: buildCompletionRatioMetaValue(optionValues),
	})
	return dto.Ok(options)
}

// syncAllowedOptionKeys is every option new-api-sync writes: pricing, group
// routing and the per-model rate-limit map, taken from MANAGED_OPTION_KEYS in
// the sync's own src/core/types.ts.
//
// The option route is the ONLY thing that ever required root of the sync, and
// nothing in this list is security-relevant. Without the gate below, the sync
// credential could still set TurnstileCheckEnabled -- the exact option a
// 2026-08-26 intruder toggled 36 times to work through the login captcha -- and
// taking the sync off root would have bought almost nothing.
//
// Adding a key here widens what a leaked sync token can rewrite. Anything
// affecting authentication, registration or payment belongs to root only.
var syncAllowedOptionKeys = map[string]bool{
	"GroupRatio":                   true,
	"UserUsableGroups":             true,
	"AutoGroups":                   true,
	"DefaultUseAutoGroup":          true,
	"ModelRatio":                   true,
	"CompletionRatio":              true,
	"ModelPrice":                   true,
	"ImageRatio":                   true,
	"CacheRatio":                   true,
	"CreateCacheRatio":             true,
	"AudioRatio":                   true,
	"AudioCompletionRatio":         true,
	"ModelQuotaType":               true,
	"ModelGridPricing":             true,
	"ModelRequestRateLimitModels":  true,
	"billing_setting.billing_mode": true,
	"billing_setting.billing_expr": true,
	"global.chat_completions_to_responses_policy": true,
}

func UpdateOption(c fuego.ContextWithBody[dto.OptionUpdateRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	option, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	// The sync's service credential reaches this route for pricing and routing
	// only. Route access alone is not enough here: one handler serves every
	// option, so without this check the token still reaches the auth-hardening
	// switches that made the takeover possible.
	if middleware.AuthenticatedViaSyncToken(ginCtx) && !syncAllowedOptionKeys[option.Key] {
		return dto.FailMsg(common.TranslateMessage(ginCtx, i18n.MsgAuthInsufficientPrivilege))
	}
	switch option.Value.(type) {
	case bool:
		option.Value = common.Interface2String(option.Value.(bool))
	case float64:
		option.Value = common.Interface2String(option.Value.(float64))
	case int:
		option.Value = common.Interface2String(option.Value.(int))
	default:
		option.Value = fmt.Sprintf("%v", option.Value)
	}
	switch option.Key {
	case "QuotaForInviter", "QuotaForInvitee":
		if isPositiveOptionValue(option.Value.(string)) && !operation_setting.IsPaymentComplianceConfirmed() {
			return dto.FailMsg(common.TranslateMessage(ginCtx, i18n.MsgPaymentComplianceRequired))
		}
	default:
		if isPaymentComplianceOptionKey(option.Key) {
			return dto.FailMsg("Compliance confirmation fields cannot be modified through the general settings interface")
		}
	}
	switch option.Key {
	case "GitHubOAuthEnabled":
		if option.Value == "true" && common.GitHubClientId == "" {
			return dto.FailMsg("Cannot enable GitHub OAuth, please fill in GitHub Client ID and GitHub Client Secret first!")
		}
	case "discord.enabled":
		if option.Value == "true" && system_setting.GetDiscordSettings().ClientId == "" {
			return dto.FailMsg("Cannot enable Discord OAuth, please fill in Discord Client ID and Discord Client Secret first!")
		}
	case "oidc.enabled":
		if option.Value == "true" && system_setting.GetOIDCSettings().ClientId == "" {
			return dto.FailMsg("Cannot enable OIDC login, please fill in OIDC Client ID and OIDC Client Secret first!")
		}
	case "LinuxDOOAuthEnabled":
		if option.Value == "true" && common.LinuxDOClientId == "" {
			return dto.FailMsg("Cannot enable LinuxDO OAuth, please fill in LinuxDO Client ID and LinuxDO Client Secret first!")
		}
	case "EmailDomainRestrictionEnabled":
		if option.Value == "true" && len(common.EmailDomainWhitelist) == 0 {
			return dto.FailMsg("Cannot enable email domain restriction, please fill in the restricted email domains first!")
		}
	case "WeChatAuthEnabled":
		if option.Value == "true" && common.WeChatServerAddress == "" {
			return dto.FailMsg("Cannot enable WeChat login, please fill in WeChat login configuration first!")
		}
	case "TurnstileCheckEnabled":
		if option.Value == "true" && common.TurnstileSiteKey == "" {
			return dto.FailMsg("Cannot enable Turnstile verification, please fill in Turnstile configuration first!")
		}
	case "TelegramOAuthEnabled":
		if option.Value == "true" && common.TelegramBotToken == "" {
			return dto.FailMsg("Cannot enable Telegram OAuth, please fill in Telegram Bot Token first!")
		}
	case "theme.frontend":
		if option.Value != "default" && option.Value != "classic" {
			return dto.FailMsg("Invalid theme value. Allowed values: default (new frontend), classic (legacy frontend)")
		}
	case "GroupRatio":
		err = ratio_setting.CheckGroupRatio(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "gemini.safety_settings":
		err = model_setting.ValidateGeminiSafetySettings(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "claude.default_max_tokens":
		err = model_setting.ValidateClaudeDefaultMaxTokens(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case operation_setting.ToolPriceOptionKey:
		err = operation_setting.ValidateToolPricesJSON(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "ImageRatio":
		err = ratio_setting.UpdateImageRatioByJSONString(option.Value.(string))
		if err != nil {
			return dto.FailMsg(fmt.Sprintf("Failed to set image ratio: %v", err.Error()))
		}
	case "AudioRatio":
		err = ratio_setting.UpdateAudioRatioByJSONString(option.Value.(string))
		if err != nil {
			return dto.FailMsg(fmt.Sprintf("Failed to set audio ratio: %v", err.Error()))
		}
	case "AudioCompletionRatio":
		err = ratio_setting.UpdateAudioCompletionRatioByJSONString(option.Value.(string))
		if err != nil {
			return dto.FailMsg(fmt.Sprintf("Failed to set audio completion ratio: %v", err.Error()))
		}
	case "CreateCacheRatio":
		err = ratio_setting.UpdateCreateCacheRatioByJSONString(option.Value.(string))
		if err != nil {
			return dto.FailMsg(fmt.Sprintf("Failed to set cache creation ratio: %v", err.Error()))
		}
	case "ModelRequestRateLimitGroup":
		err = setting.CheckModelRequestRateLimitGroup(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "ModelRequestRateLimitModels":
		err = setting.CheckModelRequestRateLimitModels(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "AutomaticDisableStatusCodes":
		_, err = operation_setting.ParseHTTPStatusCodeRanges(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "AutomaticRetryStatusCodes":
		_, err = operation_setting.ParseHTTPStatusCodeRanges(option.Value.(string))
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "console_setting.api_info":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "ApiInfo")
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "console_setting.announcements":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "Announcements")
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "console_setting.faq":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "FAQ")
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	case "console_setting.uptime_kuma_groups":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "UptimeKumaGroups")
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	err = model.UpdateOption(option.Key, option.Value.(string))
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	// 出于安全考虑只记录被修改的配置项名称，不记录配置值（可能含密钥等敏感信息）。
	recordManageAudit(dto.GinCtx(c), "option.update", map[string]interface{}{
		"key": option.Key,
	})
	return dto.Msg("")
}
