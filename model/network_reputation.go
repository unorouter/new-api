package model

import (
	"github.com/QuantumNous/new-api/common"
)

// RegistrationProvenance is what one account reveals about the network it was
// created from: whether a real third-party identity backs it, and whether it ever
// paid. An account farm produces neither.
type RegistrationProvenance struct {
	Id         int    `gorm:"column:id"`
	RegisterIp string `gorm:"column:register_ip"`
	GitHubId   string `gorm:"column:github_id"`
	DiscordId  string `gorm:"column:discord_id"`
	OidcId     string `gorm:"column:oidc_id"`
	TelegramId string `gorm:"column:telegram_id"`
	LinuxDOId  string `gorm:"column:linux_do_id"`
	WeChatId   string `gorm:"column:wechat_id"`
	GoogleId   string `gorm:"column:google_id"`
	UsedQuota  int    `gorm:"column:used_quota"`
}

// HasIdentity reports whether an external provider vouched for this account. A
// typed email is not identity here: with email verification off, anyone can enter
// any address, and the farms do exactly that.
func (p *RegistrationProvenance) HasIdentity() bool {
	return p.GitHubId != "" || p.DiscordId != "" || p.OidcId != "" ||
		p.TelegramId != "" || p.LinuxDOId != "" || p.WeChatId != "" || p.GoogleId != ""
}

// RegistrationProvenanceSince returns every ordinary account registered after the
// given unix second that recorded a register IP. Soft-deleted rows are included so
// a register/delete/re-register cycle cannot launder a network's reputation.
func RegistrationProvenanceSince(since int64) ([]RegistrationProvenance, error) {
	var rows []RegistrationProvenance
	err := DB.Unscoped().Model(&User{}).
		Select("id", "register_ip", "github_id", "discord_id", "oidc_id",
			"telegram_id", "linux_do_id", "wechat_id", "google_id", "used_quota").
		Where("created_at > ?", since).
		Where("register_ip <> ?", "").
		Where("role = ?", common.RoleCommonUser).
		Find(&rows).Error
	return rows, err
}
