package controller

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/go-fuego/fuego"
)

func TestStatus(c fuego.ContextNoBody) (*dto.Response[dto.TestStatusData], error) {
	err := model.PingDB()
	if err != nil {
		return dto.Fail[dto.TestStatusData]("Database connection failed")
	}
	// 获取HTTP统计信息
	httpStats := middleware.GetStats()
	return dto.Ok(dto.TestStatusData{HttpStats: httpStats})
}

func GetStatus(c fuego.ContextNoBody) (*dto.Response[dto.StatusData], error) {
	cs := console_setting.GetConsoleSetting()
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()

	passkeySetting := system_setting.GetPasskeySettings()
	legalSetting := system_setting.GetLegalSettings()

	data := dto.StatusData{
		Version:                  common.Version,
		StartTime:                common.StartTime,
		EmailVerification:        common.EmailVerificationEnabled,
		GitHubOAuth:              common.GitHubOAuthEnabled,
		GitHubClientId:           common.GitHubClientId,
		DiscordOAuth:             system_setting.GetDiscordSettings().Enabled,
		DiscordClientId:          system_setting.GetDiscordSettings().ClientId,
		LinuxDOOAuth:             common.LinuxDOOAuthEnabled,
		LinuxDOClientId:          common.LinuxDOClientId,
		LinuxDOMinimumTrustLevel: common.LinuxDOMinimumTrustLevel,
		TelegramOAuth:            common.TelegramOAuthEnabled,
		TelegramBotName:          common.TelegramBotName,
		Theme:                    system_setting.GetThemeSettings().Frontend,
		SystemName:               common.SystemName,
		Logo:                     common.Logo,
		FooterHtml:               common.Footer,
		WeChatQrcode:             common.WeChatAccountQRCodeImageURL,
		WeChatLogin:              common.WeChatAuthEnabled,
		ServerAddress:            system_setting.ServerAddress,
		TurnstileCheck:           common.TurnstileCheckEnabled,
		TurnstileSiteKey:         common.TurnstileSiteKey,
		TopUpLink:                common.TopUpLink,
		DocsLink:                 operation_setting.GetGeneralSetting().DocsLink,
		QuotaPerUnit:             common.QuotaPerUnit,
		// 兼容旧前端：保留 display_in_currency，同时提供新的 quota_display_type
		DisplayInCurrency:          operation_setting.IsCurrencyDisplay(),
		QuotaDisplayType:           operation_setting.GetQuotaDisplayType(),
		CustomCurrencySymbol:       operation_setting.GetGeneralSetting().CustomCurrencySymbol,
		CustomCurrencyExchangeRate: operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate,
		EnableBatchUpdate:          common.BatchUpdateEnabled,
		EnableDrawing:              common.DrawingEnabled,
		EnableTask:                 common.TaskEnabled,
		EnableDataExport:           common.DataExportEnabled,
		DataExportDefaultTime:      common.DataExportDefaultTime,
		DefaultCollapseSidebar:     common.DefaultCollapseSidebar,
		MjNotifyEnabled:            setting.MjNotifyEnabled,
		Chats:                      setting.Chats,
		DemoSiteEnabled:            operation_setting.DemoSiteEnabled,
		SelfUseModeEnabled:         operation_setting.SelfUseModeEnabled,
		RegisterEnabled:            common.RegisterEnabled,
		DefaultUseAutoGroup:        setting.DefaultUseAutoGroup,
		UsdExchangeRate:            operation_setting.USDExchangeRate,
		Price:                      operation_setting.Price,
		StripeUnitPrice:            setting.StripeUnitPrice,
		// 面板启用开关
		ApiInfoEnabled:       cs.ApiInfoEnabled,
		UptimeKumaEnabled:    cs.UptimeKumaEnabled,
		AnnouncementsEnabled: cs.AnnouncementsEnabled,
		FaqEnabled:           cs.FAQEnabled,
		// 模块管理配置
		HeaderNavModules:          common.OptionMap["HeaderNavModules"],
		SidebarModulesAdmin:       common.OptionMap["SidebarModulesAdmin"],
		OidcEnabled:               system_setting.GetOIDCSettings().Enabled,
		OidcClientId:              system_setting.GetOIDCSettings().ClientId,
		OidcAuthorizationEndpoint: system_setting.GetOIDCSettings().AuthorizationEndpoint,
		OidcDisplayName:           system_setting.GetOIDCSettings().GetEffectiveDisplayName(),
		PasskeyLogin:              passkeySetting.Enabled,
		PasskeyDisplayName:        passkeySetting.RPDisplayName,
		PasskeyRpId:               passkeySetting.RPID,
		PasskeyOrigins:            passkeySetting.Origins,
		PasskeyAllowInsecure:      passkeySetting.AllowInsecureOrigin,
		PasskeyUserVerification:   passkeySetting.UserVerification,
		PasskeyAttachment:         passkeySetting.AttachmentPreference,
		Setup:                     constant.Setup,
		UserAgreementEnabled:      legalSetting.UserAgreement != "",
		UserAgreementUrl:          externalDocUrl(legalSetting.UserAgreement),
		PrivacyPolicyEnabled:      legalSetting.PrivacyPolicy != "",
		PrivacyPolicyUrl:          externalDocUrl(legalSetting.PrivacyPolicy),
		AboutUrl:                  externalDocUrl(common.Interface2String(common.OptionMap["About"])),
		CheckinEnabled:            operation_setting.GetCheckinSetting().Enabled,
		PasswordLoginEnabled:      common.PasswordLoginEnabled,
		PasswordRegisterEnabled:   common.PasswordRegisterEnabled,
		QN:                        "new-api",
	}

	// 根据启用状态注入可选内容
	if cs.ApiInfoEnabled {
		data.ApiInfo = console_setting.GetApiInfo()
	}
	if cs.AnnouncementsEnabled {
		data.Announcements = console_setting.GetAnnouncements()
	}
	if cs.FAQEnabled {
		data.FAQ = console_setting.GetFAQ()
	}

	// Add enabled custom OAuth providers
	customProviders := oauth.GetEnabledCustomProviders()
	if len(customProviders) > 0 {
		providersInfo := make([]dto.CustomOAuthInfo, 0, len(customProviders))
		for _, p := range customProviders {
			config := p.GetConfig()
			providersInfo = append(providersInfo, dto.CustomOAuthInfo{
				Id:                    config.Id,
				Name:                  config.Name,
				Slug:                  config.Slug,
				Icon:                  config.Icon,
				ClientId:              config.ClientId,
				AuthorizationEndpoint: config.AuthorizationEndpoint,
				Scopes:                config.Scopes,
			})
		}
		data.CustomOAuthProviders = providersInfo
	}

	return dto.Ok(data)
}

