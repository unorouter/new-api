package controller

import (
	"log"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-fuego/fuego"
	"github.com/thanhpk/randstr"
)

func SubscriptionRequestCreemPay(c fuego.ContextWithBody[dto.SubscriptionCreemPayRequest]) (*dto.Response[dto.CreemPayData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.CreemPayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "subscription.not_enabled"))
	}
	if plan.CreemProductId == "" {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "payment.product_config_error"))
	}
	if setting.CreemWebhookSecret == "" && !setting.CreemTestMode {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "payment.webhook_not_configured"))
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.Fail[dto.CreemPayData](err.Error())
	}
	if user == nil {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "user.not_exists"))
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.CreemPayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "subscription.purchase_max"))
		}
	}

	reference := "sub-creem-ref-" + randstr.String(6)
	referenceId := "sub_ref_" + common.Sha1([]byte(reference+time.Now().String()+user.Username))

	// create pending order first
	order := &model.SubscriptionOrder{
		UserId:        userId,
		PlanId:        plan.Id,
		Money:         plan.PriceAmount,
		TradeNo:       referenceId,
		PaymentMethod: PaymentMethodCreem,
		CreateTime:    time.Now().Unix(),
		Status:        common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "payment.create_failed"))
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

	checkoutUrl, err := genCreemLink(referenceId, product, user.Email, user.Username)
	if err != nil {
		log.Println(i18n.Translate("topup.get_creem_pay_link_failed", map[string]any{"Error": err.Error()}))
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	return dto.Ok(dto.CreemPayData{
		CheckoutUrl: checkoutUrl,
		OrderId:     referenceId,
	})
}
