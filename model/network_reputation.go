package model

import (
	"github.com/QuantumNous/new-api/common"
)

// RegistrationProvenance is what one account reveals about the network it was
// created from: whether a real third-party identity backs it, and whether it ever
// paid. An account farm produces neither.
type RegistrationProvenance struct {
	Id         int    `gorm:"column:id"`
	Username   string `gorm:"column:username"`
	CreatedAt  int64  `gorm:"column:created_at"`
	RegisterIp string `gorm:"column:register_ip"`
	Email      string `gorm:"column:email"`
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
// given unix second. Rows without a register IP stay in: the network rule skips
// them, but the burst and username rules can still see them. Soft-deleted rows are
// included so a register/delete/re-register cycle cannot launder a network's
// reputation.
func RegistrationProvenanceSince(since int64) ([]RegistrationProvenance, error) {
	var rows []RegistrationProvenance
	err := DB.Unscoped().Model(&User{}).
		Select("id", "username", "created_at", "register_ip", "email", "github_id", "discord_id", "oidc_id",
			"telegram_id", "linux_do_id", "wechat_id", "google_id", "used_quota").
		Where("created_at > ?", since).
		Where("role = ?", common.RoleCommonUser).
		Find(&rows).Error
	return rows, err
}
