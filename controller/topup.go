package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

func GetTopUpInfo(c fuego.ContextNoBody) (*dto.Response[dto.TopUpInfoData], error) {
	complianceConfirmed := operation_setting.IsPaymentComplianceConfirmed()

	// 获取支付方式
	payMethods := operation_setting.PayMethods
	if !complianceConfirmed {
		payMethods = []map[string]string{}
	}

	// 如果启用了 Stripe 支付，添加到支付方法列表；否则过滤掉已存储的 Stripe 条目
	stripeConfigured := setting.StripeApiSecret != "" && setting.StripeWebhookSecret != "" && setting.StripePriceId != ""
	stripeEnabled := setting.StripeEnabled && stripeConfigured

	filteredMethods := make([]map[string]string, 0, len(payMethods))
	hasStripe := false
	for _, method := range payMethods {
		if method["type"] == "stripe" {
			if !stripeEnabled {
				continue
			}
			hasStripe = true
		}
		filteredMethods = append(filteredMethods, method)
	}
	payMethods = filteredMethods

	if stripeEnabled && !hasStripe {
		stripeMethod := map[string]string{
			"name":      "Stripe",
			"type":      "stripe",
			"color":     "rgba(var(--semi-purple-5), 1)",
			"min_topup": strconv.Itoa(setting.StripeMinTopUp),
		}
		payMethods = append(payMethods, stripeMethod)
	}

	// Waffo Pancake is displayed above the standard Waffo gateway.
	enableWaffoPancake := isWaffoPancakeTopUpEnabled()
	if enableWaffoPancake {
		hasWaffoPancake := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffoPancake {
				hasWaffoPancake = true
				break
			}
		}

		if !hasWaffoPancake {
			payMethods = append(payMethods, map[string]string{
				"name":      "Waffo Pancake",
				"type":      model.PaymentMethodWaffoPancake,
				"color":     "#F97316",
				"min_topup": strconv.Itoa(setting.WaffoPancakeMinTopUp),
			})
		}
	}

	enableWaffo := isWaffoTopUpEnabled()
	if enableWaffo {
		hasWaffo := false
		for _, method := range payMethods {
			if method["type"] == "waffo" {
				hasWaffo = true
				break
			}
		}
		if !hasWaffo {
			waffoMethod := map[string]string{
				"name":      "Waffo (Global Payment)",
				"type":      model.PaymentMethodWaffo,
				"color":     "rgba(var(--semi-blue-5), 1)",
				"min_topup": strconv.Itoa(setting.WaffoMinTopUp),
			}
			payMethods = append(payMethods, waffoMethod)
		}
	}

	var waffoPayMethods interface{}
	if enableWaffo {
		waffoPayMethods = setting.GetWaffoPayMethods()
	}

	enableNowPayments := isNowPaymentsTopUpEnabled()
	if enableNowPayments {
		hasNowPayments := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodNowPayments {
				hasNowPayments = true
				break
			}
		}
		if !hasNowPayments {
			payMethods = append(payMethods, map[string]string{
				"name":      "Crypto (NowPayments)",
				"type":      model.PaymentMethodNowPayments,
				"color":     "rgba(var(--semi-orange-5), 1)",
				"min_topup": strconv.Itoa(setting.NowPaymentsMinTopUp),
			})
		}
	}

	enableDeloPay := isDeloPayTopUpEnabled()
	if enableDeloPay {
		hasDeloPay := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodDeloPay {
				hasDeloPay = true
				break
			}
		}
		if !hasDeloPay {
			payMethods = append(payMethods, map[string]string{
				"name":      "PayPal",
				"type":      model.PaymentMethodDeloPay,
				"color":     "rgba(var(--semi-indigo-5), 1)",
				"min_topup": strconv.Itoa(setting.DeloPayMinTopUp),
			})
		}
	}

	data := dto.TopUpInfoData{
		EnableOnlineTopup:             isEpayTopUpEnabled(),
		EnableStripeTopup:             isStripeTopUpEnabled(),
		EnableCreemTopup:              isCreemTopUpEnabled(),
		EnableWaffoTopup:              enableWaffo,
		EnableWaffoPancakeTopup:       enableWaffoPancake,
		EnableNowPaymentsTopup:        enableNowPayments,
		EnableDeloPayTopup:            enableDeloPay,
		EnableRedemption:              complianceConfirmed,
		PaymentComplianceConfirmed:    complianceConfirmed,
		PaymentComplianceTermsVersion: operation_setting.CurrentComplianceTermsVersion,
		WaffoPayMethods:               waffoPayMethods,
		CreemProducts:                 setting.CreemProducts,
		CreemFeeFixed:                 setting.CreemFeeFixed,
		CreemFeePercent:               setting.CreemFeePercent,
		CreemFeeThreshold:             setting.CreemFeeThreshold,
		DeloPayFeeThreshold:           setting.DeloPayFeeThreshold,
		PayMethods:                    payMethods,
		MinTopup:                      operation_setting.MinTopUp,
		StripeMinTopup:                setting.StripeMinTopUp,
		WaffoMinTopup:                 setting.WaffoMinTopUp,
		WaffoPancakeMinTopup:          setting.WaffoPancakeMinTopUp,
		NowPaymentsMinTopup:           setting.NowPaymentsMinTopUp,
		DeloPayMinTopup:               setting.DeloPayMinTopUp,
		DeloPayFeeFixed:               setting.DeloPayFeeFixed,
		DeloPayFeePercent:             setting.DeloPayFeePercent,
		AmountOptions:                 operation_setting.GetPaymentSetting().AmountOptions,
		Discount:                      operation_setting.GetPaymentSetting().AmountDiscount,
		TopUpLink:                     common.TopUpLink,
	}
	return dto.Ok(data)
}

