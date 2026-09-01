package notify

import (
	"os"

	"github.com/QuantumNous/new-api/common"
)

// Enabled reports whether the notification engine is active. The whole
// feature requires Redis (pub/sub bus + snapshot store); there is no
// in-memory fallback by design. The RDB nil check matters at startup:
// option loading fires MarkDirty before InitRedisClient has run.
func Enabled() bool {
	if os.Getenv("NOTIFY_ENABLED") == "false" {
		return false
	}
	return common.RedisEnabled && common.RDB != nil
}

func WSMaxConns() int {
	return common.GetEnvOrDefault("NOTIFY_WS_MAX_CONNS", 5000)
}

func OfflineGraceSeconds() int {
	return common.GetEnvOrDefault("NOTIFY_OFFLINE_GRACE_SECONDS", 120)
}

func PriceGraceSeconds() int {
	return common.GetEnvOrDefault("NOTIFY_PRICE_GRACE_SECONDS", 60)
}

func EventCooldownSeconds() int {
	return common.GetEnvOrDefault("NOTIFY_EVENT_COOLDOWN_SECONDS", 600)
}

// BurstThreshold is the per-cycle, per-event-type count above which individual
// model events collapse into one digest. The cooldown is keyed per (model,
// event), so an operational sweep that flips hundreds of models at once (mass
// channel re-enable, provider key restored) otherwise emits one push per model.
func BurstThreshold() int {
	return common.GetEnvOrDefault("NOTIFY_BURST_THRESHOLD", 12)
}

func VapidPublicKey() string {
	return os.Getenv("VAPID_PUBLIC_KEY")
}

func VapidPrivateKey() string {
	return os.Getenv("VAPID_PRIVATE_KEY")
}

func VapidSubject() string {
	return os.Getenv("VAPID_SUBJECT")
}

func WebPushEnabled() bool {
	return Enabled() && VapidPublicKey() != "" && VapidPrivateKey() != "" && VapidSubject() != ""
}
