package controller

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
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
	attachGroupUptime(&result, p.Model, hours)
	return dto.Ok(result)
}

// Uptime comes from channel transition history, not from traffic, so it answers
// a different question than success_rate: a group nobody called still has an
// uptime. A failure here leaves the field nil rather than failing the request,
// since the latency and success columns are still worth serving.
func attachGroupUptime(result *perfmetrics.QueryResult, modelName string, hours int) {
	if len(result.Groups) == 0 {
		return
	}
	since := time.Now().Unix() - int64(hours)*3600
	uptime, err := model.GroupUptimeForModel(modelName, since)
	if err != nil {
		common.SysLog("perf metrics: group uptime failed: " + err.Error())
		return
	}
	for i := range result.Groups {
		if pct, ok := uptime[result.Groups[i].Group]; ok {
			result.Groups[i].UptimePercent = &pct
		}
	}
}

func filterActiveGroups(groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
	activeRatios := ratio_setting.GetGroupRatioCopy()
	return lo.Filter(groups, func(g perfmetrics.GroupResult, _ int) bool {
		_, ok := activeRatios[g.Group]
		return ok || g.Group == "auto"
	})
}
