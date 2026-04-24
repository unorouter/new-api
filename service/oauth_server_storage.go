package service

import (
	"context"
	"crypto/rsa"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"gorm.io/gorm"
)

// OAuth 2.1 storage layer for the zitadel/oidc OpenID Provider.
//
// All persistence runs through GORM against the existing o_auth_clients,
// o_auth_authn_sessions, o_auth_grants and o_auth_tokens tables. Wire shape
// across our DB rows is intentionally vendor-neutral JSON so swapping libs in
// the future doesn't require a migration.

var errOAuthNotFound = errors.New("oauth: not found")

// satisfy compiler-level interface checks at startup.
var (
	_ op.Storage = (*OAuthStorage)(nil)
)

// OAuthStorage backs the zitadel/oidc provider. Stateless - every method
// resolves DB rows, with the exception of SigningKey/KeySet which lazy-load
// the RSA key once via getOrLoadSigningKey.
type OAuthStorage struct{}

// NewOAuthStorage returns a Storage implementation. Caller is responsible for
// also passing it to provider.NewProvider.
func NewOAuthStorage() *OAuthStorage {
	return &OAuthStorage{}
}

// ----- Health ---------------------------------------------------------------

func (s *OAuthStorage) Health(ctx context.Context) error {
	sqlDB, err := model.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// ----- Signing keys ---------------------------------------------------------

type oauthSigningKey struct {
	id  string
	key *rsa.PrivateKey
}

func (k *oauthSigningKey) SignatureAlgorithm() jose.SignatureAlgorithm { return jose.RS256 }
func (k *oauthSigningKey) Key() any                                    { return k.key }
func (k *oauthSigningKey) ID() string                                  { return k.id }

type oauthPublicKey struct {
	id  string
	key *rsa.PublicKey
}

func (k *oauthPublicKey) ID() string                          { return k.id }
func (k *oauthPublicKey) Algorithm() jose.SignatureAlgorithm  { return jose.RS256 }
func (k *oauthPublicKey) Use() string                         { return "sig" }
func (k *oauthPublicKey) Key() any                            { return k.key }

func (s *OAuthStorage) SigningKey(ctx context.Context) (op.SigningKey, error) {
	key, err := loadOrGenerateRSAKey(setting.OAuthJwtPrivateKeyPath)
	if err != nil {
		return nil, err
	}
	return &oauthSigningKey{id: setting.OAuthJwtKeyId, key: key}, nil
}

func (s *OAuthStorage) SignatureAlgorithms(ctx context.Context) ([]jose.SignatureAlgorithm, error) {
	return []jose.SignatureAlgorithm{jose.RS256}, nil
}

func (s *OAuthStorage) KeySet(ctx context.Context) ([]op.Key, error) {
	key, err := loadOrGenerateRSAKey(setting.OAuthJwtPrivateKeyPath)
	if err != nil {
		return nil, err
	}
	return []op.Key{&oauthPublicKey{id: setting.OAuthJwtKeyId, key: &key.PublicKey}}, nil
}

// ----- Clients --------------------------------------------------------------

func (s *OAuthStorage) GetClientByClientID(ctx context.Context, clientID string) (op.Client, error) {
	var row model.OAuthClient
	if err := model.DB.WithContext(ctx).Where("client_id = ?", clientID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errOAuthNotFound
		}
		return nil, err
	}
	var rec OAuthClientRecord
	if err := common.Unmarshal([]byte(row.Data), &rec); err != nil {
		return nil, err
	}
	rec.ID = row.ClientId
	return &rec, nil
}

func (s *OAuthStorage) AuthorizeClientIDSecret(ctx context.Context, clientID, clientSecret string) error {
	c, err := s.GetClientByClientID(ctx, clientID)
	if err != nil {
		return err
	}
	rec, ok := c.(*OAuthClientRecord)
	if !ok {
		return errors.New("oauth: unexpected client type")
	}
	// Public clients (PKCE-only) authenticate via PKCE, never via secret.
	if rec.TokenAuthMethod == oidc.AuthMethodNone {
		if clientSecret == "" {
			return nil
		}
		return errors.New("oauth: public clients must not present a client_secret")
	}
	if rec.Secret == "" || rec.Secret != clientSecret {
		return errors.New("oauth: invalid client credentials")
	}
	return nil
}

// ----- Auth requests --------------------------------------------------------

