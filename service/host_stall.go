package service

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// A marketplace host stalls as a whole: for ten to twenty minutes most of its
// lanes hit the first byte deadline together, then all of them are clean again
// (a7, 2026-10-01: 11 of 11 lanes at once, three quarters of all its timeouts
// inside such bursts). Failing over between its lanes then only stacks 90 s
// waits, so a host with enough different lanes stalled in the window is passed
// over while another provider can serve. Per process, like the lane cooldowns.
// There is no timer and no probe: users outside ProviderStallUserIds keep
// sending to the host, so fresh stalls keep the window hot and their absence
// reopens it.
type hostStall struct {
	at      time.Time
	channel int
}

var (
	hostStallMu sync.Mutex
	hostStalls  = map[string][]hostStall{}
)

func hostStallWindow() time.Duration {
	return time.Duration(operation_setting.GetMonitorSetting().ProviderStallWindowSeconds) * time.Second
}

// RecordHostStall notes a first byte timeout on a lane of host.
func RecordHostStall(host string, channelId int) {
	window := hostStallWindow()
	if host == "" || window <= 0 {
		return
	}
	now := time.Now()
	hostStallMu.Lock()
	defer hostStallMu.Unlock()
	kept := hostStalls[host][:0]
	for _, s := range hostStalls[host] {
		if now.Sub(s.at) < window {
			kept = append(kept, s)
		}
	}
	hostStalls[host] = append(kept, hostStall{at: now, channel: channelId})
}

// HostStalling reports whether enough different lanes of host timed out inside
// the window to call it a host-wide stall.
func HostStalling(host string) bool {
	m := operation_setting.GetMonitorSetting()
	window := hostStallWindow()
	if host == "" || window <= 0 || m.ProviderStallLanes <= 0 {
		return false
	}
	now := time.Now()
	hostStallMu.Lock()
	defer hostStallMu.Unlock()
	lanes := map[int]struct{}{}
	for _, s := range hostStalls[host] {
		if now.Sub(s.at) < window {
			lanes[s.channel] = struct{}{}
		}
	}
	return len(lanes) >= m.ProviderStallLanes
}

// HostStallingFor applies the stall only to the users it is rolled out to.
func HostStallingFor(userId int, host string) bool {
	for _, id := range operation_setting.GetMonitorSetting().ProviderStallUserIds {
		if id == userId {
			return HostStalling(host)
		}
	}
	return false
}
