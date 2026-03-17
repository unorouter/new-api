package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	// SecureVerificationSessionKey means the user has fully passed secure verification.
	SecureVerificationSessionKey = "secure_verified_at"
	// PasskeyReadySessionKey means WebAuthn finished and /api/verify can finalize step-up verification.
	PasskeyReadySessionKey = "secure_passkey_ready_at"
	// SecureVerificationTimeout 验证有效期（秒）
	SecureVerificationTimeout = 300 // 5分钟
	// PasskeyReadyTimeout passkey ready 标记有效期（秒）
	PasskeyReadyTimeout = 60
)

func UniversalVerify(c *gin.Context) {
	userId := c.GetInt("id")
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, dto.ApiResponse{Message: common.TranslateMessage(c, "common.not_logged_in")})
		return
	}

	var req dto.UniversalVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorI18n(c, "secure_verification.param_error")
		return
	}

	// 获取用户信息
	user := &model.User{Id: userId}
	if err := user.FillUserById(); err != nil {
		common.ApiErrorI18n(c, "secure_verification.get_user_failed")
		return
	}

	if user.Status != common.UserStatusEnabled {
		common.ApiErrorI18n(c, "secure_verification.user_disabled")
		return
	}

	// 检查用户的验证方式
	twoFA, _ := model.GetTwoFAByUserId(userId)
	has2FA := twoFA != nil && twoFA.IsEnabled

	passkey, passkeyErr := model.GetPasskeyByUserID(userId)
	hasPasskey := passkeyErr == nil && passkey != nil

	if !has2FA && !hasPasskey {
		common.ApiErrorI18n(c, "secure_verification.not_enabled")
		return
	}

	// 根据验证方式进行验证
	var verified bool
	var verifyMethod string
	var err error

	switch req.Method {
	case "2fa":
		if !has2FA {
			common.ApiErrorI18n(c, "secure_verification.twofa_not_enabled")
			return
		}
		if req.Code == "" {
			common.ApiErrorI18n(c, "secure_verification.code_empty")
			return
		}
		verified = validateTwoFactorAuth(twoFA, req.Code)
		verifyMethod = "2FA"

	case "passkey":
		if !hasPasskey {
			common.ApiErrorI18n(c, "secure_verification.passkey_not_enabled")
			return
		}
		// Passkey branch only trusts the short-lived marker written by PasskeyVerifyFinish.
		verified, err = consumePasskeyReady(c)
		if err != nil {
			common.ApiError(c, fmt.Errorf("%s: %v", common.TranslateMessage(c, "secure_verification.passkey_state_error"), err))
			return
		}
		if !verified {
			common.ApiErrorI18n(c, "secure_verification.passkey_verify_first")
			return
		}
		verifyMethod = "Passkey"

	default:
		common.ApiErrorI18n(c, "secure_verification.method_not_supported")
		return
	}

	if !verified {
		common.ApiErrorI18n(c, "secure_verification.failed")
		return
	}

	// 验证成功，在 session 中记录时间戳
	now, err := setSecureVerificationSession(c)
	if err != nil {
		common.ApiErrorI18n(c, "secure_verification.save_state_failed")
		return
	}

	// 记录日志
	model.RecordLog(userId, model.LogTypeSystem, "Universal secure verification succeeded (method: "+verifyMethod+")")

	c.JSON(http.StatusOK, dto.ApiResponse{
		Success: true,
		Message: common.TranslateMessage(c, "passkey.verify_success"),
		Data: dto.VerificationStatusResponse{
			Verified:  true,
			ExpiresAt: now + SecureVerificationTimeout,
		},
	})
}

func setSecureVerificationSession(c *gin.Context) (int64, error) {
	session := sessions.Default(c)
	session.Delete(PasskeyReadySessionKey)
	now := time.Now().Unix()
	session.Set(SecureVerificationSessionKey, now)
	if err := session.Save(); err != nil {
		return 0, err
	}
	return now, nil
}

func consumePasskeyReady(c *gin.Context) (bool, error) {
	session := sessions.Default(c)
	readyAtRaw := session.Get(PasskeyReadySessionKey)
	if readyAtRaw == nil {
		return false, nil
	}

	readyAt, ok := readyAtRaw.(int64)
	if !ok {
		session.Delete(PasskeyReadySessionKey)
		_ = session.Save()
		return false, fmt.Errorf("无效的 Passkey 验证状态")
	}
	session.Delete(PasskeyReadySessionKey)
	if err := session.Save(); err != nil {
		return false, err
	}
	// Expired ready markers cannot be reused.
	if time.Now().Unix()-readyAt >= PasskeyReadyTimeout {
		return false, nil
	}
	return true, nil
}
