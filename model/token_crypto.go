package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// API keys at rest. A request finds its token by key, and AES-GCM output is random per
// write, so one column cannot do both jobs:
//
//	key_hash  HMAC-SHA256(pepper, key), unique: the lookup column
//	key_enc   "v1:" + base64(nonce + AES-256-GCM(key)), AAD = key_hash: the reveal and the BFF
//	key_hint  first 4 + last 4 characters: what a fragment search can match
//
// The plaintext `key` column of upstream is no longer mapped: it is never written or read.
//
// Both secrets come from the environment and never touch the database. They are separate
// from CRYPTO_SECRET on purpose: that one also signs sessions and rotates on its own.
// Lose the pepper and no key authenticates; lose the enc key and no key can be revealed.
const tokenKeyEncVersion = "v1:"

var (
	tokenKeyPepper []byte
	tokenKeyAEAD   cipher.AEAD
)

func loadTokenKeySecret(name string) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", name)
	}
	secret, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", name, err)
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("%s must decode to 32 bytes, got %d", name, len(secret))
	}
	return secret, nil
}

// InitTokenKeyCrypto must succeed before the first token is written: a pod without the
// secrets would create rows that no other pod can find by hash.
func InitTokenKeyCrypto() error {
	pepper, err := loadTokenKeySecret("TOKEN_KEY_PEPPER")
	if err != nil {
		return err
	}
	encKey, err := loadTokenKeySecret("TOKEN_KEY_ENC_KEY")
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	tokenKeyPepper = pepper
	tokenKeyAEAD = aead
	return nil
}

// tokenKeyCryptoReady is always true in a running server (main refuses to start otherwise).
// Under `go test` it installs fixed secrets so every suite runs on the sealed path.
func tokenKeyCryptoReady() bool {
	if tokenKeyAEAD == nil && testing.Testing() {
		block, _ := aes.NewCipher([]byte("new-api-test-only-enc-key-32byte"))
		tokenKeyAEAD, _ = cipher.NewGCM(block)
		tokenKeyPepper = []byte("new-api-test-only-pepper-32bytes")
	}
	return tokenKeyAEAD != nil && len(tokenKeyPepper) == 32
}

func hashTokenKey(key string) string {
	tokenKeyCryptoReady()
	return common.GenerateHMACWithKey(tokenKeyPepper, key)
}

func encryptTokenKey(key string, keyHash string) (string, error) {
	nonce := make([]byte, tokenKeyAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := tokenKeyAEAD.Seal(nonce, nonce, []byte(key), []byte(keyHash))
	return tokenKeyEncVersion + base64.StdEncoding.EncodeToString(sealed), nil
}

func decryptTokenKey(keyEnc string, keyHash string) (string, error) {
	payload, ok := strings.CutPrefix(keyEnc, tokenKeyEncVersion)
	if !ok {
		return "", errors.New("token key ciphertext has an unknown version")
	}
	sealed, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	nonceSize := tokenKeyAEAD.NonceSize()
	if len(sealed) <= nonceSize {
		return "", errors.New("token key ciphertext is too short")
	}
	plain, err := tokenKeyAEAD.Open(nil, sealed[:nonceSize], sealed[nonceSize:], []byte(keyHash))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func tokenKeyHint(key string) string {
	if len(key) <= 8 {
		return ""
	}
	return key[:4] + key[len(key)-4:]
}

// sealKey fills the three stored columns from the in-memory key.
func (token *Token) sealKey() error {
	if token.Key == "" || !tokenKeyCryptoReady() {
		return nil
	}
	keyHash := hashTokenKey(token.Key)
	keyEnc, err := encryptTokenKey(token.Key, keyHash)
	if err != nil {
		return err
	}
	token.KeyHash = &keyHash
	token.KeyEnc = keyEnc
	token.KeyHint = tokenKeyHint(token.Key)
	return nil
}

func (token *Token) BeforeCreate(tx *gorm.DB) error {
	return token.sealKey()
}

// AfterFind opens the key wherever a row is loaded with its sealed columns. A partial select
// without them leaves Key empty, which is what every caller that does not need the key wants.
func (token *Token) AfterFind(tx *gorm.DB) error {
	if token.KeyEnc == "" || token.KeyHash == nil || !tokenKeyCryptoReady() {
		return nil
	}
	plain, err := decryptTokenKey(token.KeyEnc, *token.KeyHash)
	if err != nil {
		common.SysError(fmt.Sprintf("token key open: token %d ciphertext does not open: %s", token.Id, err.Error()))
		return nil
	}
	token.Key = plain
	return nil
}
