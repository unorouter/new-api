package dto

type Notify struct {
	Type    string        `json:"type"`
	Title   string        `json:"title"`
	Content string        `json:"content"`
	Values  []interface{} `json:"values"`
}

const ContentValueParam = "{{value}}"

const (
	NotifyTypeQuotaExceed   = "quota_exceed"
	NotifyTypeChannelUpdate = "channel_update"
	NotifyTypeChannelTest   = "channel_test"
)

func NewNotify(t string, title string, content string, values []interface{}) Notify {
	return Notify{
		Type:    t,
		Title:   title,
		Content: content,
		Values:  values,
	}
}

// NotifyVapidKeyData is the data field for GET /api/notify/vapid.
type NotifyVapidKeyData struct {
	Key string `json:"key" description:"Base64url-encoded public VAPID key"`
}

// NotifyVapidKeyResponse is the response for GET /api/notify/vapid.
type NotifyVapidKeyResponse struct {
	Success bool               `json:"success"`
	Message string             `json:"message,omitempty"`
	Data    NotifyVapidKeyData `json:"data,omitempty"`
}

// NotifySubscriptionKeys carries the browser-supplied web push encryption keys.
type NotifySubscriptionKeys struct {
	P256dh string `json:"p256dh" description:"Base64url-encoded 65-byte uncompressed EC public key"`
	Auth   string `json:"auth" description:"Base64url-encoded 16-byte auth secret"`
}

// NotifySubscriptionRequest is the body for POST /api/notify/subscription.
type NotifySubscriptionRequest struct {
	Endpoint string                 `json:"endpoint" description:"Push service endpoint URL (https only, max 2048 chars)"`
	Keys     NotifySubscriptionKeys `json:"keys"`
	Topics   []string               `json:"topics" description:"Topics to subscribe to; concrete names or single-wildcard patterns"`
	Locale   string                 `json:"locale,omitempty" description:"Preferred locale for push payload text (max 16 chars)"`
}

// NotifySubscriptionData is the data field for POST /api/notify/subscription.
type NotifySubscriptionData struct {
	EndpointHash string   `json:"endpoint_hash"`
	Topics       []string `json:"topics"`
}

// NotifySubscriptionResponse is the response for POST /api/notify/subscription.
type NotifySubscriptionResponse struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message,omitempty"`
	Data    NotifySubscriptionData `json:"data,omitempty"`
}

// NotifyUnsubscribeRequest is the body for DELETE /api/notify/subscription.
type NotifyUnsubscribeRequest struct {
	Endpoint string `json:"endpoint" description:"Push service endpoint URL to remove"`
}

// NotifyUnsubscribeResponse is the response for DELETE /api/notify/subscription.
type NotifyUnsubscribeResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// NotifyEventData mirrors notify.EventData for the OpenAPI spec; the notify
// package imports dto, so the types cannot be shared directly.
type NotifyEventData struct {
	Model             string   `json:"model"`
	Free              bool     `json:"free"`
	Online            *bool    `json:"online,omitempty"`
	CheapestRatio     *float64 `json:"cheapest_ratio,omitempty"`
	PrevCheapestRatio *float64 `json:"prev_cheapest_ratio,omitempty"`
	CheapestGroup     string   `json:"cheapest_group,omitempty"`
}

// NotifyEvent mirrors notify.Event for the OpenAPI spec.
type NotifyEvent struct {
	Id     string          `json:"id"`
	Type   string          `json:"type"`
	Ts     int64           `json:"ts"`
	Topics []string        `json:"topics"`
	Data   NotifyEventData `json:"data"`
}

// NotifyEventsResponse is the response for GET /api/notify/events.
type NotifyEventsResponse struct {
	Success bool          `json:"success"`
	Message string        `json:"message,omitempty"`
	Data    []NotifyEvent `json:"data,omitempty"`
}
