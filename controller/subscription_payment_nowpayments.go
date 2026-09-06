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

// A subscription is sold as one NowPayments invoice carrying the order's
// trade_no as order_id, exactly like a top-up. The earlier NowPayments
// "email subscription" product delivered payments with no order_id, which the
// IPN handler cannot map to anything, so those orders never settled.
func SubscriptionRequestNowPaymentsPay(c fuego.ContextWithBody[dto.SubscriptionNowPaymentsPayRequest]) (*dto.Response[dto.NowPaymentsPayData], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, i18n.MsgPaymentComplianceRequired))
	}
	if !setting.NowPaymentsSubscriptionEnabled {
		return dto.Fail[dto.NowPaymentsPayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.NowPaymentsPayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "subscription.not_enabled"))
	}
	if setting.NowPaymentsApiKey == "" || setting.NowPaymentsIpnSecret == "" {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "payment.webhook_not_configured"))
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "user.not_exists"))
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.NowPaymentsPayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "subscription.purchase_max"))
		}
	}

	reference := fmt.Sprintf("sub-nowpayments-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := NowPaymentsSubOrderRefPrefx + common.Sha1([]byte(reference))

	returnURL := paymentReturnPath(ginCtx, "/console/subscription")
	payLink, err := genNowPaymentsInvoice(ginCtx, referenceId, applyNowPaymentsFeeSurcharge(plan.PriceAmount), returnURL, returnURL, fmt.Sprintf("new-api subscription %s", plan.Title))
	if err != nil {
		log.Println("failed to create NowPayments subscription invoice:", err)
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         referenceId,
		PaymentMethod:   PaymentMethodNowPayments,
		PaymentProvider: model.PaymentProviderNowPayments,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}

	return dto.Ok(dto.NowPaymentsPayData{PayLink: payLink})
}
