package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	secureVerificationMethod2FA     = "2fa"
	secureVerificationMethodPasskey = "passkey"
)

type UniversalVerifyRequest struct {
	Method string `json:"method"`
	Code   string `json:"code,omitempty"`
	Scope  string `json:"scope"`
}

func UniversalVerify(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "The current authentication method does not support security verification"})
		return
	}
	var request UniversalVerifyRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiError(c, fmt.Errorf("invalid parameter: %v", err))
		return
	}
	if request.Method != secureVerificationMethod2FA {
		common.ApiError(c, errors.New("Passkey verification must use the Passkey verify flow"))
		return
	}
	if !isAllowedSecurityProofScope(request.Scope) {
		common.ApiError(c, errors.New("unsupported security verification scope"))
		return
	}
	if strings.TrimSpace(request.Code) == "" {
		common.ApiError(c, errors.New("verification code cannot be empty"))
		return
	}
	twoFA, err := model.GetTwoFAByUserId(identity.UserID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if twoFA == nil || !twoFA.IsEnabled {
		common.ApiError(c, errors.New("user has not enabled 2FA"))
		return
	}
	if !validateTwoFactorAuth(twoFA, request.Code) {
		common.ApiError(c, errors.New("verification failed, please check the verification code"))
		return
	}
	proofToken, expiresAt, err := service.IssueSecurityProof(identity, request.Method, []string{request.Scope})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	model.RecordLog(identity.UserID, model.LogTypeSystem, "Universal security verification succeeded (method: 2FA)")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification succeeded",
		"data": gin.H{
			"proof_token": proofToken,
			"expires_at":  expiresAt,
			"method":      request.Method,
			"scope":       request.Scope,
		},
	})
}

func isAllowedSecurityProofScope(scope string) bool {
	switch scope {
	case securityProofScopeChannelKeyRead, securityProofScopePasskeyRegister, securityProofScopePasskeyDelete:
		return true
	default:
		return false
	}
}
