package dto

// ─── Shared / Reusable ──────────────────────────────────────────────

type StatusOnlyParams struct {
	StatusOnly string `query:"status_only" description:"Only update status"`
}

type EmailParams struct {
	Email string `query:"email" description:"Email address"`
}

type GetChannelDiagnosticsParams struct {
	ChannelId      int    `query:"channel_id"      description:"Filter by channel ID"`
	ToStatus       int    `query:"to_status"       description:"Filter by resulting status (1 enabled / 2 manual disabled / 3 auto disabled)"`
	TriggerSource  string `query:"trigger_source"  description:"Filter by trigger source (live_request|scheduled_test|manual|by_tag|balance)"`
	StatusCode     int    `query:"status_code"     description:"Filter by parsed upstream HTTP status code"`
	ModelName      string `query:"model_name"      description:"Filter by triggering model name"`
	Keyword        string `query:"keyword"         description:"Search channel name / base url / reason substring"`
	RowType        string `query:"row_type"        description:"Row type: all (default) | transitions | probe"`
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	SortBy         string `query:"sort_by"         description:"Sort column: created_at|first_seen_at|status_code|occurrence_count|channel_id|seconds_in_prev_status"`
	SortOrder      string `query:"sort_order"      description:"Sort direction: asc|desc (default desc)"`
}

type GetChannelDiagnosticStatsParams struct {
	StartTimestamp int64  `query:"start_timestamp" description:"Window start (unix seconds); defaults to last 7 days"`
	OrderBy        string `query:"order_by"        description:"Sort: transitions|uptime|downtime (default transitions)"`
	Limit          int    `query:"limit"           description:"Max channels returned (0 = all)"`
}

type PruneChannelDiagnosticsParams struct {
	BeforeTimestamp int64 `query:"before_timestamp" description:"Delete rows older than this unix-second cutoff"`
}

type TopUpSearchParams struct {
	Keyword string `query:"keyword" description:"Search keyword"`
}

// ─── OAuth ──────────────────────────────────────────────────────────

type GenerateOAuthCodeParams struct {
	Provider    string `query:"provider" description:"OAuth provider name"`
	Intent      string `query:"intent" description:"OAuth flow intent: login or bind"`
	Aff         string `query:"aff" description:"Affiliate code"`
	RedirectURI string `query:"redirect_uri" description:"URL to redirect to after OAuth login (must match allowed origins)"`
	Action      string `query:"action" description:"Set to 'bind' to link OAuth provider to existing account (requires Authorization header)"`
}

type WeChatBindParams struct {
	Code string `query:"code" description:"WeChat auth code"`
}

type EmailBindParams struct {
	Email string `query:"email" description:"Email address"`
	Code  string `query:"code"  description:"Verification code"`
}

// ─── User ───────────────────────────────────────────────────────────

type SearchUsersParams struct {
	Keyword       string `query:"keyword"    description:"Search keyword"`
	Group         string `query:"group"      description:"Filter by group"`
	Role          *int   `query:"role"       description:"Filter by role"`
	Status        *int   `query:"status"     description:"Filter by status"`
	NegativeQuota bool   `query:"negative_quota" description:"Only users whose balance is below zero"`
	SortBy        string `query:"sort_by"    description:"Field to sort by"`
	SortOrder     string `query:"sort_order" description:"Sort order: asc or desc"`
}

// ─── Checkin ────────────────────────────────────────────────────────

type GetCheckinStatusParams struct {
	Month string `query:"month" description:"Month in YYYY-MM format"`
}

// ─── Token ──────────────────────────────────────────────────────────

type SearchTokensParams struct {
	Keyword string `query:"keyword" description:"Search keyword"`
	Token   string `query:"token"   description:"Filter by token"`
}

// ─── Redemption ─────────────────────────────────────────────────────

type SearchRedemptionsParams struct {
	Keyword string `query:"keyword" description:"Search keyword"`
	Status  string `query:"status" description:"Redemption status filter"`
}

// ─── Log ────────────────────────────────────────────────────────────

type GetAllLogsParams struct {
	Type              int    `query:"type"`
	StartTimestamp    int64  `query:"start_timestamp"`
	EndTimestamp      int64  `query:"end_timestamp"`
	Username          string `query:"username"            description:"Filter by username"`
	DiscordId         string `query:"discord_id"          description:"Filter by the Discord ID bound to a user"`
	TokenName         string `query:"token_name"          description:"Filter by token name"`
	ModelName         string `query:"model_name"          description:"Filter by model name"`
	Channel           int    `query:"channel"`
	Group             string `query:"group"               description:"Filter by group"`
	RequestID         string `query:"request_id"          description:"Filter by request ID"`
	UpstreamRequestID string `query:"upstream_request_id" description:"Filter by upstream request ID"`
	SubscriptionPlan  string `query:"subscription_plan"   description:"Filter by subscription plan title (substring)"`
}

