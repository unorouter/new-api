package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
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
	Setting    string `gorm:"column:setting"`
}

// HasIdentity reports whether an external provider vouched for this account. A
// typed email is not identity here: with email verification off, anyone can enter
// any address, and the farms do exactly that.
func (p *RegistrationProvenance) HasIdentity() bool {
	unvouchedGitHub := false
	if p.GitHubId != "" && p.Setting != "" {
		var setting types.UserSetting
		if err := common.UnmarshalJsonStr(p.Setting, &setting); err == nil {
			unvouchedGitHub = setting.UnvouchedGitHub
		}
	}
	return identityVouched(p.GitHubId, p.DiscordId, p.OidcId, p.TelegramId, p.LinuxDOId, p.WeChatId, p.GoogleId, p.Email, unvouchedGitHub)
}

// identityVouched is the one rule for whether a bound login proves a person. A
// GitHub account made days before binding does not (967 farm accounts on
// 2026-09-07 were), and a Google login counts only at a mainstream address,
// since Google accepts any address as an account name.
func identityVouched(gitHubId, discordId, oidcId, telegramId, linuxDOId, weChatId, googleId, email string, unvouchedGitHub bool) bool {
	return strongIdentityVouched(gitHubId, discordId, oidcId, telegramId, linuxDOId, weChatId, unvouchedGitHub) ||
		(googleId != "" && common.MainstreamEmailDomain(email))
}

// strongIdentityVouched leaves out Google: farms buy Google accounts in bulk and
// sign in with them, so a Google login alone does not prove a person behind a script.
func strongIdentityVouched(gitHubId, discordId, oidcId, telegramId, linuxDOId, weChatId string, unvouchedGitHub bool) bool {
	return discordId != "" || oidcId != "" || telegramId != "" || linuxDOId != "" || weChatId != "" ||
		(gitHubId != "" && !unvouchedGitHub)
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
			"telegram_id", "linux_do_id", "wechat_id", "google_id", "used_quota", "setting").
		Where("created_at > ?", since).
		Where("role = ?", common.RoleCommonUser).
		Find(&rows).Error
	return rows, err
}
