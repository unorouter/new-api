package controller

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/go-fuego/fuego"
)

// Enterprise partners resell UnoRouter credit: they top up their own account at a
// negotiated discount, then either mint gift cards for their customers or grant
// balance to them directly. Both spend the partner's own balance, so these routes
// are reachable with a PAT (UserAuth, no SessionOnly) but must never be reachable
// by an ordinary user.
//
// A partner is identified by having a negotiated top-up bonus. This reuses the
// pricing field as the permission rather than introducing a second flag: an
// account only carries a bonus because we signed a commercial agreement with it.
// The tradeoff is that a 0% partner, or a bonused non-partner, would need this to
// become its own column.
func requirePartner(c dto.FuegoCtx) (int, error) {
	userId := dto.UserID(c)
	if userId <= 0 {
		return 0, errors.New("unauthorized")
	}
	user, err := model.GetUserById(userId, true)
	if err != nil || user == nil {
		return 0, errors.New("unauthorized")
	}
	if user.TopUpBonusPercent == nil || *user.TopUpBonusPercent <= 0 {
		return 0, errors.New("this account is not enrolled in the partner programme")
	}
	return userId, nil
}

// PartnerCreateRedemption mints ONE gift card funded from the caller's balance.
// Deliberately no count parameter: see model.CreateFundedRedemption for why a
// batch is a money bug once the codes are paid for.
func PartnerCreateRedemption(c fuego.ContextWithBody[dto.PartnerRedemptionRequest]) (*dto.Response[dto.PartnerRedemptionData], error) {
	ginCtx := dto.GinCtx(c)
	userId, err := requirePartner(c)
	if err != nil {
		return dto.Fail[dto.PartnerRedemptionData](err.Error())
	}
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[dto.PartnerRedemptionData](common.TranslateMessage(ginCtx, i18n.MsgPaymentComplianceRequired))
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.PartnerRedemptionData](err.Error())
	}
	if n := utf8.RuneCountInString(req.Name); n == 0 || n > 20 {
		return dto.Fail[dto.PartnerRedemptionData](common.TranslateMessage(ginCtx, i18n.MsgRedemptionNameLength))
	}
	if req.Quota <= 0 {
		return dto.Fail[dto.PartnerRedemptionData]("redemption quota must be positive")
	}
	if valid, msg := validateExpiredTime(ginCtx, req.ExpiredTime); !valid {
		return dto.Fail[dto.PartnerRedemptionData](msg)
	}

	key, err := model.CreateFundedRedemption(userId, req.Name, req.Quota, req.ExpiredTime)
	if err != nil {
		if errors.Is(err, model.ErrInsufficientQuota) {
			return dto.Fail[dto.PartnerRedemptionData]("insufficient balance to fund this gift card")
		}
		return dto.Fail[dto.PartnerRedemptionData](err.Error())
	}
	// Minting converts balance into a bearer code, so a stolen PAT can drain an
	// account into codes it controls. The audit records auth_method and IP,
	// which is what separates a stolen token from the real partner. The key
	// itself is never logged: it spends like cash.
	recordUserSecurityAudit(ginCtx, userId, "partner.redemption_create", map[string]interface{}{
		"name":  req.Name,
		"quota": req.Quota,
	})
	return dto.Ok(dto.PartnerRedemptionData{Key: key, Quota: req.Quota})
}

// PartnerListRedemptions returns only the caller's own codes, including voided
// ones so a refund can be reconciled against the code that caused it.
func PartnerListRedemptions(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.Redemption]], error) {
	userId, err := requirePartner(c)
	if err != nil {
		return dto.FailPage[*model.Redemption](err.Error())
	}
	pageInfo := dto.PageInfo(c)
	redemptions, total, err := model.GetRedemptionsByCreator(userId, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.Redemption](err.Error())
	}
	return dto.OkPage(pageInfo, redemptions, int(total))
}

// PartnerVoidRedemption disables an unredeemed code the caller minted and returns
// its face value. A code already redeemed by a customer refunds nothing.
func PartnerVoidRedemption(c fuego.ContextNoBody) (*dto.Response[dto.PartnerVoidData], error) {
	userId, err := requirePartner(c)
	if err != nil {
		return dto.Fail[dto.PartnerVoidData](err.Error())
	}
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[dto.PartnerVoidData](err.Error())
	}

	refunded, err := model.VoidFundedRedemption(userId, id)
	if err != nil {
		if errors.Is(err, model.ErrRedemptionNotVoidable) {
			return dto.Fail[dto.PartnerVoidData]("this code cannot be voided: it does not exist, is not yours, or has already been used")
		}
		return dto.Fail[dto.PartnerVoidData](err.Error())
	}
	recordUserSecurityAudit(dto.GinCtx(c), userId, "partner.redemption_void", map[string]interface{}{
		"id":       id,
		"refunded": refunded,
	})
	return dto.Ok(dto.PartnerVoidData{Refunded: refunded})
}

// PartnerGrantQuota moves balance from the partner to another user. Wraps the
// existing TransferQuotaBetweenUsers, which locks both rows in ascending id order
// and re-checks the sender's balance under the lock.
func PartnerGrantQuota(c fuego.ContextWithBody[dto.PartnerGrantRequest]) (*dto.Response[dto.PartnerGrantData], error) {
	userId, err := requirePartner(c)
	if err != nil {
		return dto.Fail[dto.PartnerGrantData](err.Error())
	}
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.PartnerGrantData](err.Error())
	}
	if req.UserId <= 0 {
		return dto.Fail[dto.PartnerGrantData]("invalid recipient user id")
	}
	if req.UserId == userId {
		return dto.Fail[dto.PartnerGrantData]("cannot grant balance to yourself")
	}
	if req.Quota <= 0 {
		return dto.Fail[dto.PartnerGrantData]("grant quota must be positive")
	}
	recipient, err := model.GetUserById(req.UserId, false)
	if err != nil || recipient == nil {
		return dto.Fail[dto.PartnerGrantData]("recipient not found")
	}

	balanceAfter, err := model.TransferQuotaBetweenUsers(userId, req.UserId, req.Quota)
	if err != nil {
		if errors.Is(err, model.ErrInsufficientQuota) {
			return dto.Fail[dto.PartnerGrantData]("insufficient balance for this grant")
		}
		return dto.Fail[dto.PartnerGrantData](err.Error())
	}

	// Both sides get a row: the sender sees what left, the recipient sees what
	// arrived, mirroring TransferDiscordQuota.
	model.RecordLog(userId, model.LogTypeManage, "Granted "+logger.LogQuota(req.Quota)+" to user "+strconv.Itoa(req.UserId))
	model.RecordLog(req.UserId, model.LogTypeTopup, "Received "+logger.LogQuota(req.Quota)+" from a partner account")

	// The irreversible one: balance leaves for an account the caller names, and
	// nothing here can claw it back. The RecordLog rows above are the partner's
	// own statement; this records WHO authorised it and from where.
	recordUserSecurityAudit(dto.GinCtx(c), userId, "partner.grant", map[string]interface{}{
		"recipient_id": req.UserId,
		"quota":        req.Quota,
	})

	return dto.Ok(dto.PartnerGrantData{Granted: req.Quota, BalanceAfter: balanceAfter})
}