type GetLogByRequestParams struct {
	RequestID string `query:"request_id" description:"Full request ID naming exactly one log row"`
}

type GetUserLogsParams struct {
	Type              int    `query:"type"`
	StartTimestamp    int64  `query:"start_timestamp"`
	EndTimestamp      int64  `query:"end_timestamp"`
	TokenName         string `query:"token_name"          description:"Filter by token name"`
	ModelName         string `query:"model_name"          description:"Filter by model name"`
	Group             string `query:"group"               description:"Filter by group"`
	RequestID         string `query:"request_id"          description:"Filter by request ID"`
	UpstreamRequestID string `query:"upstream_request_id" description:"Filter by upstream request ID"`
	SubscriptionPlan  string `query:"subscription_plan"   description:"Filter by subscription plan title (substring)"`
}

type LogStatParams struct {
	Type           int    `query:"type"`
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	TokenName      string `query:"token_name"       description:"Filter by token name"`
	Username       string `query:"username"         description:"Filter by username"`
	ModelName      string `query:"model_name"       description:"Filter by model name"`
	Channel        int    `query:"channel"`
	Group          string `query:"group"            description:"Filter by group"`
}

type LogSelfStatParams struct {
	Type           int    `query:"type"`
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	TokenName      string `query:"token_name"       description:"Filter by token name"`
	ModelName      string `query:"model_name"       description:"Filter by model name"`
	Channel        int    `query:"channel"`
	Group          string `query:"group"            description:"Filter by group"`
}

type DeleteHistoryLogsParams struct {
	TargetTimestamp int64 `query:"target_timestamp"`
}

// ─── Model Status ───────────────────────────────────────────────────

type GetModelStatusBucketsParams struct {
	Model  string `query:"model"  description:"Model name (required)"`
	Bucket string `query:"bucket" description:"Bucket size: 1m|5m|15m|1h|1d (default 15m)"`
	Hours  int    `query:"hours"  description:"History window in hours (default 24, max 720)"`
}

type GetModelStatusIncidentsParams struct {
	Since int64  `query:"since" description:"Unix seconds; default now-24h"`
	Until int64  `query:"until" description:"Unix seconds; default now"`
	Model string `query:"model" description:"Optional model filter"`
}

type GetModelStatusPageParams struct {
	Bucket  string `query:"bucket"  description:"Bucket size: 1m|5m|15m|1h|1d (default 15m)"`
	Hours   int    `query:"hours"   description:"History window in hours (default 24, max 720)"`
	Compact int    `query:"compact" description:"1 = emit compact per-bucket tuples; default 0 (verbose StatusBarData[])"`
}

// ─── Rankings ───────────────────────────────────────────────────────

type GetRankingsParams struct {
	Period string `query:"period" description:"Ranking period: today|week|month|year (default week)"`
}

type GetModelRankingParams struct {
	Model  string `query:"model"  description:"Model name (required)"`
	Period string `query:"period" description:"Ranking period: today|week|month|year (default week)"`
}

// ─── Perf Metrics ───────────────────────────────────────────────────

type GetPerfMetricsSummaryParams struct {
	Hours int `query:"hours" description:"Lookback window in hours (default 24, max 720)"`
}

type GetPerfMetricsParams struct {
	Model string `query:"model" description:"Model name (required)"`
	Group string `query:"group" description:"Optional group filter"`
	Hours int    `query:"hours" description:"Lookback window in hours (default 24, max 720)"`
}

// ─── Quota / Usage Data ─────────────────────────────────────────────

type GetAllQuotaDatesParams struct {
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	Username       string `query:"username"         description:"Filter by username"`
}

type GetQuotaSummaryParams struct {
	StartTimestamp int64 `query:"start_timestamp"`
	EndTimestamp   int64 `query:"end_timestamp"`
}

type GetUserQuotaDatesParams struct {
	StartTimestamp int64 `query:"start_timestamp"`
	EndTimestamp   int64 `query:"end_timestamp"`
}

type GetFlowQuotaDatesParams struct {
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	Username       string `query:"username"         description:"Filter by username (ignored for non-admin callers)"`
}

// ─── Prefill Group ──────────────────────────────────────────────────

type GetPrefillGroupsParams struct {
	Type string `query:"type" description:"Filter by group type"`
}

// ─── Channel ────────────────────────────────────────────────────────

type GetAllChannelsParams struct {
	IdSort    bool   `query:"id_sort"`
	TagMode   bool   `query:"tag_mode"`
	Status    string `query:"status"     description:"Filter by status"`
	Type      int    `query:"type"`
	Group     string `query:"group"      description:"Filter by group"`
	SortBy    string `query:"sort_by"    description:"Column to sort by"`
	SortOrder string `query:"sort_order" description:"asc or desc"`
}

