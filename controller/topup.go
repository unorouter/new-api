package controller

import (
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

func GetTopUpInfo(c fuego.ContextNoBody) (*dto.Response[dto.TopUpInfoData], error) {
	// 获取支付方式
	payMethods := operation_setting.PayMethods

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

	// Creem 启用检查
	creemProducts := strings.TrimSpace(setting.CreemProducts)
	creemConfigured := setting.CreemApiKey != "" &&
		setting.CreemWebhookSecret != "" &&
		creemProducts != "" && creemProducts != "[]"

	// Waffo 启用检查
	enableWaffo := setting.WaffoEnabled &&
		((!setting.WaffoSandbox &&
			setting.WaffoApiKey != "" &&
			setting.WaffoPrivateKey != "" &&
			setting.WaffoPublicCert != "") ||
			(setting.WaffoSandbox &&
				setting.WaffoSandboxApiKey != "" &&
				setting.WaffoSandboxPrivateKey != "" &&
				setting.WaffoSandboxPublicCert != ""))
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
				"type":      "waffo",
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

	data := dto.TopUpInfoData{
		EnableOnlineTopup: operation_setting.PayAddress != "" && operation_setting.EpayId != "" && operation_setting.EpayKey != "",
		EnableStripeTopup: stripeEnabled,
		EnableCreemTopup:  setting.CreemEnabled && creemConfigured,
		EnableWaffoTopup:  enableWaffo,
		WaffoPayMethods:   waffoPayMethods,
		CreemProducts:     setting.CreemProducts,
		PayMethods:        payMethods,
		MinTopup:          operation_setting.MinTopUp,
		StripeMinTopup:    setting.StripeMinTopUp,
		WaffoMinTopup:     setting.WaffoMinTopUp,
		AmountOptions:     operation_setting.GetPaymentSetting().AmountOptions,
		Discount:          operation_setting.GetPaymentSetting().AmountDiscount,
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
		minTopup = int(dMinTopup.Mul(dQuotaPerUnit).IntPart())
	}
	return int64(minTopup)
}

func RequestEpay(c fuego.ContextWithBody[dto.EpayRequest]) (*dto.Response[dto.EpayPayResponse], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}
	if req.Amount < getMinTopup() {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "topup.min_amount", map[string]any{"Amount": getMinTopup()}))
	}

	id := dto.UserID(c)
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "topup.get_group_failed"))
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney < 0.01 {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "topup.amount_too_low"))
	}

	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		return dto.Fail[dto.EpayPayResponse](common.TranslateMessage(ginCtx, "payment.method_not_exists"))
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, _ := url.Parse(system_setting.ServerAddress + "/console/log")
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
		UserId:        id,
		Amount:        amount,
		Money:         payMoney,
		TradeNo:       tradeNo,
		PaymentMethod: req.PaymentMethod,
		CreateTime:    time.Now().Unix(),
		Status:        "pending",
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
	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			log.Println(i18n.Translate("topup.epay_post_parse_failed", map[string]any{"Error": err.Error()}))
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

	if len(params) == 0 {
		log.Println(i18n.Translate("topup.epay_params_empty"))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	client := GetEpayClient()
	if client == nil {
		log.Println(i18n.Translate("topup.epay_config_not_found"))
		_, err := c.Writer.Write([]byte("fail"))
		if err != nil {
			log.Println(i18n.Translate("topup.epay_write_failed"))
		}
		return
	}
	verifyInfo, err := client.Verify(params)
	if err == nil && verifyInfo.VerifyStatus {
		_, err := c.Writer.Write([]byte("success"))
		if err != nil {
			log.Println(i18n.Translate("topup.epay_write_failed"))
		}
	} else {
		_, err := c.Writer.Write([]byte("fail"))
		if err != nil {
			log.Println(i18n.Translate("topup.epay_write_failed"))
		}
		log.Println(i18n.Translate("topup.epay_sign_failed"))
		return
	}

	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		log.Println(verifyInfo)
		LockOrder(verifyInfo.ServiceTradeNo)
		defer UnlockOrder(verifyInfo.ServiceTradeNo)
		topUp := model.GetTopUpByTradeNo(verifyInfo.ServiceTradeNo)
		if topUp == nil {
			log.Printf(i18n.Translate("topup.epay_order_not_found", map[string]any{"Info": fmt.Sprintf("%v", verifyInfo)}))
			return
		}
		if topUp.Status == "pending" {
			topUp.Status = "success"
			err := topUp.Update()
			if err != nil {
				log.Printf(i18n.Translate("topup.epay_update_order_failed", map[string]any{"Info": fmt.Sprintf("%v", topUp)}))
				return
			}
			//user, _ := model.GetUserById(topUp.UserId, false)
			//user.Quota += topUp.Amount * 500000
			dAmount := decimal.NewFromInt(int64(topUp.Amount))
			dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
			quotaToAdd := int(dAmount.Mul(dQuotaPerUnit).IntPart())
			err = model.IncreaseUserQuota(topUp.UserId, quotaToAdd, true)
			if err != nil {
				log.Printf(i18n.Translate("topup.epay_update_user_failed", map[string]any{"Info": fmt.Sprintf("%v", topUp)}))
				return
			}
			log.Printf(i18n.Translate("topup.epay_update_user_success", map[string]any{"Info": fmt.Sprintf("%v", topUp)}))
			model.RecordLog(topUp.UserId, model.LogTypeTopup, i18n.Translate("log.online_topup_success", map[string]any{"Quota": logger.LogQuota(quotaToAdd), "Money": topUp.Money}))
			// Credit referral commission to inviter (if enabled)
			if err := model.CreditReferralCommission(topUp.UserId, topUp.Money, "epay", topUp.Id); err != nil {
				log.Printf(i18n.Translate("log.referral_commission_failed", map[string]any{"UserId": topUp.UserId, "Error": err.Error()}))
			}
		}
	} else {
		log.Printf(i18n.Translate("topup.epay_abnormal_callback", map[string]any{"Info": fmt.Sprintf("%v", verifyInfo)}))
	}
}

func RequestAmount(c fuego.ContextWithBody[dto.AmountRequest]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if req.Amount < getMinTopup() {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "topup.min_amount", map[string]any{"Amount": getMinTopup()}))
	}
	id := dto.UserID(c)
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "topup.get_group_failed"))
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney <= 0.01 {
		return dto.Fail[string](common.TranslateMessage(ginCtx, "topup.amount_too_low"))
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

	if err := model.ManualCompleteTopUp(req.TradeNo); err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

