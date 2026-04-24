package dto

// BillingPortalData is returned by GET /api/user/billing-portal. The provider
// field tells the caller which hosted portal they're being sent to, so an
// agent UI can label the link appropriately ("Manage on Stripe" vs "Manage on
// Creem"). The URL is short-lived for Stripe sessions; for Creem it is
// either a per-customer API-generated link or the generic fallback.
type BillingPortalData struct {
	Provider  string `json:"provider"`
	PortalURL string `json:"portal_url"`
}
