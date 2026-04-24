package setting

import (
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// OAuth 2.1 server configuration. Almost every var has a sensible default - // operators only need to flip OAUTH_SERVER_ENABLED to get a working deploy.
// All env vars remain escape hatches for explicit overrides.
//
// Private-key material intentionally never lives in the admin-editable
// options table - env-only, with disk persistence in /data.

var (
	// OAuthServerEnabled toggles the whole /oauth/v1/* surface.
	OAuthServerEnabled = false

	// OAuthIssuerUrl is the absolute base URL of this new-api deployment,
	// e.g. https://api.unorouter.ai. Used as the JWT `iss` claim and as the
	// base for discovery document URLs. No trailing slash.
	//
	// When empty, the first incoming request's Host + X-Forwarded-Proto is
	// used and cached. Setting it explicitly is recommended in production
	// so the `iss` on JWTs is stable regardless of which edge served the
	// first request.
	OAuthIssuerUrl = ""

	// OAuthJwtPrivateKeyPath points to a PEM-encoded RSA private key.
	// Defaults to /data/oauth_private.pem (inside the persistent data
	// volume). If the file doesn't exist on first boot, we generate a fresh
	// 2048-bit RSA key and write it there - operators don't need to run
	// openssl manually.
	OAuthJwtPrivateKeyPath = "/data/oauth_private.pem"

	// OAuthJwtKeyId is the `kid` advertised in the JWKS and set on issued
	// JWTs. Rotating the signing key: bump this and drop in a new key file.
	OAuthJwtKeyId = "oauth-key-1"

	// OAuthAccessTokenTtlSeconds is how long an issued access token is valid.
	OAuthAccessTokenTtlSeconds = int64(3600) // 1 hour

	// OAuthRefreshTokenTtlSeconds is how long a refresh token is valid.
	OAuthRefreshTokenTtlSeconds = int64(60 * 60 * 24 * 30) // 30 days

	// OAuthConsentPageUrl is where /oauth/v1/authorize redirects users for
	// the approve/deny UI. When empty, we derive it from the first entry in
	// OAUTH_ALLOWED_REDIRECT_ORIGINS + "/en/consent" - so most deployments
	// don't have to set this explicitly.
	OAuthConsentPageUrl = ""
)

// InitOAuthServerEnv populates the package vars from environment variables
// and fills in any remaining defaults. Call after common.InitEnv() so
// OAuthAllowedRedirectOrigins is already parsed.
func InitOAuthServerEnv() {
	if v := os.Getenv("OAUTH_SERVER_ENABLED"); v != "" {
		OAuthServerEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("OAUTH_ISSUER_URL"); v != "" {
		OAuthIssuerUrl = v
	}
	if v := os.Getenv("OAUTH_JWT_PRIVATE_KEY_PATH"); v != "" {
		OAuthJwtPrivateKeyPath = v
	}
	if v := os.Getenv("OAUTH_JWT_KEY_ID"); v != "" {
		OAuthJwtKeyId = v
	}
	if v := os.Getenv("OAUTH_ACCESS_TOKEN_TTL_SECONDS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			OAuthAccessTokenTtlSeconds = n
		}
	}
	if v := os.Getenv("OAUTH_REFRESH_TOKEN_TTL_SECONDS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			OAuthRefreshTokenTtlSeconds = n
		}
	}
	if v := os.Getenv("OAUTH_CONSENT_PAGE_URL"); v != "" {
		OAuthConsentPageUrl = v
	} else if len(common.OAuthAllowedRedirectOrigins) > 0 {
		origin := strings.TrimRight(common.OAuthAllowedRedirectOrigins[0], "/")
		OAuthConsentPageUrl = origin + "/en/consent"
	}
}
