package controller

import (
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
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
	DigistoreOrderFormBase     = "https://www.checkout-ds24.com/product"
	DigistoreBuyUrlApi         = "https://www.digistore24.com/api/createBuyUrl"
	DigistoreTopUpRefPrefix    = "dsref_"
	DigistoreSubOrderRefPrefix = "dssubref_"
	// An IPN names the order and the product, not the payment a refund reverses,
	// so a top-up is keyed by its order item and a subscription by its order.
	digistorePaymentKeyPrefix      = "ds_pay:"
	digistoreSubscriptionKeyPrefix = "ds_sub:"
	digistoreTransactionKeyPrefix  = "ds_txn:"
	digistoreMaxIpnBytes           = 1 << 20
)

// digistoreOrderFormLink opens the product's order form with our trade_no in
// custom, which every IPN of the order carries back. A plain link cannot lock
// the email, so it is only a prefill and the fallback match.
func digistoreOrderFormLink(productId string, tradeNo string, email string) string {
	q := url.Values{}
	q.Set("custom", tradeNo)
	q.Set("currency", "USD")
	q.Set("quantity_locked", "")
	if email != "" {
		q.Set("email", email)
	}
	return fmt.Sprintf("%s/%s?%s", DigistoreOrderFormBase, url.PathEscape(productId), q.Encode())
}

// digistoreCheckoutLink asks createBuyUrl for an order form with the account
// email read only when an API key is set, else (or when the call fails) it
// falls back to the plain prefilled link.
func digistoreCheckoutLink(ctx context.Context, productId string, tradeNo string, email string) string {
	plain := digistoreOrderFormLink(productId, tradeNo, email)
	apiKey := strings.TrimSpace(setting.DigistoreApiKey)
	if apiKey == "" || email == "" {
		return plain
	}
	form := url.Values{}
	form.Set("product_id", productId)
	form.Set("buyer[email]", email)
	form.Set("buyer[readonly_keys]", "email")
	form.Set("tracking[custom]", tradeNo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DigistoreBuyUrlApi, strings.NewReader(form.Encode()))
	if err != nil {
		return plain
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-DS-API-KEY", apiKey)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 createBuyUrl failed, using the plain order form link trade_no=%s error=%q", tradeNo, err.Error()))
		return plain
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	// The API wraps results in {"result":"success","data":{...}}; the spec shows them bare.
	var out struct {
		Result  string `json:"result"`
		Message string `json:"message"`
		Url     string `json:"url"`
		Data    struct {
			Url string `json:"url"`
		} `json:"data"`
	}
	_ = common.Unmarshal(body, &out)
	buyUrl := out.Data.Url
	if buyUrl == "" {
		buyUrl = out.Url
	}
	parsed, perr := url.Parse(buyUrl)
	if resp.StatusCode != http.StatusOK || buyUrl == "" || perr != nil || parsed.Scheme != "https" {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 createBuyUrl returned no url, using the plain order form link trade_no=%s status=%d result=%q message=%q", tradeNo, resp.StatusCode, out.Result, out.Message))
		return plain
	}
	q := parsed.Query()
	q.Set("quantity_locked", "")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func RequestDigistoreAmount(c fuego.ContextWithBody[dto.DigistorePayRequest]) (*dto.Response[string], error) {
	if !setting.DigistoreAllowedFor(dto.UserID(c)) {
		return dto.Fail[string]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string]("Invalid parameters")
	}
	if _, ok := setting.GetDigistoreTopUpProducts()[int(req.Amount)]; !ok {
		return dto.Fail[string]("Digistore24 top-ups are limited to the listed amounts")
	}
	return dto.Ok(fmt.Sprintf("%.2f", float64(req.Amount)))
}

