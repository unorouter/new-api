package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// SecureVerificationRequired protects channel key disclosure. Other sensitive
// operations validate their narrower proof scopes in their controller.
func SecureVerificationRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID, err := strconv.Atoi(c.Param("id"))
		if err != nil || channelID <= 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"success": false, "code": "SECURITY_CONTEXT_INVALID", "message": service.ErrVerificationContextInvalid.Error()})
			return
		}
		context, err := common.Marshal(service.ChannelKeyReadContext{ChannelID: channelID})
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeChannelKeyRead, Context: context}) == nil {
			return
		}
		c.Set("secure_verified", true)
		c.Next()
	}
}

// SecurityProofFailure is a refused step-up proof: the HTTP status, stable code
// and message RequireSecurityProof writes.
type SecurityProofFailure struct {
	Status  int
	Code    string
	Message string
}

// RequireSecurityProof validates a proof against the authenticated dashboard
// session or scoped access token and writes the shared proof error contract on
// failure.
func RequireSecurityProof(c *gin.Context, operation service.VerificationOperation) *model.AuthFlowAuthorization {
	authorization, failure := CheckSecurityProof(c, operation)
	if failure != nil {
		c.AbortWithStatusJSON(failure.Status, gin.H{"success": false, "message": failure.Message, "code": failure.Code})
		return nil
	}
	return authorization
}

// CheckSecurityProof is RequireSecurityProof for typed handlers, which write
// their own response: it audits a refusal but leaves the response untouched.
func CheckSecurityProof(c *gin.Context, operation service.VerificationOperation) (*model.AuthFlowAuthorization, *SecurityProofFailure) {
	identity, ok := GetStepUpIdentity(c)
	if !ok {
		return nil, securityProofError(c, "SECURITY_PROOF_INVALID", "Security verification is no longer valid. Please verify again.")
	}
	raw := strings.TrimSpace(c.GetHeader("X-Security-Proof"))
	if raw == "" {
		return nil, securityProofError(c, "SECURITY_PROOF_REQUIRED", "Additional verification required")
	}
	authorization, err := service.ConsumeOperationProof(raw, identity, operation)
	if err == nil {
		return authorization, nil
	}
	switch {
	case errors.Is(err, service.ErrAuthTokenExpired):
		return nil, securityProofError(c, "SECURITY_PROOF_EXPIRED", "Security verification has expired. Please verify again.")
	case errors.Is(err, service.ErrProofScope):
		return nil, securityProofError(c, "SECURITY_PROOF_SCOPE_MISMATCH", "Verification does not match this action.")
	case errors.Is(err, service.ErrVerificationContextInvalid):
		c.Set("security_error_code", "SECURITY_CONTEXT_INVALID")
		return nil, &SecurityProofFailure{Status: http.StatusBadRequest, Code: "SECURITY_CONTEXT_INVALID", Message: service.ErrVerificationContextInvalid.Error()}
	case errors.Is(err, service.ErrVerificationUnavailable):
		return nil, securityProofError(c, "SECURITY_METHOD_UNAVAILABLE", service.ErrVerificationUnavailable.Error())
	case errors.Is(err, service.ErrProofMethod):
		return nil, securityProofError(c, "SECURITY_PROOF_METHOD_MISMATCH", "This verification method is not allowed for this action.")
	case errors.Is(err, service.ErrProofConsumed):
		return nil, securityProofError(c, "SECURITY_PROOF_CONSUMED", "This verification has already been used. Please verify again.")
	case errors.Is(err, service.ErrProofContext):
		return nil, securityProofError(c, "SECURITY_PROOF_CONTEXT_MISMATCH", "Verification does not match this action's details. Please verify again.")
	case errors.Is(err, service.ErrVerificationForbidden):
		return nil, securityProofError(c, "SECURITY_ACTION_FORBIDDEN", service.ErrVerificationForbidden.Error())
	case errors.Is(err, service.ErrAuthTokenInvalid), errors.Is(err, service.ErrLoginSessionInvalid), errors.Is(err, service.ErrLoginSessionRevoked), errors.Is(err, model.ErrUserSessionInactive):
		return nil, securityProofError(c, "SECURITY_PROOF_INVALID", "Security verification is no longer valid. Please verify again.")
	default:
		_ = c.Error(err)
		return nil, &SecurityProofFailure{Status: http.StatusInternalServerError, Code: "AUTH_INTERNAL_ERROR", Message: "Please try again later."}
	}
}

func securityProofError(c *gin.Context, code, message string) *SecurityProofFailure {
	// Single choke point for every proof refusal, so auditing here catches the
	// channel-key probing that preceded the 2026-08-26 key theft by 42 minutes.
	recordSecurityDenial(c, auditActionProofRejected, code, nil)
	c.Set("security_error_code", code)
	return &SecurityProofFailure{Status: http.StatusForbidden, Code: code, Message: message}
}
