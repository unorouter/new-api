package controller

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/go-fuego/fuego"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/thanhpk/randstr"
)

func SubscriptionRequestStripePay(c fuego.ContextWithBody[dto.SubscriptionStripePayRequest]) (*dto.Response[dto.StripePayLinkData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.StripePayLinkData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "subscription.not_enabled"))
	}
	if plan.StripePriceId == "" {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.price_id_not_configured"))
	}
	if !strings.HasPrefix(setting.StripeApiSecret, "sk_") && !strings.HasPrefix(setting.StripeApiSecret, "rk_") {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.invalid_stripe_key"))
	}
	if setting.StripeWebhookSecret == "" {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.webhook_not_configured"))
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.Fail[dto.StripePayLinkData](err.Error())
	}
	if user == nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "user.not_exists"))
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.StripePayLinkData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "subscription.purchase_max"))
		}
	}

	reference := fmt.Sprintf("sub-stripe-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "sub_ref_" + common.Sha1([]byte(reference))

	payLink, err := genStripeSubscriptionLink(referenceId, user.StripeCustomer, user.Email, plan.StripePriceId)
	if err != nil {
		log.Println(i18n.Translate("topup.get_stripe_pay_link_failed"), err)
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	order := &model.SubscriptionOrder{
		UserId:        userId,
		PlanId:        plan.Id,
		Money:         plan.PriceAmount,
		TradeNo:       referenceId,
		PaymentMethod: PaymentMethodStripe,
		CreateTime:    time.Now().Unix(),
		Status:        common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}

	return dto.Ok(dto.StripePayLinkData{PayLink: payLink})
}

func genStripeSubscriptionLink(referenceId string, customerId string, email string, priceId string) (string, error) {
	stripe.Key = setting.StripeApiSecret

	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(system_setting.ServerAddress + "/console/topup"),
		CancelURL:         stripe.String(system_setting.ServerAddress + "/console/topup"),
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
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}

	result, err := session.New(params)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}