func RequestDigistorePay(c fuego.ContextWithBody[dto.DigistorePayRequest]) (*dto.Response[dto.DigistorePayData], error) {
	if !isDigistoreTopUpEnabled() || !setting.DigistoreAllowedFor(dto.UserID(c)) {
		return dto.Fail[dto.DigistorePayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.DigistorePayData]("Invalid parameters")
	}
	if req.PaymentMethod != model.PaymentMethodDigistore {
		return dto.Fail[dto.DigistorePayData]("Payment channel is not supported")
	}
	productId, ok := setting.GetDigistoreTopUpProducts()[int(req.Amount)]
	if !ok {
		return dto.Fail[dto.DigistorePayData]("Digistore24 top-ups are limited to the listed amounts")
	}

	id := dto.UserID(c)
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		return dto.Fail[dto.DigistorePayData]("Failed to get user information")
	}

	reference := fmt.Sprintf("digistore-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	tradeNo := DigistoreTopUpRefPrefix + common.Sha1([]byte(reference))
	payLink := digistoreCheckoutLink(c.Context(), productId, tradeNo, strings.TrimSpace(user.Email))

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           float64(req.Amount),
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodDigistore,
		PaymentProvider: model.PaymentProviderDigistore,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err = topUp.Insert(); err != nil {
		return dto.Fail[dto.DigistorePayData]("Failed to create order")
	}
	return dto.Ok(dto.DigistorePayData{PayLink: payLink})
}

// SubscriptionRequestDigistorePay opens a recurring Digistore24 product: the
// first payment grants the plan, every later payment rolls it forward.
func SubscriptionRequestDigistorePay(c fuego.ContextWithBody[dto.SubscriptionDigistorePayRequest]) (*dto.Response[dto.DigistorePayData], error) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.DigistorePayData]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	if !setting.DigistoreSubscriptionEnabled || !isDigistoreTopUpEnabled() || !setting.DigistoreAllowedFor(dto.UserID(c)) {
		return dto.Fail[dto.DigistorePayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.DigistorePayData]("Invalid parameters")
	}
	productId, ok := setting.GetDigistoreSubscriptionProducts()[req.PlanId]
	if !ok {
		return dto.Fail[dto.DigistorePayData]("Payment channel is not supported")
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.DigistorePayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.DigistorePayData]("Subscription plan is not enabled")
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return dto.Fail[dto.DigistorePayData]("User does not exist")
	}

	if held, err := model.HasActiveUserSubscriptionForPlan(userId, plan.Id); err != nil {
		return dto.Fail[dto.DigistorePayData](err.Error())
	} else if held {
		return dto.Fail[dto.DigistorePayData]("You already hold this plan and it is still active. A plan can be held once at a time; you can add a different plan next to it.")
	}
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.DigistorePayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.DigistorePayData]("Purchase limit for this plan has been reached")
		}
	}

	reference := fmt.Sprintf("sub-digistore-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	tradeNo := DigistoreSubOrderRefPrefix + common.Sha1([]byte(reference))
	payLink := digistoreCheckoutLink(c.Context(), productId, tradeNo, strings.TrimSpace(user.Email))

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodDigistore,
		PaymentProvider: model.PaymentProviderDigistore,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.DigistorePayData]("Failed to create order")
	}
	return dto.Ok(dto.DigistorePayData{PayLink: payLink})
}

type digistoreField struct {
	key   string
	value string
}

// parseDigistoreIpn decodes the form body keeping field order, which the
// signature of a PHP array field (name[sub]=...) depends on.
func parseDigistoreIpn(body string) ([]digistoreField, error) {
	var fields []digistoreField
	for part := range strings.SplitSeq(body, "&") {
		if part == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			return nil, err
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil, err
		}
		fields = append(fields, digistoreField{key: key, value: value})
	}
	return fields, nil
}

