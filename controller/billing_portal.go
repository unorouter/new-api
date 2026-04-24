package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"github.com/go-fuego/fuego"
	stripe "github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/billingportal/session"
)

// GetBillingPortalURL returns a URL that sends the caller to the correct
// payment provider's self-service portal (manage subscriptions, update cards,
// download invoices, cancel).
//
// Provider is chosen from the user's latest payment (subscription order or
// top-up), so each user goes to the place where their billing actually lives.
//
// Agents reach this via /api/user/billing-portal with an OAuth bearer JWT +
// subscription:cancel scope. Dashboard callers hit the same route with a
// session cookie; no scope check (RequireScope is a no-op for non-OAuth).
const (
	creemFallbackPortalURL = "https://www.creem.io/portal/orders"
	creemBillingEndpoint   = "https://api.creem.io/v1/customers/billing"
	creemTestEndpoint      = "https://test-api.creem.io/v1/customers/billing"
)

// GetBillingPortal handles GET /api/user/billing-portal.
func GetBillingPortal(c fuego.ContextNoBody) (*dto.Response[dto.BillingPortalData], error) {
	userId := dto.UserID(c)
	if userId == 0 {
		return dto.Fail[dto.BillingPortalData](common.TranslateMessage(dto.GinCtx(c), "user.id_empty"))
	}

	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return dto.Fail[dto.BillingPortalData](common.TranslateMessage(dto.GinCtx(c), "user.not_found"))
	}

	provider := latestPaymentMethod(userId)
	switch provider {
	case model.PaymentMethodStripe:
		url, err := generateStripePortalURL(user.StripeCustomer)
		if err != nil {
			return dto.Fail[dto.BillingPortalData](err.Error())
		}
		return dto.Ok(dto.BillingPortalData{Provider: "stripe", PortalURL: url})

	case model.PaymentMethodCreem:
		url, err := generateCreemPortalURL(user.CreemCustomer)
		if err != nil {
			return dto.Fail[dto.BillingPortalData](err.Error())
		}
		return dto.Ok(dto.BillingPortalData{Provider: "creem", PortalURL: url})

	default:
		// Never paid. Send them to Creem's generic portal as a best effort - 		// they can sign in with the email they used, if any.
		return dto.Ok(dto.BillingPortalData{Provider: "creem", PortalURL: creemFallbackPortalURL})
	}
}

// latestPaymentMethod inspects the user's latest completed subscription or
// top-up and returns its payment_method ("stripe", "creem", etc.). Empty
// string when there is no payment history.
func latestPaymentMethod(userId int) string {
	var sub model.SubscriptionOrder
	subOk := model.DB.
		Where("user_id = ? AND status = ?", userId, "paid").
		Order("complete_time DESC").
		First(&sub).Error == nil

	var topup model.TopUp
	topupOk := model.DB.
		Where("user_id = ? AND status = ?", userId, "success").
		Order("complete_time DESC").
		First(&topup).Error == nil

	switch {
	case subOk && topupOk:
		if sub.CompleteTime >= topup.CompleteTime {
			return sub.PaymentMethod
		}
		return topup.PaymentMethod
	case subOk:
		return sub.PaymentMethod
	case topupOk:
		return topup.PaymentMethod
	default:
		return ""
	}
}

// generateStripePortalURL creates a one-time Stripe billing-portal session.
// The returned URL is short-lived; agents should open it immediately.
func generateStripePortalURL(customerID string) (string, error) {
	if customerID == "" {
		return "", errors.New("no stripe customer id on file")
	}
	if setting.StripeApiSecret == "" {
		return "", errors.New("stripe is not configured")
	}
	stripe.Key = setting.StripeApiSecret

	params := &stripe.BillingPortalSessionParams{
		Customer: stripe.String(customerID),
	}
	s, err := session.New(params)
	if err != nil {
		return "", err
	}
	return s.URL, nil
}

// generateCreemPortalURL calls Creem's POST /v1/customers/billing to get a
// per-customer portal link. If we have no customer ID on file, fall back to
// the generic self-service URL so the user can still sign in with their
// email. (users.creem_customer is populated going forward via the webhook;
// older users won't have it until their next purchase.)
func generateCreemPortalURL(customerID string) (string, error) {
	if customerID == "" {
		return creemFallbackPortalURL, nil
	}
	if setting.CreemApiKey == "" {
		return creemFallbackPortalURL, nil
	}

	endpoint := creemBillingEndpoint
	if setting.CreemTestMode {
		endpoint = creemTestEndpoint
	}

	body, err := common.Marshal(map[string]string{"customer_id": customerID})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", setting.CreemApiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		// API failure - fall back rather than block the user. Log the detail
		// so operators can debug.
		common.SysLog(fmt.Sprintf("creem billing portal api %d: %s", resp.StatusCode, string(raw)))
		return creemFallbackPortalURL, nil
	}

	var parsed struct {
		CustomerPortalLink string `json:"customer_portal_link"`
	}
	if err := common.Unmarshal(raw, &parsed); err != nil || parsed.CustomerPortalLink == "" {
		return creemFallbackPortalURL, nil
	}
	return parsed.CustomerPortalLink, nil
}
