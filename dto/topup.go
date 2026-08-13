package dto

// --- Topup response types ---

type StripePayLinkData struct {
	PayLink string `json:"pay_link"`
}

type CreemPayData struct {
	CheckoutUrl string `json:"checkout_url"`
	OrderId     string `json:"order_id"`
}

type EpayPayResponse struct {
	Params interface{} `json:"params"`
	Url    string      `json:"url"`
}

type TopUpInfoData struct {
	EnableOnlineTopup             bool                `json:"enable_online_topup"`
	EnableStripeTopup             bool                `json:"enable_stripe_topup"`
	EnableCreemTopup              bool                `json:"enable_creem_topup"`
	EnableWaffoTopup              bool                `json:"enable_waffo_topup"`
	EnableWaffoPancakeTopup       bool                `json:"enable_waffo_pancake_topup"`
	EnableNowPaymentsTopup        bool                `json:"enable_nowpayments_topup"`
	EnableDeloPayTopup            bool                `json:"enable_delopay_topup"`
	EnableRedemption              bool                `json:"enable_redemption"`
	PaymentComplianceConfirmed    bool                `json:"payment_compliance_confirmed"`
	PaymentComplianceTermsVersion string              `json:"payment_compliance_terms_version"`
	WaffoPayMethods               interface{}         `json:"waffo_pay_methods"`
	CreemProducts                 string              `json:"creem_products"`
	PayMethods                    []map[string]string `json:"pay_methods"`
	MinTopup                      int                 `json:"min_topup"`
	StripeMinTopup                int                 `json:"stripe_min_topup"`
	WaffoMinTopup                 int                 `json:"waffo_min_topup"`
	WaffoPancakeMinTopup          int                 `json:"waffo_pancake_min_topup"`
	NowPaymentsMinTopup           int                 `json:"nowpayments_min_topup"`
	DeloPayMinTopup               int                 `json:"delopay_min_topup"`
	// Processing fee added on top of the top-up amount, so the client can show
	// what the buyer actually pays instead of the credited amount.
	DeloPayFeeFixed   float64 `json:"delopay_fee_fixed"`
	DeloPayFeePercent float64 `json:"delopay_fee_percent"`
	CreemFeeFixed     float64 `json:"creem_fee_fixed"`
	CreemFeePercent   float64 `json:"creem_fee_percent"`
	// Above this amount no fee is added; 0 charges the fee on every amount.
	CreemFeeThreshold   float64         `json:"creem_fee_threshold"`
	DeloPayFeeThreshold float64         `json:"delopay_fee_threshold"`
	AmountOptions       []int           `json:"amount_options"`
	Discount            map[int]float64 `json:"discount"`
	TopUpLink           string          `json:"topup_link"`
}

// --- Topup request types ---

// EpayRequest is the request body for POST /api/user/pay (epay).
type EpayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}

// AmountRequest is the request body for POST /api/user/amount.
type AmountRequest struct {
	Amount int64 `json:"amount"`
}

// StripePayRequest represents a payment request for Stripe checkout.
type StripePayRequest struct {
	// Amount is the quantity of units to purchase.
	Amount int64 `json:"amount"`
	// PaymentMethod specifies the payment method (e.g., "stripe").
	PaymentMethod string `json:"payment_method"`
	// SuccessURL is the optional custom URL to redirect after successful payment.
	// If empty, defaults to the server's console log page.
	SuccessURL string `json:"success_url,omitempty"`
	// CancelURL is the optional custom URL to redirect when payment is canceled.
	// If empty, defaults to the server's console topup page.
	CancelURL string `json:"cancel_url,omitempty"`
}

// CreemPayRequest is the request body for POST /api/user/creem/pay.
type CreemPayRequest struct {
	ProductId     string `json:"product_id"`
	PaymentMethod string `json:"payment_method"`
	// Amount in whole currency units for a pay-what-you-want top-up. When set,
	// it overrides the product's own price via Creem's custom_price, so an
	// arbitrary amount no longer needs its own product. ProductId still has to
	// name a configured one-time product: Creem requires it, and its Quota
	// scales the credit.
	Amount float64 `json:"amount"`
}

// AdminCompleteTopupRequest is the request body for POST /api/user/topup/complete.
type AdminCompleteTopupRequest struct {
	TradeNo string `json:"trade_no"`
}
