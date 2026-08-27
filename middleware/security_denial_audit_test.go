package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func denialTestContext(authorization string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/channel/1/key", nil)
	if authorization != "" {
		c.Request.Header.Set("Authorization", authorization)
	}
	return c
}

// The fingerprint exists to answer "was this the PAT we revoked?" after a
// credential theft. It is only sound while it is confined to credentials that
// already failed to authenticate: an authorization denial (SessionOnly, a
// permission check) means the token authenticated fine and is live, and hashing
// those would record working user credentials on every routine 403.
func TestRejectedCredentialFingerprintOnlyHashesFailedAuth(t *testing.T) {
	const token = "Bearer sk-revoked-example"

	assert.Empty(t, rejectedCredentialFingerprint(denialTestContext(token), auditActionPermissionDenied),
		"a credential refused on scope authenticated successfully and must not be fingerprinted")
	assert.Empty(t, rejectedCredentialFingerprint(denialTestContext(token), auditActionProofRejected),
		"a credential refused at secure verification authenticated successfully and must not be fingerprinted")
	assert.NotEmpty(t, rejectedCredentialFingerprint(denialTestContext(token), auditActionAuthRejected),
		"a credential that failed to authenticate is the case the fingerprint is for")
}

func TestRejectedCredentialFingerprintValue(t *testing.T) {
	wrapped := rejectedCredentialFingerprint(denialTestContext("Bearer sk-abc"), auditActionAuthRejected)
	bare := rejectedCredentialFingerprint(denialTestContext("sk-abc"), auditActionAuthRejected)

	assert.Len(t, wrapped, 12)
	assert.Equal(t, wrapped, bare,
		"a token must fingerprint identically however the caller wrapped it, or a known token never matches")
	assert.NotContains(t, wrapped, "sk-abc", "the raw credential must not survive into the audit row")
	assert.NotEqual(t, wrapped, rejectedCredentialFingerprint(denialTestContext("Bearer sk-other"), auditActionAuthRejected),
		"distinct credentials must be distinguishable, otherwise the fingerprint identifies nothing")
	assert.Empty(t, rejectedCredentialFingerprint(denialTestContext(""), auditActionAuthRejected),
		"no credential offered, nothing to fingerprint")
}
