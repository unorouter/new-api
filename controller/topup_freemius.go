package controller

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
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
	FreemiusSignatureHeader  = "X-Signature"
	FreemiusCheckoutBase     = "https://checkout.freemius.com"
	FreemiusTopUpRefPrefix   = "fsref_"
	FreemiusSubOrderRefPrefx = "fssubref_"
	// Prefixes keep Freemius ids apart from other providers' ids in shared columns.
	freemiusPaymentKeyPrefix      = "fs_pay:"
	freemiusSubscriptionKeyPrefix = "fs_sub:"
)

var errFreemiusEmailRequired = errors.New("Add an email address to your account to pay with PayPal")

// freemiusCheckoutLink builds a hosted checkout link. Freemius returns none of
// our metadata, so the buyer email is locked (readonly_user) and the webhook
// matches the payment to the order by that email and the priced plan.
func freemiusCheckoutLink(ref setting.FreemiusPlanRef, billingCycle string, email string) string {
	q := url.Values{}
	q.Set("plan_id", ref.PlanId)
	q.Set("pricing_id", ref.PricingId)
	q.Set("billing_cycle", billingCycle)
	q.Set("currency", "usd")
	q.Set("user_email", email)
	q.Set("readonly_user", "true")
	if setting.FreemiusSandbox {
		// Same token the official SDK builds: md5(ts + product + secret + public + "checkout").
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		sum := md5.Sum([]byte(ts + setting.FreemiusProductId + setting.FreemiusSecretKey + setting.FreemiusPublicKey + "checkout"))
		q.Set("s_ctx_ts", ts)
		q.Set("sandbox", hex.EncodeToString(sum[:]))
	}
	return fmt.Sprintf("%s/product/%s/?%s", FreemiusCheckoutBase, setting.FreemiusProductId, q.Encode())
}

func RequestFreemiusAmount(c fuego.ContextWithBody[dto.FreemiusPayRequest]) (*dto.Response[string], error) {
	if !setting.FreemiusAllowedFor(dto.UserID(c)) {
		return dto.Fail[string]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[string]("Invalid parameters")
	}
	if _, ok := setting.GetFreemiusTopUpPlans()[int(req.Amount)]; !ok {
		return dto.Fail[string]("PayPal top-ups are limited to the listed amounts")
	}
	// Freemius bills the plan's fixed USD price, which is the top-up amount.
	return dto.Ok(fmt.Sprintf("%.2f", float64(req.Amount)))
}

func RequestFreemiusPay(c fuego.ContextWithBody[dto.FreemiusPayRequest]) (*dto.Response[dto.FreemiusPayData], error) {
	if !isFreemiusTopUpEnabled() || !setting.FreemiusAllowedFor(dto.UserID(c)) {
		return dto.Fail[dto.FreemiusPayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.FreemiusPayData]("Invalid parameters")
	}
	if req.PaymentMethod != model.PaymentMethodFreemius {
		return dto.Fail[dto.FreemiusPayData]("Payment channel is not supported")
	}
	ref, ok := setting.GetFreemiusTopUpPlans()[int(req.Amount)]
	if !ok {
		return dto.Fail[dto.FreemiusPayData]("PayPal top-ups are limited to the listed amounts")
	}

	id := dto.UserID(c)
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		return dto.Fail[dto.FreemiusPayData]("Failed to get user information")
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		return dto.Fail[dto.FreemiusPayData](errFreemiusEmailRequired.Error())
	}

	reference := fmt.Sprintf("freemius-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := FreemiusTopUpRefPrefix + common.Sha1([]byte(reference))
	payLink := freemiusCheckoutLink(ref, "lifetime", email)

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           float64(req.Amount),
		TradeNo:         referenceId,
		PaymentMethod:   model.PaymentMethodFreemius,
		PaymentProvider: model.PaymentProviderFreemius,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err = topUp.Insert(); err != nil {
		return dto.Fail[dto.FreemiusPayData]("Failed to create order")
	}
	return dto.Ok(dto.FreemiusPayData{PayLink: payLink})
}

