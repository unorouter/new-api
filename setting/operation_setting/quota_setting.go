package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

type QuotaSetting struct {
	EnableFreeModelPreConsume  bool `json:"enable_free_model_pre_consume"`   // 是否对免费模型启用预消耗
	EnableFreeAbuseAutoBlock   bool `json:"enable_free_abuse_auto_block"`    // 检测到免费模型滥用时自动封禁
	FreeAbuseMaxPerMinute      int  `json:"free_abuse_max_per_minute"`       // 自动封禁前每分钟允许的免费模型请求数
	FreeAbuseMaxDistinctModels int  `json:"free_abuse_max_distinct_models"`  // 每分钟内命中的不同免费模型数上限（快速切换模型判定为爬取）
	FreeAbuseMaxPerDay         int  `json:"free_abuse_max_per_day"`          // 自动封禁前每天允许的免费模型请求数（慢速持续爬取判定）；0=关闭
	FreeAbuseMaxErrorsPerHour  int  `json:"free_abuse_max_errors_per_hour"`  // 每小时内免费模型错误请求数上限（反复重试被限流模型判定为机器人）；0=关闭
	FreeAbuseMaxMediaErrModels int  `json:"free_abuse_max_media_err_models"` // 一分钟内失败的不同免费媒体模型数上限（图片/音频/视频探测扫描判定）；0=关闭
	ChargeOnError              bool `json:"charge_on_error"`                 // 请求失败时是否仍然扣费（不退还预扣额度）

	// 注册网段信誉：按 IPv4 /24、IPv6 /48 聚合，识别同一网段批量注册、几乎无人绑定第三方身份的账号农场。
	// 单 IP 上限（REGISTER_IP_MAX_ACCOUNTS）只看单个地址，农场换 IP 即可绕过。
	FreeAbuseNetworkMinAccounts    int `json:"free_abuse_network_min_accounts"`     // 判定网段为批量注册所需的账号数；0=关闭
	FreeAbuseNetworkMaxIdentityPct int `json:"free_abuse_network_max_identity_pct"` // 网段内绑定第三方身份的账号占比上限，超过则视为真实共享网络并豁免
	FreeAbuseNetworkWindowDays     int `json:"free_abuse_network_window_days"`      // 网段信誉的统计回溯天数
}

// 默认配置
var quotaSetting = QuotaSetting{
	EnableFreeModelPreConsume:  true,
	EnableFreeAbuseAutoBlock:   false,
	FreeAbuseMaxPerMinute:      5,
	FreeAbuseMaxDistinctModels: 8,
	FreeAbuseMaxPerDay:         0,
	FreeAbuseMaxErrorsPerHour:  0,
	FreeAbuseMaxMediaErrModels: 3,
	ChargeOnError:              false,

	FreeAbuseNetworkMinAccounts:    0,
	FreeAbuseNetworkMaxIdentityPct: 10,
	FreeAbuseNetworkWindowDays:     14,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("quota_setting", &quotaSetting)
}

func GetQuotaSetting() *QuotaSetting {
	return &quotaSetting
}
