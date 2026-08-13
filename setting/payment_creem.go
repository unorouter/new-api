package setting

var CreemEnabled = true
var CreemApiKey = ""
var CreemProducts = "[]"
var CreemTestMode = false
var CreemWebhookSecret = ""

// CreemModerationEnabled gates image/video generation prompts through the CREEM
// moderation API before dispatch. Toggled from the Creem payment-gateway admin tab.
var CreemModerationEnabled = false

// The card processor takes a fixed cut per transaction on top of a percentage,
// which makes the smallest top-ups a loss. These pass that cost to the buyer on
// a CUSTOM amount: the charge is amount*(1+percent)+fixed, while the credit
// granted stays the amount. Both zero means the buyer pays exactly the top-up.
var CreemFeeFixed = 0.5
var CreemFeePercent = 0.0

// Above this the fixed cut is small enough relative to the top-up to absorb, so
// no fee is added. 0 applies the fee to every custom amount.
var CreemFeeThreshold = 2.0
