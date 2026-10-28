package dto

// TokenUsageData is the response data for GET /api/token/usage.
type TokenUsageData struct {
	Object             string          `json:"object"`
	Name               string          `json:"name"`
	TotalGranted       int             `json:"total_granted"`
	TotalUsed          int             `json:"total_used"`
	TotalAvailable     int             `json:"total_available"`
	UnlimitedQuota     bool            `json:"unlimited_quota"`
	ModelLimits        map[string]bool `json:"model_limits"`
	ModelLimitsEnabled bool            `json:"model_limits_enabled"`
	ExpiresAt          int64           `json:"expires_at"`
	// Present only when the key allows it and its owner is not an administrator.
	UserQuota *TokenUserQuota `json:"user_quota,omitempty"`
}

// TokenUserQuota is the key owner's account quota, in quota units.
type TokenUserQuota struct {
	TotalAvailable int `json:"total_available"`
	TotalUsed      int `json:"total_used"`
}

// CreateTokenRequest is the request body for POST /api/token/.
type CreateTokenRequest struct {
	Name               string  `json:"name"`
	ExpiredTime        int64   `json:"expired_time"`
	RemainQuota        int     `json:"remain_quota"`
	UnlimitedQuota     bool    `json:"unlimited_quota"`
	ModelLimitsEnabled bool    `json:"model_limits_enabled"`
	ModelLimits        string  `json:"model_limits"`
	AllowIps           *string `json:"allow_ips"`
	Group              string  `json:"group"`
	// Pointer fields: a client that predates them (or a hand-written PUT) must
	// not wipe a pin by omitting the key. nil keeps the stored value.
	CrossGroupRetry *bool     `json:"cross_group_retry"`
	GroupMapping    *string   `json:"group_mapping"`
	AutoGroups      *[]string `json:"auto_groups"`
	// nil on create means on; nil on update keeps the stored value.
	AllowUserQuotaQuery *bool `json:"allow_user_quota_query"`
	// low, medium, high or max; "" clears it; nil on update keeps the stored value.
	DefaultReasoningEffort *string `json:"default_reasoning_effort"`
	// on, off or "" (auto); nil on update keeps the stored value.
	PromptCache *string `json:"prompt_cache"`
}

// UpdateTokenRequest is the request body for PUT /api/token/.
type UpdateTokenRequest struct {
	Id                 int     `json:"id"`
	Status             int     `json:"status"`
	Name               string  `json:"name"`
	ExpiredTime        int64   `json:"expired_time"`
	RemainQuota        int     `json:"remain_quota"`
	UnlimitedQuota     bool    `json:"unlimited_quota"`
	ModelLimitsEnabled bool    `json:"model_limits_enabled"`
	ModelLimits        string  `json:"model_limits"`
	AllowIps           *string `json:"allow_ips"`
	Group              string  `json:"group"`
	// Pointer fields: a client that predates them (or a hand-written PUT) must
	// not wipe a pin by omitting the key. nil keeps the stored value.
	CrossGroupRetry *bool     `json:"cross_group_retry"`
	GroupMapping    *string   `json:"group_mapping"`
	AutoGroups      *[]string `json:"auto_groups"`
	// nil on create means on; nil on update keeps the stored value.
	AllowUserQuotaQuery *bool `json:"allow_user_quota_query"`
	// low, medium, high or max; "" clears it; nil on update keeps the stored value.
	DefaultReasoningEffort *string `json:"default_reasoning_effort"`
	// on, off or "" (auto); nil on update keeps the stored value.
	PromptCache *string `json:"prompt_cache"`
}

// GuestTokenLimitsRequest is the body for PUT /api/token/guest-model-limits.
// The token is addressed by its own key rather than an id: the sync knows the
// guest key from its environment and must not be able to enumerate or edit any
// other token.
type GuestTokenLimitsRequest struct {
	Key         string `json:"key"          description:"Full guest token key, with or without the sk- prefix"`
	ModelLimits string `json:"model_limits" description:"Comma-separated model names the guest token may call"`
}

// TokenAutoGroupsData is the response body for GET /api/token/auto_groups.
type TokenAutoGroupsData struct {
	Groups   []string `json:"groups"`
	MaxCount int      `json:"max_count"`
}

// TokenBatch is the request body for batch token operations.
type TokenBatch struct {
	Ids []int `json:"ids"`
}
