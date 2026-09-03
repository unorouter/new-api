package middleware

import (
	"crypto/subtle"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// The unorouter chat BFF resolves a user's API key server-side on every
// request it relays, using the user's own session. That is not a person
// revealing a key, but it was audited as one: 13,420 token.key_view rows in a
// week from 4,682 users, and a single chat user hitting the same token 80
// times a day, burying the reveals that matter.
//
// A plain "skip the audit" header would be the first thing an attacker inside
// a hijacked account adds, so the marker is a service secret the browser can
// never hold: BFF_SERVICE_TOKEN, issued from OpenBao to both the BFF and the
// gateway, compared in constant time. The key itself stays behind the same
// session gate as before; only the audit row is elided, and only for the BFF.
const bffServiceHeader = "X-Bff-Service-Token"

// ResolvedByBFF reports whether the request carries the BFF's service secret.
func ResolvedByBFF(c *gin.Context) bool {
	secret := strings.TrimSpace(os.Getenv("BFF_SERVICE_TOKEN"))
	if secret == "" {
		return false
	}
	raw := strings.TrimSpace(c.GetHeader(bffServiceHeader))
	return raw != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(secret)) == 1
}
