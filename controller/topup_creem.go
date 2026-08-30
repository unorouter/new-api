package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
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
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

const (
	PaymentMethodCreem   = "creem"
	CreemSignatureHeader = "creem-signature"

	// Bounds for a custom (pay-what-you-want) Creem top-up, in whole currency
	// units, expressed as the CREDIT requested rather than the charge: the
	// processing fee is added on top, so a 1.00 top-up bills 1.50. Creem's own
	// floor is 1.00 (custom_price is sent in cents and their API rejects
	// anything under 100), which the surcharge keeps us above. The ceiling is
	// ours; Creem's hard limit is custom_price 99999999 (999,999.99).
	creemMinTopUp = 1.0
	creemMaxTopUp = 100000.0
)

// persistCreemCustomerID writes event.Object.Order.Customer onto the user's
// creem_customer column so we can later call Creem's POST /v1/customers/billing
// to mint per-user portal links. Idempotent, best-effort - a failure here
// does NOT interrupt the webhook flow.
//
// Resolution: look up the user via the reference_id (our trade_no). Both
// subscription_orders and top_ups carry that trade_no + user_id, so whichever
// exists, we can back-fill the user row from it.
func persistCreemCustomerID(event *dto.CreemWebhookEvent) {
	customerID := event.Object.Order.Customer
	if customerID == "" {
		return
	}
	referenceId := event.Object.RequestId
	if referenceId == "" {
		return
	}

	var userId int
	var subOrder model.SubscriptionOrder
	if err := model.DB.Where("trade_no = ?", referenceId).First(&subOrder).Error; err == nil {
		userId = subOrder.UserId
	} else {
		topUp := model.GetTopUpByTradeNo(referenceId)
		if topUp != nil {
			userId = topUp.UserId
		}
	}
	if userId == 0 {
		return
	}

	if err := model.DB.Model(&model.User{}).
		Where("id = ? AND (creem_customer IS NULL OR creem_customer = '' OR creem_customer <> ?)", userId, customerID).
		Update("creem_customer", customerID).Error; err != nil {
		common.SysLog("creem webhook: failed to persist customer id: " + err.Error())
	}
}

// 生成HMAC-SHA256签名
func generateCreemSignature(payload string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}

