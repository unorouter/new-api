package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

// Thin Gin wrappers around the zitadel/oidc OpenIDProvider. The provider's
// chi router publishes /oauth/v1/authorize, /oauth/v1/token, /oauth/v1/jwks
// and so on. We just hand requests off to it.

// ServeOAuthProvider bridges a Gin request to zitadel/oidc's http.Handler.
// The provider's internal chi router decides which sub-path maps to which
// OAuth endpoint based on the paths we configured via WithCustomEndpoints.
//
// Special case: POST /oauth/v1/authorize/<id> is the consent UI's finalize
// callback. Gin's wildcard route (`*any`) prevents registering a sibling
// `/oauth/v1/authorize/:callbackId` route, so we dispatch by hand.
func ServeOAuthProvider(c *gin.Context) {
	if !setting.OAuthServerEnabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server disabled"})
		return
	}
	// /oauth/v1/authorize/info is our auth-request-metadata lookup for the
	// consent UI. Matched ahead of zitadel since chi has no handler for it.
	if c.Request.Method == http.MethodGet && c.Request.URL.Path == "/oauth/v1/authorize/info" {
		OAuthConsentInfo(c)
		return
	}
	if c.Request.Method == http.MethodPost {
		// Match /oauth/v1/authorize/<id> exactly - one segment after the
		// authorize path, no further nesting. Anything deeper is left to
		// zitadel's chi router (e.g. /oauth/v1/authorize/callback).
		path := strings.TrimPrefix(c.Request.URL.Path, "/oauth/v1/authorize/")
		if path != c.Request.URL.Path && path != "" && !strings.Contains(path, "/") && path != "callback" {
			c.Params = append(c.Params, gin.Param{Key: "callbackId", Value: path})
			OAuthConsentFinalize(c)
			return
		}
	}
	handler, err := service.OAuthProvider()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server misconfigured: " + err.Error()})
		return
	}
	handler.ServeHTTP(c.Writer, c.Request)
}

// ServeOAuthIssuerRootMetadata bridges issuer-root well-known requests into
// zitadel/oidc's discovery handler.
//
// OIDC Discovery 1.0 section 4 mandates openid-configuration at
// {issuer}/.well-known/openid-configuration. RFC 8414 section 3 mandates the closely
// related oauth-authorization-server metadata at
// {issuer}/.well-known/oauth-authorization-server. zitadel registers a
// discovery handler at /.well-known/openid-configuration on its chi router;
// the same document also satisfies RFC 8414 (it's a superset), so we 301
// the oauth-authorization-server URL into the OIDC one rather than dual-host.
func ServeOAuthIssuerRootMetadata(c *gin.Context) {
	if !setting.OAuthServerEnabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server disabled"})
		return
	}
	handler, err := service.OAuthProvider()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server misconfigured: " + err.Error()})
		return
	}
	r := c.Request
	if c.Request.URL.Path != "/.well-known/openid-configuration" {
		r = c.Request.Clone(c.Request.Context())
		r.URL.Path = "/.well-known/openid-configuration"
		r.URL.RawPath = ""
	}
	handler.ServeHTTP(c.Writer, r)
}

// GetOAuthProtectedResourceMetadata serves the RFC 9728 discovery document.
// zitadel publishes openid-configuration (RFC 8414/OIDC Discovery) itself,
// but RFC 9728 is resource-server metadata - a separate contract pointing
// agents at which authorization server(s) protect this API. The MCP spec
// requires this endpoint.
func GetOAuthProtectedResourceMetadata(c *gin.Context) {
	issuer := setting.OAuthIssuerUrl
	if issuer == "" {
		issuer = guessIssuerFromRequest(c)
	}
	c.JSON(http.StatusOK, gin.H{
		"resource":                              issuer,
		"authorization_servers":                 []string{issuer},
		"jwks_uri":                              issuer + "/oauth/v1/jwks",
		"scopes_supported":                      setting.OAuthScopes,
		"bearer_methods_supported":              []string{"header"},
		"resource_signing_alg_values_supported": []string{"RS256"},
	})
}

func guessIssuerFromRequest(c *gin.Context) string {
	scheme := "https"
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if c.Request.TLS == nil {
		scheme = "http"
	}
	host := c.Request.Host
	if fwd := c.GetHeader("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}
