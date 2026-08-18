package dto

type DeloPayPayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}

type DeloPayPayData struct {
	PayLink string `json:"pay_link"`
}

// DeloPayCreatePaymentRequest is the body of POST /payments. Amount is in minor
// units (1000 = 10.00 USD).
type DeloPayCreatePaymentRequest struct {
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	ProfileId   string            `json:"profile_id"`
	PaymentLink bool              `json:"payment_link"`
	Description string            `json:"description,omitempty"`
	ReturnURL   string            `json:"return_url,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	TestMode    bool              `json:"test_mode,omitempty"`
	// Groups a user's payments under one customer in DeloPay's dashboard and
	// prefills the hosted checkout. CustomerId is stable per user so repeat
	// purchases attach to the same record.
	CustomerId string `json:"customer_id,omitempty"`
	Email      string `json:"email,omitempty"`
	Name       string `json:"name,omitempty"`
}

type DeloPayPaymentResponse struct {
	PaymentId   string `json:"payment_id"`
	MerchantId  string `json:"merchant_id"`
	Status      string `json:"status"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Connector   string `json:"connector"`
	PaymentLink struct {
		Link          string `json:"link"`
		PaymentLinkId string `json:"payment_link_id"`
	} `json:"payment_link"`
	NextAction struct {
		Type          string `json:"type"`
		RedirectToURL string `json:"redirect_to_url"`
	} `json:"next_action"`
	PlatformFeeAmount int64 `json:"platform_fee_amount"`
}

// DeloPayWebhookEvent carries the affected object under content.object rather
// than at the top level.
type DeloPayWebhookEvent struct {
	MerchantId string `json:"merchant_id"`
	EventId    string `json:"event_id"`
	EventType  string `json:"event_type"`
	Timestamp  string `json:"timestamp"`
	Content    struct {
		Type   string `json:"type"`
		Object struct {
			PaymentId         string            `json:"payment_id"`
			Status            string            `json:"status"`
			Amount            int64             `json:"amount"`
			Currency          string            `json:"currency"`
			Connector         string            `json:"connector"`
			Metadata          map[string]string `json:"metadata"`
			PlatformFeeAmount int64             `json:"platform_fee_amount"`
		} `json:"object"`
	} `json:"content"`
}
