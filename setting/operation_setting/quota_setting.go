package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

type QuotaSetting struct {
	EnableFreeModelPreConsume  bool `json:"enable_free_model_pre_consume"`  // 是否对免费模型启用预消耗
	EnableFreeAbuseAutoBlock   bool `json:"enable_free_abuse_auto_block"`   // 检测到免费模型滥用时自动封禁
	FreeAbuseMaxPerMinute      int  `json:"free_abuse_max_per_minute"`      // 自动封禁前每分钟允许的免费模型请求数
	FreeAbuseMaxDistinctModels int  `json:"free_abuse_max_distinct_models"` // 每分钟内命中的不同免费模型数上限（快速切换模型判定为爬取）
	// 放慢节奏的目录扫描：每分钟都低于上面的阈值，但一整天会扫遍几十个免费模型。仅在启用 Redis 时生效；0=关闭。
	FreeAbuseMaxDistinctModelsPerDay int  `json:"free_abuse_max_distinct_models_per_day"`
	FreeAbuseMaxPerDay               int  `json:"free_abuse_max_per_day"`          // 自动封禁前每天允许的免费模型请求数（慢速持续爬取判定）；0=关闭
	FreeAbuseMaxErrorsPerHour        int  `json:"free_abuse_max_errors_per_hour"`  // 每小时内免费模型错误请求数上限（反复重试被限流模型判定为机器人）；0=关闭
	FreeAbuseMaxMediaErrModels       int  `json:"free_abuse_max_media_err_models"` // 一分钟内失败的不同免费媒体模型数上限（图片/音频/视频探测扫描判定）；0=关闭
	ChargeOnError                    bool `json:"charge_on_error"`                 // 请求失败时是否仍然扣费（不退还预扣额度）

	// 注册网段信誉：按 IPv4 /24、IPv6 /48 聚合，识别同一网段批量注册、几乎无人绑定第三方身份的账号农场。
	// 单 IP 上限（REGISTER_IP_MAX_ACCOUNTS）只看单个地址，农场换 IP 即可绕过。
	FreeAbuseNetworkMinAccounts    int `json:"free_abuse_network_min_accounts"`     // 判定网段为批量注册所需的账号数；0=关闭
	FreeAbuseNetworkMaxIdentityPct int `json:"free_abuse_network_max_identity_pct"` // 网段内绑定第三方身份的账号占比上限，超过则视为真实共享网络并豁免
	FreeAbuseNetworkWindowDays     int `json:"free_abuse_network_window_days"`      // 网段信誉的统计回溯天数
	// 注册突发：同一分钟内未绑定第三方身份的注册数达到阈值，则该分钟内所有未绑定身份、无消费的账号视为农场。
	// 每个账号一个住宅代理出口的农场绕过网段聚合，但仍需批量创建。
	FreeAbuseBurstMinAccounts int `json:"free_abuse_burst_min_accounts"` // 同一分钟内无身份注册数阈值；0=关闭
	FreeAbuseBurstWindowDays  int `json:"free_abuse_burst_window_days"`  // 注册突发的统计回溯天数
	// 未验证账号（无第三方登录、无已验证邮箱、零余额）的滥用阈值按此百分比缩放；100=与其他账号相同。
	FreeAbuseUnverifiedPct int `json:"free_abuse_unverified_pct"`
	// 请求时同现检测：同一客户端 IP 或同一客户端指纹在一个窗口内轮换的零余额、无身份账号数达到阈值，则整簇账号影子封禁。
	// 0=关闭，1=仅记录，2=执行。
	FreeAbuseCooccurMode          int `json:"free_abuse_cooccur_mode"`
	FreeAbuseCooccurIpMinAccounts int `json:"free_abuse_cooccur_ip_min_accounts"` // 同一 IP 窗口内账号数阈值；0=关闭
	FreeAbuseCooccurFpMinAccounts int `json:"free_abuse_cooccur_fp_min_accounts"` // 同一非浏览器客户端指纹窗口内账号数阈值；0=关闭
	FreeAbuseCooccurWindowSeconds int `json:"free_abuse_cooccur_window_seconds"`  // 同现统计窗口（秒）
	FreeAbuseCooccurBanDays       int `json:"free_abuse_cooccur_ban_days"`        // 同现封禁保留天数，簇持续活跃则续期
	// 用户名域名簇：用户名形如邮箱、未填写邮箱，且同一稀有域名下账号数达到阈值、几乎无人绑定身份，则视为农场。
	FreeAbuseUsernameDomainMinAccounts int    `json:"free_abuse_username_domain_min_accounts"` // 0=关闭
	FreeAbuseUsernameDomainAllowlist   string `json:"free_abuse_username_domain_allowlist"`    // 逗号分隔的公共邮箱域名，不参与统计
}

// 默认配置
var quotaSetting = QuotaSetting{
	EnableFreeModelPreConsume:        true,
	EnableFreeAbuseAutoBlock:         false,
	FreeAbuseMaxPerMinute:            5,
	FreeAbuseMaxDistinctModels:       8,
	FreeAbuseMaxDistinctModelsPerDay: 0,
	FreeAbuseMaxPerDay:               0,
	FreeAbuseMaxErrorsPerHour:        0,
	FreeAbuseMaxMediaErrModels:       3,
	ChargeOnError:                    false,

	FreeAbuseNetworkMinAccounts:    0,
	FreeAbuseNetworkMaxIdentityPct: 10,
	FreeAbuseNetworkWindowDays:     14,
	FreeAbuseBurstMinAccounts:      0,
	FreeAbuseBurstWindowDays:       90,
	FreeAbuseUnverifiedPct:         100,

	FreeAbuseCooccurMode:               0,
	FreeAbuseCooccurIpMinAccounts:      0,
	FreeAbuseCooccurFpMinAccounts:      0,
	FreeAbuseCooccurWindowSeconds:      600,
	FreeAbuseCooccurBanDays:            7,
	FreeAbuseUsernameDomainMinAccounts: 0,
	FreeAbuseUsernameDomainAllowlist:   "gmail.com,googlemail.com,qq.com,outlook.com,hotmail.com,live.com,163.com,126.com,foxmail.com,yahoo.com,icloud.com,proton.me,protonmail.com,mail.ru,yandex.ru,duck.com,mozmail.com",
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("quota_setting", &quotaSetting)
}

func GetQuotaSetting() *QuotaSetting {
	return &quotaSetting
}
