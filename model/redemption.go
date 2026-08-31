package model

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"gorm.io/gorm"
)

type Redemption struct {
	Id int `json:"id"`
	// Who created the code, set from the caller at insert. UsedUserId is the
	// separate column for whoever redeemed it. Indexed because an enterprise
	// partner's listing filters on it to see only their own codes.
	UserId       int            `json:"user_id" gorm:"index"`
	Key          string         `json:"key" gorm:"type:char(32);uniqueIndex"`
	Status       int            `json:"status" gorm:"default:1"`
	Name         string         `json:"name" gorm:"index"`
	Quota        int            `json:"quota" gorm:"default:100"`
	CreatedTime  int64          `json:"created_time" gorm:"bigint"`
	RedeemedTime int64          `json:"redeemed_time" gorm:"bigint"`
	Count        int            `json:"count" gorm:"-:all"` // only for api request
	UsedUserId   int            `json:"used_user_id"`
	DeletedAt    gorm.DeletedAt `gorm:"index"`
	ExpiredTime  int64          `json:"expired_time" gorm:"bigint"` // 过期时间，0 表示不过期
}

func GetAllRedemptions(startIdx int, num int) (redemptions []*Redemption, total int64, err error) {
	// 开始事务
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 获取总数
	err = tx.Model(&Redemption{}).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 获取分页数据
	err = tx.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 提交事务
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return redemptions, total, nil
}

func SearchRedemptions(keyword string, status string, startIdx int, num int) (redemptions []*Redemption, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&Redemption{})

	if keyword != "" {
		if id, err := strconv.Atoi(keyword); err == nil {
			query = query.Where("id = ? OR name LIKE ?", id, keyword+"%")
		} else {
			query = query.Where("name LIKE ?", keyword+"%")
		}
	}

	if status != "" {
		now := common.GetTimestamp()
		switch status {
		case "expired":
			query = query.Where(
				"status = ? AND expired_time != 0 AND expired_time < ?",
				common.RedemptionCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.RedemptionCodeStatusEnabled):
			query = query.Where(
				"status = ? AND (expired_time = 0 OR expired_time >= ?)",
				common.RedemptionCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.RedemptionCodeStatusDisabled):
			query = query.Where("status = ?", common.RedemptionCodeStatusDisabled)
		case strconv.Itoa(common.RedemptionCodeStatusUsed):
			query = query.Where("status = ?", common.RedemptionCodeStatusUsed)
		}
	}

	// Get total count
	err = query.Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated data
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return redemptions, total, nil
}

func GetRedemptionById(id int) (*Redemption, error) {
	if id == 0 {
		return nil, errors.New("id is empty!")
	}
	redemption := Redemption{Id: id}
	var err error = nil
	err = DB.First(&redemption, "id = ?", id).Error
	return &redemption, err
}

func Redeem(key string, userId int) (quota int, err error) {
	if key == "" {
		return 0, errors.New("no redemption code provided")
	}
	if userId == 0 {
		return 0, errors.New("invalid user id")
	}
	redemption := &Redemption{}

	keyCol := "`key`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		keyCol = `"key"`
	}
	common.RandomSleep()
	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(keyCol+" = ?", key).First(redemption).Error
		if err != nil {
			return errors.New("invalid redemption code")
		}
		if redemption.Status != common.RedemptionCodeStatusEnabled {
			return errors.New("this redemption code has already been used")
		}
		if redemption.ExpiredTime != 0 && redemption.ExpiredTime < common.GetTimestamp() {
			return errors.New("this redemption code has expired")
		}
		// Compare-and-swap on status: only the transaction that flips
		// enabled -> used may credit quota, so a concurrent redeem of the
		// same code loses here even without a row lock (e.g. on SQLite).
		result := tx.Model(&Redemption{}).
			Where("id = ? AND status = ?", redemption.Id, common.RedemptionCodeStatusEnabled).
			Updates(map[string]interface{}{
				"redeemed_time": common.GetTimestamp(),
				"status":        common.RedemptionCodeStatusUsed,
				"used_user_id":  userId,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("this redemption code has already been used")
		}
		return creditTopUpQuota(tx, userId, redemption.Quota, nil)
	})
	if err != nil {
		common.SysError("redemption failed: " + err.Error())
		return 0, ErrRedeemFailed
	}
	syncCreditUserQuotaCache(userId, redemption.Quota, "redemption")
	RecordLog(userId, LogTypeTopup, fmt.Sprintf("Topped up %s via redemption code, code ID %d", logger.LogQuota(redemption.Quota), redemption.Id))
	return redemption.Quota, nil
}

func (redemption *Redemption) Insert() error {
	if redemption.Quota <= 0 {
		return errors.New("redemption quota must be positive")
	}
	if err := common.ValidateWalletQuota(redemption.Quota); err != nil {
		return err
	}
	var err error
	err = DB.Create(redemption).Error
	return err
}

func (redemption *Redemption) SelectUpdate() error {
	// This can update zero values
	return DB.Model(redemption).Select("redeemed_time", "status").Updates(redemption).Error
}

// Update Make sure your token's fields is completed, because this will update non-zero values
func (redemption *Redemption) Update() error {
	if redemption.Quota <= 0 {
		return errors.New("redemption quota must be positive")
	}
	if err := common.ValidateWalletQuota(redemption.Quota); err != nil {
		return err
	}
	var err error
	err = DB.Model(redemption).Select("name", "status", "quota", "redeemed_time", "expired_time").Updates(redemption).Error
	return err
}

func (redemption *Redemption) Delete() error {
	var err error
	err = DB.Delete(redemption).Error
	return err
}

