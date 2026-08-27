package system_setting

import (
	"os"
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

type DiscordSettings struct {
	Enabled      bool   `json:"enabled"`
	ClientId     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// BotServiceToken authenticates the Discord bot on the few admin routes it
	// needs, in place of a root access token that authorized the entire admin
	// API. Empty disables the bot credential entirely.
	BotServiceToken string `json:"bot_service_token"`
}

// 默认配置
var defaultDiscordSettings = DiscordSettings{}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("discord", &defaultDiscordSettings)
}

func GetDiscordSettings() *DiscordSettings {
	return &defaultDiscordSettings
}

// BotServiceToken resolves the bot credential, preferring the env var so the
// secret can stay in the secret store rather than the options table, which is
// readable from the dashboard.
func BotServiceToken() string {
	if token := strings.TrimSpace(os.Getenv("BOT_SERVICE_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(defaultDiscordSettings.BotServiceToken)
}