// digistoreSignature is digistore_signature() of Digistore24's ipn_receiver.php
// over what PHP puts in $_POST: names sorted, empty scalars skipped, every
// name=value followed by the passphrase, uppercase hex SHA512.
func digistoreSignature(passphrase string, fields []digistoreField, caseInsensitive bool) string {
	type entry struct {
		scalar string
		array  []digistoreField
	}
	byName := map[string]*entry{}
	var names []string
	for _, f := range fields {
		name, _, isArray := strings.Cut(f.key, "[")
		// PHP turns dots and spaces in a variable name into underscores.
		name = strings.NewReplacer(".", "_", " ", "_").Replace(name)
		if name == "sha_sign" || name == "SHASIGN" {
			continue
		}
		e, ok := byName[name]
		if !ok {
			e = &entry{}
			byName[name] = e
			names = append(names, name)
		}
		if isArray {
			e.array = append(e.array, digistoreField{key: name + f.key[len(name):], value: f.value})
		} else {
			e.scalar = f.value
		}
	}
	slices.SortStableFunc(names, func(a, b string) int {
		if caseInsensitive {
			return strings.Compare(strings.ToUpper(a), strings.ToUpper(b))
		}
		return strings.Compare(a, b)
	})
	var sb strings.Builder
	for _, name := range names {
		e := byName[name]
		if len(e.array) > 0 {
			for _, f := range e.array {
				sb.WriteString(f.key + "=" + f.value + passphrase)
			}
			continue
		}
		if e.scalar == "" {
			continue
		}
		sb.WriteString(name + "=" + e.scalar + passphrase)
	}
	sum := sha512.Sum512([]byte(sb.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// verifyDigistoreSignature accepts the receiver script's case sensitive sort and
// the guide's case insensitive one; they differ only for mixed case names.
func verifyDigistoreSignature(fields []digistoreField, sig string, passphrase string) bool {
	sig = strings.ToUpper(strings.TrimSpace(sig))
	if passphrase == "" || sig == "" {
		return false
	}
	for _, ci := range []bool{false, true} {
		if subtle.ConstantTimeCompare([]byte(sig), []byte(digistoreSignature(passphrase, fields, ci))) == 1 {
			return true
		}
	}
	return false
}

// digistoreAnswer ends an IPN. Digistore24 counts only a plain "OK" as
// delivered and retries anything else for days.
func digistoreAnswer(c *gin.Context, ok bool) {
	if ok {
		c.String(http.StatusOK, "OK")
		return
	}
	c.String(http.StatusInternalServerError, "ERROR")
}

// digistoreIpn is one verified IPN. live is false for an api_mode=test call,
// which only reaches the handlers while test mode is on.
type digistoreIpn struct {
	f    url.Values
	live bool
	raw  string
}

// testBlocked reports a test IPN for an account outside the test allowlist.
func (ipn digistoreIpn) testBlocked(userId int) bool {
	return !ipn.live && !setting.DigistoreTestUser(userId)
}

func (ipn digistoreIpn) amount() float64 {
	// Prices are net with VAT added on top, and the plan price is the net one.
	for _, k := range []string{"amount_netto", "amount_brutto", "transaction_amount"} {
		if v, err := strconv.ParseFloat(ipn.f.Get(k), 64); err == nil && v != 0 {
			return v
		}
	}
	return 0
}

func DigistoreWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	callerIp := c.ClientIP()
	if !isDigistoreTopUpEnabled() {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 IPN received while the channel is disabled, rejecting client_ip=%s", callerIp))
		c.String(http.StatusForbidden, "ERROR")
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, digistoreMaxIpnBytes))
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Digistore24 IPN failed to read payload client_ip=%s error=%q", callerIp, err.Error()))
		c.String(http.StatusBadRequest, "ERROR")
		return
	}
	fields, err := parseDigistoreIpn(string(bodyBytes))
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Digistore24 IPN failed to parse payload client_ip=%s error=%q", callerIp, err.Error()))
		c.String(http.StatusBadRequest, "ERROR")
		return
	}
	form := url.Values{}
	for _, f := range fields {
		form.Set(f.key, f.value)
	}
	if !verifyDigistoreSignature(fields, form.Get("sha_sign"), setting.DigistoreIpnPassphrase) {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 IPN signature verification failed client_ip=%s content_type=%q", callerIp, c.ContentType()))
		c.String(http.StatusUnauthorized, "ERROR: invalid sha signature")
		return
	}

	event := form.Get("event")
	apiMode := form.Get("api_mode")
	logger.LogInfo(ctx, fmt.Sprintf("Digistore24 IPN received event=%s api_mode=%s order_id=%s product_id=%s transaction_id=%s client_ip=%s", event, apiMode, form.Get("order_id"), form.Get("product_id"), form.Get("transaction_id"), callerIp))
	if event == "connection_test" {
		digistoreAnswer(c, true)
		return
	}

	live := apiMode == "live"
	if setting.DigistoreTestMode && live {
		// Left undelivered on purpose: Digistore24 retries for about ten days, so a
		// real payment made during testing is applied once test mode is off.
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 live IPN deferred while test mode is on event=%s order_id=%s", event, form.Get("order_id")))
		c.String(http.StatusServiceUnavailable, "ERROR: test mode")
		return
	}
	if !setting.DigistoreTestMode && !live {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 test IPN ignored while test mode is off event=%s order_id=%s api_mode=%s", event, form.Get("order_id"), apiMode))
		digistoreAnswer(c, true)
		return
	}

	ipn := digistoreIpn{f: form, live: live, raw: string(bodyBytes)}
	switch event {
	case "on_payment":
		handleDigistorePayment(c, ipn)
	case "on_refund":
		handleDigistoreReversal(c, ipn, false)
	case "on_chargeback":
		handleDigistoreReversal(c, ipn, true)
	case "last_paid_day":
		handleDigistorePaidPeriodEnd(c, ipn)
	default:
		// on_rebill_cancelled only stops the next charge, the paid period stays;
		// last_paid_day is what ends it. Missed and denied payments are retried by
		// Digistore24 and end in last_paid_day too.
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 IPN acknowledged without action event=%s order_id=%s", event, form.Get("order_id")))
		digistoreAnswer(c, true)
	}
}