// SubscriptionRequestFreemiusPay opens a real recurring Freemius subscription:
// the first payment grants the plan, every renewal payment rolls it forward.
func SubscriptionRequestFreemiusPay(c fuego.ContextWithBody[dto.SubscriptionFreemiusPayRequest]) (*dto.Response[dto.FreemiusPayData], error) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.FreemiusPayData]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	if !setting.FreemiusSubscriptionEnabled || !isFreemiusTopUpEnabled() || !setting.FreemiusAllowedFor(dto.UserID(c)) {
		return dto.Fail[dto.FreemiusPayData]("Payment channel is not supported")
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.FreemiusPayData]("Invalid parameters")
	}
	ref, ok := setting.GetFreemiusSubscriptionPlans()[req.PlanId]
	if !ok {
		return dto.Fail[dto.FreemiusPayData]("Payment channel is not supported")
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		return dto.Fail[dto.FreemiusPayData](err.Error())
	}
	if !plan.Enabled {
		return dto.Fail[dto.FreemiusPayData]("Subscription plan is not enabled")
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return dto.Fail[dto.FreemiusPayData]("User does not exist")
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		return dto.Fail[dto.FreemiusPayData](errFreemiusEmailRequired.Error())
	}

	if held, err := model.HasActiveUserSubscriptionForPlan(userId, plan.Id); err != nil {
		return dto.Fail[dto.FreemiusPayData](err.Error())
	} else if held {
		return dto.Fail[dto.FreemiusPayData]("You already hold this plan and it is still active. A plan can be held once at a time; you can add a different plan next to it.")
	}
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			return dto.Fail[dto.FreemiusPayData](err.Error())
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return dto.Fail[dto.FreemiusPayData]("Purchase limit for this plan has been reached")
		}
	}

	reference := fmt.Sprintf("sub-freemius-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := FreemiusSubOrderRefPrefx + common.Sha1([]byte(reference))
	payLink := freemiusCheckoutLink(ref, "monthly", email)

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         referenceId,
		PaymentMethod:   model.PaymentMethodFreemius,
		PaymentProvider: model.PaymentProviderFreemius,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		InvoiceUrl:      payLink,
	}
	if err := order.Insert(); err != nil {
		return dto.Fail[dto.FreemiusPayData]("Failed to create order")
	}
	return dto.Ok(dto.FreemiusPayData{PayLink: payLink})
}

// verifyFreemiusSignature checks the hex HMAC-SHA256 of the RAW body under the
// product secret key, as Freemius signs it (X-Signature).
func verifyFreemiusSignature(payload []byte, sig, secret string) bool {
	if secret == "" || sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(sig))), []byte(expected))
}

func FreemiusWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	callerIp := c.ClientIP()
	if !isFreemiusTopUpEnabled() {
		logger.LogWarn(ctx, fmt.Sprintf("Freemius webhook received while the channel is disabled, rejecting client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Freemius webhook failed to read payload client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !verifyFreemiusSignature(bodyBytes, c.GetHeader(FreemiusSignatureHeader), setting.FreemiusSecretKey) {
		logger.LogWarn(ctx, fmt.Sprintf("Freemius webhook signature verification failed client_ip=%s", callerIp))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var event dto.FreemiusWebhookEvent
	if err := common.Unmarshal(bodyBytes, &event); err != nil {
		logger.LogError(ctx, fmt.Sprintf("Freemius webhook failed to parse payload client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if string(event.PluginId) != "" && string(event.PluginId) != setting.FreemiusProductId {
		logger.LogWarn(ctx, fmt.Sprintf("Freemius webhook for another product ignored event_id=%s plugin_id=%s", event.Id, event.PluginId))
		c.Status(http.StatusOK)
		return
	}
	logger.LogInfo(ctx, fmt.Sprintf("Freemius webhook received event_id=%s type=%s client_ip=%s", event.Id, event.Type, callerIp))

	switch event.Type {
	case "payment.created":
		handleFreemiusPayment(c, &event, string(bodyBytes))
	case "payment.refund":
		handleFreemiusReversal(c, &event, string(bodyBytes), false)
	case "payment.dispute.created", "payment.dispute.lost":
		handleFreemiusReversal(c, &event, string(bodyBytes), true)
	default:
		// A cancelled or finally failed subscription simply stops renewing; the
		// paid period runs out on its own end_time.
		logger.LogInfo(ctx, fmt.Sprintf("Freemius event acknowledged without action event_id=%s type=%s", event.Id, event.Type))
		c.Status(http.StatusOK)
	}
}

func handleFreemiusPayment(c *gin.Context, event *dto.FreemiusWebhookEvent, rawBody string) {
	ctx := c.Request.Context()
	p := event.Objects.Payment
	if p == nil || string(p.Id) == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Freemius payment.created without a payment object event_id=%s", event.Id))
		c.Status(http.StatusOK)
		return
	}
	if p.Type != "" && p.Type != "payment" {
		logger.LogInfo(ctx, fmt.Sprintf("Freemius payment.created of type %s ignored event_id=%s payment_id=%s", p.Type, event.Id, p.Id))
		c.Status(http.StatusOK)
		return
	}
	payKey := freemiusPaymentKeyPrefix + string(p.Id)
	if model.FreemiusPaymentProcessed(payKey) {
		logger.LogInfo(ctx, fmt.Sprintf("Freemius payment already applied event_id=%s payment_id=%s", event.Id, p.Id))
		c.Status(http.StatusOK)
		return
	}
	email := ""
	if event.Objects.User != nil {
		email = event.Objects.User.Email
	}
	pricingId := string(p.PricingId)

	for amount, ref := range setting.GetFreemiusTopUpPlans() {
		if ref.PricingId != pricingId {
			continue
		}
		tradeNo, err := model.FindPendingFreemiusTopUp(email, int64(amount))
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Freemius top-up payment matched no open order event_id=%s payment_id=%s amount=%d body=%q", event.Id, p.Id, amount, rawBody))
			c.Status(http.StatusOK)
			return
		}
		LockOrder(tradeNo)
		defer UnlockOrder(tradeNo)
		if err := model.RechargeFreemius(tradeNo, payKey); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Freemius top-up processing failed trade_no=%s payment_id=%s error=%q", tradeNo, p.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogInfo(ctx, fmt.Sprintf("Freemius top-up processed trade_no=%s payment_id=%s", tradeNo, p.Id))
		c.Status(http.StatusOK)
		return
	}

	for planId, ref := range setting.GetFreemiusSubscriptionPlans() {
		if ref.PricingId != pricingId {
			continue
		}
		subKey := freemiusSubscriptionKeyPrefix + string(p.SubscriptionId)
		if p.IsRenewal {
			reference := ""
			if order := model.GetSubscriptionOrderByProviderPaymentId(subKey); order != nil {
				reference = order.TradeNo
			}
			LockOrder(subKey)
			defer UnlockOrder(subKey)
			userId, subId, err := model.RenewUserSubscriptionByCreem(model.CreemRenewalInput{
				ReferenceId:            reference,
				ProviderSubscriptionId: subKey,
				LastTransactionId:      payKey,
				ProviderPayload:        rawBody,
				Money:                  p.Gross,
				Provider:               model.PaymentProviderFreemius,
			})
			if err != nil {
				if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
					logger.LogWarn(ctx, fmt.Sprintf("Freemius renewal matched no subscription event_id=%s payment_id=%s subscription_id=%s body=%q", event.Id, p.Id, p.SubscriptionId, rawBody))
					c.Status(http.StatusOK)
					return
				}
				logger.LogError(ctx, fmt.Sprintf("Freemius renewal processing failed payment_id=%s error=%q", p.Id, err.Error()))
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			logger.LogInfo(ctx, fmt.Sprintf("Freemius renewal processed payment_id=%s user_id=%d subscription_id=%d", p.Id, userId, subId))
			c.Status(http.StatusOK)
			return
		}

		tradeNo, err := model.FindPendingFreemiusSubscriptionOrder(email, planId)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Freemius subscription payment matched no open order event_id=%s payment_id=%s plan_id=%d body=%q", event.Id, p.Id, planId, rawBody))
			c.Status(http.StatusOK)
			return
		}
		LockOrder(tradeNo)
		defer UnlockOrder(tradeNo)
		if err := model.CompleteSubscriptionOrder(tradeNo, rawBody, model.PaymentProviderFreemius, model.PaymentMethodFreemius); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Freemius subscription order processing failed trade_no=%s error=%q", tradeNo, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		// Renewals and refunds name only the Freemius subscription, so the order
		// and the granted row both carry its id.
		if err := model.SetSubscriptionOrderProviderPaymentId(tradeNo, subKey); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Freemius failed to record subscription id trade_no=%s subscription_id=%s error=%q", tradeNo, p.SubscriptionId, err.Error()))
		}
		if _, _, err := model.RenewUserSubscriptionByCreem(model.CreemRenewalInput{
			ReferenceId:            tradeNo,
			ProviderSubscriptionId: subKey,
			Provider:               model.PaymentProviderFreemius,
		}); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Freemius failed to link subscription id trade_no=%s subscription_id=%s error=%q", tradeNo, p.SubscriptionId, err.Error()))
		}
		logger.LogInfo(ctx, fmt.Sprintf("Freemius subscription order processed trade_no=%s payment_id=%s subscription_id=%s", tradeNo, p.Id, p.SubscriptionId))
		c.Status(http.StatusOK)
		return
	}

	logger.LogWarn(ctx, fmt.Sprintf("Freemius payment for an unmapped pricing ignored event_id=%s payment_id=%s pricing_id=%s", event.Id, p.Id, pricingId))
	c.Status(http.StatusOK)
}

