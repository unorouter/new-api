package service

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting"

	"github.com/google/uuid"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// Vendor-neutral persistence types for the OAuth provider.
//
// Anything stored as JSON in our oauth tables is one of the structs below. We
// implement zitadel/oidc's interfaces (op.AuthRequest, op.Client) on these so
// the storage layer doesn't leak across the boundary.

// OAuthClientRecord is the JSON shape persisted in o_auth_clients.data and
// implements op.Client. Mutable fields (RedirectURIs, GrantTypes, etc.) are
// stored verbatim; the op.Client method set wraps them.
type OAuthClientRecord struct {
	// ID is filled in from the row's client_id column on read; storing it in
	// JSON is harmless but redundant.
	ID string `json:"id"`

	// Secret is empty for public PKCE clients (AuthMethod == "none").
	Secret string `json:"secret,omitempty"`

	RedirectURIList           []string            `json:"redirect_uris"`
	PostLogoutRedirectURIList []string            `json:"post_logout_redirect_uris,omitempty"`
	AppType                   op.ApplicationType  `json:"application_type"`
	TokenAuthMethod           oidc.AuthMethod     `json:"token_endpoint_auth_method"`
	ResponseTypeList          []oidc.ResponseType `json:"response_types"`
	GrantTypeList             []oidc.GrantType    `json:"grant_types"`
	AccessTokenTypeVal        op.AccessTokenType  `json:"access_token_type"`
	IDTokenLifetimeSeconds    int                 `json:"id_token_lifetime_seconds,omitempty"`
	DevModeOn                 bool                `json:"dev_mode,omitempty"`
	ClientLoginURL            string              `json:"login_url,omitempty"`
	AdditionalScopes          []string            `json:"additional_scopes,omitempty"`
}

func (c *OAuthClientRecord) GetID() string                  { return c.ID }
func (c *OAuthClientRecord) RedirectURIs() []string         { return c.RedirectURIList }
func (c *OAuthClientRecord) PostLogoutRedirectURIs() []string {
	if c.PostLogoutRedirectURIList == nil {
		return []string{}
	}
	return c.PostLogoutRedirectURIList
}
func (c *OAuthClientRecord) ApplicationType() op.ApplicationType { return c.AppType }
func (c *OAuthClientRecord) AuthMethod() oidc.AuthMethod         { return c.TokenAuthMethod }
func (c *OAuthClientRecord) ResponseTypes() []oidc.ResponseType  { return c.ResponseTypeList }
func (c *OAuthClientRecord) GrantTypes() []oidc.GrantType        { return c.GrantTypeList }

