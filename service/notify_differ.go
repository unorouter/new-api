package service

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/notify"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// notifyModelState is the packed per-model snapshot stored in the Redis hash.
type notifyModelState struct {
	Online bool    `json:"o"`
	Ratio  float64 `json:"r"`
	Group  string  `json:"g"`
}

type notifyPending struct {
	deadline time.Time
	state    notifyModelState
	prev     notifyModelState
}

const notifyDebounce = 5 * time.Second

// StartNotifyDiffer runs the availability/price differ on the master node.
// It turns low-level dirty pings into deduplicated, flap-absorbed,
// user-facing events.
func StartNotifyDiffer() {
	if !notify.Enabled() || !common.IsMasterNode {
		return
	}
	dirtyCh := make(chan struct{}, 1)
	notify.StartDirtySubscriber(func(string) {
		select {
		case dirtyCh <- struct{}{}:
		default:
		}
	})
	go func() {
		reconcile := time.NewTicker(60 * time.Second)
		pendingTick := time.NewTicker(30 * time.Second)
		defer reconcile.Stop()
		defer pendingTick.Stop()
		pendings := make(map[string]notifyPending)
		for {
			select {
			case <-dirtyCh:
				time.Sleep(notifyDebounce)
				notifyRecompute(pendings)
			case <-reconcile.C:
				notifyRecompute(pendings)
			case <-pendingTick.C:
				if len(pendings) > 0 {
					notifyRecompute(pendings)
				}
			}
		}
	}()
	common.SysLog("notify: differ started on master node")
}

func notifyComputeStates() (map[string]notifyModelState, error) {
	abilities, err := model.GetAllAbilityWithChannels()
	if err != nil {
		return nil, err
	}
	// Only publicly usable groups count toward online/price state: parity with
	// GET /api/pricing visibility, which hides models whose enabled channels
	// all live in non-public groups.
	usable := GetUserUsableGroups("")
	states := make(map[string]notifyModelState, 512)
	for _, a := range abilities {
		// Long-context aliases are hidden from pricing; keep them out of events too.
		if strings.HasSuffix(a.Model, "[1m]") {
			continue
		}
		s, seen := states[a.Model]
		if !seen {
			s = notifyModelState{}
		}
		if a.Enabled {
			if _, ok := usable[a.Group]; ok || a.Group == "all" {
				r := ratio_setting.GetGroupRatio(a.Group)
				if !s.Online || r < s.Ratio {
					s.Ratio = r
					s.Group = a.Group
				}
				s.Online = true
			}
		}
		states[a.Model] = s
	}
	return states, nil
}

