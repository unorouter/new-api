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

// Provider keys at rest. channels.key_enc holds "v1:" + base64(nonce + AES-256-GCM(key)),
// where key is the same text the retired `key` column held: a single secret, a newline separated
// list for multi-key channels, or a JSON credential blob. The plaintext is only ever in
// memory: opened in AfterFind, sealed in BeforeSave.
//
// The key comes from CHANNEL_KEY_ENC_KEY and never touches the database. It is separate
// from the token key on purpose: losing it means every provider key must be re-entered,
// nothing more.
const channelKeyEncVersion = "v1:"

var channelKeyAEAD cipher.AEAD

// InitChannelKeyCrypto must succeed before the first channel is written: a pod without
// the key would store rows that no pod can open.
func InitChannelKeyCrypto() error {
	raw := strings.TrimSpace(os.Getenv("CHANNEL_KEY_ENC_KEY"))
	if raw == "" {
		return errors.New("CHANNEL_KEY_ENC_KEY is not set")
	}
	secret, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("CHANNEL_KEY_ENC_KEY is not valid base64: %w", err)
	}
	if len(secret) != 32 {
		return fmt.Errorf("CHANNEL_KEY_ENC_KEY must decode to 32 bytes, got %d", len(secret))
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	channelKeyAEAD = aead
	return nil
}

// channelKeyCryptoReady is always true in a running server (main refuses to start
// otherwise). Under `go test` it installs a fixed key so every suite runs on the sealed path.
func channelKeyCryptoReady() bool {
	if channelKeyAEAD == nil && testing.Testing() {
		block, _ := aes.NewCipher([]byte("new-api-test-only-chan-key-32byt"))
		channelKeyAEAD, _ = cipher.NewGCM(block)
	}
	return channelKeyAEAD != nil
}

func encryptChannelKey(key string) (string, error) {
	nonce := make([]byte, channelKeyAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := channelKeyAEAD.Seal(nonce, nonce, []byte(key), nil)
	return channelKeyEncVersion + base64.StdEncoding.EncodeToString(sealed), nil
}

func decryptChannelKey(keyEnc string) (string, error) {
	payload, ok := strings.CutPrefix(keyEnc, channelKeyEncVersion)
	if !ok {
		return "", errors.New("channel key ciphertext has an unknown version")
	}
	sealed, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	nonceSize := channelKeyAEAD.NonceSize()
	if len(sealed) <= nonceSize {
		return "", errors.New("channel key ciphertext is too short")
	}
	plain, err := channelKeyAEAD.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// BeforeSave seals the in-memory key on every create, save and struct update. Map updates
// bypass hooks, which is why raw key writes go through UpdateChannelKey.
func (channel *Channel) BeforeSave(tx *gorm.DB) error {
	if channel.Key == "" || !channelKeyCryptoReady() {
		return nil
	}
	keyEnc, err := encryptChannelKey(channel.Key)
	if err != nil {
		return err
	}
	channel.KeyEnc = keyEnc
	return nil
}

// AfterFind opens the key wherever a row is loaded with its ciphertext. Listings omit the
// column, so they never carry a key.
func (channel *Channel) AfterFind(tx *gorm.DB) error {
	if channel.KeyEnc == "" || !channelKeyCryptoReady() {
		return nil
	}
	plain, err := decryptChannelKey(channel.KeyEnc)
	if err != nil {
		common.SysError(fmt.Sprintf("channel key open: channel %d ciphertext does not open: %s", channel.Id, err.Error()))
		return nil
	}
	channel.Key = plain
	return nil
}

// UpdateChannelKey is the one write path for a key set outside a struct save (OAuth
// completion, credential refresh).
func UpdateChannelKey(id int, key string) error {
	channelKeyCryptoReady()
	keyEnc, err := encryptChannelKey(key)
	if err != nil {
		return err
	}
	return DB.Model(&Channel{}).Where("id = ?", id).Update("key_enc", keyEnc).Error
}
