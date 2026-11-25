package dto

import (
	"bytes"
	"strings"
)

type FreemiusPayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}

type FreemiusPayData struct {
	PayLink string `json:"pay_link"`
}

// SubscriptionFreemiusPayRequest is the request body for POST /api/subscription/freemius/pay.
type SubscriptionFreemiusPayRequest struct {
	PlanId int `json:"plan_id"`
}

// FreemiusID accepts the ids Freemius sends both as JSON strings and as numbers.
type FreemiusID string

func (f *FreemiusID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	*f = FreemiusID(strings.Trim(string(b), `"`))
	return nil
}

type FreemiusPayment struct {
	Id             FreemiusID `json:"id"`
	UserId         FreemiusID `json:"user_id"`
	PlanId         FreemiusID `json:"plan_id"`
	PricingId      FreemiusID `json:"pricing_id"`
	LicenseId      FreemiusID `json:"license_id"`
	SubscriptionId FreemiusID `json:"subscription_id"`
	BoundPaymentId FreemiusID `json:"bound_payment_id"`
	Gross          float64    `json:"gross"`
	Currency       string     `json:"currency"`
	IsRenewal      bool       `json:"is_renewal"`
	Type           string     `json:"type"`
}

type FreemiusSubscription struct {
	Id        FreemiusID `json:"id"`
	UserId    FreemiusID `json:"user_id"`
	PlanId    FreemiusID `json:"plan_id"`
	PricingId FreemiusID `json:"pricing_id"`
}

type FreemiusUser struct {
	Id    FreemiusID `json:"id"`
	Email string     `json:"email"`
}

// FreemiusWebhookEvent is the event entity Freemius posts; the affected
// resources sit under objects.
type FreemiusWebhookEvent struct {
	Id       FreemiusID `json:"id"`
	Type     string     `json:"type"`
	PluginId FreemiusID `json:"plugin_id"`
	UserId   FreemiusID `json:"user_id"`
	Objects  struct {
		User         *FreemiusUser         `json:"user"`
		Payment      *FreemiusPayment      `json:"payment"`
		Subscription *FreemiusSubscription `json:"subscription"`
	} `json:"objects"`
}
