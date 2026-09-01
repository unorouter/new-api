package controller

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/thanhpk/randstr"
)

const (
	DeloPaySignatureHeader  = "X-Webhook-Signature-512"
	DeloPayApiBase          = "https://api.delopay.net"
	DeloPayTopUpRefPrefix   = "dref_"
	DeloPaySubOrderRefPrefx = "dsubref_"
	DeloPayCustomerPrefix   = "uno_"
	// Our trade_no travels in the payment metadata so the webhook can find the
	// order without parsing it back out of the description.
	DeloPayMetadataTradeNoKey = "trade_no"
)

// deloPayMinorUnits converts dollars to the minor units the API expects
// (1000 = 10.00 USD).
func deloPayMinorUnits(money float64) int64 {
	return int64(math.Round(money * 100))
}

func RequestDeloPayAmount(c fuego.ContextWithBody[dto.DeloPayPayRequest]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.Amount < getDeloPayMinTopup() {
		return dto.Fail[string](fmt.Sprintf("Top-up amount cannot be less than %v", getDeloPayMinTopup()))
	}
	id := dto.UserID(c)
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[string]("Failed to get user group")
	}
	payMoney := getDeloPayPayMoney(float64(req.Amount), group)
	return dto.Ok(fmt.Sprintf("%.2f", payMoney))
}

