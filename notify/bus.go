package notify

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	redisDirtyChannel  = "newapi:notify:dirty"
	redisEventsChannel = "newapi:notify:events"
	redisRecentKey     = "newapi:notify:recent"
	// v2: snapshot states are computed against publicly usable groups only;
	// the key bump makes the first post-deploy recompute reseed silently
	// instead of emitting a transition burst for previously miscounted models.
	redisSnapshotKey   = "newapi:notify:snapshot:v2"
	redisPubDedupPfx   = "newapi:notify:pub:"
	redisCooldownPfx   = "newapi:notify:cooldown:"
	redisSentPfx       = "newapi:notify:sent:"
	redisOnlinePfx     = "newapi:notify:online:"
	redisSubsDirtyChan = "newapi:notify:push_subs_dirty"

	recentRingSize = 200
)

var lastDirtyUnixNano atomic.Int64

// MarkDirty signals that model availability or pricing may have changed.
// Called from hot paths in model/, so it must be cheap: throttled to one
// Redis publish per second per instance, fire and forget.
func MarkDirty(reason string) {
	if !Enabled() {
		return
	}
	now := time.Now().UnixNano()
	last := lastDirtyUnixNano.Load()
	if now-last < int64(time.Second) {
		return
	}
	if !lastDirtyUnixNano.CompareAndSwap(last, now) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := common.RDB.Publish(ctx, redisDirtyChannel, reason).Err(); err != nil {
			common.SysError("notify: publish dirty failed: " + err.Error())
		}
	}()
}

// Publish emits a high-level event to every replica (WS fanout) and appends
// it to the recent-events ring. A SETNX guard makes publishing idempotent
// across accidental double masters during rolling deploys.
func Publish(evt *Event) bool {
	if !Enabled() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sum := sha1.Sum([]byte(evt.Type + "|" + evt.Data.Model + "|" + fmt.Sprintf("%v|%v", evt.Data.Online, evt.Data.CheapestRatio)))
	dedupKey := redisPubDedupPfx + hex.EncodeToString(sum[:])
	ok, err := common.RDB.SetNX(ctx, dedupKey, 1, 5*time.Minute).Result()
	if err != nil {
		common.SysError("notify: publish dedup check failed: " + err.Error())
		return false
	}
	if !ok {
		return false
	}

	payload, err := common.Marshal(evt)
	if err != nil {
		common.SysError("notify: marshal event failed: " + err.Error())
		return false
	}
	pipe := common.RDB.Pipeline()
	pipe.RPush(ctx, redisRecentKey, payload)
	pipe.LTrim(ctx, redisRecentKey, -recentRingSize, -1)
	pipe.Publish(ctx, redisEventsChannel, payload)
	if _, err := pipe.Exec(ctx); err != nil {
		common.SysError("notify: publish event failed: " + err.Error())
		return false
	}
	return true
}

// CooldownAcquire returns true when this model+type may emit (max one event
// per type per model per cooldown window).
func CooldownAcquire(model string, eventType string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key := redisCooldownPfx + model + ":" + eventType
	ok, err := common.RDB.SetNX(ctx, key, 1, time.Duration(EventCooldownSeconds())*time.Second).Result()
	if err != nil {
		common.SysError("notify: cooldown check failed: " + err.Error())
		return true
	}
	return ok
}

// SentAcquire guards web push sending so only one replica sends each event.
func SentAcquire(eventId string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, err := common.RDB.SetNX(ctx, redisSentPfx+eventId, 1, time.Hour).Result()
	if err != nil {
		return false
	}
	return ok
}

// Presence gating: a device with a live WS connection must not receive web
// push for the same events (it already gets the in-app toast).

func RefreshPresence(endpointHash string) {
	if endpointHash == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = common.RDB.Set(ctx, redisOnlinePfx+endpointHash, 1, 90*time.Second).Err()
}

func ClearPresence(endpointHash string) {
	if endpointHash == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = common.RDB.Del(ctx, redisOnlinePfx+endpointHash).Err()
}

func IsPresent(endpointHash string) bool {
	if endpointHash == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	n, err := common.RDB.Exists(ctx, redisOnlinePfx+endpointHash).Result()
	return err == nil && n > 0
}

// MarkPushSubsDirty tells the sender replicas to reload the subscription cache.
func MarkPushSubsDirty() {
	if !Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = common.RDB.Publish(ctx, redisSubsDirtyChan, 1).Err()
}

// SnapshotLoad returns the previous per-model snapshot map (model -> packed json).
func SnapshotLoad() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return common.RDB.HGetAll(ctx, redisSnapshotKey).Result()
}

func SnapshotSet(model string, packed string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = common.RDB.HSet(ctx, redisSnapshotKey, model, packed).Err()
}

func SnapshotSetAll(fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return common.RDB.HSet(ctx, redisSnapshotKey, fields).Err()
}

func SnapshotDelete(model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = common.RDB.HDel(ctx, redisSnapshotKey, model).Err()
}

// RecentEvents returns ring events newer than sinceTs matching any of the
// given topics (empty topics = all).
func RecentEvents(sinceTs int64, topics []string) []Event {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := common.RDB.LRange(ctx, redisRecentKey, 0, -1).Result()
	if err != nil {
		return nil
	}
	want := make(map[string]struct{}, len(topics))
	wildcards := make([]string, 0)
	for _, t := range topics {
		if strings.Contains(t, "*") {
			wildcards = append(wildcards, t)
		} else {
			want[t] = struct{}{}
		}
	}
	out := make([]Event, 0, len(raw))
	for _, item := range raw {
		var evt Event
		if err := common.UnmarshalJsonStr(item, &evt); err != nil {
			continue
		}
		if evt.Ts < sinceTs {
			continue
		}
		if len(want) > 0 || len(wildcards) > 0 {
			matched := false
			for _, t := range evt.Topics {
				if _, ok := want[t]; ok {
					matched = true
					break
				}
			}
			for _, w := range wildcards {
				if matched {
					break
				}
				for _, t := range evt.Topics {
					if TopicMatches(w, t) {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, evt)
	}
	return out
}

// StartDirtySubscriber invokes cb for every dirty ping. Runs until process exit.
func StartDirtySubscriber(cb func(reason string)) {
	go subscribeLoop(redisDirtyChannel, func(payload string) { cb(payload) })
}

// StartEventSubscriber invokes cb for every published event. Runs until process exit.
func StartEventSubscriber(cb func(evt Event)) {
	go subscribeLoop(redisEventsChannel, func(payload string) {
		var evt Event
		if err := common.UnmarshalJsonStr(payload, &evt); err != nil {
			common.SysError("notify: bad event payload: " + err.Error())
			return
		}
		cb(evt)
	})
}

// StartSubsDirtySubscriber invokes cb whenever push subscriptions changed.
func StartSubsDirtySubscriber(cb func()) {
	go subscribeLoop(redisSubsDirtyChan, func(string) { cb() })
}

func subscribeLoop(channel string, handle func(payload string)) {
	for {
		sub := common.RDB.Subscribe(context.Background(), channel)
		ch := sub.Channel()
		for msg := range ch {
			handle(msg.Payload)
		}
		_ = sub.Close()
		common.SysError("notify: redis subscription lost on " + channel + ", reconnecting")
		time.Sleep(3 * time.Second)
	}
}