// 验证Creem webhook签名
func verifyCreemSignature(payload string, signature string, secret string) bool {
	if secret == "" {
		logger.LogWarn(context.Background(), fmt.Sprintf("Creem webhook secret not configured test_mode=%t signature=%q body=%q", setting.CreemTestMode, signature, payload))
		if setting.CreemTestMode {
			logger.LogInfo(context.Background(), fmt.Sprintf("Creem webhook signature verification skipped reason=test_mode signature=%q body=%q", signature, payload))
			return true
		}
		return false
	}

	expectedSignature := generateCreemSignature(payload, secret)
	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

func RequestCreemPay(c fuego.ContextWithBody[dto.CreemPayRequest]) (*dto.Response[dto.CreemPayData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if req.PaymentMethod != PaymentMethodCreem {
		return dto.Fail[dto.CreemPayData]("Payment channel is not supported")
	}

	if req.ProductId == "" {
		return dto.Fail[dto.CreemPayData]("Please select a product")
	}

	// 解析产品列表
	var products []dto.CreemProduct
	err = common.Unmarshal([]byte(setting.CreemProducts), &products)
	if err != nil {
		log.Println("failed to parse Creem product list", err)
		return dto.Fail[dto.CreemPayData]("Product configuration error")
	}

	// 查找对应的产品
	var selectedProduct *dto.CreemProduct
	for _, product := range products {
		if product.ProductId == req.ProductId {
			selectedProduct = &product
			break
		}
	}

	if selectedProduct == nil {
		return dto.Fail[dto.CreemPayData]("Product does not exist")
	}

	// Pay-what-you-want: req.Amount overrides the product's price for this one
	// checkout. Quota scales off the product's own price/quota ratio so a
	// custom amount credits proportionally, and the TopUp row we insert below
	// is what the webhook credits from - the product is only ever used to build
	// the checkout, so nothing downstream needs to know this was custom.
	payAmount := selectedProduct.Price
	payQuota := selectedProduct.Quota
	customPriceCents := 0
	if req.Amount > 0 {
		if req.Amount < creemMinTopUp {
			return dto.Fail[dto.CreemPayData](fmt.Sprintf("Top-up amount cannot be less than %v", creemMinTopUp))
		}
		if req.Amount > creemMaxTopUp {
			return dto.Fail[dto.CreemPayData](fmt.Sprintf("Top-up amount cannot exceed %v", creemMaxTopUp))
		}
		if selectedProduct.Price <= 0 {
			return dto.Fail[dto.CreemPayData]("Product configuration error")
		}
		payAmount = req.Amount
		payQuota = int64(math.Round(float64(selectedProduct.Quota) * (req.Amount / selectedProduct.Price)))
		customPriceCents = int(math.Round(req.Amount * 100))
	}

	// Preset products keep the price configured in Creem: their tiers are
	// priced deliberately, so neither the discount nor the fee applies. Both
	// only reshape a pay-what-you-want amount.
	billedMoney := payAmount
	if req.Amount > 0 {
		// Every other gateway honors the configured amount discount; Creem did
		// not, so a tier priced at 0.9 in payment_setting.amount_discount
		// silently charged full price. Quota stays keyed to the pre-discount
		// amount: the discount is a price cut, not a smaller top-up.
		if discount := creemAmountDiscount(payAmount); discount < 1 {
			payAmount = math.Round(payAmount*discount*100) / 100
		}
		// The buyer covers the processing fee, so the charge sits above the
		// credit: a 1.00 top-up bills 1.50 and still grants 1.00. payQuota is
		// already fixed above and deliberately untouched here, and TopUp.Money
		// keeps the credited value because that is what becomes quota.
		billedMoney = applyCreemFeeSurcharge(payAmount)
		customPriceCents = int(math.Round(billedMoney * 100))
	}

	id := dto.UserID(c)
	// Check the quota this order actually credits (payQuota), not the product's
	// listed quota: a pay-what-you-want amount scales it.
	if err := checkCreditedQuota(id, decimal.NewFromInt(payQuota)); err != nil {
		return dto.Fail[dto.CreemPayData](err.Error())
	}

	user, _ := model.GetUserById(id, false)

	// 生成唯一的订单引用ID
	reference := fmt.Sprintf("creem-api-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))

	// 先创建订单记录，使用产品配置的金额和充值额度
	// (payQuota/payAmount equal the product's own values unless this is a
	// custom-amount top-up, in which case they are scaled above. The webhook
	// credits from THIS row, not from the product.)
	topUp := &model.TopUp{
		UserId:          id,
		Amount:          payQuota,    // 充值额度
		Money:           payAmount,   // credited, and what the webhook turns into quota
		ChargedMoney:    billedMoney, // what Creem actually bills, fee included
		TradeNo:         referenceId,
		PaymentMethod:   PaymentMethodCreem,
		PaymentProvider: model.PaymentProviderCreem,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		log.Printf("failed to create Creem order: %s", err.Error())
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}

	// 创建支付链接，传入用户邮箱
	checkoutUrl, err := genCreemLink(referenceId, selectedProduct, user.Email, user.Username, customPriceCents)
	if err != nil {
		log.Printf("failed to get Creem payment link: %s", err.Error())
		return dto.Fail[dto.CreemPayData](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}

	log.Printf("Creem order created - UserID: %d, OrderNo: %s, Product: %s, Quota: %d, PayAmount: %.2f, CustomPriceCents: %d", id, referenceId, selectedProduct.Name, payQuota, payAmount, customPriceCents)

	return dto.Ok(dto.CreemPayData{
		CheckoutUrl: checkoutUrl,
		OrderId:     referenceId,
	})
}

// applyCreemFeeSurcharge returns what to bill for a top-up that credits money.
// Rounded UP to the cent so the fee is never silently undercharged, and never
// below the credited amount in case the settings go negative.
func applyCreemFeeSurcharge(money float64) float64 {
	if money <= 0 {
		return money
	}
	if setting.CreemFeeThreshold > 0 && money > setting.CreemFeeThreshold {
		return money
	}
	billed := money*(1+setting.CreemFeePercent) + setting.CreemFeeFixed
	billed = math.Ceil(billed*100) / 100
	if billed < money {
		return money
	}
	return billed
}

// creemAmountDiscount returns the configured multiplier for a top-up amount,
// or 1 when no discount applies. Keyed by whole units, matching how the other
// gateways read payment_setting.amount_discount.
func creemAmountDiscount(amount float64) float64 {
	ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]
	if !ok || ds <= 0 || ds >= 1 {
		return 1
	}
	return ds
}

// 新的Creem Webhook结构体，匹配实际的webhook数据格式

func CreemWebhook(c *gin.Context) {
	// 读取body内容用于打印，同时保留原始数据供后续使用
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook failed to read request body path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// 获取签名头
	signature := c.GetHeader(CreemSignatureHeader)

	// 打印关键信息（避免输出完整敏感payload）
	log.Printf("Creem Webhook - URI: %s", c.Request.RequestURI)
	if setting.CreemTestMode {
		log.Printf("Creem Webhook - Signature: %s , Body: %s", signature, bodyBytes)
	} else if signature == "" {
		log.Printf("Creem webhook missing signature header")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// 验证签名
	if !verifyCreemSignature(string(bodyBytes), signature, setting.CreemWebhookSecret) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook signature verification failed path=%q client_ip=%s signature=%q body=%q", c.Request.RequestURI, c.ClientIP(), signature, string(bodyBytes)))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook signature verification succeeded path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))

	// 重新设置body供后续的ShouldBindJSON使用
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// 解析新格式的webhook数据
	var webhookEvent dto.CreemWebhookEvent
	if err := c.ShouldBindJSON(&webhookEvent); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook parse failed path=%q client_ip=%s error=%q body=%q", c.Request.RequestURI, c.ClientIP(), err.Error(), string(bodyBytes)))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook parse succeeded event_type=%s event_id=%s request_id=%s order_id=%s order_status=%s", webhookEvent.EventType, webhookEvent.Id, webhookEvent.Object.RequestId, webhookEvent.Object.Order.Id, webhookEvent.Object.Order.Status))

	// 根据事件类型处理不同的webhook
	switch webhookEvent.EventType {
	case "checkout.completed":
		handleCheckoutCompleted(c, &webhookEvent)
	case "subscription.paid":
		// Fires on the initial charge AND every recurring renewal. The initial
		// charge is already handled by checkout.completed, so this path is the
		// renewal driver (checkout.completed does not fire on auto-renewal).
		handleSubscriptionPaid(c, &webhookEvent, string(bodyBytes))
	case "refund.created", "subscription.canceled", "subscription.expired":
		// Creem delivers all three already; until this branch existed they fell
		// through to the default log, so a refunded customer kept a live
		// subscription and its full quota pool until end_time.
		handleSubscriptionTerminated(c, &webhookEvent, string(bodyBytes))
	default:
		// Log the FULL raw payload for any event we don't handle yet, so the
		// exact shape (e.g. subscription.active / past_due) is captured for
		// follow-up instead of silently dropped.
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook ignored event event_type=%s event_id=%s body=%q", webhookEvent.EventType, webhookEvent.Id, string(bodyBytes)))
		c.Status(http.StatusOK)
	}
}

// handleSubscriptionTerminated ends a subscription when Creem reports a refund,
// cancellation or expiry, so access stops with the money rather than running to
// the original end_time.
func handleSubscriptionTerminated(c *gin.Context, event *dto.CreemWebhookEvent, rawBody string) {
	persistCreemCustomerID(event)

	referenceId := event.ReferenceID()
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem %s received event_id=%s sub_id=%s reference_id=%s customer=%s product=%s", event.EventType, event.Id, event.Object.Id, referenceId, event.Object.Customer.Id, event.Object.Product.Id))

	lockKey := event.Object.Id
	if lockKey == "" {
		lockKey = referenceId
	}
	if lockKey != "" {
		LockOrder(lockKey)
		defer UnlockOrder(lockKey)
	}

	userId, subId, err := model.TerminateUserSubscriptionByCreem(model.CreemTerminationInput{
		ReferenceId:     referenceId,
		CreemCustomerId: event.Object.Customer.Id,
		CreemProductId:  event.Object.Product.Id,
		Reason:          event.EventType,
	})
	if err != nil {
		if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			// Not mappable to a local user. Log the payload for reconciliation and
			// 200 so Creem stops retrying.
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem %s could not match user event_id=%s sub_id=%s reference_id=%s customer=%s body=%q", event.EventType, event.Id, event.Object.Id, referenceId, event.Object.Customer.Id, rawBody))
			c.Status(http.StatusOK)
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem %s processing failed event_id=%s sub_id=%s error=%q", event.EventType, event.Id, event.Object.Id, err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if subId == 0 {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem %s no active subscription to end event_id=%s user_id=%d", event.EventType, event.Id, userId))
	} else {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem %s subscription ended event_id=%s user_id=%d subscription_id=%d", event.EventType, event.Id, userId, subId))
	}
	c.Status(http.StatusOK)
}