func handleDigistorePayment(c *gin.Context, ipn digistoreIpn) {
	ctx := c.Request.Context()
	orderId := ipn.f.Get("order_id")
	productId := ipn.f.Get("product_id")
	custom := strings.TrimSpace(ipn.f.Get("custom"))
	email := ipn.f.Get("email")
	seq, _ := strconv.Atoi(ipn.f.Get("pay_sequence_no"))
	if orderId == "" || productId == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 on_payment without order or product body=%q", ipn.raw))
		digistoreAnswer(c, true)
		return
	}
	// Every product is one fixed priced item; a changed quantity is not guessed at.
	if q := strings.TrimSpace(ipn.f.Get("quantity")); q != "" && q != "1" {
		logger.LogError(ctx, fmt.Sprintf("Digistore24 payment with quantity %s not credited, needs a manual decision order_id=%s product_id=%s custom=%s body=%q", q, orderId, productId, custom, ipn.raw))
		digistoreAnswer(c, true)
		return
	}

	for amount, pid := range setting.GetDigistoreTopUpProducts() {
		if pid != productId {
			continue
		}
		itemKey := digistorePaymentKeyPrefix + orderId + "/" + productId
		if model.ProviderPaymentProcessed(itemKey) {
			logger.LogInfo(ctx, fmt.Sprintf("Digistore24 payment already applied order_id=%s product_id=%s", orderId, productId))
			digistoreAnswer(c, true)
			return
		}
		if seq > 1 {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 top-up product billed again as a rebilling, not credited order_id=%s product_id=%s pay_sequence_no=%d body=%q", orderId, productId, seq, ipn.raw))
			digistoreAnswer(c, true)
			return
		}
		// custom is buyer editable, so it only counts for an open Digistore24
		// top-up of exactly the amount this product sells.
		var topUp *model.TopUp
		if custom != "" {
			if t := model.GetTopUpByTradeNo(custom); t != nil && t.PaymentProvider == model.PaymentProviderDigistore &&
				t.Status == common.TopUpStatusPending && t.Amount == int64(amount) {
				topUp = t
			}
		}
		if topUp == nil {
			if tradeNo, err := model.FindPendingTopUpByEmail(model.PaymentProviderDigistore, email, int64(amount)); err == nil {
				topUp = model.GetTopUpByTradeNo(tradeNo)
			}
		}
		if topUp == nil {
			logger.LogWarn(ctx, fmt.Sprintf("Digistore24 top-up payment matched no open order order_id=%s amount=%d body=%q", orderId, amount, ipn.raw))
			digistoreAnswer(c, true)
			return
		}
		if ipn.testBlocked(topUp.UserId) {
			logger.LogWarn(ctx, fmt.Sprintf("Digistore24 test payment for a user outside the test allowlist ignored order_id=%s user_id=%d", orderId, topUp.UserId))
			digistoreAnswer(c, true)
			return
		}
		LockOrder(topUp.TradeNo)
		defer UnlockOrder(topUp.TradeNo)
		if err := model.RechargeHostedCheckout(model.PaymentProviderDigistore, "Digistore24", topUp.TradeNo, itemKey); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 top-up processing failed trade_no=%s order_id=%s error=%q", topUp.TradeNo, orderId, err.Error()))
			digistoreAnswer(c, false)
			return
		}
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 top-up processed trade_no=%s order_id=%s api_mode=%s", topUp.TradeNo, orderId, ipn.f.Get("api_mode")))
		digistoreAnswer(c, true)
		return
	}

	for planId, pid := range setting.GetDigistoreSubscriptionProducts() {
		if pid != productId {
			continue
		}
		subKey := digistoreSubscriptionKeyPrefix + orderId
		if seq > 1 {
			handleDigistoreRenewal(c, ipn, subKey)
			return
		}
		if model.GetSubscriptionOrderByProviderPaymentId(subKey) != nil {
			logger.LogInfo(ctx, fmt.Sprintf("Digistore24 subscription payment already applied order_id=%s", orderId))
			digistoreAnswer(c, true)
			return
		}
		var order *model.SubscriptionOrder
		if custom != "" {
			if o := model.GetSubscriptionOrderByTradeNo(custom); o != nil && o.PaymentProvider == model.PaymentProviderDigistore &&
				o.Status == common.TopUpStatusPending && o.PlanId == planId {
				order = o
			}
		}
		if order == nil {
			if tradeNo, err := model.FindPendingSubscriptionOrderByEmail(model.PaymentProviderDigistore, email, planId); err == nil {
				order = model.GetSubscriptionOrderByTradeNo(tradeNo)
			}
		}
		if order == nil {
			logger.LogWarn(ctx, fmt.Sprintf("Digistore24 subscription payment matched no open order order_id=%s plan_id=%d body=%q", orderId, planId, ipn.raw))
			digistoreAnswer(c, true)
			return
		}
		if ipn.testBlocked(order.UserId) {
			logger.LogWarn(ctx, fmt.Sprintf("Digistore24 test subscription payment for a user outside the test allowlist ignored order_id=%s user_id=%d", orderId, order.UserId))
			digistoreAnswer(c, true)
			return
		}
		tradeNo := order.TradeNo
		LockOrder(tradeNo)
		defer UnlockOrder(tradeNo)
		if err := model.CompleteSubscriptionOrder(tradeNo, ipn.raw, model.PaymentProviderDigistore, model.PaymentMethodDigistore); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 subscription order processing failed trade_no=%s error=%q", tradeNo, err.Error()))
			digistoreAnswer(c, false)
			return
		}
		// Renewals, refunds and last_paid_day name only the Digistore24 order, so
		// the order and the granted row both carry its id.
		if err := model.SetSubscriptionOrderProviderPaymentId(tradeNo, subKey); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 failed to record order id trade_no=%s order_id=%s error=%q", tradeNo, orderId, err.Error()))
		}
		if _, _, err := model.RenewUserSubscriptionByCreem(model.CreemRenewalInput{
			ReferenceId:            tradeNo,
			ProviderSubscriptionId: subKey,
			Provider:               model.PaymentProviderDigistore,
		}); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 failed to link order id trade_no=%s order_id=%s error=%q", tradeNo, orderId, err.Error()))
		}
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 subscription order processed trade_no=%s order_id=%s api_mode=%s", tradeNo, orderId, ipn.f.Get("api_mode")))
		digistoreAnswer(c, true)
		return
	}

	logger.LogWarn(ctx, fmt.Sprintf("Digistore24 payment for an unmapped product ignored order_id=%s product_id=%s", orderId, productId))
	digistoreAnswer(c, true)
}

