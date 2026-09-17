package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// API keys at rest. A request finds its token by key, and AES-GCM output is random per
// write, so one column cannot do both jobs:
//
//	key_hash  HMAC-SHA256(pepper, key), unique: the lookup column
//	key_enc   "v1:" + base64(nonce + AES-256-GCM(key)), AAD = key_hash: the reveal and the BFF
//	key_hint  first 4 + last 4 characters: the masked list view, no decrypt needed
//
// Both secrets come from the environment and never touch the database. They are separate
// from CRYPTO_SECRET on purpose: that one also signs sessions and rotates on its own.
// Lose the pepper and no key authenticates; lose the enc key and no key can be revealed.
const (
	tokenKeyEncVersion  = "v1:"
	tokenKeyBackfillMax = 500
)

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

func tokenKeyCryptoReady() bool {
	return tokenKeyAEAD != nil && len(tokenKeyPepper) == 32
}

func hashTokenKey(key string) string {
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

// sealKey fills the three derived columns from the plaintext key. Without the secrets
// (unit tests on sqlite) it leaves them empty; main refuses to start in that state.
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

type tokenKeyRow struct {
	Id      int
	Key     string
	KeyHash *string
	KeyEnc  string
}

// backfillTokenKeysOnce seals up to tokenKeyBackfillMax rows that still lack a hash (rows
// from before the columns existed, or written by an older pod during a rolling deploy).
func backfillTokenKeysOnce() (int, error) {
	var rows []tokenKeyRow
	if err := DB.Model(&Token{}).Unscoped().
		Select("id", commonKeyCol).
		Where("key_hash IS NULL AND " + commonKeyCol + " <> ''").
		Order("id").Limit(tokenKeyBackfillMax).
		Find(&rows).Error; err != nil {
		return 0, err
	}
	sealed := 0
	for _, row := range rows {
		token := Token{Key: row.Key}
		if err := token.sealKey(); err != nil {
			return sealed, err
		}
		result := DB.Model(&Token{}).Unscoped().
			Where("id = ? AND key_hash IS NULL", row.Id).
			Updates(map[string]any{"key_hash": *token.KeyHash, "key_enc": token.KeyEnc, "key_hint": token.KeyHint})
		if result.Error != nil {
			return sealed, result.Error
		}
		sealed += int(result.RowsAffected)
	}
	return sealed, nil
}

// verifyTokenKeys re-reads every sealed row and checks that the ciphertext opens to the
// stored plaintext and that the hash matches. It only exists while `key` is still written.
func verifyTokenKeys() (checked int, mismatched int, err error) {
	lastId := 0
	for {
		var rows []tokenKeyRow
		if err = DB.Model(&Token{}).Unscoped().
			Select("id", commonKeyCol, "key_hash", "key_enc").
			Where("id > ? AND key_hash IS NOT NULL", lastId).
			Order("id").Limit(tokenKeyBackfillMax).
			Find(&rows).Error; err != nil {
			return checked, mismatched, err
		}
		if len(rows) == 0 {
			return checked, mismatched, nil
		}
		for _, row := range rows {
			lastId = row.Id
			checked++
			plain, decryptErr := decryptTokenKey(row.KeyEnc, *row.KeyHash)
			hashOk := subtle.ConstantTimeCompare([]byte(hashTokenKey(row.Key)), []byte(*row.KeyHash)) == 1
			if decryptErr != nil || plain != row.Key || !hashOk {
				mismatched++
				common.SysError(fmt.Sprintf("token key verify: row %d does not round trip", row.Id))
			}
		}
	}
}

// RunTokenKeyBackfill runs on the master only. It drains the backlog in small batches, then
// keeps sweeping for stragglers and logs one verification pass after the first drain.
func RunTokenKeyBackfill() {
	verified := false
	for {
		sealed, err := backfillTokenKeysOnce()
		if err != nil {
			common.SysError("token key backfill: " + err.Error())
			time.Sleep(time.Minute)
			continue
		}
		if sealed > 0 {
			common.SysLog(fmt.Sprintf("token key backfill: sealed %d rows", sealed))
			verified = false
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if !verified {
			checked, mismatched, verifyErr := verifyTokenKeys()
			if verifyErr != nil {
				common.SysError("token key verify: " + verifyErr.Error())
			} else {
				common.SysLog(fmt.Sprintf("token key verify: checked=%d mismatched=%d", checked, mismatched))
				verified = true
			}
		}
		time.Sleep(5 * time.Minute)
	}
}