func GetNotice(c fuego.ContextNoBody) (*dto.Response[string], error) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return dto.Ok(common.Interface2String(common.OptionMap["Notice"]))
}

func GetAbout(c fuego.ContextNoBody) (*dto.Response[string], error) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return dto.Ok(common.Interface2String(common.OptionMap["About"]))
}

// externalDocUrl returns the trimmed content when it is a plain http(s) URL,
// otherwise an empty string. Used to let the login/register pages and nav bar
// link directly to an external document instead of the internal viewer route.
func externalDocUrl(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	return trimmed
}

func GetUserAgreement(c fuego.ContextNoBody) (*dto.Response[string], error) {
	return dto.Ok(system_setting.GetLegalSettings().UserAgreement)
}

func GetPrivacyPolicy(c fuego.ContextNoBody) (*dto.Response[string], error) {
	return dto.Ok(system_setting.GetLegalSettings().PrivacyPolicy)
}

func GetMidjourney(c fuego.ContextNoBody) (*dto.Response[string], error) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return dto.Ok(common.Interface2String(common.OptionMap["Midjourney"]))
}

func GetHomePageContent(c fuego.ContextNoBody) (*dto.Response[string], error) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return dto.Ok(common.Interface2String(common.OptionMap["HomePageContent"]))
}

