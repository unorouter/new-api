package model

import (
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type TopUp struct {
	Id              int     `json:"id"`
	UserId          int     `json:"user_id" gorm:"index"`
	Amount          int64   `json:"amount"`
	Money           float64 `json:"money"`
	TradeNo         string  `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	CreateTime      int64   `json:"create_time"`
	CompleteTime    int64   `json:"complete_time"`
	Status          string  `json:"status"`
	InvoiceUrl      string  `json:"invoice_url" gorm:"type:varchar(512);default:''"`
	// Provider-side payment identifier, recorded from the webhook. NowPayments
	// exposes payment lookups only by payment_id, so without this a top-up that
	// was paid but never reached a final status cannot be reconciled later.
	ProviderPaymentId string `json:"provider_payment_id" gorm:"type:varchar(64);default:'';index"`
	// What the gateway actually billed, when it differs from Money because a
	// processing fee was passed to the buyer. Credit is always granted from
	// Money, so this stays out of the quota calculation by design. Zero means
	// no surcharge and the buyer paid Money.
	//
	// NOTE this is set at ORDER CREATION and is therefore intent, not evidence:
	// an abandoned checkout carries a ChargedMoney it never paid. Use PaidAmount
	// to tell "money arrived" from "invoice opened".
	ChargedMoney float64 `json:"charged_money" gorm:"default:0"`
	// Funds the provider reports as RECEIVED, in the order's own currency, from
	// the webhook rather than from our own record of what we asked for. Nothing
	// else in this table distinguishes a paid-but-uncredited order from a
	// checkout the buyer walked away from: status stays 'pending' for both,
	// complete_time is only written on success, and provider_payment_id is
	// recorded on every webhook including 'waiting'. A monitor that cannot tell
	// those apart reports money owed that was never sent (27 orders / $142, all
	// of them abandoned, Aug 2026).
	PaidAmount float64 `json:"paid_amount" gorm:"default:0;index"`
}

const (
	PaymentMethodBalance      = "balance"
	PaymentMethodStripe       = "stripe"
	PaymentMethodCreem        = "creem"
	PaymentMethodWaffo        = "waffo"
	PaymentMethodWaffoPancake = "waffo_pancake"
	PaymentMethodNowPayments  = "nowpayments"
	PaymentMethodDeloPay      = "delopay"
)

const (
	PaymentProviderEpay         = "epay"
	PaymentProviderBalance      = "balance"
	PaymentProviderStripe       = "stripe"
	PaymentProviderCreem        = "creem"
	PaymentProviderWaffo        = "waffo"
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentProviderNowPayments  = "nowpayments"
	PaymentProviderDeloPay      = "delopay"
)

var (
	ErrPaymentMethodMismatch    = errors.New("payment method mismatch")
	ErrTopUpNotFound            = errors.New("topup not found")
	ErrTopUpStatusInvalid       = errors.New("topup status invalid")
	ErrInvalidTopUpQuota        = errors.New("invalid top-up quota")
	ErrTopUpQuotaLimitExceeded  = errors.New("top-up quota limit exceeded")
	ErrWalletQuotaLimitExceeded = errors.New("wallet quota limit exceeded")
)

func (topUp *TopUp) Insert() error {
	var err error
	err = DB.Create(topUp).Error
	return err
}

func topUpQuotaMaxCurrent(creditedQuota int) (int, error) {
	if creditedQuota <= 0 || creditedQuota > common.MaxWalletQuota {
		return 0, ErrInvalidTopUpQuota
	}
	return common.MaxWalletQuota - creditedQuota, nil
}

// topUpBonusMaxPercent caps the enterprise bonus at double. A larger stored
// value is clamped rather than rejected: the column is admin-only and a webhook
// arrives for an order the customer has already paid, so crediting the
// documented maximum beats failing the settlement over a typo. This is
// deliberately unlike CreditReferralCommission, which pays nothing when its
// rate is out of range -- that is commission owed to a third party, this is
// money already in hand.
const topUpBonusMaxPercent = 100.0

// applyTopUpBonus grants a partner's enterprise bonus on top of a paid top-up
// and reports the percent actually applied (0 when there is none) for the log
// line. Reseller deals are priced as a discount on volume, and this is how that
// discount is expressed: pay $10,000 at 25%, receive $12,500 of quota.
//
// MUST be called by the settlement functions and never inside
// creditTopUpQuota: redemption codes settle through that same helper, and
// bonusing them would double the discount on cards the partner already minted
// from bonused balance. Callers reassign their existing quota local so the
// out-of-transaction syncCreditUserQuotaCache sees the same number the database
// got; a separate variable would desync the cached balance.
//
// Fails open. A bonus lookup must never fail a settlement for money already
// received, so any read error credits the base amount and logs.
func applyTopUpBonus(tx *gorm.DB, userId int, baseQuota int) (int, float64) {
	if baseQuota <= 0 {
		return baseQuota, 0
	}
	var user User
	if err := tx.Model(&User{}).Select("topup_bonus_percent").
		Where("id = ?", userId).First(&user).Error; err != nil {
		common.SysLog(fmt.Sprintf("top-up bonus lookup failed for user %d, crediting base amount: %v", userId, err))
		return baseQuota, 0
	}
	if user.TopUpBonusPercent == nil {
		return baseQuota, 0
	}
	percent := *user.TopUpBonusPercent
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent <= 0 {
		return baseQuota, 0
	}
	if percent > topUpBonusMaxPercent {
		common.SysLog(fmt.Sprintf("top-up bonus for user %d is %v%%, clamping to %v%%", userId, percent, topUpBonusMaxPercent))
		percent = topUpBonusMaxPercent
	}

	bonused, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromInt(int64(baseQuota)).
			Mul(decimal.NewFromFloat(1 + percent/100)),
	)
	// Out of range means the bonus alone would breach the wallet ceiling. Credit
	// the base rather than nothing: the customer paid for that much.
	if err != nil || bonused < baseQuota {
		common.SysLog(fmt.Sprintf("top-up bonus overflowed for user %d, crediting base amount: %v", userId, err))
		return baseQuota, 0
	}
	return bonused, percent
}

// topUpBonusNote renders the log suffix for a credited bonus, empty when none
// applied. Support reads the top-up log to explain why a balance is larger than
// the payment.
func topUpBonusNote(percent float64) string {
	if percent <= 0 {
		return ""
	}
	return fmt.Sprintf(" (includes %g%% enterprise bonus)", percent)
}

// ValidateTopUpQuotaCapacity performs the user-facing pre-payment check. The
// settlement path repeats the same invariant with an atomic conditional
// update, because the wallet balance can change after checkout creation.
//
// The bonus is applied here too, so checkout is judged on the same number
// settlement will credit: a partner near the ceiling would otherwise pass
// checkout, pay, and only then have settlement rejected.
func ValidateTopUpQuotaCapacity(userId int, creditedQuota int) error {
	creditedQuota, _ = applyTopUpBonus(DB, userId, creditedQuota)
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	var user User
	if err := DB.Select("quota").Where("id = ?", userId).First(&user).Error; err != nil {
		return err
	}
	if user.Quota > maxCurrentQuota {
		return ErrTopUpQuotaLimitExceeded
	}
	return nil
}

// creditTopUpQuota atomically enforces the wallet ceiling while adding quota.
// Keeping the predicate and increment in one UPDATE prevents two
// concurrent callbacks from both passing a separate read/check.
func creditTopUpQuota(tx *gorm.DB, userId int, creditedQuota int, updates map[string]interface{}) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	updateFields := make(map[string]interface{}, len(updates)+1)
	for key, value := range updates {
		updateFields[key] = value
	}
	updateFields["quota"] = gorm.Expr("quota + ?", creditedQuota)

	result := tx.Model(&User{}).
		Where("id = ? AND quota <= ?", userId, maxCurrentQuota).
		Updates(updateFields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var count int64
	if err := tx.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrTopUpQuotaLimitExceeded
}

func (topUp *TopUp) Update() error {
	var err error
	err = DB.Save(topUp).Error
	return err
}

func GetTopUpByTradeNo(tradeNo string) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("trade_no = ?", tradeNo).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

// SetPaymentInvoiceUrl stores a provider-hosted invoice link against a paid
// record. The trade number is unique across both payment tables, so whichever
// one owns it gets the URL.
func SetPaymentInvoiceUrl(tradeNo string, invoiceUrl string) error {
	if tradeNo == "" || invoiceUrl == "" {
		return nil
	}

	res := DB.Model(&TopUp{}).Where("trade_no = ?", tradeNo).Update("invoice_url", invoiceUrl)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}

	return DB.Model(&SubscriptionOrder{}).Where("trade_no = ?", tradeNo).Update("invoice_url", invoiceUrl).Error
}

// SetTopUpProviderPaymentId records the provider's own payment identifier
// against a top-up. Stored on every webhook, including non-final statuses, so an
// order that stalls (e.g. an underpayment that never reaches "finished") can
// still be looked up with the provider afterwards and reconciled.
func SetTopUpProviderPaymentId(tradeNo string, paymentId string) error {
	if tradeNo == "" || paymentId == "" {
		return nil
	}
	return DB.Model(&TopUp{}).
		Where("trade_no = ? AND provider_payment_id = ?", tradeNo, "").
		Update("provider_payment_id", paymentId).Error
}

// SetTopUpPaidAmount records funds the provider says it RECEIVED. Written from
// the webhook on any status that carries a real figure, not only the final one,
// so an order that stalls after the money landed is still identifiable as owed.
//
// Monotonic: a later webhook reporting less than we already recorded (a partial
// figure arriving out of order) must not erase the higher one.
func SetTopUpPaidAmount(tradeNo string, paidAmount float64) error {
	if tradeNo == "" || paidAmount <= 0 {
		return nil
	}
	return DB.Model(&TopUp{}).
		Where("trade_no = ? AND paid_amount < ?", tradeNo, paidAmount).
		Update("paid_amount", paidAmount).Error
}

func UpdatePendingTopUpStatus(tradeNo string, expectedPaymentProvider string, targetStatus string) error {
	if tradeNo == "" {
		return errors.New("payment order number not provided")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if expectedPaymentProvider != "" && topUp.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		topUp.Status = targetStatus
		return tx.Save(topUp).Error
	})
}

// RechargeEpay 原子完成易支付订单：订单行锁、状态校验、成功更新与用户额度增加
// 在同一个事务内完成，因此同一订单的并发/重复回调（包括多实例部署下）最多充值一次。
// alreadyDone=true 表示订单此前已完成，本次为幂等重复回调。
// 进程内的 LockOrder 只是优化，正确性由本函数的数据库行锁保证。
func RechargeEpay(tradeNo string, actualPaymentMethod string, callerIp string) (alreadyDone bool, err error) {
	if tradeNo == "" {
		return false, errors.New("payment order number not provided")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var quotaToAdd int
	var bonusPercent float64
	topUp := &TopUp{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderEpay {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			alreadyDone = true
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		if actualPaymentMethod != "" && topUp.PaymentMethod != actualPaymentMethod {
			topUp.PaymentMethod = actualPaymentMethod
		}
		var quotaErr error
		quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}
		quotaToAdd, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quotaToAdd)
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})
	if err != nil {
		if !errors.Is(err, ErrTopUpNotFound) && !errors.Is(err, ErrPaymentMethodMismatch) && !errors.Is(err, ErrTopUpStatusInvalid) {
			common.SysError("epay topup failed: " + err.Error())
		}
		return false, err
	}
	if alreadyDone {
		return true, nil
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "epay topup")

	common.SysLog(fmt.Sprintf("易支付充值成功 trade_no=%s user_id=%d quota_to_add=%d money=%.2f", topUp.TradeNo, topUp.UserId, quotaToAdd, topUp.Money))
	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%f%s", logger.LogQuota(quotaToAdd), topUp.Money, topUpBonusNote(bonusPercent)), callerIp, topUp.PaymentMethod, PaymentProviderEpay)
	return false, nil
}

func Recharge(referenceId string, customerId string, callerIp string) (err error) {
	if referenceId == "" {
		return errors.New("payment order number not provided")
	}

	var quota int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("top-up order does not exist")
		}

		if topUp.PaymentProvider != PaymentProviderStripe {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("top-up order status error")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		quota, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}
		quota, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quota)
		return creditTopUpQuota(tx, topUp.UserId, quota, map[string]interface{}{
			"stripe_customer": customerId,
		})
	})

	if err != nil {
		common.SysError("topup failed: " + err.Error())
		return errors.New("top-up failed, please try again later")
	}
	syncCreditUserQuotaCache(topUp.UserId, quota, "stripe topup")

	RecordTopupLog(topUp.UserId, fmt.Sprintf("online top-up successful, quota: %v, payment amount: %d%s", logger.FormatQuota(quota), topUp.Amount, topUpBonusNote(bonusPercent)), callerIp, topUp.PaymentMethod, PaymentMethodStripe)
	common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "stripe", topUp.Id)

	// Credit referral commission to inviter (if enabled)
	if err := CreditReferralCommission(topUp.UserId, topUp.Money, "stripe", topUp.Id); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", topUp.UserId, err))
	}

	return nil
}

func GetUserTopUps(userId int, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	// Start transaction
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Get total count within transaction
	err = tx.Model(&TopUp{}).Where("user_id = ?", userId).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated topups within same transaction
	err = tx.Where("user_id = ?", userId).Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Commit transaction
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// GetAllTopUps 获取全平台的充值记录（管理员使用）
func GetAllTopUps(pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err = tx.Model(&TopUp{}).Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// SearchUserTopUps 按订单号搜索某用户的充值记录
func SearchUserTopUps(userId int, keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{}).Where("user_id = ?", userId)
	if keyword != "" {
		like := "%%" + keyword + "%%"
		query = query.Where("trade_no LIKE ?", like)
	}

	if err = query.Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// SearchAllTopUps 按订单号搜索全平台充值记录（管理员使用）
func SearchAllTopUps(keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{})
	if keyword != "" {
		like := "%%" + keyword + "%%"
		query = query.Where("trade_no LIKE ?", like)
	}

	if err = query.Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// ManualCompleteTopUp 管理员手动完成订单并给用户充值
func ManualCompleteTopUp(tradeNo string, callerIp string) error {
	if tradeNo == "" {
		return errors.New("order number not provided")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var userId int
	var quotaToAdd int
	var bonusPercent float64
	var payMoney float64
	var topUpId int
	var paymentMethod string

	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		// 行级锁，避免并发补单
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return errors.New("top-up order does not exist")
		}

		// 幂等处理：已成功直接返回
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("order status is not pending payment, cannot reprocess order")
		}

		// 计算应充值额度：
		// - Stripe 订单：Money 代表经分组倍率换算后的美元数量，直接 * QuotaPerUnit
		// - 其他订单（如易支付）：Amount 为美元数量，* QuotaPerUnit
		// DeloPay belongs with the Money providers: RechargeDeloPay credits
		// Money*QuotaPerUnit, so settling the same order by hand from Amount
		// would grant a different quota than the webhook would have.
		var quotaErr error
		if topUp.PaymentProvider == PaymentProviderStripe ||
			topUp.PaymentProvider == PaymentProviderNowPayments ||
			topUp.PaymentProvider == PaymentProviderDeloPay {
			quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(
				decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
			)
		} else {
			quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(
				decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
			)
		}
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}
		quotaToAdd, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quotaToAdd)

		// 标记完成
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		// 增加用户额度（立即写库，保持一致性）
		if err := creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil); err != nil {
			return err
		}

		userId = topUp.UserId
		payMoney = topUp.Money
		topUpId = topUp.Id
		paymentMethod = topUp.PaymentMethod
		return nil
	})

	if err != nil {
		return err
	}

	// 事务外记录日志，避免阻塞
	syncCreditUserQuotaCache(userId, quotaToAdd, "manual topup")
	RecordTopupLog(userId, fmt.Sprintf("admin manual completion successful, quota: %v, payment amount: %v%s", logger.FormatQuota(quotaToAdd), payMoney, topUpBonusNote(bonusPercent)), callerIp, paymentMethod, "admin")
	common.CapturePaymentSuccess(userId, payMoney, "manual", topUpId)

	// Credit referral commission to inviter (if enabled)
	if err := CreditReferralCommission(userId, payMoney, "manual", topUpId); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", userId, err))
	}

	return nil
}
func RechargeCreem(referenceId string, customerEmail string, customerName string) (err error) {
	if referenceId == "" {
		return errors.New("payment order number not provided")
	}

	var quota int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("top-up order does not exist")
		}

		if topUp.PaymentProvider != PaymentProviderCreem {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("top-up order status error")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		// Creem 直接使用 Amount 作为充值额度（整数）
		quota, err = common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(topUp.Amount))
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}

		// 构建更新字段，优先使用邮箱，如果邮箱为空则使用用户名
		updateFields := map[string]interface{}{}

		// 如果有客户邮箱，尝试更新用户邮箱（仅当用户邮箱为空时）
		if customerEmail != "" {
			// 先检查用户当前邮箱是否为空
			var user User
			err = tx.Where("id = ?", topUp.UserId).First(&user).Error
			if err != nil {
				return err
			}

			// 如果用户邮箱为空，则更新为支付时使用的邮箱
			if user.Email == "" {
				updateFields["email"] = customerEmail
			}
		}

		quota, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quota)
		return creditTopUpQuota(tx, topUp.UserId, quota, updateFields)
	})

	if err != nil {
		common.SysError("creem topup failed: " + err.Error())
		return errors.New("top-up failed, please try again later")
	}
	syncCreditUserQuotaCache(topUp.UserId, quota, "creem topup")

	RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("Creem top-up successful, quota: %v, payment amount: %.2f%s", quota, topUp.Money, topUpBonusNote(bonusPercent)))
	common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "creem", topUp.Id)

	// Credit referral commission to inviter (if enabled)
	if err := CreditReferralCommission(topUp.UserId, topUp.Money, "creem", topUp.Id); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", topUp.UserId, err))
	}

	return nil
}

type CreemReversalInput struct {
	EventId       string
	Reference     string // our trade_no, when the payload carries it
	OrderId       string
	TransactionId string
	CustomerId    string
	PaidCents     int // what the buyer paid for the charge being reversed
	ReversedCents int // how much of it went back
	Dispute       bool
}

type CreemReversalResult struct {
	UserId       int
	TopUpId      int
	QuotaRemoved int
	AlreadyDone  bool
}

var ErrCreemReversalUnmatched = errors.New("creem reversal: no user matches the event")

// ReverseCreemTopUp takes back the quota a Creem one-time top-up granted once
// Creem reports the charge refunded or disputed. Matched by our trade_no, then
// by the Creem order or transaction id recorded at checkout, then by the Creem
// customer plus the paid amount. A dispute reverses the whole top-up, a refund
// reverses pro rata. The balance may go negative on purpose: spent credits are
// a debt, not a floor. Idempotent on the top-up status, which is what makes
// Creem's retries safe.
func ReverseCreemTopUp(in CreemReversalInput) (CreemReversalResult, error) {
	var res CreemReversalResult
	status := common.TopUpStatusRefunded
	if in.Dispute {
		status = common.TopUpStatusDisputed
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp, userId, err := resolveCreemReversalTopUp(tx, in)
		if err != nil {
			return err
		}
		res.UserId = userId
		if topUp == nil {
			return ErrTopUpNotFound
		}
		res.TopUpId = topUp.Id
		if topUp.Status == common.TopUpStatusRefunded || topUp.Status == common.TopUpStatusDisputed {
			res.AlreadyDone = true
			return nil
		}
		if topUp.Status != common.TopUpStatusSuccess {
			// Never credited (abandoned or still pending): nothing to take back.
			return tx.Model(&TopUp{}).Where("id = ?", topUp.Id).Update("status", status).Error
		}
		quota := int(topUp.Amount)
		if !in.Dispute && in.PaidCents > 0 && in.ReversedCents > 0 && in.ReversedCents < in.PaidCents {
			quota = int(math.Round(float64(topUp.Amount) * float64(in.ReversedCents) / float64(in.PaidCents)))
		}
		if quota <= 0 {
			return ErrInvalidTopUpQuota
		}
		flip := tx.Model(&TopUp{}).Where("id = ? AND status = ?", topUp.Id, common.TopUpStatusSuccess).Update("status", status)
		if flip.Error != nil {
			return flip.Error
		}
		if flip.RowsAffected == 0 {
			res.AlreadyDone = true
			return nil
		}
		if err := tx.Model(&User{}).Where("id = ?", topUp.UserId).Update("quota", gorm.Expr("quota - ?", quota)).Error; err != nil {
			return err
		}
		res.QuotaRemoved = quota
		return nil
	})
	if err != nil {
		return res, err
	}
	if res.QuotaRemoved > 0 {
		if cerr := invalidateUserCache(res.UserId); cerr != nil {
			common.SysLog("creem reversal: failed to drop user cache: " + cerr.Error())
		}
	}
	return res, nil
}

func resolveCreemReversalTopUp(tx *gorm.DB, in CreemReversalInput) (*TopUp, int, error) {
	var t TopUp
	if in.Reference != "" {
		if err := lockForUpdate(tx).Where("trade_no = ?", in.Reference).First(&t).Error; err == nil {
			return &t, t.UserId, nil
		}
	}
	for _, id := range []string{in.OrderId, in.TransactionId} {
		if id == "" {
			continue
		}
		if err := lockForUpdate(tx).Where("provider_payment_id = ? AND payment_provider = ?", id, PaymentProviderCreem).First(&t).Error; err == nil {
			return &t, t.UserId, nil
		}
	}
	if in.CustomerId == "" {
		return nil, 0, ErrCreemReversalUnmatched
	}
	var user User
	if err := tx.Where("creem_customer = ?", in.CustomerId).First(&user).Error; err != nil {
		return nil, 0, ErrCreemReversalUnmatched
	}
	if in.PaidCents <= 0 {
		return nil, user.Id, nil
	}
	paid := float64(in.PaidCents) / 100
	err := lockForUpdate(tx).
		Where("user_id = ? AND payment_provider = ? AND status = ? AND (abs(paid_amount - ?) < 0.011 OR abs(charged_money - ?) < 0.011 OR abs(money - ?) < 0.011)",
			user.Id, PaymentProviderCreem, common.TopUpStatusSuccess, paid, paid, paid).
		Order("complete_time desc, id desc").First(&t).Error
	if err != nil {
		return nil, user.Id, nil
	}
	return &t, t.UserId, nil
}

// CreemMoneyCommitted sums what an account has put through Creem: top-ups that
// landed, plus checkouts opened in the last hour that may still land.
func CreemMoneyCommitted(userId int) (float64, error) {
	var total float64
	err := DB.Model(&TopUp{}).Select("coalesce(sum(money),0)").
		Where("user_id = ? AND payment_provider = ? AND (status = ? OR (status = ? AND create_time >= ?))",
			userId, PaymentProviderCreem, common.TopUpStatusSuccess, common.TopUpStatusPending, common.GetTimestamp()-3600).
		Scan(&total).Error
	return total, err
}

func RechargeWaffo(tradeNo string, callerIp string) (err error) {
	_ = callerIp
	if tradeNo == "" {
		return errors.New("payment order number not provided")
	}

	var quotaToAdd int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("top-up order does not exist")
		}

		if topUp.PaymentProvider != PaymentProviderWaffo {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil // 幂等：已成功直接返回
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("top-up order status error")
		}

		quotaToAdd, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		quotaToAdd, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quotaToAdd)
		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})

	if err != nil {
		common.SysError("waffo topup failed: " + err.Error())
		return errors.New("top-up failed, please try again later")
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "waffo topup")

	if quotaToAdd > 0 {
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("Waffo top-up succeeded, credit added: %v, amount paid: %.2f%s", logger.FormatQuota(quotaToAdd), topUp.Money, topUpBonusNote(bonusPercent)))
		common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "waffo", topUp.Id)
	}

	return nil
}

func RechargeWaffoPancake(tradeNo string) (err error) {
	if tradeNo == "" {
		return errors.New("payment order number not provided")
	}

	var quotaToAdd int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("top-up order does not exist")
		}

		if topUp.PaymentProvider != PaymentProviderWaffoPancake {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("top-up order status error")
		}

		quotaToAdd, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		quotaToAdd, bonusPercent = applyTopUpBonus(tx, topUp.UserId, quotaToAdd)
		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})

	if err != nil {
		common.SysError("waffo pancake topup failed: " + err.Error())
		return errors.New("top-up failed, please try again later")
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "waffo pancake topup")

	if quotaToAdd > 0 {
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("Waffo Pancake top-up succeeded, credit added: %v, amount paid: %.2f%s", logger.FormatQuota(quotaToAdd), topUp.Money, topUpBonusNote(bonusPercent)))
		common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "waffo_pancake", topUp.Id)
	}

	return nil
}

func RechargeNowPayments(referenceId string, payerCurrency string, actuallyPaid float64) (err error) {
	if referenceId == "" {
		return errors.New("NowPayments order reference not provided")
	}

	var quota int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("NowPayments order not found")
		}

		if topUp.PaymentProvider != PaymentProviderNowPayments {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("NowPayments order status error")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		// Convert through the shared helper like every other gateway: a raw
		// float64 multiplication reaches the UPDATE unrounded and skips the
		// non-positive guard, so a fractional Money would write a fractional
		// quota and a zero one would silently credit nothing.
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
		common.SysError("NowPayments top-up processing failed: " + err.Error())
		return errors.New("NowPayments top-up failed")
	}

	if quota > 0 {
		syncCreditUserQuotaCache(topUp.UserId, quota, "nowpayments topup")
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("NowPayments top-up success, quota: %v, amount: %v, currency: %v, paid: %v%s", logger.FormatQuota(quota), topUp.Money, payerCurrency, actuallyPaid, topUpBonusNote(bonusPercent)))
		common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "nowpayments", topUp.Id)
	}

	if err := CreditReferralCommission(topUp.UserId, topUp.Money, "nowpayments", topUp.Id); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", topUp.UserId, err))
	}

	return nil
}

func RechargeDeloPay(referenceId string, connector string) (err error) {
	if referenceId == "" {
		return errors.New("DeloPay order reference not provided")
	}

	var quota int
	var bonusPercent float64
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("DeloPay order not found")
		}

		if topUp.PaymentProvider != PaymentProviderDeloPay {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("DeloPay order status error")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		// Convert through the shared helper like every other gateway: a raw
		// float64 multiplication reaches the UPDATE unrounded and skips the
		// non-positive guard, so a fractional Money would write a fractional
		// quota and a zero one would silently credit nothing.
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
		common.SysError("DeloPay top-up processing failed: " + err.Error())
		return errors.New("DeloPay top-up failed")
	}

	if quota > 0 {
		syncCreditUserQuotaCache(topUp.UserId, quota, "delopay topup")
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("DeloPay top-up success, quota: %v, amount: %v, connector: %v%s", logger.FormatQuota(quota), topUp.Money, connector, topUpBonusNote(bonusPercent)))
		common.CapturePaymentSuccess(topUp.UserId, topUp.Money, "delopay", topUp.Id)
	}

	if err := CreditReferralCommission(topUp.UserId, topUp.Money, "delopay", topUp.Id); err != nil {
		common.SysLog(fmt.Sprintf("referral commission failed for user %v: %v", topUp.UserId, err))
	}

	return nil
}