func handleDigistoreRenewal(c *gin.Context, ipn digistoreIpn, subKey string) {
	ctx := c.Request.Context()
	orderId := ipn.f.Get("order_id")
	txnId := ipn.f.Get("transaction_id")
	order := model.GetSubscriptionOrderByProviderPaymentId(subKey)
	if order == nil || txnId == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 renewal matched no subscription order_id=%s transaction_id=%s body=%q", orderId, txnId, ipn.raw))
		digistoreAnswer(c, true)
		return
	}
	if ipn.testBlocked(order.UserId) {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 test renewal for a user outside the test allowlist ignored order_id=%s user_id=%d", orderId, order.UserId))
		digistoreAnswer(c, true)
		return
	}
	txnKey := digistoreTransactionKeyPrefix + txnId
	LockOrder(subKey)
	defer UnlockOrder(subKey)
	// Without Redis the renewal path dedupes only its ledger row, after the
	// subscription was already rolled forward.
	if model.GetSubscriptionOrderByTradeNo(model.PaymentProviderDigistore+"_renewal:"+txnKey) != nil {
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 renewal already applied order_id=%s transaction_id=%s", orderId, txnId))
		digistoreAnswer(c, true)
		return
	}
	userId, subId, err := model.RenewUserSubscriptionByCreem(model.CreemRenewalInput{
		ReferenceId:            order.TradeNo,
		ProviderSubscriptionId: subKey,
		LastTransactionId:      txnKey,
		ProviderPayload:        ipn.raw,
		Money:                  ipn.amount(),
		Provider:               model.PaymentProviderDigistore,
	})
	if err != nil {
		if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			logger.LogWarn(ctx, fmt.Sprintf("Digistore24 renewal matched no subscription order_id=%s transaction_id=%s body=%q", orderId, txnId, ipn.raw))
			digistoreAnswer(c, true)
			return
		}
		logger.LogError(ctx, fmt.Sprintf("Digistore24 renewal processing failed order_id=%s transaction_id=%s error=%q", orderId, txnId, err.Error()))
		digistoreAnswer(c, false)
		return
	}
	logger.LogInfo(ctx, fmt.Sprintf("Digistore24 renewal processed order_id=%s transaction_id=%s user_id=%d subscription_id=%d", orderId, txnId, userId, subId))
	digistoreAnswer(c, true)
}