func SendEmailVerification(c fuego.ContextWithParams[dto.EmailParams]) (dto.MessageResponse, error) {
	p, err := dto.ParseParams[dto.EmailParams](c)
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if err := common.Validate.Var(p.Email, "required,email"); err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	parts := strings.Split(p.Email, "@")
	if len(parts) != 2 {
		return dto.FailMsg("Invalid email address")
	}
	localPart := parts[0]
	domainPart := parts[1]
	if common.EmailDomainRestrictionEnabled {
		allowed := false
		for _, domain := range common.EmailDomainWhitelist {
			if domainPart == domain {
				allowed = true
				break
			}
		}
		if !allowed {
			return dto.FailMsg("The administrator has enabled the email domain name whitelist, and your email address is not allowed due to special symbols or it's not in the whitelist.")
		}
	}
	if common.EmailAliasRestrictionEnabled {
		containsSpecialSymbols := strings.Contains(localPart, "+") || strings.Contains(localPart, ".")
		if containsSpecialSymbols {
			return dto.FailMsg("Administrator has enabled email address alias restriction, your email address has been rejected due to containing special characters.")
		}
	}

	if model.IsEmailAlreadyTaken(p.Email) {
		return dto.FailMsg("Email address is already taken")
	}
	code := common.GenerateVerificationCode(6)
	common.RegisterVerificationCodeWithKey(p.Email, code, common.EmailVerificationPurpose)
	subject := fmt.Sprintf("%v Email Verification", common.SystemName)
	content := fmt.Sprintf("<p>Hello, you are performing %v email verification.</p><p>Your verification code is: <strong>%v</strong></p><p>The verification code is valid for %v minutes. If this was not initiated by you, please ignore this email.</p>", common.SystemName, code, common.VerificationValidMinutes)
	err = common.SendEmail(subject, p.Email, content)
	if err != nil {
		common.SysError("failed to send email verification: " + err.Error())
		return dto.FailMsg("Failed to send email, please try again later")
	}
	return dto.Msg("")
}

func SendPasswordResetEmail(c fuego.ContextWithParams[dto.EmailParams]) (dto.MessageResponse, error) {
	p, err := dto.ParseParams[dto.EmailParams](c)
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if err := common.Validate.Var(p.Email, "required,email"); err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	// Never reveal whether the address is registered: send silently when it is,
	// stay quiet otherwise, and only log genuine lookup failures.
	if _, err := model.GetUniqueUserForPasswordReset(p.Email); err == nil {
		code := common.GenerateVerificationCode(0)
		common.RegisterVerificationCodeWithKey(p.Email, code, common.PasswordResetPurpose)
		// The bundled UI serves the confirm page at /user/reset; a separate
		// frontend owns its own route, so the path follows the base.
		resetPath := "/user/reset"
		if system_setting.FrontendAddress != "" {
			resetPath = "/reset"
		}
		link := fmt.Sprintf("%s%s?email=%s&token=%s", system_setting.UserLinkBase(), resetPath, url.QueryEscape(p.Email), url.QueryEscape(code))
		subject := fmt.Sprintf("%v Password Reset", common.SystemName)
		content := fmt.Sprintf("<p>Hello, you are performing %v password reset.</p><p>Click <a href='%v'>here</a> to reset your password.</p><p>If the link cannot be opened, please copy the following URL into your browser: %v</p><p>The reset link is valid for %v minutes. If this was not initiated by you, please ignore this email.</p>", common.SystemName, link, link, common.VerificationValidMinutes)
		if err := common.SendEmail(subject, p.Email, content); err != nil {
			common.SysError("failed to send password reset email: " + err.Error())
			return dto.FailMsg("Failed to send email, please try again later")
		}
	} else if !errors.Is(err, model.ErrEmailNotFound) {
		common.SysError("failed to send password reset email: " + err.Error())
	}
	return dto.Msg("")
}

func ResetPassword(c fuego.ContextWithBody[dto.PasswordResetRequest]) (*dto.Response[string], error) {
	req, err := c.Body()
	if err != nil || req.Email == "" || req.Token == "" {
		return dto.Fail[string](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if !common.VerifyCodeWithKey(req.Email, req.Token, common.PasswordResetPurpose) {
		return dto.Fail[string]("Reset link is invalid or has expired")
	}
	password := common.GenerateVerificationCode(12)
	err = model.ResetUserPasswordByEmail(req.Email, password)
	if err != nil {
		common.SysError("failed to reset password for " + req.Email + ": " + err.Error())
		return dto.Fail[string]("Failed to reset password, please try again later")
	}
	common.DeleteKey(req.Email, common.PasswordResetPurpose)
	return dto.Ok(password)
}
