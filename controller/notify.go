package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/notify"

	"github.com/gin-gonic/gin"
)

const notifyMaxSubTopics = 100

// GetNotifyVapidKey returns the public VAPID key browsers use to subscribe.
func GetNotifyVapidKey(c *gin.Context) {
	if !notify.WebPushEnabled() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "web push disabled"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"key": notify.VapidPublicKey()}})
}

type notifySubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Topics []string `json:"topics"`
	Locale string   `json:"locale"`
}

func notifyEndpointHash(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

func validWebPushKey(value string, minBytes int) bool {
	if value == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return false
	}
	return len(decoded) >= minBytes
}

// SubscribeNotifyPush upserts an anonymous device-scoped push subscription.
func SubscribeNotifyPush(c *gin.Context) {
	if !notify.WebPushEnabled() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "web push disabled"})
		return
	}
	var req notifySubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid request"})
		return
	}
	if !strings.HasPrefix(req.Endpoint, "https://") || len(req.Endpoint) > 2048 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid endpoint"})
		return
	}
	// webpush-go fails silently on malformed keys, so reject them here:
	// p256dh is a 65-byte uncompressed EC point, auth is a 16-byte secret.
	if !validWebPushKey(req.Keys.P256dh, 65) || !validWebPushKey(req.Keys.Auth, 16) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid subscription keys"})
		return
	}
	topics := notify.SanitizeTopics(req.Topics, notifyMaxSubTopics)
	if len(topics) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "no valid topics"})
		return
	}
	topicsJSON, err := common.Marshal(topics)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid topics"})
		return
	}
	locale := req.Locale
	if len(locale) > 16 {
		locale = locale[:16]
	}
	ua := c.GetHeader("User-Agent")
	if len(ua) > 255 {
		ua = ua[:255]
	}
	sub := &model.PushSubscription{
		Endpoint:     req.Endpoint,
		EndpointHash: notifyEndpointHash(req.Endpoint),
		P256dh:       req.Keys.P256dh,
		Auth:         req.Keys.Auth,
		Topics:       string(topicsJSON),
		Locale:       locale,
		UserAgent:    ua,
	}
	if err := model.UpsertPushSubscription(sub); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to store subscription"})
		return
	}
	notify.MarkPushSubsDirty()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"endpoint_hash": sub.EndpointHash, "topics": topics}})
}

type notifyUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

// UnsubscribeNotifyPush removes a push subscription by endpoint.
func UnsubscribeNotifyPush(c *gin.Context) {
	var req notifyUnsubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Endpoint == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid request"})
		return
	}
	if err := model.DeletePushSubscriptionByEndpointHash(notifyEndpointHash(req.Endpoint)); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to delete subscription"})
		return
	}
	notify.MarkPushSubsDirty()
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// GetNotifyEvents returns recent events from the ring for reconnect catch-up.
func GetNotifyEvents(c *gin.Context) {
	if !notify.Enabled() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "notifications disabled"})
		return
	}
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	var topics []string
	if raw := c.Query("topics"); raw != "" {
		topics = notify.SanitizeTopics(strings.Split(raw, ","), notifyMaxSubTopics)
	}
	events := notify.RecentEvents(since, topics)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": events})
}

// GetRoomHistory returns a multiplayer room's stored transcript.
//
// The room id is the only credential: it is 143 bits of client-generated
// randomness, so knowing it is what proves membership, exactly as it does for
// the join link itself. History is served over HTTP rather than the socket
// because a long room exceeds both the 4096-byte frame limit and the 64-frame
// outbound buffer, and overflowing that buffer drops the connection.
func GetRoomHistory(c *gin.Context) {
	if !notify.Enabled() {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "notifications disabled"})
		return
	}
	topic := notify.RoomTopicPrefix + c.Query("room")
	if !notify.ValidRoomTopic(topic) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid room"})
		return
	}
	meta, msgs := notify.ReadRoomHistory(topic)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"meta": meta, "messages": msgs},
	})
}
