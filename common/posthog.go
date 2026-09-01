package common

import (
	"bytes"
	"fmt"
	"net/http"
	"time"
)

// Minimal fire-and-forget PostHog capture over raw HTTP so no Go dependency is
// added. No-ops unless POSTHOG_KEY is set. Host defaults to the EU ingest
// endpoint (matches the frontend's eu.posthog.com project); override with
// POSTHOG_HOST. distinctId MUST match the frontend identify id (the user id as
// a string) so server + client events stitch to the same person. Lives in
// common (not service) so model-layer callers can use it without an import
// cycle.
func CapturePostHog(distinctId string, event string, properties map[string]interface{}) {
	key := GetEnvOrDefaultString("POSTHOG_KEY", "")
	if key == "" || distinctId == "" {
		return
	}
	host := GetEnvOrDefaultString("POSTHOG_HOST", "https://eu.i.posthog.com")

	payload := map[string]interface{}{
		"api_key":     key,
		"event":       event,
		"distinct_id": distinctId,
		"properties":  properties,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}

	go func() {
		body, err := Marshal(payload)
		if err != nil {
			return
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(host+"/i/v0/e/", "application/json", bytes.NewReader(body))
		if err != nil {
			SysLog("posthog capture failed: " + err.Error())
			return
		}
		defer resp.Body.Close()
	}()
}

// CapturePaymentSuccess fires billing_topup_completed for a credited topup.
func CapturePaymentSuccess(userId int, money float64, provider string, topUpId int) {
	CapturePostHog(fmt.Sprintf("%d", userId), "billing_topup_completed", map[string]interface{}{
		"amount":   money,
		"provider": provider,
		"topup_id": topUpId,
		"user_id":  userId,
	})
}

// CaptureSubscriptionSuccess fires billing_subscription_completed for an
// activated subscription order.
func CaptureSubscriptionSuccess(userId int, money float64, plan string, method string, orderId int) {
	CapturePostHog(fmt.Sprintf("%d", userId), "billing_subscription_completed", map[string]interface{}{
		"amount":   money,
		"plan":     plan,
		"method":   method,
		"order_id": orderId,
		"user_id":  userId,
	})
}
