package controller

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

// OAuthConsentInfo is a small public endpoint that returns the metadata of
// an in-flight auth request keyed by its id. Used by the consent UI to render
// the client name and the requested scopes after the browser is redirected
// from /authorize. Returns 404 if the request expired or never existed.
//
//	GET /oauth/v1/authorize/info?id=<authRequestID>
//	200 { client_id, scope, redirect_uri }
//	404 { error }
func OAuthConsentInfo(c *gin.Context) {
	if !setting.OAuthServerEnabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server disabled"})
		return
	}
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	provider, err := service.OAuthProvider()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server misconfigured: " + err.Error()})
		return
	}
	storage, ok := provider.Storage().(*service.OAuthStorage)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "oauth storage not initialised"})
		return
	}
	rec, err := storage.AuthRequestRecordByID(c.Request.Context(), id)
	if err != nil || rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth request not found or expired"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"client_id":    rec.ClientID,
		"scope":        strings.Join(rec.Scopes, " "),
		"redirect_uri": rec.RedirectURI,
		"state":        rec.State,
	})
}

// OAuthConsentFinalize is the server-side endpoint the consent UI POSTs to
// after the user clicks approve/deny.
//
// Wire shape:
//
//	POST /oauth/v1/authorize/:callbackId
//	form: consented=true|false
//
// On `true`, we mark the persisted auth request as done (so zitadel's callback
// handler will emit a code) and redirect the browser to
// /oauth/v1/authorize/callback?id={callbackId} which is zitadel's
// AuthorizeCallback. On `false` (or anything else), we redirect back to the
// agent's redirect_uri with `error=access_denied`.
//
// Authentication: the consent UI is only reachable by an authenticated user
// (Next.js handles login redirect). We confirm here too, by reading the
// session cookie ourselves before approving anything - defense in depth.
func OAuthConsentFinalize(c *gin.Context) {
	if !setting.OAuthServerEnabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server disabled"})
		return
	}
	callbackID := c.Param("callbackId")
	if callbackID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing callback id"})
		return
	}
	provider, err := service.OAuthProvider()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth server misconfigured: " + err.Error()})
		return
	}

	storage, ok := provider.Storage().(*service.OAuthStorage)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "oauth storage not initialised"})
		return
	}

	subject, err := service.ReadUserIDStringFromCookie(c.Request)
	if err != nil || subject == "" {
		// Not authenticated. Bounce out to the login page with a return URL
		// that lands back on the consent page.
		loginURL := firstAllowedOriginOrEmpty()
		if loginURL == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no session"})
			return
		}
		ret := setting.OAuthConsentPageUrl
		if ret == "" {
			ret = loginURL + "/en/consent"
		}
		ret += "?callback_id=" + url.QueryEscape(callbackID)
		c.Redirect(http.StatusFound, loginURL+"/login?redirect="+url.QueryEscape(ret))
		return
	}

	consented := c.PostForm("consented")
	if consented != "true" {
		// Bounce back to the agent's redirect_uri with access_denied.
		req, err := storage.AuthRequestRecordByID(c.Request.Context(), callbackID)
		if err == nil && req != nil && req.RedirectURI != "" {
			redirect := req.RedirectURI
			sep := "?"
			if strings.Contains(redirect, "?") {
				sep = "&"
			}
			redirect += sep + "error=access_denied"
			if req.State != "" {
				redirect += "&state=" + url.QueryEscape(req.State)
			}
			c.Redirect(http.StatusFound, redirect)
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied"})
		return
	}

	if err := service.FinalizeAuthRequest(c.Request.Context(), storage, callbackID, subject, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not finalize: " + err.Error()})
		return
	}

	// Hand control back to zitadel/oidc's AuthorizeCallback handler. It will
	// read the now-Done auth request, generate the authorization code, and
	// 302 the browser to the client's redirect_uri.
	c.Redirect(http.StatusFound, "/oauth/v1/authorize/callback?id="+url.QueryEscape(callbackID))
}

func firstAllowedOriginOrEmpty() string {
	for _, origin := range common.OAuthAllowedRedirectOrigins {
		return strings.TrimRight(origin, "/")
	}
	return ""
}
