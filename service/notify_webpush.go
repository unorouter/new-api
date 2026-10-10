package service

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/notify"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const (
	webPushTTLSeconds     = 3600
	webPushMaxFailures    = 5
	webPushWorkerCount    = 8
	webPushSubPruneMaxAge = 90 * 24 * time.Hour
)

var (
	webPushQueue    = make(chan notify.Event, 256)
	webPushSubs     []model.PushSubscription
	webPushSubsLock sync.RWMutex
)

// EnqueueWebPush hands an event to the web push sender. Non-blocking.
func EnqueueWebPush(evt notify.Event) {
	select {
	case webPushQueue <- evt:
	default:
		common.SysError("notify: web push queue full, dropping event " + evt.Id)
	}
}

// StartWebPushSender delivers events to browser push subscriptions.
// Master node only; a SETNX per event id keeps rolling deploys single-send.
func StartWebPushSender() {
	if !notify.Enabled() || !common.IsMasterNode {
		return
	}
	if !notify.WebPushEnabled() {
		common.SysLog("notify: web push disabled (VAPID env vars not set)")
		return
	}
	reloadWebPushSubs()
	notify.StartSubsDirtySubscriber(reloadWebPushSubs)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			reloadWebPushSubs()
		}
	}()
	go func() {
		pruneTicker := time.NewTicker(6 * time.Hour)
		defer pruneTicker.Stop()
		for range pruneTicker.C {
			if n, err := model.PrunePushSubscriptions(webPushSubPruneMaxAge); err == nil && n > 0 {
				common.SysLog(fmt.Sprintf("notify: pruned %d stale push subscriptions", n))
			}
		}
	}()
	go func() {
		for evt := range webPushQueue {
			if !notify.SentAcquire(evt.Id) {
				continue
			}
			sendWebPushEvent(evt)
		}
	}()
	common.SysLog("notify: web push sender started on master node")
}

func reloadWebPushSubs() {
	subs, err := model.GetAllPushSubscriptions()
	if err != nil {
		common.SysError("notify: load push subscriptions failed: " + err.Error())
		return
	}
	webPushSubsLock.Lock()
	webPushSubs = subs
	webPushSubsLock.Unlock()
}

// webPushTexts holds the title and body template of each push event; subscribers
// read them in English whatever their locale.
var webPushTexts = map[string][2]string{
	"model_online":            {"{{model}} is back online", "Available again at {{ratio}}x"},
	"model_offline":           {"{{model}} is unavailable", "All providers are currently down"},
	"model_price_change":      {"{{model}} price changed", "Now {{ratio}}x, was {{prev_ratio}}x"},
	"model_added":             {"New model: {{model}}", "Now available at {{ratio}}x"},
	"model_removed":           {"{{model}} was removed", "No longer offered"},
	"bulk.model_online":       {"{{count}} models are back online", "Including {{models}}"},
	"bulk.model_offline":      {"{{count}} models went unavailable", "Including {{models}}"},
	"bulk.model_added":        {"{{count}} new models added", "Including {{models}}"},
	"bulk.model_removed":      {"{{count}} models were removed", "Including {{models}}"},
	"bulk.model_price_change": {"{{count}} model prices changed", "Including {{models}}"},
}

func webPushText(evt notify.Event) (string, string) {
	args := map[string]any{"model": evt.Data.Model}
	if evt.Data.CheapestRatio != nil {
		args["ratio"] = fmt.Sprintf("%.3g", *evt.Data.CheapestRatio)
	}
	if evt.Data.PrevCheapestRatio != nil {
		args["prev_ratio"] = fmt.Sprintf("%.3g", *evt.Data.PrevCheapestRatio)
	}
	key := evt.Type
	// A digest names no single model; it keys off the collapsed event instead.
	if evt.Type == notify.EventModelBulkChange {
		args["count"] = evt.Data.BulkCount
		args["models"] = strings.Join(evt.Data.Models, ", ")
		key = "bulk." + evt.Data.BulkEvent
	}
	text, ok := webPushTexts[key]
	if !ok {
		return key, ""
	}
	return common.NewMessage(text[0], args).Error(), common.NewMessage(text[1], args).Error()
}

func webPushURL(evt notify.Event) string {
	// Digests have no single model: land on the catalog instead of "?model=".
	if evt.Data.Model == "" {
		return "https://unorouter.com/en/models"
	}
	return "https://unorouter.com/en/models?model=" + url.QueryEscape(evt.Data.Model)
}

func sendWebPushEvent(evt notify.Event) {
	webPushSubsLock.RLock()
	subs := make([]model.PushSubscription, len(webPushSubs))
	copy(subs, webPushSubs)
	webPushSubsLock.RUnlock()
	if len(subs) == 0 {
		return
	}

	topicSet := make(map[string]struct{}, len(evt.Topics))
	for _, t := range evt.Topics {
		topicSet[t] = struct{}{}
	}

	targets := make([]model.PushSubscription, 0)
	for _, sub := range subs {
		var topics []string
		if err := common.UnmarshalJsonStr(sub.Topics, &topics); err != nil {
			continue
		}
		matched := false
		for _, t := range topics {
			if _, ok := topicSet[t]; ok {
				matched = true
				break
			}
			if strings.Contains(t, "*") {
				for _, et := range evt.Topics {
					if notify.TopicMatches(t, et) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
		}
		if !matched {
			continue
		}
		// Presence gate: device has a live WS tab, it gets the in-app toast.
		if notify.IsPresent(sub.EndpointHash) {
			continue
		}
		targets = append(targets, sub)
	}
	if len(targets) == 0 {
		return
	}

	urgency := webpush.UrgencyNormal
	if evt.Type == notify.EventModelOnline || evt.Type == notify.EventModelAdded {
		urgency = webpush.UrgencyHigh
	}

	jobs := make(chan model.PushSubscription)
	var wg sync.WaitGroup
	for i := 0; i < webPushWorkerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sub := range jobs {
				sendWebPushToSub(evt, sub, urgency)
			}
		}()
	}
	for _, sub := range targets {
		jobs <- sub
	}
	close(jobs)
	wg.Wait()
	subject := evt.Data.Model
	if evt.Type == notify.EventModelBulkChange {
		subject = fmt.Sprintf("%d models (%s)", evt.Data.BulkCount, evt.Data.BulkEvent)
	}
	common.SysLog(fmt.Sprintf("notify: web push %s for %s sent to %d subscriptions", evt.Type, subject, len(targets)))
}

func sendWebPushToSub(evt notify.Event, sub model.PushSubscription, urgency webpush.Urgency) {
	title, body := webPushText(evt)
	payload, err := common.Marshal(map[string]interface{}{
		"title": title,
		"body":  body,
		"url":   webPushURL(evt),
		"event": evt,
	})
	if err != nil {
		return
	}
	resp, err := webpush.SendNotification(payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		Subscriber:      notify.VapidSubject(),
		VAPIDPublicKey:  notify.VapidPublicKey(),
		VAPIDPrivateKey: notify.VapidPrivateKey(),
		TTL:             webPushTTLSeconds,
		Urgency:         urgency,
		Topic:           evt.Type,
	})
	if err != nil {
		model.BumpPushSubscriptionFailure(sub.Id, webPushMaxFailures)
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		_ = model.DeletePushSubscriptionByEndpointHash(sub.EndpointHash)
	case resp.StatusCode >= 400:
		model.BumpPushSubscriptionFailure(sub.Id, webPushMaxFailures)
	default:
		model.ResetPushSubscriptionFailure(sub.Id)
	}
}
