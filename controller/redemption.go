package controller

import (
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-fuego/fuego"

	"github.com/gin-gonic/gin"
)

func GetAllRedemptions(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.Redemption]], error) {
	pageInfo := dto.PageInfo(c)
	redemptions, total, err := model.GetAllRedemptions(pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.Redemption](err.Error())
	}
	return dto.OkPage(pageInfo, redemptions, int(total))
}

func SearchRedemptions(c fuego.ContextWithParams[dto.SearchRedemptionsParams]) (*dto.Response[dto.PageData[*model.Redemption]], error) {
	p, _ := dto.ParseParams[dto.SearchRedemptionsParams](c)
	pageInfo := dto.PageInfo(c)
	redemptions, total, err := model.SearchRedemptions(p.Keyword, p.Status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.Redemption](err.Error())
	}
	return dto.OkPage(pageInfo, redemptions, int(total))
}

func GetRedemption(c fuego.ContextNoBody) (*dto.Response[model.Redemption], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[model.Redemption](err.Error())
	}
	redemption, err := model.GetRedemptionById(id)
	if err != nil {
		return dto.Fail[model.Redemption](err.Error())
	}
	return dto.Ok(*redemption)
}

func AddRedemption(c fuego.ContextWithBody[model.Redemption]) (*dto.Response[[]string], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[[]string]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	redemption, err := c.Body()
	if err != nil {
		return dto.Fail[[]string](err.Error())
	}
	if utf8.RuneCountInString(redemption.Name) == 0 || utf8.RuneCountInString(redemption.Name) > 20 {
		return dto.Fail[[]string]("Redemption code name length must be between 1-20")
	}
	if redemption.Count <= 0 {
		return dto.Fail[[]string]("Redemption code count must be greater than 0")
	}
	if redemption.Count > 100 {
		return dto.Fail[[]string]("Maximum 100 redemption codes can be generated at once")
	}
	if redemption.Quota <= 0 {
		return dto.Fail[[]string]("redemption quota must be positive")
	}
	if err := common.ValidateWalletQuota(redemption.Quota); err != nil {
		return dto.Fail[[]string](err.Error())
	}
	if err := validateExpiredTime(redemption.ExpiredTime); err != nil {
		return dto.Fail[[]string](err.Error())
	}
	var keys []string
	for i := 0; i < redemption.Count; i++ {
		key := common.GetUUID()
		cleanRedemption := model.Redemption{
			UserId:      dto.UserID(c),
			Name:        redemption.Name,
			Key:         key,
			CreatedTime: common.GetTimestamp(),
			Quota:       redemption.Quota,
			ExpiredTime: redemption.ExpiredTime,
		}
		err := cleanRedemption.Insert()
		if err != nil {
			common.SysError(common.LogText("failed to insert redemption: %s", err.Error()))
			return &dto.Response[[]string]{
				Message: common.NewMessage("Failed to create redemption code, please try again later").Error(),
				Data:    keys,
			}, nil
		}
		keys = append(keys, key)
	}
	recordManageAudit(ginCtx, "redemption.create", map[string]any{
		"name":  redemption.Name,
		"count": redemption.Count,
		"quota": logger.FormatQuota(redemption.Quota),
	})
	return dto.Ok(keys)
}

func DeleteRedemption(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	id := c.PathParamInt("id")
	err := model.DeleteRedemptionById(id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

func UpdateRedemption(c fuego.Context[model.Redemption, dto.StatusOnlyParams]) (*dto.Response[model.Redemption], error) {
	p, _ := dto.ParseParams[dto.StatusOnlyParams](c)
	redemption, err := c.Body()
	if err != nil {
		return dto.Fail[model.Redemption](err.Error())
	}
	cleanRedemption, err := model.GetRedemptionById(redemption.Id)
	if err != nil {
		return dto.Fail[model.Redemption](err.Error())
	}
	if p.StatusOnly == "" {
		if redemption.Quota <= 0 {
			return dto.Fail[model.Redemption]("redemption quota must be positive")
		}
		if err := common.ValidateWalletQuota(redemption.Quota); err != nil {
			return dto.Fail[model.Redemption](err.Error())
		}
		if err := validateExpiredTime(redemption.ExpiredTime); err != nil {
			return dto.Fail[model.Redemption](err.Error())
		}
		// If you add more fields, please also update redemption.Update()
		cleanRedemption.Name = redemption.Name
		cleanRedemption.Quota = redemption.Quota
		cleanRedemption.ExpiredTime = redemption.ExpiredTime
	}
	if p.StatusOnly != "" {
		cleanRedemption.Status = redemption.Status
	}
	err = cleanRedemption.Update()
	if err != nil {
		return dto.Fail[model.Redemption](err.Error())
	}
	return dto.Ok(*cleanRedemption)
}

func DeleteInvalidRedemption(c fuego.ContextNoBody) (*dto.Response[int64], error) {
	rows, err := model.DeleteInvalidRedemptions()
	if err != nil {
		return dto.Fail[int64](err.Error())
	}
	return dto.Ok(rows)
}

func validateExpiredTime(expired int64) error {
	if expired != 0 && expired < common.GetTimestamp() {
		return common.NewMessage("Expiration time cannot be earlier than current time")
	}
	return nil
}

func DeleteRedemptionBatch(c *gin.Context) {
	var request struct {
		Ids []int `json:"ids" binding:"required,min=1,max=1000,dive,gt=0"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	count, err := model.BatchDeleteRedemptions(request.Ids)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "redemption.delete_batch", map[string]any{
		"count":                    count,
		"total":                    len(request.Ids),
		"requested_redemption_ids": request.Ids,
	})
	common.ApiSuccess(c, count)
}
