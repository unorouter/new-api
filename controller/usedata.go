package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

func GetAllQuotaDates(c fuego.ContextWithParams[dto.GetAllQuotaDatesParams]) (*dto.Response[[]*model.QuotaData], error) {
	p, err := dto.ParseParams[dto.GetAllQuotaDatesParams](c)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	dates, err := model.GetAllQuotaDates(p.StartTimestamp, p.EndTimestamp, p.Username)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](err.Error())
	}
	return dto.Ok(dates)
}

func GetQuotaDatesByUser(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	dates, err := model.GetQuotaDataGroupByUser(startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    dates,
	})
}

func GetUserQuotaDates(c fuego.ContextWithParams[dto.GetUserQuotaDatesParams]) (*dto.Response[[]*model.QuotaData], error) {
	userId := dto.UserID(c)
	p, err := dto.ParseParams[dto.GetUserQuotaDatesParams](c)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if p.EndTimestamp < p.StartTimestamp {
		return dto.Fail[[]*model.QuotaData](common.TranslateMessage(dto.GinCtx(c), "usedata.end_before_start"))
	}
	if p.EndTimestamp-p.StartTimestamp > 2592000 {
		return dto.Fail[[]*model.QuotaData](common.TranslateMessage(dto.GinCtx(c), "usedata.max_one_month"))
	}
	dates, err := model.GetQuotaDataByUserId(userId, p.StartTimestamp, p.EndTimestamp)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](err.Error())
	}
	return dto.Ok(dates)
}
