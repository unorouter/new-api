package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/go-fuego/fuego"
)

func GetRankings(c fuego.ContextWithParams[dto.GetRankingsParams]) (*dto.Response[*service.RankingsResponse], error) {
	p, err := dto.ParseParams[dto.GetRankingsParams](c)
	if err != nil {
		return dto.Fail[*service.RankingsResponse](err.Error())
	}
	period := p.Period
	if period == "" {
		period = "week"
	}

	result, err := service.GetRankingsSnapshot(period)
	if err != nil {
		return dto.Fail[*service.RankingsResponse](err.Error())
	}
	return dto.Ok(result)
}

func GetModelRanking(c fuego.ContextWithParams[dto.GetModelRankingParams]) (*dto.Response[*service.ModelRankingResponse], error) {
	p, err := dto.ParseParams[dto.GetModelRankingParams](c)
	if err != nil {
		return dto.Fail[*service.ModelRankingResponse](err.Error())
	}
	if p.Model == "" {
		return dto.Fail[*service.ModelRankingResponse]("model is required")
	}
	period := p.Period
	if period == "" {
		period = "week"
	}

	result, err := service.GetModelRankingSnapshot(p.Model, period)
	if err != nil {
		return dto.Fail[*service.ModelRankingResponse](err.Error())
	}
	return dto.Ok(result)
}
