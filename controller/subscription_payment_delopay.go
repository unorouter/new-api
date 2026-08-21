package controller

import (
	"fmt"
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

// SubscriptionRequestDeloPayPay charges one subscription cycle. DeloPay needs no
// provider-side plan object: each cycle is an ordinary payment for the plan
// price, so no plan id is cached on the row.
func SubscriptionRequestDeloPayPay(c fuego.ContextWithBody[dto.SubscriptionDeloPayPayRequest]) (*dto.Response[dto.DeloPayPayData], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, i18n.MsgPaymentComplianceRequired))
	}
	if !setting.DeloPaySubscriptionEnabled {
		return dto.Fail[dto.DeloPayPayData]("Payment channel is not supported")
	}
	if !isDeloPayTopUpEnabled() {
		return dto.Fail[dto.DeloPayPayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.DeloPayPayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "subscription.not_enabled"))
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "user.not_exists"))
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.DeloPayPayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "subscription.purchase_max"))
		}
	}

	reference := fmt.Sprintf("sub-delopay-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := DeloPaySubOrderRefPrefx + common.Sha1([]byte(reference))

	// The buyer covers the processing fee here too; the order still records the
	// plan price, which is what grants the subscription.
	payLink, _, err := createDeloPayPayment(referenceId, applyDeloPayFeeSurcharge(plan.PriceAmount), plan.Title, paymentReturnPath(ginCtx, "/console/subscription"), deloPayCustomerFor(user))
	if err != nil {
		log.Println("failed to get DeloPay subscription payment link:", err)
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         referenceId,
		PaymentMethod:   model.PaymentMethodDeloPay,
		PaymentProvider: model.PaymentProviderDeloPay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}

	return dto.Ok(dto.DeloPayPayData{PayLink: payLink})
}
