package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// SecureVerificationRequired protects channel key disclosure. Other sensitive
// operations validate their narrower proof scopes in their controller.
func SecureVerificationRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !RequireSecurityProof(c, "channel.key.read", []string{"2fa", "passkey"}) {
			return
		}
		c.Set("secure_verified", true)
		c.Next()
	}
}

// RequireSecurityProof validates a proof against the authenticated dashboard
// session and writes the shared proof error contract on failure.
func RequireSecurityProof(c *gin.Context, requiredScope string, allowedMethods []string) bool {
	identity, ok := GetSessionAuthIdentity(c)
	if !ok {
		securityProofError(c, "SECURITY_PROOF_INVALID", "Invalid security verification state")
		return false
	}
	raw := strings.TrimSpace(c.GetHeader("X-Security-Proof"))
	if raw == "" {
		securityProofError(c, "SECURITY_PROOF_REQUIRED", "Security verification required")
		return false
	}
	if _, err := service.VerifySecurityProof(raw, identity, requiredScope, allowedMethods); err != nil {
		switch {
		case errors.Is(err, service.ErrAuthTokenExpired):
			securityProofError(c, "SECURITY_PROOF_EXPIRED", "Security verification has expired")
		case errors.Is(err, service.ErrProofScope):
			securityProofError(c, "SECURITY_PROOF_SCOPE_MISMATCH", "Security verification scope mismatch")
		case errors.Is(err, service.ErrProofMethod):
			securityProofError(c, "SECURITY_PROOF_METHOD_MISMATCH", "Security verification method mismatch")
		default:
			securityProofError(c, "SECURITY_PROOF_INVALID", "Invalid security verification state")
		}
		return false
	}
	return true
}

func securityProofError(c *gin.Context, code, message string) {
	// Single choke point for every proof refusal, so auditing here catches the
	// channel-key probing that preceded the 2026-08-26 key theft by 42 minutes.
	recordSecurityDenial(c, auditActionProofRejected, code, nil)
	c.JSON(http.StatusForbidden, gin.H{
		"success": false,
		"message": message,
		"code":    code,
	})
	c.Abort()
}