func GetEpayClient() *epay.Client {
	if operation_setting.PayAddress == "" || operation_setting.EpayId == "" || operation_setting.EpayKey == "" {
		return nil
	}
	withUrl, err := epay.NewClient(&epay.Config{
		PartnerID: operation_setting.EpayId,
		Key:       operation_setting.EpayKey,
	}, operation_setting.PayAddress)
	if err != nil {
		return nil
	}
	return withUrl
}

func getPayMoney(amount int64, group string) float64 {
	dAmount := decimal.NewFromInt(amount)
	// 充值金额以"展示类型"为准：
	// - USD/CNY: 前端传 amount 为金额单位；TOKENS: 前端传 tokens，需要换成 USD 金额
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		dAmount = dAmount.Div(dQuotaPerUnit)
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	dTopupGroupRatio := decimal.NewFromFloat(topupGroupRatio)
	dPrice := decimal.NewFromFloat(operation_setting.Price)
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	dDiscount := decimal.NewFromFloat(discount)

	payMoney := dAmount.Mul(dPrice).Mul(dTopupGroupRatio).Mul(dDiscount)

	return payMoney.InexactFloat64()
}

func getMinTopup() int64 {
	minTopup := operation_setting.MinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dMinTopup := decimal.NewFromInt(int64(minTopup))
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		minTopup = common.QuotaFromDecimal(dMinTopup.Mul(dQuotaPerUnit))
	}
	return int64(minTopup)
}

func getTopUpQuota(amount int64) (int, error) {
	quota := decimal.NewFromInt(amount)
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		quota = decimal.NewFromInt(quota.Div(quotaPerUnit).IntPart()).Mul(quotaPerUnit)
	} else {
		quota = quota.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	}
	return common.QuotaFromDecimalStrict(quota)
}

func getMaxTopUpAmount() int64 {
	if common.QuotaPerUnit <= 0 {
		return 0
	}
	quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
	maxStoredAmount := decimal.NewFromInt(common.MaxQuota - 1).
		Div(quotaPerUnit).
		Floor()
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		return maxStoredAmount.Add(decimal.NewFromInt(1)).
			Mul(quotaPerUnit).
			Ceil().
			Sub(decimal.NewFromInt(1)).
			IntPart()
	}
	return maxStoredAmount.IntPart()
}

func validateCreditedQuota(quota decimal.Decimal) (int, error) {
	value, err := common.QuotaFromDecimalStrict(quota)
	if err != nil {
		return 0, errors.New("top-up quota exceeds the representable range")
	}
	if value <= 0 {
		return 0, errors.New("top-up quota must be greater than 0")
	}
	return value, nil
}

func validateTopUpQuota(amount int64) (int, error) {
	quota, err := getTopUpQuota(amount)
	if err == nil && quota > 0 {
		return quota, nil
	}
	maxAmount := getMaxTopUpAmount()
	if maxAmount > 0 && amount > maxAmount {
		return 0, fmt.Errorf("a single top-up cannot exceed %d", maxAmount)
	}
	return 0, errors.New("invalid top-up amount")
}

// checkCreditedQuota mirrors checkTopUpQuota for gateways that price the order
// in money rather than in a top-up amount.
func checkCreditedQuota(userId int, quota decimal.Decimal) error {
	creditedQuota, err := validateCreditedQuota(quota)
	if err != nil {
		return err
	}
	return model.ValidateTopUpQuotaCapacity(userId, creditedQuota)
}