func RequestDeloPayPay(c fuego.ContextWithBody[dto.DeloPayPayRequest]) (*dto.Response[dto.DeloPayPayData], error) {
	ginCtx := dto.GinCtx(c)
	if !isDeloPayTopUpEnabled() {
		return dto.Fail[dto.DeloPayPayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.PaymentMethod != model.PaymentMethodDeloPay {
		return dto.Fail[dto.DeloPayPayData]("Payment channel is not supported")
	}
	if req.Amount < getDeloPayMinTopup() {
		return dto.Fail[dto.DeloPayPayData](fmt.Sprintf("Top-up amount cannot be less than %v", getDeloPayMinTopup()))
	}
	if req.Amount > 10000 {
		return dto.Fail[dto.DeloPayPayData]("Top-up amount cannot exceed 10000")
	}

	id := dto.UserID(c)
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		return dto.Fail[dto.DeloPayPayData]("Failed to get user information")
	}
	creditMoney := GetChargedAmount(float64(req.Amount), *user)
	// The buyer covers the processing fee, so what DeloPay bills is above what
	// we credit. Only creditMoney reaches TopUp.Money, which is what the
	// webhook turns into quota.
	billedMoney := applyDeloPayFeeSurcharge(creditMoney)

	reference := fmt.Sprintf("delopay-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := DeloPayTopUpRefPrefix + common.Sha1([]byte(reference))

	payLink, paymentId, err := createDeloPayPayment(referenceId, billedMoney, fmt.Sprintf("new-api topup %d units", req.Amount), paymentReturnPath(ginCtx, "/console/log"), deloPayCustomerFor(user))
	if err != nil {
		log.Println("failed to get DeloPay payment link:", err)
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	topUp := &model.TopUp{
		UserId:            id,
		Amount:            req.Amount,
		Money:             creditMoney,
		ChargedMoney:      billedMoney,
		TradeNo:           referenceId,
		PaymentMethod:     model.PaymentMethodDeloPay,
		PaymentProvider:   model.PaymentProviderDeloPay,
		CreateTime:        time.Now().Unix(),
		Status:            common.TopUpStatusPending,
		InvoiceUrl:        payLink,
		ProviderPaymentId: paymentId,
	}
	if err = topUp.Insert(); err != nil {
		return dto.Fail[dto.DeloPayPayData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}
	return dto.Ok(dto.DeloPayPayData{PayLink: payLink})
}

// applyDeloPayFeeSurcharge returns what to bill for a top-up that credits
// money. Rounded UP to the cent so the fee is never silently undercharged, and
// never below the credited amount in case the settings go negative.
func applyDeloPayFeeSurcharge(money float64) float64 {
	if money <= 0 {
		return money
	}
	if setting.DeloPayFeeThreshold > 0 && money > setting.DeloPayFeeThreshold {
		return money
	}
	billed := money*(1+setting.DeloPayFeePercent) + setting.DeloPayFeeFixed
	billed = math.Ceil(billed*100) / 100
	if billed < money {
		return money
	}
	return billed
}

// deloPayCustomer identifies the buyer to DeloPay so payments group under one
// customer record instead of showing as anonymous. Every field is optional to
// DeloPay when ABSENT but rejected as an empty string (IR_06, no payment
// created), so the request struct must keep `omitempty` on all three.
type deloPayCustomer struct {
	Id    string
	Email string
	Name  string
}

func deloPayCustomerFor(user *model.User) deloPayCustomer {
	if user == nil {
		return deloPayCustomer{}
	}
	// OAuth-only accounts carry no email and may carry no display name.
	name := user.DisplayName
	if name == "" {
		name = user.Username
	}
	return deloPayCustomer{
		Id:    fmt.Sprintf("%s%d", DeloPayCustomerPrefix, user.Id),
		Email: user.Email,
		Name:  name,
	}
}

// createDeloPayPayment returns the hosted-checkout link and the provider payment
// id. Which methods that checkout offers is decided by the connectors enabled on
// the configured profile, not by this request.
func createDeloPayPayment(referenceId string, payMoney float64, description, returnURL string, customer deloPayCustomer) (string, string, error) {
	if setting.DeloPayApiKey == "" {
		return "", "", errors.New("DeloPay API key is not configured")
	}
	if setting.DeloPayProfileId == "" {
		return "", "", errors.New("DeloPay profile id is not configured")
	}

	body := dto.DeloPayCreatePaymentRequest{
		Amount:      deloPayMinorUnits(payMoney),
		Currency:    "USD",
		ProfileId:   setting.DeloPayProfileId,
		PaymentLink: true,
		Description: description,
		ReturnURL:   returnURL,
		Metadata:    map[string]string{DeloPayMetadataTradeNoKey: referenceId},
		TestMode:    setting.DeloPayTestMode,
		CustomerId:  customer.Id,
		Email:       customer.Email,
		Name:        customer.Name,
	}
	jsonData, err := common.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal DeloPay request: %s", err.Error())
	}

	req, err := http.NewRequest("POST", DeloPayApiBase+"/payments", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", "", fmt.Errorf("failed to create DeloPay request: %s", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", setting.DeloPayApiKey)
	// Our reference is stable per order, so a retried create cannot double-charge.
	req.Header.Set("Idempotency-Key", referenceId)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to send DeloPay request: %s", err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read DeloPay response: %s", err.Error())
	}
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("delopay payment creation failed status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var paymentResp dto.DeloPayPaymentResponse
	if err = common.Unmarshal(respBody, &paymentResp); err != nil {
		return "", "", fmt.Errorf("failed to parse DeloPay response: %s", err.Error())
	}

	link := paymentResp.PaymentLink.Link
	if link == "" && paymentResp.MerchantId != "" && paymentResp.PaymentId != "" {
		link = fmt.Sprintf("https://checkout.delopay.net/pay/%s/%s", paymentResp.MerchantId, paymentResp.PaymentId)
	}
	if link == "" {
		return "", "", errors.New("delopay returned no checkout link")
	}
	if pane := strings.TrimSpace(setting.DeloPayCheckoutPane); pane != "" {
		link = appendDeloPayPane(link, pane)
	}
	return link, paymentResp.PaymentId, nil
}

// appendDeloPayPane adds the pane selector without clobbering a query string
// DeloPay already put on the link.
func appendDeloPayPane(link, pane string) string {
	sep := "?"
	if strings.Contains(link, "?") {
		sep = "&"
	}
	return link + sep + "pane=" + url.QueryEscape(pane)
}

// verifyDeloPaySignature checks the HMAC-SHA512 of the RAW body; unlike
// NowPayments, DeloPay signs the bytes as sent, so the payload must not be
// re-marshaled before hashing.
func verifyDeloPaySignature(payload []byte, sig, secret string) bool {
	if secret == "" || sig == "" {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(sig)), []byte(expected))
}

func DeloPayWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	callerIp := c.ClientIP()

	if setting.DeloPayWebhookSecret == "" {
		logger.LogWarn(ctx, fmt.Sprintf("DeloPay webhook secret is not configured, rejecting request client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("DeloPay webhook failed to read payload client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	signature := c.GetHeader(DeloPaySignatureHeader)
	if signature == "" {
		logger.LogWarn(ctx, fmt.Sprintf("DeloPay webhook missing signature client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if !verifyDeloPaySignature(bodyBytes, signature, setting.DeloPayWebhookSecret) {
		logger.LogWarn(ctx, fmt.Sprintf("DeloPay webhook signature verification failed client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var event dto.DeloPayWebhookEvent
	if err = common.Unmarshal(bodyBytes, &event); err != nil {
		logger.LogError(ctx, fmt.Sprintf("DeloPay webhook failed to parse payload client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("DeloPay webhook received event_id=%s event_type=%s payment_id=%s connector=%s client_ip=%s",
		event.EventId, event.EventType, event.Content.Object.PaymentId, event.Content.Object.Connector, callerIp))

	handleDeloPayEvent(c, &event, callerIp)
}

func handleDeloPayEvent(c *gin.Context, event *dto.DeloPayWebhookEvent, callerIp string) {
	ctx := c.Request.Context()
	object := event.Content.Object
	orderId := object.Metadata[DeloPayMetadataTradeNoKey]

	if orderId == "" {
		// Not one of ours (or a non-payment object): acknowledge so DeloPay stops
		// retrying for 24h.
		logger.LogWarn(ctx, fmt.Sprintf("DeloPay webhook without trade_no metadata ignored event_id=%s payment_id=%s", event.EventId, object.PaymentId))
		c.Status(http.StatusOK)
		return
	}

	if object.PaymentId != "" {
		if err := model.SetTopUpProviderPaymentId(orderId, object.PaymentId); err != nil {
			logger.LogError(ctx, fmt.Sprintf("DeloPay failed to record payment id trade_no=%s payment_id=%s error=%q", orderId, object.PaymentId, err.Error()))
		}
	}

	switch event.EventType {
	case "payment_succeeded":
		LockOrder(orderId)
		defer UnlockOrder(orderId)

		// Only on success: DeloPay reports the invoice Amount on every status,
		// including requires_payment_method, so recording it earlier would mark
		// abandoned checkouts as paid. Amount is in minor units.
		if object.Amount > 0 {
			if err := model.SetTopUpPaidAmount(orderId, float64(object.Amount)/100); err != nil {
				logger.LogError(ctx, fmt.Sprintf("DeloPay failed to record paid amount trade_no=%s amount=%d error=%q", orderId, object.Amount, err.Error()))
			}
		}

		subPayload := common.GetJsonString(event)
		if err := model.CompleteSubscriptionOrder(orderId, subPayload, model.PaymentProviderDeloPay, ""); err == nil {
			// Completion upserts a success top_ups ledger row under the same
			// trade_no, so falling through to the top-up recharge would always
			// fail on it. A subscription trade_no is never also a top-up.
			logger.LogInfo(ctx, fmt.Sprintf("DeloPay subscription order processed successfully trade_no=%s client_ip=%s", orderId, callerIp))
			c.Status(http.StatusOK)
			return
		} else if !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			logger.LogError(ctx, fmt.Sprintf("DeloPay subscription order processing failed trade_no=%s error=%q", orderId, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		if err := model.RechargeDeloPay(orderId, object.Connector); err != nil {
			logger.LogError(ctx, fmt.Sprintf("DeloPay topup processing failed trade_no=%s client_ip=%s error=%q", orderId, callerIp, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		if topUp := model.GetTopUpByTradeNo(orderId); topUp != nil {
			go service.SendTopupConfirmationEmail(topUp.UserId, topUp.Money, topUp.Money, "USD", topUp.TradeNo)
		}
		c.Status(http.StatusOK)

	case "payment_failed", "payment_cancelled", "payment_expired":
		LockOrder(orderId)
		defer UnlockOrder(orderId)
		err := model.UpdatePendingTopUpStatus(orderId, model.PaymentProviderDeloPay, common.TopUpStatusFailed)
		if err != nil && !errors.Is(err, model.ErrTopUpNotFound) && !errors.Is(err, model.ErrTopUpStatusInvalid) {
			logger.LogError(ctx, fmt.Sprintf("DeloPay failed to mark failure status trade_no=%s event_type=%s error=%q", orderId, event.EventType, err.Error()))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("DeloPay topup order marked event_type=%s trade_no=%s", event.EventType, orderId))
		}
		c.Status(http.StatusOK)

	case "payment_processing", "action_required":
		logger.LogInfo(ctx, fmt.Sprintf("DeloPay awaiting completion trade_no=%s event_type=%s", orderId, event.EventType))
		c.Status(http.StatusOK)

	default:
		logger.LogInfo(ctx, fmt.Sprintf("DeloPay event ignored trade_no=%s event_type=%s", orderId, event.EventType))
		c.Status(http.StatusOK)
	}
}

func getDeloPayPayMoney(amount float64, group string) float64 {
	originalAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.QuotaPerUnit
	}
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	return amount * topupGroupRatio * discount
}

func getDeloPayMinTopup() int64 {
	minTopup := setting.DeloPayMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}
