package dto

type DigistorePayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}

type DigistorePayData struct {
	PayLink string `json:"pay_link"`
}

// SubscriptionDigistorePayRequest is the request body for POST /api/subscription/digistore/pay.
type SubscriptionDigistorePayRequest struct {
	PlanId int `json:"plan_id"`
}