// handleSubscriptionPaid extends a recurring subscription on each successful
// Creem renewal charge. Idempotent (dedup on last_transaction_id in the model).
func handleSubscriptionPaid(c *gin.Context, event *dto.CreemWebhookEvent, rawBody string) {
	persistCreemCustomerID(event)

	referenceId := event.ReferenceID()
	txId := event.Object.LastTransactionId
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem subscription.paid received event_id=%s sub_id=%s reference_id=%s last_transaction_id=%s customer=%s product=%s status=%s", event.Id, event.Object.Id, referenceId, txId, event.Object.Customer.Id, event.Object.Product.Id, event.Object.Status))

	lockKey := txId
	if lockKey == "" {
		lockKey = event.Object.Id
	}
	LockOrder(lockKey)
	defer UnlockOrder(lockKey)

	// Prefer the real charged amount from the renewal transaction; fall back to
	// the product price when the transaction block is absent.
	money := float64(event.Object.LastTransaction.AmountPaid) / 100
	if money == 0 {
		money = float64(event.Object.Product.Price) / 100
	}
	userId, subId, err := model.RenewUserSubscriptionByCreem(model.CreemRenewalInput{
		ReferenceId:       referenceId,
		CreemCustomerId:   event.Object.Customer.Id,
		CreemProductId:    event.Object.Product.Id,
		LastTransactionId: txId,
		CreemOrderId:      event.Object.LastTransaction.Order,
		ProviderPayload:   common.GetJsonString(event),
		Money:             money,
	})
	if err != nil {
		if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			// Could not map this renewal to a user/plan. Log the full payload for
			// manual reconciliation and 200 so Creem stops retrying.
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem subscription.paid could not match user event_id=%s sub_id=%s reference_id=%s customer=%s product=%s body=%q", event.Id, event.Object.Id, referenceId, event.Object.Customer.Id, event.Object.Product.Id, rawBody))
			c.Status(http.StatusOK)
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem subscription.paid processing failed event_id=%s sub_id=%s error=%q", event.Id, event.Object.Id, err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem subscription.paid renewal succeeded event_id=%s user_id=%d subscription_id=%d last_transaction_id=%s", event.Id, userId, subId, txId))
	c.Status(http.StatusOK)
}

