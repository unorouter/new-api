package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// OAuth bearer integration for the existing UserAuth surface.
//
// Instead of building a parallel /oauth/v1/* endpoint tree, we let agent
// JWTs flow through the same /api/* routes humans use. tryOAuthBearerAuth is
// called from authHelper as a third authentication fallback (after session
// cookie and after the legacy access-token header). If it succeeds, the
// agent's user identity + granted scopes are populated onto the gin context
// and authHelper proceeds as if a session were present.
//
// Per-scope gates live on write endpoints via RequireScope(...). Reads pass
// through scope-free because observing state is already gated by user role.

const (
	ContextKeyOAuthScopes   = "oauth_scopes"
	ContextKeyOAuthClientID = "oauth_client_id"
)

// wwwAuthenticateBearer points unauthenticated agents at the public RFC 9728
// resource-metadata document on the BFF mirror. Sent on every 401 from the
// OAuth bearer path and the session/legacy access-token paths in authHelper.
// Browsers ignore it; agents that haven't tried OAuth yet read this to find
// the authorization server.
const wwwAuthenticateBearer = `Bearer realm="api", resource_metadata="https://unorouter.ai/.well-known/oauth-protected-resource"`

// tryOAuthBearerAuth attempts to authenticate the current request via an
// OAuth 2.1 bearer JWT. Returns (matched, ok).
//   - matched=false: no JWT in header, fall through to other auth paths.
//   - matched=true, ok=false: a JWT was present but invalid; response has
//     already been written and caller should c.Abort().
//   - matched=true, ok=true: context has been populated, caller should
//     skip its remaining auth logic and c.Next().
func tryOAuthBearerAuth(c *gin.Context) (matched bool, ok bool) {
	if !setting.OAuthServerEnabled {
		return false, false
	}
	header := c.Request.Header.Get("Authorization")
	if header == "" {
		return false, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(header, "Bearer "), "bearer "))
	// JWT = three segments separated by dots. Anything else is an API key
	// (sk-...) or the legacy access-token - not for us.
	if strings.Count(token, ".") != 2 {
		return false, false
	}

	provider, err := service.OAuthProvider()
	if err != nil {
		// OAuth misconfigured - don't block other auth paths from trying.
		return false, false
	}
	// zitadel's verifier reads the expected issuer from the context. Outside
	// the provider's own HTTP handler that value isn't pre-populated, so we
	// inject our static issuer here before calling AccessTokenVerifier.
	ctx := op.ContextWithIssuer(c.Request.Context(), setting.OAuthIssuerUrl)
	verifier := provider.AccessTokenVerifier(ctx)
	claims, err := op.VerifyAccessToken[*oidc.AccessTokenClaims](ctx, token, verifier)
	if err != nil || claims == nil {
		c.Header("WWW-Authenticate", wwwAuthenticateBearer)
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthAccessTokenInvalid),
		})
		c.Abort()
		return true, false
	}
	// Defense-in-depth: zitadel verifies signature + expiry but does not pin
	// audience to the resource server. Reject any token whose `aud` does not
	// include our issuer URL so a token minted for a different resource cannot
	// be replayed against this API.
	if !audienceContains(claims.Audience, setting.OAuthIssuerUrl) {
		c.Header("WWW-Authenticate", wwwAuthenticateBearer)
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthAccessTokenInvalid),
		})
		c.Abort()
		return true, false
	}

	userId, err := strconv.Atoi(claims.Subject)
	if err != nil {
		c.Header("WWW-Authenticate", wwwAuthenticateBearer)
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid),
		})
		c.Abort()
		return true, false
	}

	// Load the user so the rest of new-api sees a consistent view. We need
	// the full row for Role; GetUserById(false) returns everything needed.
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		c.Header("WWW-Authenticate", wwwAuthenticateBearer)
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid),
		})
		c.Abort()
		return true, false
	}
	if user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserBanned),
		})
		c.Abort()
		return true, false
	}

	scopes := []string(claims.Scopes)

	c.Set("id", user.Id)
	c.Set("username", user.Username)
	c.Set("role", user.Role)
	c.Set("group", user.Group)
	c.Set("user_group", user.Group)
	c.Set("use_access_token", false)
	c.Set(ContextKeyOAuthScopes, scopes)
	c.Set(ContextKeyOAuthClientID, claims.ClientID)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, user.Group)
	return true, true
}

// RequireScope is a middleware that enforces a specific OAuth scope on the
// current request. Only fires for OAuth-authenticated requests - session /
// API-key users pass through untouched (they're already role-gated). Mount
// this AFTER UserAuth on endpoints where agent writes need narrower consent
// than the user's role would imply (e.g. "create API key").
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawScopes, ok := c.Get(ContextKeyOAuthScopes)
		if !ok {
			// No OAuth on this request - session or API key, already gated
			// upstream. Let it through.
			c.Next()
			return
		}
		scopes, _ := rawScopes.([]string)
		if !containsScope(scopes, scope) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "oauth: scope '" + scope + "' is required for this action",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// HasOAuthScope is a non-middleware helper for controllers that need an
// inline scope check mid-handler (e.g. behaviour toggle based on scope).
func HasOAuthScope(c *gin.Context, scope string) bool {
	raw, ok := c.Get(ContextKeyOAuthScopes)
	if !ok {
		return true // not an OAuth request
	}
	scopes, _ := raw.([]string)
	return containsScope(scopes, scope)
}

func containsScope(scopes []string, needle string) bool {
	for _, s := range scopes {
		if s == needle {
			return true
		}
	}
	return false
}

// audienceContains returns true when expected appears in aud. Empty expected
// is treated as "no expectation set" and matches anything (defensive: keeps
// the gate open if OAUTH_ISSUER_URL was unset at startup, since we already
// 503 the /authorize endpoint in that case).
func audienceContains(aud []string, expected string) bool {
	if expected == "" {
		return true
	}
	for _, a := range aud {
		if a == expected {
			return true
		}
	}
	return false
}
