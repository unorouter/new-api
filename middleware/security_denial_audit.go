package middleware

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Denied attempts were the blind spot in the 2026-08-26 admin takeover. Every
// SUCCESSFUL action was already audited with an IP and an auth_method, which is
// how we later proved the intruder held a stolen PAT (auth_method=access_token
// on 75 actions, before they changed the root password and switched to a real
// session for the last 4).
//
// What no source recorded was the reconnaissance. At 19:53 they hit
// /api/channel/{11720,8871}/key and were refused by SecureVerificationRequired,
// 42 minutes before they defeated that gate and read four upstream keys. Those
// refusals existed only as edge-layer HTTP 403s in Cloudflare, which retains
// ~24h on this plan. By the time anyone looked they would normally be gone, and
// the first probe is exactly the moment worth alerting on: it is the earliest
// point where an attacker holding a credential is distinguishable from its
// legitimate owner.
//
// These helpers write those refusals into the same `logs` table as everything
// else, so the existing IP/auth_method forensics and the Prometheus security
// alerts see them without any new plumbing.
const (
	auditActionAuthRejected     = "security.auth_rejected"
	auditActionProofRejected    = "security.proof_rejected"
	auditActionPermissionDenied = "security.permission_denied"
)

// recordSecurityDenial persists one refused attempt. userId is best-effort: an
// unauthenticated caller has none, and 0 is meaningful here (it says the
// credential never resolved to a user).
func recordSecurityDenial(c *gin.Context, action string, reason string, extra map[string]interface{}) {
	// The log store is absent before init and in unit tests, where the write
	// panics on a nil handle. Refusing the request is the security-critical
	// half; recording it must never be what takes the request down.
	if c == nil || model.LOG_DB == nil {
		return
	}
	params := map[string]interface{}{
		"reason": reason,
		"path":   c.Request.URL.Path,
		"method": c.Request.Method,
	}
	for k, v := range extra {
		params[k] = v
	}
	// Never store the credential itself, only its shape, so the audit trail can
	// distinguish "presented a PAT" from "presented a session" without becoming
	// a place secrets leak to.
	params["credential"] = presentedCredentialKind(c)
	if fingerprint := rejectedCredentialFingerprint(c, action); fingerprint != "" {
		params["credential_fingerprint"] = fingerprint
	}

	model.RecordOperationAuditLog(
		c.GetInt("id"),
		"Security check refused: "+reason,
		c.ClientIP(),
		action,
		params,
		map[string]interface{}{
			"admin_id":       c.GetInt("id"),
			"admin_username": c.GetString("username"),
			"admin_role":     c.GetInt("role"),
			"auth_method":    auditAuthMethodForDenial(c),
		},
		nil,
	)
}

// presentedCredentialKind reports what the caller offered without revealing it.
// An unauthenticated probe and a probe carrying a stolen PAT look identical in
// an HTTP log; they must not look identical here.
func presentedCredentialKind(c *gin.Context) string {
	if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
		return "authorization_header"
	}
	if _, err := c.Cookie("session"); err == nil {
		return "session_cookie"
	}
	return "none"
}

// rejectedCredentialFingerprint identifies WHICH secret an unauthenticated
// caller presented, without storing it. Shape alone could not answer the
// question that mattered after the takeover -- "is this the PAT we revoked, or
// a second one we never found?" -- because every probe looks the same:
// `authorization_header`, `auth_method=unauthenticated`.
//
// ONLY for auth rejection, never for the authorization denials. The distinction
// is what keeps this safe: a credential reaching auditActionAuthRejected has
// already FAILED to authenticate, so it is by definition not a live secret --
// it is a guess, a revoked token, or one from another system. A caller refused
// by SessionOnly or a permission check authenticated FINE and was stopped on
// scope, so their token is valid and working; fingerprinting those would hash
// live credentials on every routine 403 by a legitimate user, which is exactly
// what this audit trail must not accumulate.
//
// Keyed HMAC-SHA256 under CryptoSecret and truncated, so it is not reversible
// and not comparable across deployments; match it by hashing a token you hold.
//
// Empty for a session cookie: those rotate per login, so a fingerprint
// identifies nothing and only widens what the table holds.
func rejectedCredentialFingerprint(c *gin.Context, action string) string {
	if action != auditActionAuthRejected {
		return ""
	}
	// Normalized through AuthorizationToken so "Bearer sk-x" and a bare "sk-x"
	// fingerprint identically; otherwise the wrapping decides the hash and a
	// known token never matches.
	credential, ok := AuthorizationToken(c.GetHeader("Authorization"))
	if !ok {
		return ""
	}
	return common.GenerateHMAC(credential)[:12]
}

func auditAuthMethodForDenial(c *gin.Context) string {
	if c.GetBool("use_access_token") {
		return "access_token"
	}
	if c.GetString("session_id") != "" {
		return "session"
	}
	return "unauthenticated"
}
