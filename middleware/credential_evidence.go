package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// Audit credentials are independent of encryption/session keys. Keep old audit
// key versions for incident correlation when this key is rotated.
func credentialEvidence(c *gin.Context) map[string]interface{} {
	out := map[string]interface{}{"audit_key_version": os.Getenv("AUDIT_FINGERPRINT_KEY_VERSION"), "pod_uid": os.Getenv("POD_UID"), "build": common.Version}
	key, err := hex.DecodeString(os.Getenv("AUDIT_FINGERPRINT_KEY"))
	if err != nil || len(key) != 32 || out["audit_key_version"] == "" {
		out["fingerprint_status"] = "audit_key_unavailable"
		return out
	}
	credential, ok := AuthorizationToken(c.GetHeader("Authorization"))
	kind := "authorization"
	if !ok {
		out["fingerprint_status"] = "no_valid_authorization_header"
		return out
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(kind + "\x00" + credential))
	out["audit_credential_fingerprint"] = hex.EncodeToString(mac.Sum(nil))
	out["credential_kind"] = kind
	return out
}

// The container log is the local collection source. No remote archive operation
// or additional database write runs on this request path.
func recordPrivilegedAuthentication(c *gin.Context) {
	if c.GetBool("privileged_evidence_recorded") {
		return
	}
	c.Set("privileged_evidence_recorded", true)
	event := credentialEvidence(c)
	event["event"] = "security.privileged_authentication"
	event["user_id"] = c.GetInt("id")
	event["role"] = c.GetInt("role")
	event["auth_method"] = auditAuthMethod(c)
	event["principal_kind"] = auditAuthMethod(c)
	if AuthenticatedViaBotToken(c) {
		event["principal_kind"] = "bot_service"
	} else if AuthenticatedViaSyncToken(c) {
		event["principal_kind"] = "sync_service"
	}
	event["source_ip"] = c.ClientIP()
	event["trusted_network"] = IsTrustedNetwork(c.ClientIP())
	event["method"] = c.Request.Method
	event["route"] = c.FullPath()
	event["pod_uid"] = os.Getenv("POD_UID")
	event["build"] = common.Version
	body, err := common.Marshal(event)
	if err == nil {
		log.Printf("SECURITY_EVIDENCE %s", body)
	}
}
