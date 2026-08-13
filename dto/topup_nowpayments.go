package dto

type NowPaymentsPayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
}

type NowPaymentsPayData struct {
	PayLink string `json:"pay_link"`
}

type NowPaymentsInvoiceRequest struct {
	PriceAmount      float64 `json:"price_amount"`
	PriceCurrency    string  `json:"price_currency"`
	OrderId          string  `json:"order_id"`
	OrderDescription string  `json:"order_description"`
	IpnCallbackURL   string  `json:"ipn_callback_url"`
	SuccessURL       string  `json:"success_url"`
	CancelURL        string  `json:"cancel_url"`
	IsFixedRate      bool    `json:"is_fixed_rate"`
	IsFeePaidByUser  bool    `json:"is_fee_paid_by_user"`
}

type NowPaymentsInvoiceResponse struct {
	Id         string `json:"id"`
	InvoiceURL string `json:"invoice_url"`
	OrderId    string `json:"order_id"`
}

type NowPaymentsPlanRequest struct {
	Title            string  `json:"title"`
	IntervalDay      string  `json:"interval_day"`
	Amount           float64 `json:"amount"`
	Currency         string  `json:"currency"`
	IpnCallbackURL   string  `json:"ipn_callback_url"`
	SuccessURL       string  `json:"success_url"`
	CancelURL        string  `json:"cancel_url"`
	PartiallyPaidURL string  `json:"partially_paid_url"`
}

type NowPaymentsPlanResponse struct {
	Result struct {
		Id          string  `json:"id"`
		Title       string  `json:"title"`
		IntervalDay string  `json:"interval_day"`
		Amount      float64 `json:"amount"`
		Currency    string  `json:"currency"`
	} `json:"result"`
}

type NowPaymentsEmailSubRequest struct {
	SubscriptionPlanId string `json:"subscription_plan_id"`
	Email              string `json:"email"`
}

type NowPaymentsEmailSubResponse struct {
	Result []struct {
		Id                 string `json:"id"`
		SubscriptionPlanId string `json:"subscription_plan_id"`
		EmailAddress       string `json:"email_address"`
		Status             string `json:"status"`
	} `json:"result"`
}

type NowPaymentsWebhookEvent struct {
	PaymentId        int64   `json:"payment_id"`
	PaymentStatus    string  `json:"payment_status"`
	PayAddress       string  `json:"pay_address"`
	PriceAmount      float64 `json:"price_amount"`
	PriceCurrency    string  `json:"price_currency"`
	PayAmount        float64 `json:"pay_amount"`
	PayCurrency      string  `json:"pay_currency"`
	OrderId          string  `json:"order_id"`
	OrderDescription string  `json:"order_description"`
	ActuallyPaid     float64 `json:"actually_paid"`
	OutcomeAmount    float64 `json:"outcome_amount"`
	OutcomeCurrency  string  `json:"outcome_currency"`
	InvoiceId        int64   `json:"invoice_id"`
	SubscriptionId   int64   `json:"subscription_id"`
}