// 处理支付完成事件
func handleCheckoutCompleted(c *gin.Context, event *dto.CreemWebhookEvent) {
	// 验证订单状态
	if event.Object.Order.Status != "paid" {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem order status not paid, ignoring request_id=%s order_id=%s order_status=%s", event.Object.RequestId, event.Object.Order.Id, event.Object.Order.Status))
		c.Status(http.StatusOK)
		return
	}

	// 获取引用ID（这是我们创建订单时传递的request_id）
	referenceId := event.Object.RequestId
	if referenceId == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook missing request_id event_id=%s order_id=%s", event.Id, event.Object.Order.Id))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Persist the Creem customer ID to the user row so we can call
	// /v1/customers/billing later for per-user portal links. Best-effort - 	// we don't block the webhook on this.
	persistCreemCustomerID(event)

	// Try complete subscription order first
	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	if err := model.CompleteSubscriptionOrder(referenceId, common.GetJsonString(event), model.PaymentProviderCreem, ""); err == nil {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem subscription order processed successfully trade_no=%s creem_order_id=%s", referenceId, event.Object.Order.Id))
		c.Status(http.StatusOK)
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem subscription order processing failed trade_no=%s creem_order_id=%s error=%q", referenceId, event.Object.Order.Id, err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// 验证订单类型，目前只处理一次性付款（充值）
	if event.Object.Order.Type != "onetime" {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem order type not currently supported, ignoring request_id=%s creem_order_id=%s order_type=%s", referenceId, event.Object.Order.Id, event.Object.Order.Type))
		c.Status(http.StatusOK)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem payment completed callback trade_no=%s creem_order_id=%s amount_paid=%d currency=%s product_name=%q customer_email=%q customer_name=%q", referenceId, event.Object.Order.Id, event.Object.Order.AmountPaid, event.Object.Order.Currency, event.Object.Product.Name, event.Object.Customer.Email, event.Object.Customer.Name))

	// 查询本地订单确认存在
	topUp := model.GetTopUpByTradeNo(referenceId)
	if topUp == nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem topup order does not exist trade_no=%s creem_order_id=%s", referenceId, event.Object.Order.Id))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if topUp.Status != common.TopUpStatusPending {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem topup order status is not pending, ignoring trade_no=%s status=%s creem_order_id=%s", referenceId, topUp.Status, event.Object.Order.Id))
		c.Status(http.StatusOK) // 已处理过的订单，返回成功避免重复处理
		return
	}

	// 处理充值，传入客户邮箱和姓名信息
	customerEmail := event.Object.Customer.Email
	customerName := event.Object.Customer.Name

	// 防护性检查，确保邮箱和姓名不为空字符串
	if customerEmail == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem callback customer email is empty trade_no=%s creem_order_id=%s", referenceId, event.Object.Order.Id))
	}
	if customerName == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem callback customer name is empty trade_no=%s creem_order_id=%s", referenceId, event.Object.Order.Id))
	}

	err := model.RechargeCreem(referenceId, customerEmail, customerName)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem topup processing failed trade_no=%s creem_order_id=%s client_ip=%s error=%q", referenceId, event.Object.Order.Id, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem topup succeeded trade_no=%s creem_order_id=%s quota=%d money=%.2f client_ip=%s", referenceId, event.Object.Order.Id, topUp.Amount, topUp.Money, c.ClientIP()))

	go service.SendTopupConfirmationEmail(topUp.UserId, topUp.Money, float64(event.Object.Order.AmountPaid)/100, event.Object.Order.Currency, topUp.TradeNo)

	c.Status(http.StatusOK)
}