func (s *OAuthStorage) CreateAuthRequest(ctx context.Context, req *oidc.AuthRequest, userID string) (op.AuthRequest, error) {
	rec := newAuthRequestRecord(req, userID)
	if err := s.saveAuthRequest(ctx, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *OAuthStorage) AuthRequestByID(ctx context.Context, id string) (op.AuthRequest, error) {
	return s.loadAuthRequest(ctx, "session_id = ?", id)
}

// AuthRequestRecordByID is a typed accessor for our consent finalize handler.
// Returns the concrete *OAuthAuthRequest so callers can read the redirect URI
// and state when constructing access_denied responses.
func (s *OAuthStorage) AuthRequestRecordByID(ctx context.Context, id string) (*OAuthAuthRequest, error) {
	return s.loadAuthRequestRecord(ctx, "session_id = ?", id)
}

func (s *OAuthStorage) AuthRequestByCode(ctx context.Context, code string) (op.AuthRequest, error) {
	return s.loadAuthRequest(ctx, "auth_code = ?", code)
}

func (s *OAuthStorage) SaveAuthCode(ctx context.Context, id string, code string) error {
	return model.DB.WithContext(ctx).
		Model(&model.OAuthAuthnSession{}).
		Where("session_id = ?", id).
		Update("auth_code", code).Error
}

func (s *OAuthStorage) DeleteAuthRequest(ctx context.Context, id string) error {
	return model.DB.WithContext(ctx).
		Where("session_id = ?", id).
		Delete(&model.OAuthAuthnSession{}).Error
}

// MarkAuthRequestDone is called from the consent callback after the user
// approves the request. Bumping the `done` flag inside the JSON blob is the
// signal zitadel/oidc reads via AuthRequest.Done() before issuing the code.
func (s *OAuthStorage) MarkAuthRequestDone(ctx context.Context, id, subject string, authTime time.Time, scopes []string) error {
	rec, err := s.loadAuthRequestRecord(ctx, "session_id = ?", id)
	if err != nil {
		return err
	}
	rec.UserID = subject
	rec.AuthTime = authTime
	if len(scopes) > 0 {
		rec.Scopes = scopes
	}
	rec.DoneFlag = true
	return s.saveAuthRequest(ctx, rec)
}

func (s *OAuthStorage) saveAuthRequest(ctx context.Context, rec *OAuthAuthRequest) error {
	data, err := common.Marshal(rec)
	if err != nil {
		return err
	}
	row := model.OAuthAuthnSession{
		SessionId:     rec.ID,
		CallbackId:    rec.ID, // we don't have a separate callback id; use session_id
		AuthCode:      rec.AuthCode,
		Data:          string(data),
		ExpiresAtUnix: rec.CreationDate.Add(15 * time.Minute).Unix(),
	}
	return model.DB.WithContext(ctx).
		Where("session_id = ?", rec.ID).
		Assign(row).
		FirstOrCreate(&row).Error
}

func (s *OAuthStorage) loadAuthRequest(ctx context.Context, where string, args ...any) (op.AuthRequest, error) {
	rec, err := s.loadAuthRequestRecord(ctx, where, args...)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *OAuthStorage) loadAuthRequestRecord(ctx context.Context, where string, args ...any) (*OAuthAuthRequest, error) {
	var row model.OAuthAuthnSession
	if err := model.DB.WithContext(ctx).Where(where, args...).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errOAuthNotFound
		}
		return nil, err
	}
	rec := new(OAuthAuthRequest)
	if err := common.Unmarshal([]byte(row.Data), rec); err != nil {
		return nil, err
	}
	rec.ID = row.SessionId
	if rec.AuthCode == "" {
		rec.AuthCode = row.AuthCode
	}
	return rec, nil
}

// ----- Tokens ---------------------------------------------------------------

func (s *OAuthStorage) CreateAccessToken(ctx context.Context, req op.TokenRequest) (string, time.Time, error) {
	clientID, _, _ := tokenRequestInfo(req)
	tok := &model.OAuthToken{
		TokenId:       uuid.NewString(),
		ClientId:      clientID,
		Data:          mustMarshal(map[string]any{"sub": req.GetSubject(), "scopes": req.GetScopes()}),
		ExpiresAtUnix: time.Now().Add(time.Duration(setting.OAuthAccessTokenTtlSeconds) * time.Second).Unix(),
	}
	if err := model.DB.WithContext(ctx).Create(tok).Error; err != nil {
		return "", time.Time{}, err
	}
	return tok.TokenId, time.Unix(tok.ExpiresAtUnix, 0), nil
}

