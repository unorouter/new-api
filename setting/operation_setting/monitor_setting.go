package operation_setting

import (
	"fmt"
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled           bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes           float64 `json:"auto_test_channel_minutes"`
	AutoTestDisabledChannelsOnly     bool    `json:"auto_test_disabled_channels_only"`
	ChannelTestMode                  string  `json:"channel_test_mode"`
	ChannelTestConcurrency           int     `json:"channel_test_concurrency"`
	ChannelStatusNotifyEnabled       bool    `json:"channel_status_notify_enabled"`
	SnapshotModelStatusEnabled       bool    `json:"snapshot_model_status_enabled"`
	SnapshotModelStatusRetentionDays int     `json:"snapshot_model_status_retention_days"`
	// Fail a channel test when the upstream returns 200 with no content, so the
	// disable-on-failure / re-enable-on-success loop treats blank channels as broken.
	DisableOnEmptyResponse bool `json:"disable_on_empty_response"`
	// Live empty-response auto-disable is rate-based: disable a channel once its empty
	// share of traffic in the counter window reaches EmptyResponseRateThreshold over at
	// least EmptyResponseMinSamples requests, so a healthy high-volume channel is never
	// false-disabled by scattered empties. EmptyResponseAbsoluteFloor still disables a
	// fully dead channel (no successes) before the min-sample bar is met.
	EmptyResponseRateThreshold float64 `json:"empty_response_rate_threshold"`
	EmptyResponseMinSamples    int     `json:"empty_response_min_samples"`
	EmptyResponseAbsoluteFloor int     `json:"empty_response_absolute_floor"`
	// Live auto-disable for every OTHER upstream fault is rate-based on the same
	// window. An upstream 429 or 5xx is a capacity blip, not a dead channel: the
	// busiest lanes trip them most often precisely because they carry the most
	// traffic, so a single-error disable pulls the healthiest channels out of
	// rotation. Credential faults bypass this and still disable on first sight.
	ChannelFailureRateThreshold float64 `json:"channel_failure_rate_threshold"`
	ChannelFailureMinSamples    int     `json:"channel_failure_min_samples"`
	ChannelFailureAbsoluteFloor int     `json:"channel_failure_absolute_floor"`
	// A lane that fails half its requests at a few requests an hour never puts
	// enough failures into one ten minute window, so the fast gate above is blind
	// to it by construction (no1 gemini-2.5-pro: 64 failures a day, 3 an hour).
	// The slow window sums hourly buckets over ChannelSlowWindowHours and disables
	// on the same rate once ChannelSlowFailureAbsoluteFloor failures are in it.
	// Threshold 0 falls back to ChannelFailureRateThreshold; hours 0 disables it.
	ChannelSlowWindowHours          int     `json:"channel_slow_window_hours"`
	ChannelSlowFailureAbsoluteFloor int     `json:"channel_slow_failure_absolute_floor"`
	ChannelSlowFailureRateThreshold float64 `json:"channel_slow_failure_rate_threshold"`
	// A truncation is an upstream failure that arrived after the client had already
	// received content: the answer is cut off and no retry can hide it. Measured on
	// a7 lane 4056, the failure rate is a function of how long the request runs:
	// 1.0% under 60s, 79.6% between 120s and 180s. The ordinary failure gate cannot
	// see that, because the lane still reads 94.8% healthy overall. Counted and
	// gated separately, over requests that ran at least
	// ChannelTruncationMinDurationSeconds so short traffic cannot dilute the rate.
	// Threshold 0 disables the gate entirely.
	ChannelTruncationRateThreshold  float64 `json:"channel_truncation_rate_threshold"`
	ChannelTruncationMinSamples     int     `json:"channel_truncation_min_samples"`
	ChannelTruncationMinDurationSec int     `json:"channel_truncation_min_duration_sec"`
	// Floor for a channel with zero successes in the window. Kept far below the
	// absolute floor because a dead channel on a low-traffic model only fails a few
	// times per counter window and must still be able to park.
	ChannelFailureDeadFloor int `json:"channel_failure_dead_floor"`
	// Consecutive failures (reset by any success) that park a channel regardless of
	// the window counts. A trickle of failures spread thin never reaches a count
	// floor inside one fixed window, so a fully dead upstream would otherwise keep
	// serving errors indefinitely.
	ChannelFailureStreakFloor int `json:"channel_failure_streak_floor"`
	// Probation after an automatic re-enable. The recovery probe is one tiny
	// request, so a lane that answers probes but fails under load comes back and
	// dies again within minutes; while on probation these tighter thresholds
	// replace the normal ones so the relapse is caught in a handful of requests.
	ChannelProbationSeconds       int     `json:"channel_probation_seconds"`
	ChannelProbationStreakFloor   int     `json:"channel_probation_streak_floor"`
	ChannelProbationRateThreshold float64 `json:"channel_probation_rate_threshold"`
	ChannelProbationMinSamples    int     `json:"channel_probation_min_samples"`
	// Consecutive clean recovery probes before an auto-disabled channel comes back.
	ChannelReenableProbePasses int `json:"channel_reenable_probe_passes"`
	// A channel failing its scheduled probe over and over is asked less often:
	// after ChannelProbeBackoffFloor consecutive failures the wait doubles from
	// ChannelProbeBackoffBaseSeconds up to ChannelProbeBackoffMaxSeconds. Most
	// of these never recover (a model archived upstream, a revoked key), and at
	// full cadence they spend real upstream quota: one lane had logged 19,198
	// identical 410s. 0 on base or max disables the backoff.
	ChannelProbeBackoffFloor       int `json:"channel_probe_backoff_floor"`
	ChannelProbeBackoffBaseSeconds int `json:"channel_probe_backoff_base_seconds"`
	ChannelProbeBackoffMaxSeconds  int `json:"channel_probe_backoff_max_seconds"`
	// Minimum seconds a paid lane (group ratio > 0) stays auto-disabled, 0 = none.
	// Skipped when none of the lane's models has another enabled channel.
	ChannelPaidReenableHoldSeconds int `json:"channel_paid_reenable_hold_seconds"`
	// Transient faults skip the lane for a growing cooldown instead of disabling it;
	// a provider-wide throttle skips every lane of that host.
	ChannelCooldownBaseSeconds int `json:"channel_cooldown_base_seconds"`
	ChannelCooldownMaxSeconds  int `json:"channel_cooldown_max_seconds"`
	ProviderCooldownSeconds    int `json:"provider_cooldown_seconds"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModeAutoBanOnly     = "auto_ban_only"
	ChannelTestModePassiveRecovery = "passive_recovery"

	ChannelTestConcurrencyOptionKey = "monitor_setting.channel_test_concurrency"
	DefaultChannelTestConcurrency   = 1
	// The probe pool has to clear the whole disabled backlog inside one system-task
	// lease. At a ~10s average probe (a fraction of channels burn the full first-byte
	// timeout), a four-figure backlog against a 32-worker ceiling takes hours, the
	// lease lapses mid-run, and the channels past the abort point are never retried.
	// Probes spread across ~180 distinct upstreams, so a wide pool is a handful of
	// concurrent requests per host rather than a burst at any one of them.
	MaxChannelTestConcurrency = 128
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:           false,
	AutoTestChannelMinutes:           10,
	AutoTestDisabledChannelsOnly:     false,
	ChannelTestMode:                  ChannelTestModeScheduledAll,
	ChannelTestConcurrency:           DefaultChannelTestConcurrency,
	ChannelStatusNotifyEnabled:       true,
	SnapshotModelStatusEnabled:       true,
	SnapshotModelStatusRetentionDays: 30,
	DisableOnEmptyResponse:           true,
	EmptyResponseRateThreshold:       0.5,
	EmptyResponseMinSamples:          8,
	EmptyResponseAbsoluteFloor:       5,
	ChannelFailureRateThreshold:      0.5,
	ChannelFailureMinSamples:         10,
	ChannelTruncationRateThreshold:   0,
	ChannelTruncationMinSamples:      200,
	ChannelTruncationMinDurationSec:  60,
	ChannelFailureAbsoluteFloor:      20,
	ChannelSlowWindowHours:           6,
	ChannelSlowFailureAbsoluteFloor:  20,
	ChannelSlowFailureRateThreshold:  0,
	ChannelFailureDeadFloor:          5,
	ChannelFailureStreakFloor:        3,
	ChannelProbationSeconds:          1800,
	ChannelProbationStreakFloor:      3,
	ChannelProbationRateThreshold:    0.2,
	ChannelProbationMinSamples:       5,
	ChannelReenableProbePasses:       1,
	ChannelProbeBackoffFloor:         3,
	ChannelProbeBackoffBaseSeconds:   600,
	ChannelProbeBackoffMaxSeconds:    3600,
	ChannelPaidReenableHoldSeconds:   0,
	ChannelCooldownBaseSeconds:       30,
	ChannelCooldownMaxSeconds:        600,
	ProviderCooldownSeconds:          60,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
			monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			monitorSetting.AutoTestChannelEnabled = parsed
		}
	}
	switch monitorSetting.ChannelTestMode {
	case ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
	default:
		monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	monitorSetting.ChannelTestConcurrency = NormalizeChannelTestConcurrency(monitorSetting.ChannelTestConcurrency)
	return &monitorSetting
}

func NormalizeChannelTestConcurrency(concurrency int) int {
	if concurrency < 1 {
		return DefaultChannelTestConcurrency
	}
	if concurrency > MaxChannelTestConcurrency {
		return MaxChannelTestConcurrency
	}
	return concurrency
}

func ValidateChannelTestConcurrency(value string) error {
	concurrency, err := strconv.Atoi(value)
	if err != nil || concurrency < 1 || concurrency > MaxChannelTestConcurrency {
		return fmt.Errorf("channel test concurrency must be between 1 and %d", MaxChannelTestConcurrency)
	}
	return nil
}
