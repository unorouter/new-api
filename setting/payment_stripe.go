package setting

var StripeEnabled = true
var StripeApiSecret = ""
var StripeWebhookSecret = ""
var StripePriceId = ""
var StripeUnitPrice = 8.0
var StripeMinTopUp = 1
var StripePromotionCodesEnabled = false

// Ask the issuer to authenticate every card at checkout. A payment that passes
// 3D Secure shifts fraud-dispute liability to the issuer; an issuer that cannot
// authenticate still lets the payment through. Ignored under Managed Payments,
// where Stripe owns the fraud rules.
var StripeRequest3DS = true

// StripeManagedPayments enables Stripe Managed Payments (merchant-of-record) on
// checkout sessions. Toggled from the Stripe payment-gateway admin tab; dormant
// fallback used only when bridging away from the primary processor.
var StripeManagedPayments = false

// StripeTextModerationEnabled gates TEXT generation prompts through moderation
// before relay. Independent of CreemModerationEnabled (which covers image/video);
// turned on alongside Stripe MoR where the processor holds the merchant liable for
// AI text outputs. Off by default - text relay runs unmoderated.
var StripeTextModerationEnabled = false