type SearchChannelsParams struct {
	Keyword   string `query:"keyword"    description:"Search keyword"`
	Group     string `query:"group"      description:"Filter by group"`
	Model     string `query:"model"      description:"Filter by model"`
	Status    string `query:"status"     description:"Filter by status"`
	IdSort    bool   `query:"id_sort"`
	TagMode   bool   `query:"tag_mode"`
	Type      int    `query:"type"`
	SortBy    string `query:"sort_by"    description:"Column to sort by"`
	SortOrder string `query:"sort_order" description:"asc or desc"`
}

type GetTagModelsParams struct {
	Tag string `query:"tag" description:"Tag name"`
}

type CopyChannelParams struct {
	Suffix       string `query:"suffix"        description:"Name suffix"`
	ResetBalance bool   `query:"reset_balance"`
}

type TestChannelParams struct {
	Model        string `query:"model"         description:"Model to test"`
	EndpointType string `query:"endpoint_type"  description:"Endpoint type"`
	Stream       bool   `query:"stream"`
}

// ─── Channel Affinity Cache ─────────────────────────────────────────

type ClearChannelAffinityCacheParams struct {
	All      string `query:"all"       description:"Clear all entries"`
	RuleName string `query:"rule_name" description:"Filter by rule name"`
}

type GetChannelAffinityUsageCacheStatsParams struct {
	RuleName   string `query:"rule_name"   description:"Filter by rule name"`
	UsingGroup string `query:"using_group" description:"Filter by group"`
	KeyFp      string `query:"key_fp"      description:"Filter by key fingerprint"`
}

// ─── Vendor ─────────────────────────────────────────────────────────

type SearchVendorsParams struct {
	Keyword string `query:"keyword" description:"Search keyword"`
}

// ─── Model Meta ─────────────────────────────────────────────────────

type SearchModelsMetaParams struct {
	Keyword      string `query:"keyword"       description:"Search keyword"`
	Vendor       string `query:"vendor"        description:"Filter by vendor"`
	Status       string `query:"status"        description:"Filter by status"`
	SyncOfficial string `query:"sync_official" description:"Filter by sync official flag"`
}

// ─── Model Sync ─────────────────────────────────────────────────────

type SyncUpstreamPreviewParams struct {
	Locale string `query:"locale" description:"Locale for model descriptions"`
}

// ─── Midjourney ─────────────────────────────────────────────────────

type GetAllMidjourneyParams struct {
	ChannelID      string `query:"channel_id"      description:"Filter by channel ID"`
	MjID           string `query:"mj_id"           description:"Midjourney task ID"`
	StartTimestamp string `query:"start_timestamp"  description:"Start timestamp"`
	EndTimestamp   string `query:"end_timestamp"    description:"End timestamp"`
}

type GetUserMidjourneyParams struct {
	MjID           string `query:"mj_id"           description:"Midjourney task ID"`
	StartTimestamp string `query:"start_timestamp"  description:"Start timestamp"`
	EndTimestamp   string `query:"end_timestamp"    description:"End timestamp"`
}

// ─── Task ───────────────────────────────────────────────────────────

type GetAllTaskParams struct {
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	Platform       string `query:"platform"        description:"Filter by platform"`
	TaskID         string `query:"task_id"         description:"Filter by task ID"`
	Status         string `query:"status"          description:"Filter by status"`
	Action         string `query:"action"          description:"Filter by action"`
	ChannelID      string `query:"channel_id"      description:"Filter by channel ID"`
}

type GetUserTaskParams struct {
	StartTimestamp int64  `query:"start_timestamp"`
	EndTimestamp   int64  `query:"end_timestamp"`
	Platform       string `query:"platform"        description:"Filter by platform"`
	TaskID         string `query:"task_id"         description:"Filter by task ID"`
	Status         string `query:"status"          description:"Filter by status"`
	Action         string `query:"action"          description:"Filter by action"`
}

// ─── Deployment ─────────────────────────────────────────────────────

type GetAllDeploymentsParams struct {
	Status string `query:"status" description:"Filter by status"`
}

type SearchDeploymentsParams struct {
	Status  string `query:"status"  description:"Filter by status"`
	Keyword string `query:"keyword" description:"Search keyword"`
}

type GetAvailableReplicasParams struct {
	HardwareID int `query:"hardware_id"`
	GpuCount   int `query:"gpu_count"`
}

type CheckClusterNameAvailabilityParams struct {
	Name string `query:"name" description:"Cluster name to check"`
}

type GetDeploymentLogsParams struct {
	ContainerID string `query:"container_id" description:"Container ID"`
	Level       string `query:"level"        description:"Log level"`
	Stream      string `query:"stream"       description:"Stream type"`
	Cursor      string `query:"cursor"       description:"Pagination cursor"`
	Limit       int    `query:"limit"`
	Follow      bool   `query:"follow"`
	StartTime   string `query:"start_time"   description:"Start time (RFC3339)"`
	EndTime     string `query:"end_time"     description:"End time (RFC3339)"`
}
