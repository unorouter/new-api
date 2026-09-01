package setting

var DeloPayEnabled = false
var DeloPayApiKey = ""

// Which payment methods the hosted checkout offers is a property of the profile's
// connectors, set in the DeloPay dashboard; the API takes no per-request filter.
var DeloPayProfileId = ""
var DeloPayWebhookSecret = ""
var DeloPayTestMode = false
var DeloPayMinTopUp = 1
var DeloPaySubscriptionEnabled = false

// PayPal takes a fixed cut per transaction on top of a percentage, which makes
// the smallest top-ups a loss. These pass that cost to the buyer: the charge is
// amount*(1+rate)+fixed, while the credit granted stays the amount. Both zero
// means the buyer pays exactly the top-up amount.
var DeloPayFeeFixed = 0.5
var DeloPayFeePercent = 0.0

// Above this the fixed cut is small enough relative to the top-up to absorb, so
// no fee is added. 0 applies the fee to every amount.
var DeloPayFeeThreshold = 2.0

// Opens the hosted checkout on a single method instead of the picker. Empty
// shows every method the profile's connectors offer.
var DeloPayCheckoutPane = "paypal"
