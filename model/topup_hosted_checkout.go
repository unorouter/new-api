package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Hosted checkouts (Freemius, Digistore24) sell fixed priced items and may lose
// our reference, so a payment can be matched to the order the buyer opened: same
// account email and the same priced item, newest first, within this window.
const hostedCheckoutOrderMatchWindowSeconds = 48 * 3600

// FindPendingTopUpByEmail returns the trade_no of the newest open top-up of this
// provider and amount placed by an account with this email.
func FindPendingTopUpByEmail(provider string, email string, amount int64) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", ErrTopUpNotFound
	}
	var topUp TopUp
	err := DB.Model(&TopUp{}).
		Joins("JOIN users ON users.id = top_ups.user_id").
		Where("LOWER(users.email) = ? AND top_ups.payment_provider = ? AND top_ups.status = ? AND top_ups.amount = ? AND top_ups.create_time >= ?",
			email, provider, common.TopUpStatusPending, amount, common.GetTimestamp()-hostedCheckoutOrderMatchWindowSeconds).
		Order("top_ups.id DESC").
		Select("top_ups.trade_no").
		First(&topUp).Error
	if err != nil {
		return "", ErrTopUpNotFound
	}
	return topUp.TradeNo, nil
}

// FindPendingSubscriptionOrderByEmail returns the trade_no of the newest open
// subscription order of this provider and plan placed by an account with this email.
func FindPendingSubscriptionOrderByEmail(provider string, email string, planId int) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", ErrSubscriptionOrderNotFound
	}
	var order SubscriptionOrder
	err := DB.Model(&SubscriptionOrder{}).
		Joins("JOIN users ON users.id = subscription_orders.user_id").
		Where("LOWER(users.email) = ? AND subscription_orders.payment_provider = ? AND subscription_orders.status = ? AND subscription_orders.plan_id = ? AND subscription_orders.create_time >= ?",
			email, provider, common.TopUpStatusPending, planId, common.GetTimestamp()-hostedCheckoutOrderMatchWindowSeconds).
		Order("subscription_orders.id DESC").
		Select("subscription_orders.trade_no").
		First(&order).Error
	if err != nil {
		return "", ErrSubscriptionOrderNotFound
	}
	return order.TradeNo, nil
}

// ProviderPaymentProcessed reports whether a provider payment id was already
// applied to a top-up or a subscription order (webhook redelivery).
func ProviderPaymentProcessed(providerPaymentId string) bool {
	var n int64
	DB.Model(&TopUp{}).Where("provider_payment_id = ?", providerPaymentId).Count(&n)
	if n > 0 {
		return true
	}
	DB.Model(&SubscriptionOrder{}).Where("provider_payment_id = ?", providerPaymentId).Count(&n)
	return n > 0
}

// GetSubscriptionOrderByProviderPaymentId returns the order whose provider
// payment id matches, e.g. the first Freemius order of a subscription.
func GetSubscriptionOrderByProviderPaymentId(providerPaymentId string) *SubscriptionOrder {
	var order SubscriptionOrder
	if err := DB.Where("provider_payment_id = ?", providerPaymentId).First(&order).Error; err != nil {
		return nil
	}
	return &order
}

func GetTopUpByProviderPaymentId(provider string, providerPaymentId string) *TopUp {
	var topUp TopUp
	if err := DB.Where("provider_payment_id = ? AND payment_provider = ?", providerPaymentId, provider).First(&topUp).Error; err != nil {
		return nil
	}
	return &topUp
}

// RechargeHostedCheckout credits a pending fixed priced top-up of this provider:
// Money is the USD price, so the credit is Money times QuotaPerUnit.
func RechargeHostedCheckout(provider string, label string, referenceId string, providerPaymentId string) (err error) {
	if referenceId == "" {
		return errors.New(label + " order reference not provided")
	}

	var quota int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error; err != nil {
			return errors.New(label + " order not found")
		}
		if topUp.PaymentProvider != provider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return errors.New(label + " order status error")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		topUp.ProviderPaymentId = providerPaymentId
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		quota, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}
		quota, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quota)
		return creditTopUpQuota(tx, topUp.UserId, quota, nil)
	})

	if err != nil {
		common.SysError(label + " top-up processing failed: " + err.Error())
		return errors.New(label + " top-up failed")
	}

	if quota > 0 {
		syncCreditUserQuotaCache(topUp.UserId, quota, common.LogText("%s topup", provider))
		RecordTopupLog(topUp.UserId, withTopUpBonus(common.NewMessage("{{provider}} top-up succeeded, amount added: {{quota}}, payment amount: {{amount}}", map[string]any{"provider": label, "quota": logger.FormatQuota(quota), "amount": fmt.Sprintf("%.2f", topUp.Money)}), bonusPercent), "", topUp.PaymentMethod, provider)
		common.CapturePaymentSuccess(topUp.UserId, topUp.Money, provider, topUp.Id)
	}

	if err := CreditReferralCommission(topUp.UserId, topUp.Money, provider, topUp.Id); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", topUp.UserId, err))
	}
	return nil
}
