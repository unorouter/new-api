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

// Freemius checkout carries no metadata of ours, so a payment is matched to the
// order the buyer opened: same account email (the checkout locks it) and the
// same priced item, newest first, within this window.
const freemiusOrderMatchWindowSeconds = 48 * 3600

// FindPendingFreemiusTopUp returns the trade_no of the newest open Freemius
// top-up of this amount placed by an account with this email.
func FindPendingFreemiusTopUp(email string, amount int64) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", ErrTopUpNotFound
	}
	var topUp TopUp
	err := DB.Model(&TopUp{}).
		Joins("JOIN users ON users.id = top_ups.user_id").
		Where("LOWER(users.email) = ? AND top_ups.payment_provider = ? AND top_ups.status = ? AND top_ups.amount = ? AND top_ups.create_time >= ?",
			email, PaymentProviderFreemius, common.TopUpStatusPending, amount, common.GetTimestamp()-freemiusOrderMatchWindowSeconds).
		Order("top_ups.id DESC").
		Select("top_ups.trade_no").
		First(&topUp).Error
	if err != nil {
		return "", ErrTopUpNotFound
	}
	return topUp.TradeNo, nil
}

// FindPendingFreemiusSubscriptionOrder returns the trade_no of the newest open
// Freemius subscription order for this plan placed by an account with this email.
func FindPendingFreemiusSubscriptionOrder(email string, planId int) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", ErrSubscriptionOrderNotFound
	}
	var order SubscriptionOrder
	err := DB.Model(&SubscriptionOrder{}).
		Joins("JOIN users ON users.id = subscription_orders.user_id").
		Where("LOWER(users.email) = ? AND subscription_orders.payment_provider = ? AND subscription_orders.status = ? AND subscription_orders.plan_id = ? AND subscription_orders.create_time >= ?",
			email, PaymentProviderFreemius, common.TopUpStatusPending, planId, common.GetTimestamp()-freemiusOrderMatchWindowSeconds).
		Order("subscription_orders.id DESC").
		Select("subscription_orders.trade_no").
		First(&order).Error
	if err != nil {
		return "", ErrSubscriptionOrderNotFound
	}
	return order.TradeNo, nil
}

// FreemiusPaymentProcessed reports whether a Freemius payment id was already
// applied to a top-up or a subscription order (webhook redelivery).
func FreemiusPaymentProcessed(providerPaymentId string) bool {
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

func RechargeFreemius(referenceId string, providerPaymentId string) (err error) {
	if referenceId == "" {
		return errors.New("Freemius order reference not provided")
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
			return errors.New("Freemius order not found")
		}
		if topUp.PaymentProvider != PaymentProviderFreemius {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return errors.New("Freemius order status error")
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
		common.SysError("Freemius top-up processing failed: " + err.Error())
		return errors.New("Freemius top-up failed")
	}

	if quota > 0 {
		syncCreditUserQuotaCache(topUp.UserId, quota, common.LogText("freemius topup"))
		RecordTopupLog(topUp.UserId, withTopUpBonus(common.NewMessage("Freemius top-up succeeded, amount added: {{quota}}, payment amount: {{amount}}", map[string]any{"quota": logger.FormatQuota(quota), "amount": topUp.Money}), bonusPercent), "", topUp.PaymentMethod, PaymentProviderFreemius)
		common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "freemius", topUp.Id)
	}

	if err := CreditReferralCommission(topUp.UserId, topUp.Money, "freemius", topUp.Id); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", topUp.UserId, err))
	}
	return nil
}
