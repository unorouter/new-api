package controller

import (
	"fmt"
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

// A subscription is sold as one NowPayments invoice carrying the order's
// trade_no as order_id, exactly like a top-up. The earlier NowPayments
// "email subscription" product delivered payments with no order_id, which the
// IPN handler cannot map to anything, so those orders never settled.
func SubscriptionRequestNowPaymentsPay(c fuego.ContextWithBody[dto.SubscriptionNowPaymentsPayRequest]) (*dto.Response[dto.NowPaymentsPayData], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.NowPaymentsPayData]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	if !setting.NowPaymentsSubscriptionEnabled {
		return dto.Fail[dto.NowPaymentsPayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.NowPaymentsPayData]("Invalid parameters")
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.NowPaymentsPayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.NowPaymentsPayData]("Subscription plan is not enabled")
	}
	if setting.NowPaymentsApiKey == "" || setting.NowPaymentsIpnSecret == "" {
		return dto.Fail[dto.NowPaymentsPayData]("Webhook is not configured")
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return dto.Fail[dto.NowPaymentsPayData]("User does not exist")
	}

	if held, err := model.HasActiveUserSubscriptionForPlan(userId, plan.Id); err != nil {
		return dto.Fail[dto.NowPaymentsPayData](err.Error())
	} else if held {
		return dto.Fail[dto.NowPaymentsPayData]("You already hold this plan and it is still active. A plan can be held once at a time; you can add a different plan next to it.")
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.NowPaymentsPayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.NowPaymentsPayData]("Purchase limit for this plan has been reached")
		}
	}

	reference := fmt.Sprintf("sub-nowpayments-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := NowPaymentsSubOrderRefPrefx + common.Sha1([]byte(reference))

	returnURL := paymentReturnPath(ginCtx, "/console/subscription")
	payLink, err := genNowPaymentsInvoice(ginCtx, referenceId, applyNowPaymentsFeeSurcharge(plan.PriceAmount), returnURL, returnURL, fmt.Sprintf("new-api subscription %s", plan.Title))
	if err != nil {
		log.Println("failed to create NowPayments subscription invoice:", err)
		return dto.Fail[dto.NowPaymentsPayData]("Failed to start payment")
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
		return dto.Fail[dto.NowPaymentsPayData]("Failed to create order")
	}

	return dto.Ok(dto.NowPaymentsPayData{PayLink: payLink})
}
