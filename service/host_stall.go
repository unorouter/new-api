package service

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-redis/redis/v8"
)

// A marketplace host stalls as a whole: for ten to twenty minutes most of its
// lanes hit the first byte deadline together, then all of them are clean again
// (a7, 2026-10-01: 11 of 11 lanes at once, three quarters of all its timeouts
// inside such bursts). Failing over between its lanes then only stacks 90 s
// waits, so a host with enough different lanes stalled in the window is passed
// over while another provider can serve.
// There is no timer and no probe: users outside ProviderStallUserIds keep
// sending to the host, so fresh stalls keep the window hot and their absence
// reopens it.
//
// The stalls live in Redis, one sorted set per host with the lane as member and
// its last stall as score: a replica sees a third of the traffic, and counting
// alone it reacted to half of a burst (2026-10-02). Without Redis each process
// counts for itself.
type hostStall struct {
	at      time.Time
	channel int
}

type hostStallAnswer struct {
	stalling bool
	at       time.Time
}

// Selection asks once per candidate lane, so the shared count is read at most
// this often per host.
const hostStallCacheTTL = 2 * time.Second

var (
	hostStallMu      sync.Mutex
	hostStalls       = map[string][]hostStall{}
	hostStallAnswers = map[string]hostStallAnswer{}
)

func hostStallWindow() time.Duration {
	return time.Duration(operation_setting.GetMonitorSetting().ProviderStallWindowSeconds) * time.Second
}

func hostStallKey(host string) string {
	return "host_stall:" + host
}

// RecordHostStall notes a first byte timeout on a lane of host.
func RecordHostStall(host string, channelId int) {
	window := hostStallWindow()
	if host == "" || window <= 0 {
		return
	}
	now := time.Now()
	if common.RedisEnabled {
		ctx := context.Background()
		key := hostStallKey(host)
		pipe := common.RDB.Pipeline()
		pipe.ZAdd(ctx, key, &redis.Z{Score: float64(now.UnixMilli()), Member: strconv.Itoa(channelId)})
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-window).UnixMilli(), 10))
		pipe.Expire(ctx, key, 2*window)
		if _, err := pipe.Exec(ctx); err != nil {
			common.SysError("host stall Redis write failed: " + err.Error())
		}
		hostStallMu.Lock()
		delete(hostStallAnswers, host)
		hostStallMu.Unlock()
		return
	}
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
	if !common.RedisEnabled {
		lanes := map[int]struct{}{}
		for _, s := range hostStalls[host] {
			if now.Sub(s.at) < window {
				lanes[s.channel] = struct{}{}
			}
		}
		return len(lanes) >= m.ProviderStallLanes
	}
	if v, ok := hostStallAnswers[host]; ok && now.Sub(v.at) < hostStallCacheTTL {
		return v.stalling
	}
	lanes, err := common.RDB.ZCount(context.Background(), hostStallKey(host),
		strconv.FormatInt(now.Add(-window).UnixMilli(), 10), "+inf").Result()
	if err != nil {
		common.SysError("host stall Redis read failed: " + err.Error())
		return false
	}
	stalling := int(lanes) >= m.ProviderStallLanes
	hostStallAnswers[host] = hostStallAnswer{stalling: stalling, at: now}
	return stalling
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
