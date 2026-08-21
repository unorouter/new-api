package controller

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	stdjson "encoding/json"
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
	PaymentMethodNowPayments    = "nowpayments"
	NowPaymentsSignatureHeader  = "x-nowpayments-sig"
	NowPaymentsApiBaseProd      = "https://api.nowpayments.io/v1"
	NowPaymentsApiBaseSandbox   = "https://api-sandbox.nowpayments.io/v1"
	NowPaymentsTopUpRefPrefix   = "ref_"
	NowPaymentsSubOrderRefPrefx = "subref_"
)

// Crypto fee pass-through: NowPayments charges a percentage service fee plus a
// flat per-tx network (gas) fee. The flat network fee only matters on the $1
// test tier, where it would otherwise wipe out the merchant; on larger top-ups
// the percentage alone covers it, so the flat buffer is NOT added there (it would
// read as an absurd ~50% fee on a small buy and a needless charge on a large one).
const (
	nowPaymentsFeePercent      = 0.01 // service fee share, all amounts
	nowPaymentsFeeFlatUSD      = 0.50 // flat network-fee buffer, $1 tier only
	nowPaymentsFlatFeeMaxMoney = 1.0  // apply the flat buffer only at/below this
)

func applyNowPaymentsFeeSurcharge(baseMoney float64) float64 {
	surcharged := baseMoney * (1 + nowPaymentsFeePercent)
	if baseMoney <= nowPaymentsFlatFeeMaxMoney {
		surcharged += nowPaymentsFeeFlatUSD
	}
	return surcharged
}

func nowPaymentsApiBase() string {
	if setting.NowPaymentsSandbox {
		return NowPaymentsApiBaseSandbox
	}
	return NowPaymentsApiBaseProd
}

func RequestNowPaymentsAmount(c fuego.ContextWithBody[dto.NowPaymentsPayRequest]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.Amount < getNowPaymentsMinTopup() {
		return dto.Fail[string](fmt.Sprintf("Top-up amount cannot be less than %v", getNowPaymentsMinTopup()))
	}
	id := dto.UserID(c)
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[string]("Failed to get user group")
	}
	payMoney := getNowPaymentsPayMoney(float64(req.Amount), group)
	if payMoney <= 0.01 {
		return dto.Fail[string]("Top-up amount is too low")
	}
	// Quote the fee-inclusive total so the displayed amount matches the invoice.
	payMoney = applyNowPaymentsFeeSurcharge(payMoney)
	return dto.Ok(strconv.FormatFloat(payMoney, 'f', 2, 64))
}

