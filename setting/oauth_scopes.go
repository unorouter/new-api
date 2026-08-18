package setting

// OAuthScopes is the single source of truth for the resource scopes this
// deployment advertises on the consent screen, publishes in the RFC 9728
// protected-resource document, and registers with the zitadel/oidc provider.
// The order is the order shown on the consent screen.
//
// openid and offline_access are OIDC protocol scopes registered separately
// by the provider setup path. They are deliberately left off this list so
// the RFC 9728 resource-server document only advertises actionable scopes
// to agents.
var OAuthScopes = []string{
	"models:read",
	"balance:read",
	"tokens:read",
	"tokens:write",
	"subscription:read",
	"subscription:cancel",
	"checkout:create",
}