// checkTopUpQuota rejects an order whose credited quota is unrepresentable or
// would push the wallet past the ceiling, before the buyer is sent to the
// gateway. Settlement repeats the check atomically.
func checkTopUpQuota(userId int, amount int64) error {
	creditedQuota, err := validateTopUpQuota(amount)
	if err != nil {
		return err
	}
	return model.ValidateTopUpQuotaCapacity(userId, creditedQuota)
}

func rejectInvalidTopUpQuota(c *gin.Context, userId int, amount int64) bool {
	if err := checkTopUpQuota(userId, amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return true
	}
	return false
}

func RequestEpay(c fuego.ContextWithBody[dto.EpayRequest]) (*dto.Response[dto.EpayPayResponse], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.Amount < getMinTopup() {
		return dto.Fail[dto.EpayPayResponse](fmt.Sprintf("Top-up amount cannot be less than %v", getMinTopup()))
	}

	id := dto.UserID(c)
	if err := checkTopUpQuota(id, req.Amount); err != nil {
		return dto.Fail[dto.EpayPayResponse](err.Error())
	}
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[dto.EpayPayResponse]("Failed to get user group")
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney < 0.01 {
		return dto.Fail[dto.EpayPayResponse]("Top-up amount is too low")
	}

	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "payment.method_not_exists"))
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, _ := url.Parse(paymentReturnPath("/usage-logs"))
	notifyUrl, _ := url.Parse(callBackAddress + "/api/user/epay/notify")
	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("USR%dNO%s", id, tradeNo)
	client := GetEpayClient()
	if client == nil {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "payment.not_configured"))
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("TUC%d", req.Amount),
		Money:          strconv.FormatFloat(payMoney, 'f', 2, 64),
		Device:         epay.PC,
		NotifyUrl:      notifyUrl,
		ReturnUrl:      returnUrl,
	})
	if err != nil {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "payment.start_failed"))
	}
	amount := req.Amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount := decimal.NewFromInt(int64(amount))
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		amount = dAmount.Div(dQuotaPerUnit).IntPart()
	}
	topUp := &model.TopUp{
		UserId:          id,
		Amount:          amount,
		Money:           payMoney,
		TradeNo:         tradeNo,
		PaymentMethod:   req.PaymentMethod,
		PaymentProvider: model.PaymentProviderEpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "payment.create_failed"))
	}
	return dto.Ok(dto.EpayPayResponse{Params: params, Url: uri})
}

// tradeNo lock
var orderLocks sync.Map
var createLock sync.Mutex

// refCountedMutex 带引用计数的互斥锁，确保最后一个使用者才从 map 中删除
type refCountedMutex struct {
	mu       sync.Mutex
	refCount int
}

// LockOrder 尝试对给定订单号加锁
func LockOrder(tradeNo string) {
	createLock.Lock()
	var rcm *refCountedMutex
	if v, ok := orderLocks.Load(tradeNo); ok {
		rcm = v.(*refCountedMutex)
	} else {
		rcm = &refCountedMutex{}
		orderLocks.Store(tradeNo, rcm)
	}
	rcm.refCount++
	createLock.Unlock()
	rcm.mu.Lock()
}

// UnlockOrder 释放给定订单号的锁
func UnlockOrder(tradeNo string) {
	v, ok := orderLocks.Load(tradeNo)
	if !ok {
		return
	}
	rcm := v.(*refCountedMutex)
	rcm.mu.Unlock()

	createLock.Lock()
	rcm.refCount--
	if rcm.refCount == 0 {
		orderLocks.Delete(tradeNo)
	}
	createLock.Unlock()
}