// handleDigistoreReversal takes back what a refunded or charged back order
// bought. A chargeback also locks the account, as with the other card providers.
func handleDigistoreReversal(c *gin.Context, ipn digistoreIpn, dispute bool) {
	ctx := c.Request.Context()
	kind := "refund"
	reason := "refund.created"
	if dispute {
		kind = "chargeback"
		reason = "dispute.created"
	}
	orderId := ipn.f.Get("order_id")
	productId := ipn.f.Get("product_id")
	txnId := ipn.f.Get("transaction_id")
	itemKey := digistorePaymentKeyPrefix + orderId + "/" + productId
	subKey := digistoreSubscriptionKeyPrefix + orderId

	topUp := model.GetTopUpByProviderPaymentId(model.PaymentProviderDigistore, itemKey)
	var order *model.SubscriptionOrder
	userId := 0
	switch {
	case topUp != nil:
		userId = topUp.UserId
	default:
		order = model.GetSubscriptionOrderByProviderPaymentId(subKey)
		if order == nil {
			logger.LogWarn(ctx, fmt.Sprintf("Digistore24 %s matched nothing order_id=%s product_id=%s body=%q", kind, orderId, productId, ipn.raw))
			digistoreAnswer(c, true)
			return
		}
		userId = order.UserId
	}
	if ipn.testBlocked(userId) {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 test %s for a user outside the test allowlist ignored order_id=%s user_id=%d", kind, orderId, userId))
		digistoreAnswer(c, true)
		return
	}

	if topUp != nil {
		LockOrder(itemKey)
		res, err := model.ReverseTopUp(model.TopUpReversalInput{
			Provider: model.PaymentProviderDigistore,
			EventId:  txnId,
			OrderId:  itemKey,
			Dispute:  dispute,
		})
		UnlockOrder(itemKey)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 %s processing failed order_id=%s error=%q", kind, orderId, err.Error()))
			digistoreAnswer(c, false)
			return
		}
		if !res.AlreadyDone {
			note := "Digistore24 {{kind}}: {{quota}} quota reversed, top-up {{top_up_id}}, order {{order_id}}, transaction {{transaction_id}}"
			model.RecordLog(userId, model.LogTypeRefund, common.NewMessage(note, map[string]any{"kind": kind, "quota": res.QuotaRemoved, "top_up_id": res.TopUpId, "order_id": orderId, "transaction_id": txnId}))
		}
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 %s applied to top-up order_id=%s user_id=%d top_up_id=%d quota_removed=%d already=%t", kind, orderId, userId, res.TopUpId, res.QuotaRemoved, res.AlreadyDone))
	} else {
		subUserId, subId, err := model.TerminateUserSubscriptionByCreem(model.CreemTerminationInput{
			ReferenceId:            order.TradeNo,
			ProviderSubscriptionId: subKey,
			Reason:                 reason,
		})
		if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 %s subscription termination failed order_id=%s error=%q", kind, orderId, err.Error()))
			digistoreAnswer(c, false)
			return
		}
		if subId != 0 {
			service.ClearTerminatedSubscriptionPerks(subUserId, true)
		}
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 %s on subscription order_id=%s user_id=%d subscription_id=%d", kind, orderId, subUserId, subId))
	}

	if dispute && userId > 0 {
		if err := model.DisableUserForFraud(userId); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Digistore24 chargeback failed to disable user order_id=%s user_id=%d error=%q", orderId, userId, err.Error()))
			digistoreAnswer(c, false)
			return
		}
	}
	digistoreAnswer(c, true)
}