func (c *OAuthClientRecord) LoginURL(authReqID string) string {
	base := c.ClientLoginURL
	if base == "" {
		base = setting.OAuthConsentPageUrl
	}
	if base == "" {
		base = "/login"
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "authRequestID=" + authReqID
}

func (c *OAuthClientRecord) AccessTokenType() op.AccessTokenType { return c.AccessTokenTypeVal }
func (c *OAuthClientRecord) IDTokenLifetime() time.Duration {
	if c.IDTokenLifetimeSeconds == 0 {
		return time.Hour
	}
	return time.Duration(c.IDTokenLifetimeSeconds) * time.Second
}
func (c *OAuthClientRecord) DevMode() bool { return c.DevModeOn }

// RestrictAdditionalIdTokenScopes / AccessTokenScopes pass through whatever
// the client requested; we don't filter.
func (c *OAuthClientRecord) RestrictAdditionalIdTokenScopes() func(scopes []string) []string {
	return func(scopes []string) []string { return scopes }
}
func (c *OAuthClientRecord) RestrictAdditionalAccessTokenScopes() func(scopes []string) []string {
	return func(scopes []string) []string { return scopes }
}

func (c *OAuthClientRecord) IsScopeAllowed(scope string) bool {
	for _, s := range setting.OAuthScopes {
		if s == scope {
			return true
		}
	}
	for _, s := range c.AdditionalScopes {
		if s == scope {
			return true
		}
	}
	return false
}

func (c *OAuthClientRecord) IDTokenUserinfoClaimsAssertion() bool { return false }

// ClockSkew is the leeway zitadel applies when validating exp/iat/nbf. With
// multi-replica deploys behind a load balancer a small NTP drift is normal;
// 30s is the conventional default and avoids flaking valid tokens at the
// boundary of their validity window.
func (c *OAuthClientRecord) ClockSkew() time.Duration { return 30 * time.Second }

// OAuthAuthRequest is the JSON shape persisted in o_auth_authn_sessions.data
// and implements op.AuthRequest. It captures every parameter needed to issue
// the code response after the user consents.
type OAuthAuthRequest struct {
	ID                  string             `json:"id"`
	CreationDate        time.Time          `json:"created_at"`
	ClientID            string             `json:"client_id"`
	RedirectURI         string             `json:"redirect_uri"`
	State               string             `json:"state,omitempty"`
	Nonce               string             `json:"nonce,omitempty"`
	Scopes              []string           `json:"scopes"`
	Audience            []string           `json:"audience,omitempty"`
	ResponseType        oidc.ResponseType  `json:"response_type"`
	ResponseMode        oidc.ResponseMode  `json:"response_mode,omitempty"`
	CodeChallenge       string             `json:"code_challenge,omitempty"`
	CodeChallengeMethod oidc.CodeChallengeMethod `json:"code_challenge_method,omitempty"`
	UserID              string             `json:"user_id,omitempty"`
	AuthTime            time.Time          `json:"auth_time,omitempty"`
	DoneFlag            bool               `json:"done"`
	AuthCode            string             `json:"auth_code,omitempty"`
}

func newAuthRequestRecord(r *oidc.AuthRequest, userID string) *OAuthAuthRequest {
	// Audience is the resource server (RFC 8707). We always sign tokens for
	// our own issuer URL so downstream verifiers (including our own bearer
	// middleware) can pin `aud` against a constant. Without this, GetAudience
	// would fall back to ClientID and any cross-client confused-deputy
	// mitigation (RFC 8707) would be moot.
	aud := []string{setting.OAuthIssuerUrl}
	if setting.OAuthIssuerUrl == "" {
		aud = []string{r.ClientID}
	}
	return &OAuthAuthRequest{
		ID:                  uuid.NewString(),
		CreationDate:        time.Now().UTC(),
		ClientID:            r.ClientID,
		RedirectURI:         r.RedirectURI,
		State:               r.State,
		Nonce:               r.Nonce,
		Scopes:              append([]string(nil), r.Scopes...),
		Audience:            aud,
		ResponseType:        r.ResponseType,
		ResponseMode:        r.ResponseMode,
		CodeChallenge:       r.CodeChallenge,
		CodeChallengeMethod: r.CodeChallengeMethod,
		UserID:              userID,
	}
}

func (a *OAuthAuthRequest) GetID() string                { return a.ID }
func (a *OAuthAuthRequest) GetACR() string               { return "" }
func (a *OAuthAuthRequest) GetAMR() []string             { if a.DoneFlag { return []string{"pwd"} }; return nil }
func (a *OAuthAuthRequest) GetAudience() []string {
	if len(a.Audience) > 0 {
		return a.Audience
	}
	return []string{a.ClientID}
}
func (a *OAuthAuthRequest) GetAuthTime() time.Time           { return a.AuthTime }
func (a *OAuthAuthRequest) GetClientID() string              { return a.ClientID }
func (a *OAuthAuthRequest) GetCodeChallenge() *oidc.CodeChallenge {
	if a.CodeChallenge == "" {
		return nil
	}
	return &oidc.CodeChallenge{Challenge: a.CodeChallenge, Method: a.CodeChallengeMethod}
}
func (a *OAuthAuthRequest) GetNonce() string              { return a.Nonce }
func (a *OAuthAuthRequest) GetRedirectURI() string        { return a.RedirectURI }
func (a *OAuthAuthRequest) GetResponseType() oidc.ResponseType { return a.ResponseType }
func (a *OAuthAuthRequest) GetResponseMode() oidc.ResponseMode { return a.ResponseMode }
func (a *OAuthAuthRequest) GetScopes() []string           { return a.Scopes }
func (a *OAuthAuthRequest) GetState() string              { return a.State }
func (a *OAuthAuthRequest) GetSubject() string            { return a.UserID }
func (a *OAuthAuthRequest) Done() bool                    { return a.DoneFlag }