// handleFreemiusReversal takes back what a refunded or disputed payment bought.
// A dispute also locks the account, as with the other card providers.
func handleFreemiusReversal(c *gin.Context, event *dto.FreemiusWebhookEvent, rawBody string, dispute bool) {
	ctx := c.Request.Context()
	kind := "refund"
	if dispute {
		kind = "dispute"
	}
	p := event.Objects.Payment
	if p == nil {
		logger.LogWarn(ctx, fmt.Sprintf("Freemius %s without a payment object event_id=%s", kind, event.Id))
		c.Status(http.StatusOK)
		return
	}
	// A payment.refund carries the refunded payment as id and the refund's own
	// payment as bound_payment_id; try the original first, then the bound one.
	var res model.TopUpReversalResult
	var err error
	original := ""
	for _, cand := range []string{string(p.Id), string(p.BoundPaymentId)} {
		if cand == "" {
			continue
		}
		original = cand
		payKey := freemiusPaymentKeyPrefix + cand
		LockOrder(payKey)
		res, err = model.ReverseTopUp(model.TopUpReversalInput{
			Provider:      model.PaymentProviderFreemius,
			EventId:       string(event.Id),
			TransactionId: payKey,
			ReversedCents: int(math.Round(math.Abs(p.Gross) * 100)),
			Dispute:       dispute,
		})
		UnlockOrder(payKey)
		if !errors.Is(err, model.ErrReversalUnmatched) && !errors.Is(err, model.ErrTopUpNotFound) {
			break
		}
	}
	if original == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Freemius %s without a payment id event_id=%s", kind, event.Id))
		c.Status(http.StatusOK)
		return
	}
	userId := res.UserId
	switch {
	case err == nil:
		note := "Freemius {{kind}}: {{quota}} quota reversed, top-up {{top_up_id}}, payment {{payment_id}}, event {{event_id}}"
		if !res.AlreadyDone {
			model.RecordLog(userId, model.LogTypeRefund, common.NewMessage(note, map[string]any{"kind": kind, "quota": res.QuotaRemoved, "top_up_id": res.TopUpId, "payment_id": original, "event_id": event.Id}))
		}
		logger.LogInfo(ctx, fmt.Sprintf("Freemius %s applied to top-up event_id=%s user_id=%d top_up_id=%d quota_removed=%d already=%t", kind, event.Id, userId, res.TopUpId, res.QuotaRemoved, res.AlreadyDone))
	case errors.Is(err, model.ErrReversalUnmatched), errors.Is(err, model.ErrTopUpNotFound):
		// Not a top-up: it reverses a subscription payment.
		subKey := freemiusSubscriptionKeyPrefix + string(p.SubscriptionId)
		if string(p.SubscriptionId) == "" {
			logger.LogWarn(ctx, fmt.Sprintf("Freemius %s matched nothing event_id=%s payment_id=%s body=%q", kind, event.Id, original, rawBody))
			c.Status(http.StatusOK)
			return
		}
		reference := ""
		if order := model.GetSubscriptionOrderByProviderPaymentId(subKey); order != nil {
			reference = order.TradeNo
		}
		reason := "refund.created"
		if dispute {
			reason = "dispute.created"
		}
		subUserId, subId, terr := model.TerminateUserSubscriptionByCreem(model.CreemTerminationInput{
			ReferenceId:            reference,
			ProviderSubscriptionId: subKey,
			Reason:                 reason,
		})
		if terr != nil && !errors.Is(terr, model.ErrSubscriptionOrderNotFound) {
			logger.LogError(ctx, fmt.Sprintf("Freemius %s subscription termination failed event_id=%s error=%q", kind, event.Id, terr.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		userId = subUserId
		if subId != 0 {
			service.ClearTerminatedSubscriptionPerks(subUserId, true)
		}
		logger.LogInfo(ctx, fmt.Sprintf("Freemius %s on subscription event_id=%s user_id=%d subscription_id=%d", kind, event.Id, subUserId, subId))
	default:
		logger.LogError(ctx, fmt.Sprintf("Freemius %s processing failed event_id=%s payment_id=%s error=%q", kind, event.Id, original, err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if dispute && userId > 0 {
		if derr := model.DisableUserForFraud(userId); derr != nil {
			logger.LogError(ctx, fmt.Sprintf("Freemius dispute failed to disable user event_id=%s user_id=%d error=%q", event.Id, userId, derr.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}
	c.Status(http.StatusOK)
}
