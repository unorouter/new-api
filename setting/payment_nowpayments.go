package setting

var NowPaymentsEnabled = false
var NowPaymentsApiKey = ""
var NowPaymentsIpnSecret = ""
var NowPaymentsSandbox = false
var NowPaymentsUnitPrice = 1.0
var NowPaymentsMinTopUp = 1
var NowPaymentsFeePaidByUser = false
var NowPaymentsIsFixedRate = false
var NowPaymentsSubscriptionEnabled = false

// Subscription plan APIs require JWT auth (POST /v1/auth with account email + password).
// Top-up and IPN paths keep using x-api-key.
var NowPaymentsEmail = ""
var NowPaymentsPassword = ""
