package types

type UserSetting struct {
	QuotaWarningEnabled              bool     `json:"quota_warning_enabled,omitempty"`                // 额度预警总开关，默认关闭：必须显式开启才会发送，仅填写通知邮箱不生效
	NotifyType                       string   `json:"notify_type,omitempty"`                          // QuotaWarningType 额度预警类型
	QuotaWarningThreshold            float64  `json:"quota_warning_threshold,omitempty"`              // QuotaWarningThreshold 额度预警阈值
	WebhookUrl                       string   `json:"webhook_url,omitempty"`                          // WebhookUrl webhook地址
	WebhookSecret                    string   `json:"webhook_secret,omitempty"`                       // WebhookSecret webhook密钥
	NotificationEmail                string   `json:"notification_email,omitempty"`                   // NotificationEmail 通知邮箱地址
	BarkUrl                          string   `json:"bark_url,omitempty"`                             // BarkUrl Bark推送URL
	GotifyUrl                        string   `json:"gotify_url,omitempty"`                           // GotifyUrl Gotify服务器地址
	GotifyToken                      string   `json:"gotify_token,omitempty"`                         // GotifyToken Gotify应用令牌
	GotifyPriority                   int      `json:"gotify_priority"`                                // GotifyPriority Gotify消息优先级
	UpstreamModelUpdateNotifyEnabled bool     `json:"upstream_model_update_notify_enabled,omitempty"` // 是否接收上游模型更新定时检测通知（仅管理员）
	AcceptUnsetRatioModel            bool     `json:"accept_unset_model_ratio_model,omitempty"`       // AcceptUnsetRatioModel 是否接受未设置价格的模型
	RecordIpLog                      bool     `json:"record_ip_log,omitempty"`                        // 是否记录请求和错误日志IP
	SidebarModules                   string   `json:"sidebar_modules,omitempty"`                      // SidebarModules 左侧边栏模块配置
	BillingPreference                string   `json:"billing_preference,omitempty"`                   // BillingPreference 扣费策略（订阅/钱包）
	Language                         string   `json:"language,omitempty"`                             // Language 用户语言偏好 (zh, en)
	BlockFreeWhenNoQuota             bool     `json:"block_free_when_no_quota,omitempty"`             // 余额为零时禁止调用免费模型（手动或自动检测滥用后置位）
	UsableGroups                     []string `json:"usable_groups,omitempty"`                        // 该用户额外可用的分组（私有分组授权，按用户ID生效，叠加在全局可用分组之上）
	UnlimitedFreeModels              bool     `json:"unlimited_free_models,omitempty"`                // 管理员授予：免除免费模型的按模型限流
	ModerationExempt                 bool     `json:"moderation_exempt,omitempty"`                    // 管理员授予：免除图像/视频生成提示词审核
}

var (
	NotifyTypeEmail   = "email"   // Email 邮件
	NotifyTypeWebhook = "webhook" // Webhook
	NotifyTypeBark    = "bark"    // Bark 推送
	NotifyTypeGotify  = "gotify"  // Gotify 推送
)
