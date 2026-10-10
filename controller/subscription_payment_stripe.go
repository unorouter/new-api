package controller

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-fuego/fuego"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/thanhpk/randstr"
)

// Managed Payments requires API version 2025-03-31.basil or later; the v81 SDK
// pins 2025-02-24.acacia, so the version is overridden per-request via the header.
const stripeManagedPaymentsAPIVersion = "2025-03-31.basil"

// applyStripeManagedPayments conditionally turns a Checkout Session into a
// Managed-Payments (merchant-of-record) session when the toggle is on. No-op
// otherwise, so normal Stripe sessions are unaffected.
func applyStripeManagedPayments(params *stripe.CheckoutSessionParams) {
	if !setting.StripeManagedPayments {
		return
	}
	params.AddExtra("managed_payments[enabled]", "true")
	if params.Headers == nil {
		params.Headers = http.Header{}
	}
	params.Headers.Set("Stripe-Version", stripeManagedPaymentsAPIVersion)
}

func SubscriptionRequestStripePay(c fuego.ContextWithBody[dto.SubscriptionStripePayRequest]) (*dto.Response[dto.StripePayLinkData], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.StripePayLinkData]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.StripePayLinkData]("Invalid parameters")
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.StripePayLinkData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.StripePayLinkData]("Subscription plan is not enabled")
	}
	if plan.StripePriceId == "" {
		return dto.Fail[dto.StripePayLinkData]("StripePriceId is not configured for this plan")
	}
	if !strings.HasPrefix(setting.StripeApiSecret, "sk_") && !strings.HasPrefix(setting.StripeApiSecret, "rk_") {
		return dto.Fail[dto.StripePayLinkData]("Invalid Stripe API key")
	}
	if setting.StripeWebhookSecret == "" {
		return dto.Fail[dto.StripePayLinkData]("Webhook is not configured")
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.Fail[dto.StripePayLinkData](err.Error())
	}
	if user == nil {
		return dto.Fail[dto.StripePayLinkData]("User does not exist")
	}

	if held, err := model.HasActiveUserSubscriptionForPlan(userId, plan.Id); err != nil {
		return dto.Fail[dto.StripePayLinkData](err.Error())
	} else if held {
		return dto.Fail[dto.StripePayLinkData]("You already hold this plan and it is still active. A plan can be held once at a time; you can add a different plan next to it.")
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.StripePayLinkData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.StripePayLinkData]("Purchase limit for this plan has been reached")
		}
	}

	reference := fmt.Sprintf("sub-stripe-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "sub_ref_" + common.Sha1([]byte(reference))

	payLink, err := genStripeSubscriptionLink(ginCtx, referenceId, user.StripeCustomer, user.Email, plan.StripePriceId)
	if err != nil {
		log.Println("failed to get Stripe Checkout payment link", err)
		return dto.Fail[dto.StripePayLinkData]("Failed to start payment")
	}

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         referenceId,
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.StripePayLinkData]("Failed to create order")
	}

	return dto.Ok(dto.StripePayLinkData{PayLink: payLink})
}

func genStripeSubscriptionLink(c *gin.Context, referenceId string, customerId string, email string, priceId string) (string, error) {
	stripe.Key = setting.StripeApiSecret

	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(paymentReturnPath(c, "/wallet")),
		CancelURL:         stripe.String(paymentReturnPath(c, "/wallet")),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceId),
				Quantity: stripe.Int64(1),
			},
		},
		Mode: stripe.String(string(stripe.CheckoutSessionModeSubscription)),
	}

	if "" == customerId {
		if "" != email {
			params.CustomerEmail = stripe.String(email)
		}
		// CustomerCreation is invalid in subscription mode (Stripe creates the
		// customer automatically); only valid in payment mode.
	} else {
		params.Customer = stripe.String(customerId)
	}

	applyStripeManagedPayments(params)

	result, err := session.New(params)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}
