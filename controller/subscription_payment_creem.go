package controller

import (
	"log"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-fuego/fuego"
	"github.com/thanhpk/randstr"
)

func SubscriptionRequestCreemPay(c fuego.ContextWithBody[dto.SubscriptionCreemPayRequest]) (*dto.Response[dto.CreemPayData], error) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.CreemPayData]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.CreemPayData]("Invalid parameters")
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.CreemPayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.CreemPayData]("Subscription plan is not enabled")
	}
	if plan.CreemProductId == "" {
		return dto.Fail[dto.CreemPayData]("Product configuration error")
	}
	if setting.CreemWebhookSecret == "" && !setting.CreemTestMode {
		return dto.Fail[dto.CreemPayData]("Webhook is not configured")
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.Fail[dto.CreemPayData](err.Error())
	}
	if user == nil {
		return dto.Fail[dto.CreemPayData]("User does not exist")
	}

	if held, err := model.HasActiveUserSubscriptionForPlan(userId, plan.Id); err != nil {
		return dto.Fail[dto.CreemPayData](err.Error())
	} else if held {
		return dto.Fail[dto.CreemPayData]("You already hold this plan and it is still active. A plan can be held once at a time; you can add a different plan next to it.")
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.CreemPayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.CreemPayData]("Purchase limit for this plan has been reached")
		}
	}

	reference := "sub-creem-ref-" + randstr.String(6)
	referenceId := "sub_ref_" + common.Sha1([]byte(reference+time.Now().String()+user.Username))

	// create pending order first
	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         referenceId,
		PaymentMethod:   PaymentMethodCreem,
		PaymentProvider: model.PaymentProviderCreem,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.CreemPayData]("Failed to create order")
	}

	// Reuse Creem checkout generator by building a lightweight product reference.
	currency := "USD"
	switch operation_setting.GetGeneralSetting().QuotaDisplayType {
	case operation_setting.QuotaDisplayTypeCNY:
		currency = "CNY"
	case operation_setting.QuotaDisplayTypeUSD:
		currency = "USD"
	default:
		currency = "USD"
	}
	product := &dto.CreemProduct{
		ProductId: plan.CreemProductId,
		Name:      plan.Title,
		Price:     plan.PriceAmount,
		Currency:  currency,
		Quota:     0,
	}

	// 0: subscriptions always charge the plan's configured price, never a
	// custom amount.
	checkoutUrl, err := genCreemLink(referenceId, product, user.Email, user.Username, 0)
	if err != nil {
		log.Printf("failed to get Creem payment link: %s", err.Error())
		return dto.Fail[dto.CreemPayData]("Failed to start payment")
	}

	return dto.Ok(dto.CreemPayData{
		CheckoutUrl: checkoutUrl,
		OrderId:     referenceId,
	})
}
