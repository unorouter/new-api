package controller

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/thanhpk/randstr"
)

const (
	PaymentMethodStripe = "stripe"
)

func RequestStripeAmount(c fuego.ContextWithBody[dto.StripePayRequest]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.Amount < getStripeMinTopup() {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "topup.min_amount", map[string]any{"Amount": getStripeMinTopup()}))
	}
	id := dto.UserID(c)
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "topup.get_group_failed"))
	}
	payMoney := getStripePayMoney(float64(req.Amount), group)
	if payMoney <= 0.01 {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "topup.amount_too_low"))
	}
	return dto.Ok(strconv.FormatFloat(payMoney, 'f', 2, 64))
}

func RequestStripePay(c fuego.ContextWithBody[dto.StripePayRequest]) (*dto.Response[dto.StripePayLinkData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.PaymentMethod != PaymentMethodStripe {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.channel_not_supported"))
	}
	if req.Amount < getStripeMinTopup() {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "topup.min_amount", map[string]any{"Amount": getStripeMinTopup()}))
	}
	if req.Amount > 10000 {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "topup.max_amount"))
	}

	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "topup.success_redirect_untrusted"))
	}

	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "topup.cancel_redirect_untrusted"))
	}

	id := dto.UserID(c)
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "topup.get_user_failed"))
	}
	chargedMoney := GetChargedAmount(float64(req.Amount), *user)

	reference := fmt.Sprintf("new-api-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))

	payLink, err := genStripeLink(referenceId, user.StripeCustomer, user.Email, req.Amount, req.SuccessURL, req.CancelURL)
	if err != nil {
		log.Println(i18n.Translate("topup.stripe_get_pay_link_failed"), err)
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	topUp := &model.TopUp{
		UserId:        id,
		Amount:        req.Amount,
		Money:         chargedMoney,
		TradeNo:       referenceId,
		PaymentMethod: PaymentMethodStripe,
		CreateTime:    time.Now().Unix(),
		Status:        common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		return dto.Fail[dto.StripePayLinkData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}
	return dto.Ok(dto.StripePayLinkData{PayLink: payLink})
}

func StripeWebhook(c *gin.Context) {
	if setting.StripeWebhookSecret == "" {
		log.Println("Stripe Webhook Secret 未配置，拒绝处理")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf(i18n.Translate("topup.stripe_parse_payload_failed", map[string]any{"Error": err.Error()}))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	signature := c.GetHeader("Stripe-Signature")
	event, err := webhook.ConstructEventWithOptions(payload, signature, setting.StripeWebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})

	if err != nil {
		log.Printf(i18n.Translate("topup.stripe_sign_failed", map[string]any{"Error": err.Error()}))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		sessionCompleted(event)
	case stripe.EventTypeCheckoutSessionExpired:
		sessionExpired(event)
	case stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded:
		sessionAsyncPaymentSucceeded(event)
	case stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		sessionAsyncPaymentFailed(event)
	default:
		log.Printf(i18n.Translate("topup.stripe_unsupported_event", map[string]any{"Type": string(event.Type)}))
	}

	c.Status(http.StatusOK)
}

func sessionCompleted(event stripe.Event) {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "complete" != status {
		log.Println(i18n.Translate("topup.stripe_invalid_complete_status", map[string]any{"Status": status, "OrderNo": referenceId}))
		return
	}

	paymentStatus := event.GetObjectValue("payment_status")
	if paymentStatus != "paid" {
		log.Printf("Stripe Checkout 支付尚未完成，payment_status: %s, ref: %s（等待异步支付结果）", paymentStatus, referenceId)
		return
	}

	fulfillOrder(event, referenceId, customerId)
}

// sessionAsyncPaymentSucceeded handles delayed payment methods (bank transfer, SEPA, etc.)
// that confirm payment after the checkout session completes.
func sessionAsyncPaymentSucceeded(event stripe.Event) {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	log.Printf("Stripe 异步支付成功: %s", referenceId)

	fulfillOrder(event, referenceId, customerId)
}

// sessionAsyncPaymentFailed marks orders as failed when delayed payment methods
// ultimately fail (e.g. bank transfer not received, SEPA rejected).
func sessionAsyncPaymentFailed(event stripe.Event) {
	referenceId := event.GetObjectValue("client_reference_id")
	log.Printf("Stripe 异步支付失败: %s", referenceId)

	if len(referenceId) == 0 {
		log.Println("异步支付失败事件未提供支付单号")
		return
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)

	topUp := model.GetTopUpByTradeNo(referenceId)
	if topUp == nil {
		log.Println("异步支付失败，充值订单不存在:", referenceId)
		return
	}

	if topUp.PaymentMethod != PaymentMethodStripe {
		log.Printf("异步支付失败，订单支付方式不匹配: %s, ref: %s", topUp.PaymentMethod, referenceId)
		return
	}

	if topUp.Status != common.TopUpStatusPending {
		log.Printf("异步支付失败，订单状态非pending: %s, ref: %s", topUp.Status, referenceId)
		return
	}

	topUp.Status = common.TopUpStatusFailed
	if err := topUp.Update(); err != nil {
		log.Printf("标记充值订单失败出错: %v, ref: %s", err, referenceId)
		return
	}
	log.Printf("充值订单已标记为失败: %s", referenceId)
}

// fulfillOrder is the shared logic for crediting quota after payment is confirmed.
func fulfillOrder(event stripe.Event, referenceId string, customerId string) {
	if len(referenceId) == 0 {
		log.Println("未提供支付单号")
		return
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	payload := map[string]any{
		"customer":     customerId,
		"amount_total": event.GetObjectValue("amount_total"),
		"currency":     strings.ToUpper(event.GetObjectValue("currency")),
		"event_type":   string(event.Type),
	}
	if err := model.CompleteSubscriptionOrder(referenceId, common.GetJsonString(payload)); err == nil {
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		log.Println(i18n.Translate("ctrl.complete_subscription_order_failed"), err.Error(), referenceId)
		return
	}

	err := model.Recharge(referenceId, customerId)
	if err != nil {
		log.Println(err.Error(), referenceId)
		return
	}

	total, _ := strconv.ParseFloat(event.GetObjectValue("amount_total"), 64)
	currency := strings.ToUpper(event.GetObjectValue("currency"))
	log.Printf(i18n.Translate("topup.stripe_payment_received", map[string]any{"OrderNo": referenceId, "Amount": fmt.Sprintf("%.2f", total/100), "Currency": currency}))
}

func sessionExpired(event stripe.Event) {
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "expired" != status {
		log.Println(i18n.Translate("topup.stripe_invalid_expired_status", map[string]any{"Status": status, "OrderNo": referenceId}))
		return
	}

	if len(referenceId) == 0 {
		log.Println(i18n.Translate("topup.stripe_no_order_number"))
		return
	}

	// Subscription order expiration
	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	if err := model.ExpireSubscriptionOrder(referenceId); err == nil {
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		log.Println(i18n.Translate("topup.stripe_expire_sub_failed", map[string]any{"OrderNo": referenceId, "Error": err.Error()}))
		return
	}

	topUp := model.GetTopUpByTradeNo(referenceId)
	if topUp == nil {
		log.Println(i18n.Translate("topup.stripe_order_not_found", map[string]any{"OrderNo": referenceId}))
		return
	}

	if topUp.Status != common.TopUpStatusPending {
		log.Println(i18n.Translate("topup.stripe_order_status_error", map[string]any{"OrderNo": referenceId}))
	}

	topUp.Status = common.TopUpStatusExpired
	err := topUp.Update()
	if err != nil {
		log.Println(i18n.Translate("topup.stripe_expire_order_failed", map[string]any{"OrderNo": referenceId, "Error": err.Error()}))
		return
	}

	log.Println(i18n.Translate("topup.stripe_order_expired", map[string]any{"OrderNo": referenceId}))
}

// genStripeLink generates a Stripe Checkout session URL for payment.
func genStripeLink(referenceId string, customerId string, email string, amount int64, successURL string, cancelURL string) (string, error) {
	if !strings.HasPrefix(setting.StripeApiSecret, "sk_") && !strings.HasPrefix(setting.StripeApiSecret, "rk_") {
		return "", fmt.Errorf(i18n.Translate("topup.stripe_invalid_key"))
	}

	stripe.Key = setting.StripeApiSecret

	// Use custom URLs if provided, otherwise use defaults
	if successURL == "" {
		successURL = system_setting.ServerAddress + "/console/log"
	}
	if cancelURL == "" {
		cancelURL = system_setting.ServerAddress + "/console/topup"
	}

	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(setting.StripePriceId),
				Quantity: stripe.Int64(amount),
			},
		},
		Mode:                stripe.String(string(stripe.CheckoutSessionModePayment)),
		AllowPromotionCodes: stripe.Bool(setting.StripePromotionCodesEnabled),
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

func GetChargedAmount(count float64, user model.User) float64 {
	topUpGroupRatio := common.GetTopupGroupRatio(user.Group)
	if topUpGroupRatio == 0 {
		topUpGroupRatio = 1
	}

	return count * topUpGroupRatio
}

func getStripePayMoney(amount float64, group string) float64 {
	originalAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.QuotaPerUnit
	}
	// Using float64 for monetary calculations is acceptable here due to the small amounts involved
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	payMoney := amount * setting.StripeUnitPrice * topupGroupRatio * discount
	return payMoney
}

func getStripeMinTopup() int64 {
	minTopup := setting.StripeMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}
