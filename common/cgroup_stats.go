package common

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Container-aware resource sampling. gopsutil reports the HOST's cpu and memory,
// so in a container it shows the node's usage (or 0 where /proc is masked) instead
// of this instance's. Read the cgroup v2 interface first and fall back to gopsutil
// when it is absent (bare metal, cgroup v1).

const (
	cgroupMemMax     = "/sys/fs/cgroup/memory.max"
	cgroupMemCurrent = "/sys/fs/cgroup/memory.current"
	cgroupCPUMax     = "/sys/fs/cgroup/cpu.max"
	cgroupCPUStat    = "/sys/fs/cgroup/cpu.stat"
)

type cpuSample struct {
	usageUsec int64
	at        time.Time
}

var lastCPUSample cpuSample

func readCgroupUint(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	field := strings.TrimSpace(string(data))
	if field == "max" || field == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(field, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// cgroupMemoryUsage returns this container's memory usage as a percentage of its
// limit. Reports false when unlimited: a percentage of "max" is meaningless, and
// the host figure would be misleading.
func cgroupMemoryUsage() (float64, bool) {
	limit, ok := readCgroupUint(cgroupMemMax)
	if !ok || limit == 0 {
		return 0, false
	}
	used, ok := readCgroupUint(cgroupMemCurrent)
	if !ok {
		return 0, false
	}
	return float64(used) / float64(limit) * 100, true
}

// cgroupCPUQuota returns the container's CPU allowance in cores. Falls back to
// the host core count when the cgroup is unconstrained, so the percentage is
// always relative to what this process may actually use.
func cgroupCPUQuota() float64 {
	data, err := os.ReadFile(cgroupCPUMax)
	if err != nil {
		return 0
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) != 2 || fields[0] == "max" {
		return 0
	}
	quota, err1 := strconv.ParseFloat(fields[0], 64)
	period, err2 := strconv.ParseFloat(fields[1], 64)
	if err1 != nil || err2 != nil || period == 0 {
		return 0
	}
	return quota / period
}

func cgroupCPUUsageUsec() (int64, bool) {
	data, err := os.ReadFile(cgroupCPUStat)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "usage_usec ") {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "usage_usec ")), 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// cgroupCPUUsage returns CPU usage since the previous call as a percentage of the
// container's quota. The first call only seeds the baseline (no prior sample to
// diff against) and reports false.
func cgroupCPUUsage(cores float64) (float64, bool) {
	usage, ok := cgroupCPUUsageUsec()
	if !ok {
		return 0, false
	}
	now := time.Now()
	prev := lastCPUSample
	lastCPUSample = cpuSample{usageUsec: usage, at: now}
	if prev.at.IsZero() || !now.After(prev.at) {
		return 0, false
	}
	if cores <= 0 {
		cores = float64(runtime.NumCPU())
	}
	elapsedUsec := float64(now.Sub(prev.at).Microseconds())
	if elapsedUsec <= 0 {
		return 0, false
	}
	pct := float64(usage-prev.usageUsec) / (elapsedUsec * cores) * 100
	if pct < 0 {
		return 0, false
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
}
