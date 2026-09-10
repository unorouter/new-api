package service

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// Per-channel in-flight cap and token bucket from the channel's own settings.
// Per process, like the cooldowns: a replica only knows what it sent, so the
// configured limits are per replica.
var (
	laneInFlight sync.Map // channel id -> *atomic.Int64
	laneBuckets  sync.Map // channel id -> *laneBucket
)

type laneBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (b *laneBucket) take(rate float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.last.IsZero() {
		b.tokens = rate
	} else {
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > rate {
			b.tokens = rate
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// TryAcquireLane reserves one in-flight slot and one rate token on the channel.
// Channels without limits always succeed and reserve nothing.
func TryAcquireLane(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	setting := channel.GetSetting()
	if setting.MaxConcurrency <= 0 && setting.MaxRPS <= 0 {
		return true
	}
	if setting.MaxRPS > 0 {
		v, _ := laneBuckets.LoadOrStore(channel.Id, &laneBucket{})
		if !v.(*laneBucket).take(setting.MaxRPS) {
			return false
		}
	}
	if setting.MaxConcurrency > 0 {
		v, _ := laneInFlight.LoadOrStore(channel.Id, new(atomic.Int64))
		counter := v.(*atomic.Int64)
		if counter.Add(1) > int64(setting.MaxConcurrency) {
			counter.Add(-1)
			return false
		}
	}
	return true
}

// ReleaseLane returns the in-flight slot taken by TryAcquireLane. Rate tokens
// are not returned: the request happened.
func ReleaseLane(channel *model.Channel) {
	if channel == nil || channel.GetSetting().MaxConcurrency <= 0 {
		return
	}
	if v, ok := laneInFlight.Load(channel.Id); ok {
		v.(*atomic.Int64).Add(-1)
	}
}