func (s *OAuthStorage) CreateAccessAndRefreshTokens(ctx context.Context, req op.TokenRequest, currentRefreshToken string) (string, string, time.Time, error) {
	accessTokenID, expiration, err := s.CreateAccessToken(ctx, req)
	if err != nil {
		return "", "", time.Time{}, err
	}

	clientID, authTime, amr := tokenRequestInfo(req)
	newRefresh := uuid.NewString()

	if currentRefreshToken == "" {
		// Initial issuance after authorization_code.
		grant := &model.OAuthGrant{
			GrantId:       uuid.NewString(),
			RefreshToken:  newRefresh,
			ClientId:      clientID,
			UserId:        req.GetSubject(),
			Data:          mustMarshal(refreshTokenRecord{Subject: req.GetSubject(), ClientID: clientID, AuthTime: authTime, AMR: amr, Audience: req.GetAudience(), Scopes: req.GetScopes(), AccessTokenID: accessTokenID}),
			ExpiresAtUnix: time.Now().Add(time.Duration(setting.OAuthRefreshTokenTtlSeconds) * time.Second).Unix(),
		}
		if err := model.DB.WithContext(ctx).Create(grant).Error; err != nil {
			return "", "", time.Time{}, err
		}
		return accessTokenID, newRefresh, expiration, nil
	}

	// Refresh token rotation: renew row, return new opaque token.
	var grantRow model.OAuthGrant
	if err := model.DB.WithContext(ctx).Where("refresh_token = ?", currentRefreshToken).First(&grantRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", time.Time{}, op.ErrInvalidRefreshToken
		}
		return "", "", time.Time{}, err
	}
	if grantRow.ExpiresAtUnix < time.Now().Unix() {
		return "", "", time.Time{}, op.ErrInvalidRefreshToken
	}
	grantRow.RefreshToken = newRefresh
	grantRow.ExpiresAtUnix = time.Now().Add(time.Duration(setting.OAuthRefreshTokenTtlSeconds) * time.Second).Unix()
	var rec refreshTokenRecord
	_ = common.Unmarshal([]byte(grantRow.Data), &rec)
	rec.AccessTokenID = accessTokenID
	grantRow.Data = mustMarshal(rec)
	if err := model.DB.WithContext(ctx).Save(&grantRow).Error; err != nil {
		return "", "", time.Time{}, err
	}
	return accessTokenID, newRefresh, expiration, nil
}

func (s *OAuthStorage) TokenRequestByRefreshToken(ctx context.Context, refreshToken string) (op.RefreshTokenRequest, error) {
	var grantRow model.OAuthGrant
	if err := model.DB.WithContext(ctx).Where("refresh_token = ?", refreshToken).First(&grantRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, op.ErrInvalidRefreshToken
		}
		return nil, err
	}
	if grantRow.ExpiresAtUnix < time.Now().Unix() {
		return nil, op.ErrInvalidRefreshToken
	}
	var rec refreshTokenRecord
	if err := common.Unmarshal([]byte(grantRow.Data), &rec); err != nil {
		return nil, err
	}
	rec.id = grantRow.GrantId
	return &rec, nil
}

func (s *OAuthStorage) TerminateSession(ctx context.Context, userID, clientID string) error {
	if err := model.DB.WithContext(ctx).
		Where("user_id = ? AND client_id = ?", userID, clientID).
		Delete(&model.OAuthGrant{}).Error; err != nil {
		return err
	}
	return model.DB.WithContext(ctx).
		Where("client_id = ?", clientID).
		Delete(&model.OAuthToken{}).Error
}

func (s *OAuthStorage) GetRefreshTokenInfo(ctx context.Context, clientID, token string) (string, string, error) {
	var grantRow model.OAuthGrant
	if err := model.DB.WithContext(ctx).Where("refresh_token = ?", token).First(&grantRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", op.ErrInvalidRefreshToken
		}
		return "", "", err
	}
	return grantRow.UserId, grantRow.GrantId, nil
}

