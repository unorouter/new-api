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

// GetQuotaDataSummary returns SQL-aggregated totals over quota_data. Exists so
// totals-only consumers (the site badge) stop pulling the entire table (1M+
// rows) just to sum three columns.
func GetQuotaDataSummary(c fuego.ContextWithParams[dto.GetQuotaSummaryParams]) (*dto.Response[*model.QuotaDataSummary], error) {
	p, err := dto.ParseParams[dto.GetQuotaSummaryParams](c)
	if err != nil {
		return dto.Fail[*model.QuotaDataSummary](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	summary, err := model.GetQuotaDataSummary(p.StartTimestamp, p.EndTimestamp)
	if err != nil {
		return dto.Fail[*model.QuotaDataSummary](err.Error())
	}
	return dto.Ok(summary)
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

const maxQuotaDateSpan = 2592000

// GetFlowQuotaDates serves the admin/root Sankey. The model widens the returned
// dimensions by role, so the caller's real role is passed through.
func GetFlowQuotaDates(c fuego.ContextWithParams[dto.GetFlowQuotaDatesParams]) (*dto.Response[[]*model.FlowQuotaData], error) {
	p, err := dto.ParseParams[dto.GetFlowQuotaDatesParams](c)
	if err != nil {
		return dto.Fail[[]*model.FlowQuotaData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if p.EndTimestamp < p.StartTimestamp {
		return dto.Fail[[]*model.FlowQuotaData]("End time cannot be earlier than start time")
	}
	if p.EndTimestamp-p.StartTimestamp > maxQuotaDateSpan {
		return dto.Fail[[]*model.FlowQuotaData]("Time span cannot exceed 1 month")
	}
	ginCtx := dto.GinCtx(c)
	dates, err := model.GetFlowQuotaData(p.StartTimestamp, p.EndTimestamp, p.Username, dto.UserID(c), ginCtx.GetInt("role"))
	if err != nil {
		return dto.Fail[[]*model.FlowQuotaData](err.Error())
	}
	return dto.Ok(dates)
}

// GetUserFlowQuotaDates serves the self Sankey. Role is forced to common user
// and username is dropped so an admin's own call cannot widen the scope here.
func GetUserFlowQuotaDates(c fuego.ContextWithParams[dto.GetFlowQuotaDatesParams]) (*dto.Response[[]*model.FlowQuotaData], error) {
	p, err := dto.ParseParams[dto.GetFlowQuotaDatesParams](c)
	if err != nil {
		return dto.Fail[[]*model.FlowQuotaData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if p.EndTimestamp < p.StartTimestamp {
		return dto.Fail[[]*model.FlowQuotaData]("End time cannot be earlier than start time")
	}
	if p.EndTimestamp-p.StartTimestamp > maxQuotaDateSpan {
		return dto.Fail[[]*model.FlowQuotaData]("Time span cannot exceed 1 month")
	}
	dates, err := model.GetFlowQuotaData(p.StartTimestamp, p.EndTimestamp, "", dto.UserID(c), common.RoleCommonUser)
	if err != nil {
		return dto.Fail[[]*model.FlowQuotaData](err.Error())
	}
	return dto.Ok(dates)
}

func GetUserQuotaDates(c fuego.ContextWithParams[dto.GetUserQuotaDatesParams]) (*dto.Response[[]*model.QuotaData], error) {
	userId := dto.UserID(c)
	p, err := dto.ParseParams[dto.GetUserQuotaDatesParams](c)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if p.EndTimestamp < p.StartTimestamp {
		return dto.Fail[[]*model.QuotaData]("End time cannot be earlier than start time")
	}
	if p.EndTimestamp-p.StartTimestamp > maxQuotaDateSpan {
		return dto.Fail[[]*model.QuotaData]("Time span cannot exceed 1 month")
	}
	dates, err := model.GetQuotaDataByUserId(userId, p.StartTimestamp, p.EndTimestamp)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](err.Error())
	}
	return dto.Ok(dates)
}