func EpayNotify(c *gin.Context) {
	if !isEpayWebhookEnabled() {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay webhook rejected reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Epay webhook POST form parsing failed path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		params = lo.Reduce(lo.Keys(c.Request.PostForm), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.PostForm.Get(t)
			return r
		}, map[string]string{})
	} else {
		// GET 请求：从 URL Query 解析参数
		params = lo.Reduce(lo.Keys(c.Request.URL.Query()), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.URL.Query().Get(t)
			return r
		}, map[string]string{})
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Epay webhook received request path=%q client_ip=%s method=%s params=%q", c.Request.RequestURI, c.ClientIP(), c.Request.Method, common.GetJsonString(params)))

	if len(params) == 0 {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay webhook parameters are empty path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	client := GetEpayClient()
	if client == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Epay client not initialized path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		_, err := c.Writer.Write([]byte("fail"))
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Epay webhook response write failed path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		}
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		if _, writeErr := c.Writer.Write([]byte("fail")); writeErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Epay webhook response write failed path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), writeErr.Error()))
		}
		if err != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay webhook signature verification failed path=%q client_ip=%s verify_error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		} else {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay webhook signature verification failed path=%q client_ip=%s verify_status=false", c.Request.RequestURI, c.ClientIP()))
		}
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Epay webhook signature verified trade_no=%s callback_type=%s trade_status=%s client_ip=%s verify_info=%q", verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.TradeStatus, c.ClientIP(), common.GetJsonString(verifyInfo)))

	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		// In-process lock is only an optimization: correctness under duplicate or
		// concurrent callbacks comes from RechargeEpay's row lock + in-transaction
		// status check, which also holds across multiple instances.
		LockOrder(verifyInfo.ServiceTradeNo)
		defer UnlockOrder(verifyInfo.ServiceTradeNo)
		alreadyDone, err := model.RechargeEpay(verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP())
		if err != nil {
			switch {
			case errors.Is(err, model.ErrTopUpNotFound):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay callback order does not exist trade_no=%s callback_type=%s client_ip=%s verify_info=%q", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP(), common.GetJsonString(verifyInfo)))
			case errors.Is(err, model.ErrPaymentMethodMismatch):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay order payment gateway mismatch trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
			case errors.Is(err, model.ErrTopUpStatusInvalid):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("Epay order status invalid trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
			default:
				logger.LogError(c.Request.Context(), fmt.Sprintf("Epay topup processing failed trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), err.Error()))
			}
			if _, writeErr := c.Writer.Write([]byte("fail")); writeErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Epay webhook response write failed trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
			}
			return
		}
		if alreadyDone {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Epay duplicate callback ignored trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
		} else {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Epay topup succeeded trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
			if topUp := model.GetTopUpByTradeNo(verifyInfo.ServiceTradeNo); topUp != nil {
				if err := model.CreditReferralCommission(topUp.UserId, topUp.Money, "epay", topUp.Id); err != nil {
					logger.LogError(c.Request.Context(), fmt.Sprintf("referral commission failed user_id=%d error=%q", topUp.UserId, err.Error()))
				}
			}
		}
	} else {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Epay webhook ignored event trade_no=%s callback_type=%s trade_status=%s client_ip=%s verify_info=%q", verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.TradeStatus, c.ClientIP(), common.GetJsonString(verifyInfo)))
	}
	if _, writeErr := c.Writer.Write([]byte("success")); writeErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Epay webhook response write failed trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
	}
}

func RequestAmount(c fuego.ContextWithBody[dto.AmountRequest]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if req.Amount < getMinTopup() {
		return dto.Fail[string](fmt.Sprintf("Top-up amount cannot be less than %v", getMinTopup()))
	}
	id := dto.UserID(c)
	if err := checkTopUpQuota(id, req.Amount); err != nil {
		return dto.Fail[string](err.Error())
	}
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[string]("Failed to get user group")
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney <= 0.01 {
		return dto.Fail[string]("Top-up amount is too low")
	}
	return dto.Ok(strconv.FormatFloat(payMoney, 'f', 2, 64))
}

func GetUserTopUps(c fuego.ContextWithParams[dto.TopUpSearchParams]) (*dto.Response[dto.PageData[*model.TopUp]], error) {
	userId := dto.UserID(c)
	pageInfo := dto.PageInfo(c)
	p, _ := dto.ParseParams[dto.TopUpSearchParams](c)

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if p.Keyword != "" {
		topups, total, err = model.SearchUserTopUps(userId, p.Keyword, pageInfo)
	} else {
		topups, total, err = model.GetUserTopUps(userId, pageInfo)
	}
	if err != nil {
		return dto.FailPage[*model.TopUp](err.Error())
	}

	return dto.OkPage(pageInfo, topups, int(total))
}

func GetAllTopUps(c fuego.ContextWithParams[dto.TopUpSearchParams]) (*dto.Response[dto.PageData[*model.TopUp]], error) {
	pageInfo := dto.PageInfo(c)
	p, _ := dto.ParseParams[dto.TopUpSearchParams](c)

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if p.Keyword != "" {
		topups, total, err = model.SearchAllTopUps(p.Keyword, pageInfo)
	} else {
		topups, total, err = model.GetAllTopUps(pageInfo)
	}
	if err != nil {
		return dto.FailPage[*model.TopUp](err.Error())
	}

	return dto.OkPage(pageInfo, topups, int(total))
}

func AdminCompleteTopUp(c fuego.ContextWithBody[dto.AdminCompleteTopupRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil || req.TradeNo == "" {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	// 订单级互斥，防止并发补单
	LockOrder(req.TradeNo)
	defer UnlockOrder(req.TradeNo)

	if err := model.ManualCompleteTopUp(req.TradeNo, ginCtx.ClientIP()); err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}