func (s *OAuthStorage) RevokeToken(ctx context.Context, tokenOrTokenID, userID, clientID string) *oidc.Error {
	// Defense-in-depth: filter by user + client even though zitadel validates
	// the caller upstream. Prevents a regression in upstream validation from
	// letting one client revoke another client's tokens.
	if err := model.DB.WithContext(ctx).
		Where("token_id = ? AND client_id = ?", tokenOrTokenID, clientID).
		Delete(&model.OAuthToken{}).Error; err == nil {
		return nil
	}
	if err := model.DB.WithContext(ctx).
		Where("refresh_token = ? AND client_id = ? AND user_id = ?", tokenOrTokenID, clientID, userID).
		Delete(&model.OAuthGrant{}).Error; err != nil {
		return oidc.ErrServerError().WithParent(err)
	}
	return nil
}

// ----- Userinfo + introspection --------------------------------------------

func (s *OAuthStorage) SetUserinfoFromScopes(ctx context.Context, userinfo *oidc.UserInfo, userID, clientID string, scopes []string) error {
	// Deprecated method per the interface docs.
	return nil
}

func (s *OAuthStorage) SetUserinfoFromRequest(ctx context.Context, userinfo *oidc.UserInfo, request op.IDTokenRequest, scopes []string) error {
	return s.populateUserinfo(ctx, userinfo, request.GetSubject(), scopes)
}

func (s *OAuthStorage) SetUserinfoFromToken(ctx context.Context, userinfo *oidc.UserInfo, tokenID, subject, origin string) error {
	return s.populateUserinfo(ctx, userinfo, subject, nil)
}

func (s *OAuthStorage) SetIntrospectionFromToken(ctx context.Context, introspection *oidc.IntrospectionResponse, tokenID, subject, clientID string) error {
	introspection.Active = true
	introspection.Subject = subject
	introspection.ClientID = clientID
	return nil
}

func (s *OAuthStorage) GetPrivateClaimsFromScopes(ctx context.Context, userID, clientID string, scopes []string) (map[string]any, error) {
	return map[string]any{
		"scope":     strings.Join(scopes, " "),
		"client_id": clientID,
	}, nil
}

func (s *OAuthStorage) GetKeyByIDAndClientID(ctx context.Context, keyID, clientID string) (*jose.JSONWebKey, error) {
	return nil, errors.New("oauth: private_key_jwt client auth not supported")
}

func (s *OAuthStorage) ValidateJWTProfileScopes(ctx context.Context, userID string, scopes []string) ([]string, error) {
	return scopes, nil
}

func (s *OAuthStorage) populateUserinfo(ctx context.Context, userinfo *oidc.UserInfo, subject string, scopes []string) error {
	if subject == "" {
		return nil
	}
	userinfo.Subject = subject
	return nil
}

// ----- helpers --------------------------------------------------------------

func tokenRequestInfo(req op.TokenRequest) (clientID string, authTime time.Time, amr []string) {
	switch r := req.(type) {
	case *OAuthAuthRequest:
		return r.ClientID, r.AuthTime, []string{"pwd"}
	case *refreshTokenRecord:
		return r.ClientID, r.AuthTime, r.AMR
	}
	return "", time.Time{}, nil
}

func mustMarshal(v any) string {
	b, _ := common.Marshal(v)
	return string(b)
}

// refreshTokenRecord is the JSON shape persisted in OAuthGrant.Data and the
// op.RefreshTokenRequest impl returned by TokenRequestByRefreshToken.
type refreshTokenRecord struct {
	id            string

	Subject       string    `json:"sub"`
	ClientID      string    `json:"client_id"`
	AuthTime      time.Time `json:"auth_time"`
	AMR           []string  `json:"amr,omitempty"`
	Audience      []string  `json:"aud,omitempty"`
	Scopes        []string  `json:"scope,omitempty"`
	AccessTokenID string    `json:"access_token_id,omitempty"`
}

func (r *refreshTokenRecord) GetSubject() string         { return r.Subject }
func (r *refreshTokenRecord) GetAudience() []string      { return r.Audience }
func (r *refreshTokenRecord) GetScopes() []string        { return r.Scopes }
func (r *refreshTokenRecord) GetClientID() string        { return r.ClientID }
func (r *refreshTokenRecord) GetAuthTime() time.Time     { return r.AuthTime }
func (r *refreshTokenRecord) GetAMR() []string           { return r.AMR }
func (r *refreshTokenRecord) SetCurrentScopes(scopes []string) { r.Scopes = scopes }
