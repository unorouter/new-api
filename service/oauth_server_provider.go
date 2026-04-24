package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"

	"github.com/zitadel/oidc/v3/pkg/op"
)

// Singleton zitadel/oidc OpenIDProvider wired to our Postgres-backed Storage.
// Built lazily on first /oauth/v1/* request so the binary still starts when
// OAuth server settings are incomplete - those routes 503 in that case
// instead of crashing the process.

var (
	oauthProviderOnce sync.Once
	oauthProvider     *op.Provider
	oauthProviderErr  error
)

// OAuthProvider returns the shared zitadel/oidc provider. Safe to call
// concurrently. Subsequent calls after the first return the cached result
// (including any error, so a misconfigured deploy stays misconfigured
// instead of flapping).
func OAuthProvider() (*op.Provider, error) {
	oauthProviderOnce.Do(func() {
		oauthProvider, oauthProviderErr = buildOAuthProvider()
	})
	return oauthProvider, oauthProviderErr
}

func buildOAuthProvider() (*op.Provider, error) {
	if setting.OAuthIssuerUrl == "" {
		return nil, errors.New("OAUTH_ISSUER_URL is not set")
	}
	if _, err := loadOrGenerateRSAKey(setting.OAuthJwtPrivateKeyPath); err != nil {
		return nil, err
	}

	cfg := &op.Config{
		CryptoKey:                cryptoKeyFromSeed(setting.OAuthJwtKeyId + ":" + setting.OAuthIssuerUrl),
		CryptoKeyId:              "oauth-aes-1",
		DefaultLogoutRedirectURI: "/",
		CodeMethodS256:           true,
		AuthMethodPost:           false,
		AuthMethodPrivateKeyJWT:  false,
		GrantTypeRefreshToken:    true,
		RequestObjectSupported:   false,
		SupportedClaims:          op.DefaultSupportedClaims,
		// openid is the OIDC seed scope. offline_access is required by
		// agents that want a refresh token (RFC 6749 + OIDC Core section 11). The
		// rest are our resource scopes; the consent UI hides the two
		// protocol scopes from the user-visible permission list.
		SupportedScopes: append(
			[]string{"openid", "offline_access"},
			setting.OAuthScopes...,
		),
	}

	storage := NewOAuthStorage()

	opts := []op.Option{
		op.WithCustomEndpoints(
			op.NewEndpoint("/oauth/v1/authorize"),
			op.NewEndpoint("/oauth/v1/token"),
			op.NewEndpoint("/oauth/v1/userinfo"),
			op.NewEndpoint("/oauth/v1/revoke"),
			op.NewEndpoint("/oauth/v1/end_session"),
			op.NewEndpoint("/oauth/v1/jwks"),
		),
		op.WithAllowInsecure(), // we terminate TLS at the edge; issuer may be http:// in dev
	}

	provider, err := op.NewProvider(cfg, storage, op.StaticIssuer(setting.OAuthIssuerUrl), opts...)
	if err != nil {
		return nil, err
	}
	return provider, nil
}

// cryptoKeyFromSeed derives a deterministic 32-byte AES key from a stable
// seed string. zitadel/oidc uses this key only to wrap the auth-request id
// inside the authorization code (so the code is opaque to the outside but
// reversible to us). Tying it to the JWT key id + issuer means we don't need
// a separate CRYPTO_KEY env var.
func cryptoKeyFromSeed(seed string) [32]byte {
	return sha256.Sum256([]byte(seed))
}

// loadOrGenerateRSAKey reads an RSA private key from disk, or generates and
// persists a fresh one if the file is missing. Returns the parsed key.
func loadOrGenerateRSAKey(path string) (*rsa.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("OAUTH_JWT_PRIVATE_KEY_PATH is not set")
	}
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("oauth signing key: PEM decode failed")
		}
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("oauth signing key: not an RSA key")
		}
		return rsaKey, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	common.SysLog("oauth: generating fresh RSA signing key")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