// handleDigistorePaidPeriodEnd ends a subscription on last_paid_day, which
// Digistore24 sends once the last paid billing period is over (after a
// cancelled rebilling, a refund, or payment retries that all failed).
func handleDigistorePaidPeriodEnd(c *gin.Context, ipn digistoreIpn) {
	ctx := c.Request.Context()
	orderId := ipn.f.Get("order_id")
	subKey := digistoreSubscriptionKeyPrefix + orderId
	order := model.GetSubscriptionOrderByProviderPaymentId(subKey)
	if order == nil {
		logger.LogInfo(ctx, fmt.Sprintf("Digistore24 last_paid_day for no subscription order_id=%s", orderId))
		digistoreAnswer(c, true)
		return
	}
	if ipn.testBlocked(order.UserId) {
		logger.LogWarn(ctx, fmt.Sprintf("Digistore24 test last_paid_day for a user outside the test allowlist ignored order_id=%s user_id=%d", orderId, order.UserId))
		digistoreAnswer(c, true)
		return
	}
	userId, subId, err := model.TerminateUserSubscriptionByCreem(model.CreemTerminationInput{
		ReferenceId:            order.TradeNo,
		ProviderSubscriptionId: subKey,
		Reason:                 "subscription.expired",
	})
	if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(ctx, fmt.Sprintf("Digistore24 last_paid_day termination failed order_id=%s error=%q", orderId, err.Error()))
		digistoreAnswer(c, false)
		return
	}
	if subId != 0 {
		service.ClearTerminatedSubscriptionPerks(userId, false)
	}
	logger.LogInfo(ctx, fmt.Sprintf("Digistore24 paid period ended order_id=%s user_id=%d subscription_id=%d", orderId, userId, subId))
	digistoreAnswer(c, true)
}