func DeleteRedemptionById(id int) (err error) {
	if id == 0 {
		return errors.New("id is empty!")
	}
	redemption := Redemption{Id: id}
	err = DB.Where(redemption).First(&redemption).Error
	if err != nil {
		return err
	}
	return redemption.Delete()
}

func DeleteInvalidRedemptions() (int64, error) {
	now := common.GetTimestamp()
	result := DB.Where("status IN ? OR (status = ? AND expired_time != 0 AND expired_time < ?)", []int{common.RedemptionCodeStatusUsed, common.RedemptionCodeStatusDisabled}, common.RedemptionCodeStatusEnabled, now).Delete(&Redemption{})
	return result.RowsAffected, result.Error
}

// ErrRedemptionNotVoidable means the code was not the caller's, or was already
// redeemed or voided. Deliberately one error for all three: a partner must not be
// able to probe another partner's code ids by the difference in the message.
var ErrRedemptionNotVoidable = errors.New("redemption code cannot be voided")

// CreateFundedRedemption mints a single gift card paid for out of the creator's
// own balance, for enterprise partners who resell credit to their customers.
//
// One card per call, never a batch: the stock AddRedemption loop inserts codes
// one at a time and does not roll back a partial failure, which is harmless when
// codes are conjured by an admin and a money bug once they are funded.
//
// The deduction goes through TryReserveUserQuota, the existing atomic
// check-and-deduct, rather than a hand-rolled UPDATE: it holds the balance
// guard AND keeps the quota cache coherent. Do not add a manual
// cacheDecrUserQuota here, it would double-count.
func CreateFundedRedemption(creatorId int, name string, quota int, expiredTime int64) (string, error) {
	if creatorId <= 0 {
		return "", errors.New("invalid creator id")
	}
	if quota <= 0 {
		return "", errors.New("redemption quota must be positive")
	}
	if err := common.ValidateWalletQuota(quota); err != nil {
		return "", err
	}

	reserved, err := TryReserveUserQuota(creatorId, quota)
	if err != nil {
		return "", err
	}
	if !reserved {
		return "", ErrInsufficientQuota
	}

	redemption := &Redemption{
		UserId:      creatorId,
		Name:        name,
		Key:         common.GetUUID(),
		Status:      common.RedemptionCodeStatusEnabled,
		Quota:       quota,
		CreatedTime: common.GetTimestamp(),
		ExpiredTime: expiredTime,
	}
	if err := redemption.Insert(); err != nil {
		// Give the money back: the partner must never be charged for a card that
		// does not exist. Single row, so this compensation cannot be partial.
		if refundErr := IncreaseUserQuota(creatorId, quota, true); refundErr != nil {
			common.SysError(fmt.Sprintf("failed to refund reserved quota after redemption insert failed user_id=%d quota=%d: %v", creatorId, quota, refundErr))
		}
		return "", err
	}

	RecordLog(creatorId, LogTypeManage, fmt.Sprintf("Created gift card %s from balance, code ID %d", logger.LogQuota(quota), redemption.Id))
	return redemption.Key, nil
}

// VoidFundedRedemption disables an unredeemed code the caller minted and returns
// its full face value to their balance.
//
// The status flip is the same compare-and-swap the redeem path uses, so a void
// racing a customer's redemption cannot both refund the partner and credit the
// customer: whichever transaction flips enabled -> {disabled,used} first wins and
// the loser changes nothing. The refund therefore runs ONLY when this update
// actually claimed the row.
//
// user_id in the predicate is the authorization: a partner can only void a code
// they created.
func VoidFundedRedemption(creatorId int, redemptionId int) (int, error) {
	if creatorId <= 0 || redemptionId <= 0 {
		return 0, ErrRedemptionNotVoidable
	}

	var quota int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var redemption Redemption
		if err := tx.Where("id = ? AND user_id = ?", redemptionId, creatorId).
			First(&redemption).Error; err != nil {
			return ErrRedemptionNotVoidable
		}
		result := tx.Model(&Redemption{}).
			Where("id = ? AND user_id = ? AND status = ?", redemptionId, creatorId, common.RedemptionCodeStatusEnabled).
			Update("status", common.RedemptionCodeStatusDisabled)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrRedemptionNotVoidable
		}
		quota = redemption.Quota
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Outside the transaction, mirroring how Redeem credits after commit.
	// IncreaseUserQuota guards the wallet ceiling and syncs the cache itself.
	if err := IncreaseUserQuota(creatorId, quota, true); err != nil {
		common.SysError(fmt.Sprintf("failed to refund voided redemption user_id=%d redemption_id=%d quota=%d: %v", creatorId, redemptionId, quota, err))
		return 0, err
	}
	RecordLog(creatorId, LogTypeManage, fmt.Sprintf("Voided gift card, refunded %s, code ID %d", logger.LogQuota(quota), redemptionId))
	return quota, nil
}

// GetRedemptionsByCreator lists one partner's own codes. The user_id predicate is
// what makes the partner API multi-tenant: without it every partner would see
// every other partner's codes, since the stock list and search functions do not
// filter by owner at all.
func GetRedemptionsByCreator(creatorId int, startIdx int, num int) ([]*Redemption, int64, error) {
	if creatorId <= 0 {
		return nil, 0, errors.New("invalid creator id")
	}
	var redemptions []*Redemption
	var total int64
	query := DB.Model(&Redemption{}).Where("user_id = ?", creatorId)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error; err != nil {
		return nil, 0, err
	}
	return redemptions, total, nil
}