// customPriceCents > 0 overrides the product's own price for this checkout
// (Creem's pay-what-you-want field, in cents); 0 leaves the product price.
func genCreemLink(referenceId string, product *dto.CreemProduct, email string, username string, customPriceCents int) (string, error) {
	if setting.CreemApiKey == "" {
		return "", fmt.Errorf("Creem API key not configured")
	}

	// 根据测试模式选择 API 端点
	apiUrl := "https://api.creem.io/v1/checkouts"
	if setting.CreemTestMode {
		apiUrl = "https://test-api.creem.io/v1/checkouts"
		log.Printf("using Creem test environment: %s", apiUrl)
	}

	// 构建请求数据
	requestData := dto.CreemCheckoutRequest{
		ProductId: product.ProductId,
		RequestId: referenceId,
		Metadata: map[string]string{
			"username":     username,
			"reference_id": referenceId,
			"product_name": product.Name,
			"quota":        fmt.Sprintf("%d", product.Quota),
		},
	}
	if customPriceCents > 0 {
		requestData.CustomPrice = customPriceCents
	}
	if email != "" {
		requestData.Customer = &dto.CreemCustomer{Email: email}
	}

	// 序列化请求数据
	jsonData, err := common.Marshal(requestData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request data: %v", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", apiUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %v", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", setting.CreemApiKey)

	log.Printf("sending Creem payment request - URL: %s, ProductID: %s, UserEmail: %s, OrderNo: %s", apiUrl, product.ProductId, email, referenceId)

	// 发送请求
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send HTTP request: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}

	log.Printf("Creem API resp - status code: %d, resp: %s", resp.StatusCode, string(body))

	// 检查响应状态
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Creem API http status %d ", resp.StatusCode)
	}
	// 解析响应
	var checkoutResp dto.CreemCheckoutResponse
	err = common.Unmarshal(body, &checkoutResp)
	if err != nil {
		return "", fmt.Errorf("failed to parse response: %v", err)
	}

	if checkoutResp.CheckoutUrl == "" {
		return "", errors.New("Creem API resp no checkout url ")
	}

	log.Printf("Creem payment link created - OrderNo: %s, PayLink: %s", referenceId, checkoutResp.CheckoutUrl)
	return checkoutResp.CheckoutUrl, nil
}
