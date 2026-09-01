package dto

type CreemProduct struct {
	ProductId string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Quota     int64   `json:"quota"`
}

type CreemWebhookEvent struct {
	Id        string `json:"id"`
	EventType string `json:"eventType"`
	CreatedAt int64  `json:"created_at"`
	Object    struct {
		Id        string `json:"id"`
		Object    string `json:"object"`
		RequestId string `json:"request_id"`
		Order     struct {
			Object      string `json:"object"`
			Id          string `json:"id"`
			Customer    string `json:"customer"`
			Product     string `json:"product"`
			Amount      int    `json:"amount"`
			Currency    string `json:"currency"`
			SubTotal    int    `json:"sub_total"`
			TaxAmount   int    `json:"tax_amount"`
			AmountDue   int    `json:"amount_due"`
			AmountPaid  int    `json:"amount_paid"`
			Status      string `json:"status"`
			Type        string `json:"type"`
			Transaction string `json:"transaction"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
			Mode        string `json:"mode"`
		} `json:"order"`
		Product struct {
			Id                string  `json:"id"`
			Object            string  `json:"object"`
			Name              string  `json:"name"`
			Description       string  `json:"description"`
			Price             int     `json:"price"`
			Currency          string  `json:"currency"`
			BillingType       string  `json:"billing_type"`
			BillingPeriod     string  `json:"billing_period"`
			Status            string  `json:"status"`
			TaxMode           string  `json:"tax_mode"`
			TaxCategory       string  `json:"tax_category"`
			DefaultSuccessUrl *string `json:"default_success_url"`
			CreatedAt         string  `json:"created_at"`
			UpdatedAt         string  `json:"updated_at"`
			Mode              string  `json:"mode"`
		} `json:"product"`
		Units    int `json:"units"`
		Customer struct {
			Id        string `json:"id"`
			Object    string `json:"object"`
			Email     string `json:"email"`
			Name      string `json:"name"`
			Country   string `json:"country"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			Mode      string `json:"mode"`
		} `json:"customer"`
		Status   string            `json:"status"`
		Metadata map[string]string `json:"metadata"`
		Mode     string            `json:"mode"`
		// subscription.* events: object is a subscription, these identify the
		// renewal charge (last_transaction_* change on every successful renewal).
		LastTransactionId   string `json:"last_transaction_id"`
		LastTransactionDate string `json:"last_transaction_date"`
		LastTransaction     struct {
			Id         string `json:"id"`
			Amount     int    `json:"amount"`
			AmountPaid int    `json:"amount_paid"`
			Currency   string `json:"currency"`
			Status     string `json:"status"`
			Order      string `json:"order"`
		} `json:"last_transaction"`
	} `json:"object"`
}

// ReferenceID returns the checkout reference id (our order trade_no) from the
// event metadata, tolerating Creem's camelCase/snake_case drift.
func (e *CreemWebhookEvent) ReferenceID() string {
	if e == nil || e.Object.Metadata == nil {
		return ""
	}
	if v := e.Object.Metadata["referenceId"]; v != "" {
		return v
	}
	return e.Object.Metadata["reference_id"]
}

type CreemCustomer struct {
	Email string `json:"email"`
}

type CreemCheckoutRequest struct {
	ProductId string `json:"product_id"`
	RequestId string `json:"request_id"`
	// CustomPrice overrides the product's unit price for this checkout only,
	// in CENTS (Creem's minimum is 100, maximum 99999999, one-time products
	// only). Omitted when zero so a preset tile still charges the product's
	// configured price.
	CustomPrice int               `json:"custom_price,omitempty"`
	Customer    *CreemCustomer    `json:"customer,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type CreemCheckoutResponse struct {
	CheckoutUrl string `json:"checkout_url"`
	Id          string `json:"id"`
}