func notifyPackState(s notifyModelState) string {
	b, err := common.Marshal(&s)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func notifyMakeEvent(eventType string, m string, data notify.EventData) *notify.Event {
	now := time.Now().Unix()
	return &notify.Event{
		Id:     fmt.Sprintf("%d-%s", now, common.GetRandomString(8)),
		Type:   eventType,
		Ts:     now,
		Topics: notify.TopicsFor(m, eventType),
		Data:   data,
	}
}

// notifyBatch collects one recompute cycle's events so a mass transition can be
// collapsed into a digest instead of one push per model.
type notifyBatch struct {
	byType map[string][]notify.EventData
}

func newNotifyBatch() *notifyBatch {
	return &notifyBatch{byType: make(map[string][]notify.EventData)}
}

// notifyEmit records the event (cooldown-gated) and always persists the new
// snapshot state so a suppressed event is not retried forever. Publishing is
// deferred to flush so the cycle's total per event type is known first.
func (b *notifyBatch) notifyEmit(eventType string, m string, data notify.EventData, newState *notifyModelState) {
	if newState != nil {
		notify.SnapshotSet(m, notifyPackState(*newState))
	} else {
		notify.SnapshotDelete(m)
	}
	if !notify.CooldownAcquire(m, eventType) {
		return
	}
	b.byType[eventType] = append(b.byType[eventType], data)
}

// flush publishes the cycle. Under the burst threshold every model gets its own
// event as before; at or above it, the whole set becomes ONE digest so a bulk
// operational change cannot fan out into hundreds of notifications per client.
func (b *notifyBatch) flush() {
	threshold := notify.BurstThreshold()
	for eventType, list := range b.byType {
		if len(list) < threshold {
			for _, data := range list {
				publishEvent(notifyMakeEvent(eventType, data.Model, data))
			}
			continue
		}
		publishEvent(notifyMakeBulkEvent(eventType, list))
		common.SysLog(fmt.Sprintf(
			"notify: collapsed %d %s events into one digest", len(list), eventType))
	}
}

func publishEvent(evt *notify.Event) {
	if notify.Publish(evt) {
		EnqueueWebPush(*evt)
	}
}

// notifyMakeBulkEvent builds the digest envelope. Models are sorted so the
// sample is stable, and only a handful ride along (the payload also goes out as
// a web push, which has a hard size limit).
func notifyMakeBulkEvent(eventType string, list []notify.EventData) *notify.Event {
	const sampleSize = 5
	models := make([]string, 0, len(list))
	freeCount := 0
	for _, d := range list {
		models = append(models, d.Model)
		if d.Free {
			freeCount++
		}
	}
	sort.Strings(models)
	sample := models
	if len(sample) > sampleSize {
		sample = sample[:sampleSize]
	}
	now := time.Now().Unix()
	return &notify.Event{
		Id:     fmt.Sprintf("%d-%s", now, common.GetRandomString(8)),
		Type:   notify.EventModelBulkChange,
		Ts:     now,
		Topics: notify.BulkTopicsFor(eventType, freeCount > 0),
		Data: notify.EventData{
			BulkEvent: eventType,
			BulkCount: len(list),
			BulkFree:  freeCount,
			Models:    sample,
			Free:      freeCount == len(list),
		},
	}
}

func notifyRecompute(pendings map[string]notifyPending) {
	cur, err := notifyComputeStates()
	if err != nil {
		common.SysError("notify: compute states failed: " + err.Error())
		return
	}
	prevRaw, err := notify.SnapshotLoad()
	if err != nil {
		common.SysError("notify: snapshot load failed: " + err.Error())
		return
	}
	if len(prevRaw) == 0 {
		// First run ever: seed silently so deploys never flood 2788 events.
		fields := make(map[string]interface{}, len(cur))
		for m, s := range cur {
			fields[m] = notifyPackState(s)
		}
		if err := notify.SnapshotSetAll(fields); err != nil {
			common.SysError("notify: snapshot seed failed: " + err.Error())
		} else {
			common.SysLog(fmt.Sprintf("notify: seeded snapshot with %d models", len(fields)))
		}
		return
	}

	prev := make(map[string]notifyModelState, len(prevRaw))
	for m, raw := range prevRaw {
		var s notifyModelState
		if err := common.UnmarshalJsonStr(raw, &s); err != nil {
			continue
		}
		prev[m] = s
	}

	batch := newNotifyBatch()
	defer batch.flush()

	now := time.Now()
	for m, c := range cur {
		p, existed := prev[m]
		if !existed {
			online := c.Online
			data := notify.EventData{Model: m, Free: notify.IsFreeModel(m), Online: &online}
			if c.Online {
				r := c.Ratio
				data.CheapestRatio = &r
				data.CheapestGroup = c.Group
			}
			cs := c
			batch.notifyEmit(notify.EventModelAdded, m, data, &cs)
			continue
		}
		if c.Online && !p.Online {
			delete(pendings, m+"|"+notify.EventModelOffline)
			online := true
			r := c.Ratio
			cs := c
			batch.notifyEmit(notify.EventModelOnline, m, notify.EventData{
				Model: m, Free: notify.IsFreeModel(m), Online: &online,
				CheapestRatio: &r, CheapestGroup: c.Group,
			}, &cs)
			continue
		}
		if !c.Online && p.Online {
			key := m + "|" + notify.EventModelOffline
			if _, held := pendings[key]; !held {
				pendings[key] = notifyPending{
					deadline: now.Add(time.Duration(notify.OfflineGraceSeconds()) * time.Second),
					state:    c,
					prev:     p,
				}
			}
			continue
		}
		if c.Online && p.Online && math.Abs(c.Ratio-p.Ratio) > 1e-9 {
			key := m + "|" + notify.EventModelPriceChange
			if _, held := pendings[key]; !held {
				pendings[key] = notifyPending{
					deadline: now.Add(time.Duration(notify.PriceGraceSeconds()) * time.Second),
					state:    c,
					prev:     p,
				}
			}
			continue
		}
		// No user-facing transition; keep hash in sync on silent drift
		// (cheapest group swap at equal ratio, offline ratio bookkeeping).
		if c != p {
			notify.SnapshotSet(m, notifyPackState(c))
		}
	}

	for m := range prev {
		if _, ok := cur[m]; !ok {
			batch.notifyEmit(notify.EventModelRemoved, m, notify.EventData{Model: m, Free: notify.IsFreeModel(m)}, nil)
			delete(pendings, m+"|"+notify.EventModelOffline)
			delete(pendings, m+"|"+notify.EventModelPriceChange)
		}
	}

	// Resolve pending grace-period transitions against the fresh state.
	for key, pending := range pendings {
		idx := strings.LastIndex(key, "|")
		m, eventType := key[:idx], key[idx+1:]
		c, stillExists := cur[m]
		if !stillExists {
			delete(pendings, key)
			continue
		}
		switch eventType {
		case notify.EventModelOffline:
			if c.Online {
				delete(pendings, key)
				continue
			}
			if now.After(pending.deadline) {
				online := false
				pr := pending.prev.Ratio
				cs := c
				batch.notifyEmit(notify.EventModelOffline, m, notify.EventData{
					Model: m, Free: notify.IsFreeModel(m), Online: &online,
					PrevCheapestRatio: &pr,
				}, &cs)
				delete(pendings, key)
			}
		case notify.EventModelPriceChange:
			if !c.Online || math.Abs(c.Ratio-pending.prev.Ratio) < 1e-9 {
				delete(pendings, key)
				continue
			}
			if now.After(pending.deadline) {
				online := true
				r := c.Ratio
				pr := pending.prev.Ratio
				cs := c
				batch.notifyEmit(notify.EventModelPriceChange, m, notify.EventData{
					Model: m, Free: notify.IsFreeModel(m), Online: &online,
					CheapestRatio: &r, PrevCheapestRatio: &pr, CheapestGroup: c.Group,
				}, &cs)
				delete(pendings, key)
			}
		default:
			delete(pendings, key)
		}
	}
}
