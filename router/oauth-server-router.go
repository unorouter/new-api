package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

// SetOAuthServerRouter mounts the OAuth 2.1 authorization-server surface.
//
// zitadel/oidc publishes the full provider surface (authorize, token, jwks,
// userinfo, revoke, end_session, plus discovery) on its own chi router. We
// route every /oauth/v1/* URL into that handler via a single wildcard
// catch-all.
//
// RFC 9728 protected-resource metadata is root-level and not shipped by the
// library, so we register it separately. RFC 8414 section 3 and OIDC
// Discovery 1.0 section 4 mandate the AS metadata + openid-configuration at
// the issuer ROOT, not under the path prefix; we mirror those two docs back
// to the root so a client probing {issuer}/.well-known/* (per spec) doesn't
// fall through to the SPA NoRoute handler.
func SetOAuthServerRouter(router *gin.Engine, engine *fuego.Engine) {
	// RFC 9728 protected-resource metadata.
	wellKnown := dto.NewRouter(engine, router, "OAuthServer", secPublic())
	wellKnown.GinGet("/.well-known/oauth-protected-resource", controller.GetOAuthProtectedResourceMetadata, dto.GinResp[dto.ApiResponse]())

	// Issuer-root mirrors of zitadel's discovery docs (RFC 8414 + OIDC
	// Discovery 1.0). Internally rewrites the path to /oauth/v1/.well-known/*
	// and delegates to the same provider handler, so the documents stay
	// byte-identical.
	router.GET("/.well-known/oauth-authorization-server", controller.ServeOAuthIssuerRootMetadata)
	router.GET("/.well-known/openid-configuration", controller.ServeOAuthIssuerRootMetadata)

	// zitadel/oidc's full surface under /oauth/v1/*. The provider's internal
	// chi router decides which sub-path maps to which OAuth endpoint based
	// on the WithCustomEndpoints we wired in service/oauth_server_provider.go.
	// ServeOAuthProvider also intercepts POST /oauth/v1/authorize/:callbackId
	// to drive the consent finalize flow (Gin disallows registering a
	// param route alongside the wildcard, so we route by hand).
	router.Any("/oauth/v1/*any", controller.ServeOAuthProvider)
}
