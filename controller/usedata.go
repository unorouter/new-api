package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-fuego/fuego"
)

func GetAllQuotaDates(c fuego.ContextWithParams[dto.GetAllQuotaDatesParams]) (*dto.Response[[]*model.QuotaData], error) {
	p, _ := dto.ParseParams[dto.GetAllQuotaDatesParams](c)
	dates, err := model.GetAllQuotaDates(p.StartTimestamp, p.EndTimestamp, p.Username)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](err.Error())
	}
	return dto.Ok(dates)
}

func GetUserQuotaDates(c fuego.ContextWithParams[dto.GetUserQuotaDatesParams]) (*dto.Response[[]*model.QuotaData], error) {
	userId := dto.UserID(c)
	p, _ := dto.ParseParams[dto.GetUserQuotaDatesParams](c)
	if p.EndTimestamp-p.StartTimestamp > 2592000 {
		return dto.Fail[[]*model.QuotaData]("时间跨度不能超过 1 个月")
	}
	dates, err := model.GetQuotaDataByUserId(userId, p.StartTimestamp, p.EndTimestamp)
	if err != nil {
		return dto.Fail[[]*model.QuotaData](err.Error())
	}
	return dto.Ok(dates)
}
