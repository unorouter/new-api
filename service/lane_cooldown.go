package service

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// A cooldown keeps a lane enabled but out of rotation for a while. It replaces
// the disable/probe/re-enable cycle for transient faults (rate limits, capacity):
// no status change, no diagnostics row, no probe, and the lane serves again the
// moment the cooldown ends. Per process: each replica learns its own picture,
// which is enough because every replica sees the same upstream behave the same.
type laneCooldown struct {
	until   time.Time
	strikes int
}

var (
	laneCooldowns sync.Map // channel id -> laneCooldown
	hostCooldowns sync.Map // upstream host -> time.Time
)

func cooldownFor(strikes int) time.Duration {
	m := operation_setting.GetMonitorSetting()
	base := time.Duration(m.ChannelCooldownBaseSeconds) * time.Second
	if base <= 0 {
		return 0
	}
	max := time.Duration(m.ChannelCooldownMaxSeconds) * time.Second
	d := base
	for i := 1; i < strikes && d < max; i++ {
		d *= 2
	}
	if max > 0 && d > max {
		d = max
	}
	return d
}

// CoolLane skips the channel for the next cooldown period; repeats double it up
// to the cap. Returns the period, 0 when cooldowns are off.
func CoolLane(channelId int) time.Duration {
	strikes := 1
	if v, ok := laneCooldowns.Load(channelId); ok {
		strikes = v.(laneCooldown).strikes + 1
	}
	d := cooldownFor(strikes)
	if d <= 0 {
		return 0
	}
	laneCooldowns.Store(channelId, laneCooldown{until: time.Now().Add(d), strikes: strikes})
	return d
}

// ClearLaneCooldown ends the current cooldown and sheds ONE strike, rather than
// forgetting the run outright. Deleting the entry let a lane that alternates
// pass/fail reset the ladder forever: a7-3304 answered every ~22s, inside its own
// 30s base cooldown, so every strike was erased before the next doubling and a
// 59%-failing lane never cooled for longer than the first rung. Decaying keeps the
// original intent (a recovering lane climbs back down) without that.
func ClearLaneCooldown(channelId int) {
	v, ok := laneCooldowns.Load(channelId)
	if !ok {
		return
	}
	strikes := v.(laneCooldown).strikes - 1
	if strikes <= 0 {
		laneCooldowns.Delete(channelId)
		return
	}
	laneCooldowns.Store(channelId, laneCooldown{strikes: strikes})
}

// LaneStrikes reports the lane's unshed cooldown strikes. Strikes outlive the
// cooldown itself (only a success sheds one), so this answers "has this lane been
// misbehaving lately" rather than "is it skipped right now".
func LaneStrikes(channelId int) int {
	v, ok := laneCooldowns.Load(channelId)
	if !ok {
		return 0
	}
	return v.(laneCooldown).strikes
}

func LaneCooled(channelId int) bool {
	v, ok := laneCooldowns.Load(channelId)
	if !ok {
		return false
	}
	return time.Now().Before(v.(laneCooldown).until)
}

// CoolHost skips every lane of an upstream host: a provider-wide throttle answers
// the same on all of them, so walking them only burns the retry budget.
func CoolHost(host string) time.Duration {
	if host == "" {
		return 0
	}
	d := time.Duration(operation_setting.GetMonitorSetting().ProviderCooldownSeconds) * time.Second
	if d <= 0 {
		return 0
	}
	hostCooldowns.Store(host, time.Now().Add(d))
	return d
}

func HostCooled(host string) bool {
	if host == "" {
		return false
	}
	v, ok := hostCooldowns.Load(host)
	if !ok {
		return false
	}
	return time.Now().Before(v.(time.Time))
}

// UpstreamHostOf is the host key the cooldowns use for a channel base URL.
func UpstreamHostOf(baseURL string) string {
	return upstreamHost(baseURL)
}
