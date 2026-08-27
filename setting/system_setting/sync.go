package system_setting

import (
	"os"
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

type SyncSettings struct {
	// SyncServiceToken authenticates new-api-sync on the channel, model, vendor
	// and pricing-option routes it needs, in place of a root access token that
	// authorized the entire admin API. Empty disables the sync credential
	// entirely, which leaves the sync falling back to whatever account its
	// Authorization header resolves to.
	SyncServiceToken string `json:"sync_service_token"`
}

var defaultSyncSettings = SyncSettings{}

func init() {
	config.GlobalConfig.Register("sync", &defaultSyncSettings)
}

func GetSyncSettings() *SyncSettings {
	return &defaultSyncSettings
}

// SyncServiceToken resolves the sync credential, preferring the env var so the
// secret can stay in the secret store rather than the options table, which is
// readable from the dashboard.
func SyncServiceToken() string {
	if token := strings.TrimSpace(os.Getenv("SYNC_SERVICE_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(defaultSyncSettings.SyncServiceToken)
}
