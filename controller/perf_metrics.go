package controller

import (
	"github.com/QuantumNous/new-api/dto"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/go-fuego/fuego"
	"github.com/samber/lo"
)

func GetPerfMetricsSummary(c fuego.ContextWithParams[dto.GetPerfMetricsSummaryParams]) (*dto.Response[perfmetrics.SummaryAllResult], error) {
	p, err := dto.ParseParams[dto.GetPerfMetricsSummaryParams](c)
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	hours := p.Hours
	if hours <= 0 {
		hours = 24
	}

	activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	result, err := perfmetrics.QuerySummaryAll(hours, activeGroups)
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	return dto.Ok(result)
}

func GetPerfMetrics(c fuego.ContextWithParams[dto.GetPerfMetricsParams]) (*dto.Response[perfmetrics.QueryResult], error) {
	p, err := dto.ParseParams[dto.GetPerfMetricsParams](c)
	if err != nil {
		return dto.Fail[perfmetrics.QueryResult](err.Error())
	}
	if p.Model == "" {
		return dto.Fail[perfmetrics.QueryResult]("model is required")
	}
	hours := p.Hours
	if hours <= 0 {
		hours = 24
	}

	result, err := perfmetrics.Query(perfmetrics.QueryParams{
		Model: p.Model,
		Group: p.Group,
		Hours: hours,
	})
	if err != nil {
		return dto.Fail[perfmetrics.QueryResult](err.Error())
	}

	result.Groups = filterActiveGroups(result.Groups)
	return dto.Ok(result)
}

func filterActiveGroups(groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
	activeRatios := ratio_setting.GetGroupRatioCopy()
	return lo.Filter(groups, func(g perfmetrics.GroupResult, _ int) bool {
		_, ok := activeRatios[g.Group]
		return ok || g.Group == "auto"
	})
}
