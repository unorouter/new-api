package middleware

import (
	"log"
	"strings"
	"sync"
	"time"

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
// authRejectAuditBudget caps auth-rejection rows per process per minute. A junk-key flood
// wrote 80k rows a minute on 2026-09-03; past the cap the refusal still happens, only the row does not.
const authRejectAuditBudget = 600

var authRejectAudit struct {
	sync.Mutex
	window int64
	count  int
}

func authRejectAuditAllowed() bool {
	now := time.Now().Unix() / 60
	authRejectAudit.Lock()
	defer authRejectAudit.Unlock()
	if authRejectAudit.window != now {
		authRejectAudit.window = now
		authRejectAudit.count = 0
	}
	authRejectAudit.count++
	return authRejectAudit.count <= authRejectAuditBudget
}

func recordSecurityDenial(c *gin.Context, action string, reason string, extra map[string]interface{}) {
	if action == auditActionAuthRejected && c != nil {
		// No credential presented is browsing, not probing: nothing to fingerprint or alert on.
		if presentedCredentialKind(c) == "none" || !authRejectAuditAllowed() {
			return
		}
	}
	if extra == nil {
		extra = map[string]interface{}{}
	}
	extra["trusted_network"] = IsTrustedNetwork(c.ClientIP())
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
	evidence := credentialEvidence(c)
	for key, value := range evidence {
		params[key] = value
	}
	evidence["event"] = action
	evidence["user_id"] = c.GetInt("id")
	evidence["auth_method"] = auditAuthMethodForDenial(c)
	evidence["source_ip"] = c.ClientIP()
	evidence["method"] = c.Request.Method
	evidence["route"] = c.FullPath()
	if body, err := common.Marshal(evidence); err == nil {
		log.Printf("SECURITY_EVIDENCE %s", body)
	}
	for k, v := range originSignals(c) {
		params[k] = v
	}
	if fingerprint := rejectedCredentialFingerprint(c, action); fingerprint != "" {
		params["credential_fingerprint"] = fingerprint
	}

	model.RecordOperationAuditLog(
		c.GetInt("id"),
		c.GetInt("role"),
		"Security check refused: "+reason,
		c.ClientIP(),
		action,
		params,
		&model.AuditAdminInfo{
			AdminID:        c.GetInt("id"),
			AdminUsername:  c.GetString("username"),
			AdminRole:      c.GetInt("role"),
			AuthMethod:     auditAuthMethodForDenial(c),
			TrustedNetwork: IsTrustedNetwork(c.ClientIP()),
		},
		nil,
		c,
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

// originSignals captures the cheap edge-provided hints about where a request
// came from. In the 2026-08-26 takeover the only origin evidence was IP
// ownership, which took manual WHOIS work to resolve to a tunnel broker and
// says nothing once an address is recycled.
//
// CF-IPCountry and CF-Ray are set by Cloudflare on every request and cannot be
// spoofed from outside, because the edge overwrites whatever the client sent
// (same property CF-Connecting-IP relies on); the ray id is the join key into
// Cloudflare's own request and firewall logs. Accept-Language and the client
// product are client-controlled and therefore prove nothing on their own, but a
// credential whose refusals suddenly arrive with a different locale or client
// than its owner's successful requests is worth a look. All are recorded as
// signals to correlate, never as authorization input.
//
// Absent values are omitted rather than stored empty: a missing country means
// the request did not traverse the edge, which is itself the interesting case.
func originSignals(c *gin.Context) map[string]interface{} {
	out := make(map[string]interface{}, 4)
	if country := strings.TrimSpace(c.GetHeader("CF-IPCountry")); country != "" && country != "XX" {
		out["country"] = country
	}
	if ray := strings.TrimSpace(c.GetHeader("CF-Ray")); ray != "" {
		out["cf_ray"] = ray
	}
	if lang := strings.TrimSpace(c.GetHeader("Accept-Language")); lang != "" {
		// First tag only: the full header is long, low-entropy and turns the
		// params blob into a fingerprinting surface for no forensic gain.
		if i := strings.IndexAny(lang, ",;"); i > 0 {
			lang = lang[:i]
		}
		out["accept_language"] = strings.TrimSpace(lang)
	}
	if ua := strings.TrimSpace(c.Request.UserAgent()); ua != "" {
		// Product token only ("Kilo-Code/7.5.15", "Mozilla/5.0"): enough to tell
		// an SDK from a browser, without storing the full fingerprint.
		if i := strings.IndexByte(ua, ' '); i > 0 {
			ua = ua[:i]
		}
		out["client"] = ua
	}
	return out
}
