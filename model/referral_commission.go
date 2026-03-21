package model

import "github.com/QuantumNous/new-api/common"

type ReferralCommission struct {
	Id              int     `json:"id"               gorm:"primaryKey"`
	InviterId       int     `json:"inviter_id"       gorm:"index"`
	InviteeId       int     `json:"invitee_id"       gorm:"index;uniqueIndex:idx_invitee_topup_method"`
	TopUpId         int     `json:"top_up_id"        gorm:"uniqueIndex:idx_invitee_topup_method"`
	RechargeAmount  float64 `json:"recharge_amount"`
	CommissionQuota int     `json:"commission_quota"`
	CommissionRate  float64 `json:"commission_rate"`
	PaymentMethod   string  `json:"payment_method"   gorm:"type:varchar(50);uniqueIndex:idx_invitee_topup_method"`
	CreatedAt       int64   `json:"created_at"       gorm:"autoCreateTime"`
}

type ReferralCommissionWithUser struct {
	ReferralCommission
	InviteeUsername string `json:"invitee_username"`
}

func GetUserReferralCommissions(inviterId int, pageInfo *common.PageInfo) ([]*ReferralCommissionWithUser, int64, error) {
	var total int64
	var commissions []*ReferralCommissionWithUser

	query := DB.Table("referral_commissions").
		Select("referral_commissions.*, users.username as invitee_username").
		Joins("LEFT JOIN users ON users.id = referral_commissions.invitee_id").
		Where("referral_commissions.inviter_id = ?", inviterId)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := query.Order("referral_commissions.id desc").
		Limit(pageInfo.GetPageSize()).
		Offset(pageInfo.GetStartIdx()).
		Find(&commissions).Error
	return commissions, total, err
}