func RequestNowPaymentsPay(c fuego.ContextWithBody[dto.NowPaymentsPayRequest]) (*dto.Response[dto.NowPaymentsPayData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.PaymentMethod != PaymentMethodNowPayments {
		return dto.Fail[dto.NowPaymentsPayData]("Payment channel is not supported")
	}
	if req.Amount < getNowPaymentsMinTopup() {
		return dto.Fail[dto.NowPaymentsPayData](fmt.Sprintf("Top-up amount cannot be less than %v", getNowPaymentsMinTopup()))
	}
	if req.Amount > 10000 {
		return dto.Fail[dto.NowPaymentsPayData]("Top-up amount cannot exceed 10000")
	}

	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		return dto.Fail[dto.NowPaymentsPayData]("Success redirect URL is not in the trusted domain list")
	}
	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		return dto.Fail[dto.NowPaymentsPayData]("Cancel redirect URL is not in the trusted domain list")
	}

	id := dto.UserID(c)
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		return dto.Fail[dto.NowPaymentsPayData]("Failed to get user information")
	}
	chargedMoney := GetChargedAmount(float64(req.Amount), *user)
	// User covers the crypto network/service fee: invoice for a bit more than the
	// credited amount. Quota is credited on chargedMoney (topUp.Money below), the
	// surcharge only inflates the NowPayments invoice price.
	invoicePrice := applyNowPaymentsFeeSurcharge(chargedMoney)

	reference := fmt.Sprintf("nowpayments-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := NowPaymentsTopUpRefPrefix + common.Sha1([]byte(reference))

	payLink, err := genNowPaymentsInvoice(ginCtx, referenceId, invoicePrice, req.SuccessURL, req.CancelURL, fmt.Sprintf("new-api topup %d units", req.Amount))
	if err != nil {
		log.Println("failed to get NowPayments payment link:", err)
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           chargedMoney,
		TradeNo:         referenceId,
		PaymentMethod:   PaymentMethodNowPayments,
		PaymentProvider: model.PaymentProviderNowPayments,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err = topUp.Insert(); err != nil {
		return dto.Fail[dto.NowPaymentsPayData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}
	return dto.Ok(dto.NowPaymentsPayData{PayLink: payLink})
}

func genNowPaymentsInvoice(c *gin.Context, referenceId string, payMoney float64, successURL, cancelURL, description string) (string, error) {
	if setting.NowPaymentsApiKey == "" {
		return "", errors.New("NowPayments API key is not configured")
	}

	if successURL == "" {
		successURL = paymentReturnPath(c, "/console/log")
	}
	if cancelURL == "" {
		cancelURL = paymentReturnPath(c, "/console/topup")
	}

	body := dto.NowPaymentsInvoiceRequest{
		PriceAmount:      payMoney,
		PriceCurrency:    "usd",
		OrderId:          referenceId,
		OrderDescription: description,
		IpnCallbackURL:   service.GetCallbackAddress() + "/api/nowpayments/webhook",
		SuccessURL:       successURL,
		CancelURL:        cancelURL,
		IsFixedRate:      setting.NowPaymentsIsFixedRate,
		IsFeePaidByUser:  setting.NowPaymentsFeePaidByUser,
	}
	jsonData, err := common.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal NowPayments request: %s", err.Error())
	}

	apiUrl := nowPaymentsApiBase() + "/invoice"
	req, err := http.NewRequest("POST", apiUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create NowPayments request: %s", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", setting.NowPaymentsApiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send NowPayments request: %s", err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read NowPayments response: %s", err.Error())
	}

	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("nowpayments invoice creation failed status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var invoiceResp dto.NowPaymentsInvoiceResponse
	if err = common.Unmarshal(respBody, &invoiceResp); err != nil {
		return "", fmt.Errorf("failed to parse NowPayments response: %s", err.Error())
	}
	if invoiceResp.InvoiceURL == "" {
		return "", errors.New("nowpayments returned empty invoice_url")
	}
	return invoiceResp.InvoiceURL, nil
}

// canonicalJSONForNowPaymentsHMAC re-marshals payload with sorted keys for
// HMAC-SHA512 signature parity with NowPayments. Direct encoding/json use
// (Rule 1 exception) is required because byte-exact key ordering matters.
func canonicalJSONForNowPaymentsHMAC(payload []byte) ([]byte, error) {
	dec := stdjson.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return stdjson.Marshal(obj)
}

func verifyNowPaymentsSignature(payload []byte, sig, secret string) bool {
	if secret == "" || sig == "" {
		return false
	}
	sorted, err := canonicalJSONForNowPaymentsHMAC(payload)
	if err != nil {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(sorted)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

func NowPaymentsWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	callerIp := c.ClientIP()

	if setting.NowPaymentsIpnSecret == "" {
		logger.LogWarn(ctx, fmt.Sprintf("NowPayments webhook IPN secret is not configured, rejecting request client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("NowPayments webhook failed to read payload client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	signature := c.GetHeader(NowPaymentsSignatureHeader)
	if signature == "" {
		logger.LogWarn(ctx, fmt.Sprintf("NowPayments webhook missing signature client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if !verifyNowPaymentsSignature(bodyBytes, signature, setting.NowPaymentsIpnSecret) {
		logger.LogWarn(ctx, fmt.Sprintf("NowPayments webhook signature verification failed client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var event dto.NowPaymentsWebhookEvent
	if err = common.Unmarshal(bodyBytes, &event); err != nil {
		logger.LogError(ctx, fmt.Sprintf("NowPayments webhook failed to parse payload client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("NowPayments webhook received event order_id=%s status=%s pay_currency=%s actually_paid=%v client_ip=%s",
		event.OrderId, event.PaymentStatus, event.PayCurrency, event.ActuallyPaid, callerIp))

	if event.OrderId == "" {
		logger.LogWarn(ctx, fmt.Sprintf("NowPayments webhook missing order_id client_ip=%s", callerIp))
		c.Status(http.StatusOK)
		return
	}

	handleNowPaymentsEvent(c, &event, callerIp)
}

func handleNowPaymentsEvent(c *gin.Context, event *dto.NowPaymentsWebhookEvent, callerIp string) {
	ctx := c.Request.Context()
	orderId := event.OrderId
	status := event.PaymentStatus

	// Record the provider payment id on every event, not just the final one:
	// NowPayments can only be queried by payment_id, so an order left stalled in
	// a non-final status is otherwise impossible to reconcile later.
	if event.PaymentId != 0 {
		if err := model.SetTopUpProviderPaymentId(orderId, strconv.FormatInt(event.PaymentId, 10)); err != nil {
			logger.LogError(ctx, fmt.Sprintf("NowPayments failed to record payment id trade_no=%s payment_id=%d error=%q", orderId, event.PaymentId, err.Error()))
		}
	}

	switch status {
	case "finished":
		LockOrder(orderId)
		defer UnlockOrder(orderId)

		subPayload := common.GetJsonString(event)
		if err := model.CompleteSubscriptionOrder(orderId, subPayload, model.PaymentProviderNowPayments, ""); err == nil {
			// Completion upserts a success top_ups ledger row under the same
			// trade_no, so falling through to the top-up recharge would always
			// fail on it. A subscription trade_no is never also a top-up.
			logger.LogInfo(ctx, fmt.Sprintf("NowPayments subscription order processed successfully trade_no=%s client_ip=%s", orderId, callerIp))
			c.Status(http.StatusOK)
			return
		} else if !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			logger.LogError(ctx, fmt.Sprintf("NowPayments subscription order processing failed trade_no=%s error=%q", orderId, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		if err := model.RechargeNowPayments(orderId, event.PayCurrency, event.ActuallyPaid); err != nil {
			logger.LogError(ctx, fmt.Sprintf("NowPayments topup processing failed trade_no=%s client_ip=%s error=%q", orderId, callerIp, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		if topUp := model.GetTopUpByTradeNo(orderId); topUp != nil {
			go service.SendTopupConfirmationEmail(topUp.UserId, topUp.Money, event.ActuallyPaid, strings.ToUpper(event.PayCurrency), topUp.TradeNo)
		}
		c.Status(http.StatusOK)

	case "failed", "expired", "refunded":
		LockOrder(orderId)
		defer UnlockOrder(orderId)
		err := model.UpdatePendingTopUpStatus(orderId, model.PaymentProviderNowPayments, common.TopUpStatusFailed)
		if err != nil && !errors.Is(err, model.ErrTopUpNotFound) && !errors.Is(err, model.ErrTopUpStatusInvalid) {
			logger.LogError(ctx, fmt.Sprintf("NowPayments failed to mark failure status trade_no=%s status=%s error=%q", orderId, status, err.Error()))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("NowPayments topup order marked status=%s trade_no=%s", status, orderId))
		}
		c.Status(http.StatusOK)

	case "waiting", "confirming", "confirmed", "sending", "partially_paid":
		logger.LogInfo(ctx, fmt.Sprintf("NowPayments awaiting confirmation trade_no=%s status=%s", orderId, status))
		c.Status(http.StatusOK)

	default:
		logger.LogWarn(ctx, fmt.Sprintf("NowPayments unknown status ignored trade_no=%s status=%s", orderId, status))
		c.Status(http.StatusOK)
	}
}

func getNowPaymentsPayMoney(amount float64, group string) float64 {
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
	return amount * setting.NowPaymentsUnitPrice * topupGroupRatio * discount
}

func getNowPaymentsMinTopup() int64 {
	minTopup := setting.NowPaymentsMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}
