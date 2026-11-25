package controller

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"
)

// usablePerfMetricGroups limits public metrics to the groups the caller may
// use, so a private group's traffic is not visible to everyone.
func usablePerfMetricGroups(c *gin.Context) []string {
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	usableGroups := service.GetUserUsableGroups(userGroup)
	// A non-nil empty list restricts the metrics query to zero groups.
	groups := make([]string, 0, len(usableGroups))
	for group := range usableGroups {
		if group == "auto" || ratio_setting.ContainsGroupRatio(group) {
			groups = append(groups, group)
		}
	}
	slices.Sort(groups)
	return groups
}

func GetPerfMetricsSummary(c fuego.ContextWithParams[dto.GetPerfMetricsSummaryParams]) (*dto.Response[perfmetrics.SummaryAllResult], error) {
	p, err := dto.ParseParams[dto.GetPerfMetricsSummaryParams](c)
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	g := dto.GinCtx(c)
	result, err := perfSummaryForGroups(p.Hours, g.QueryArray("recent_model"), usablePerfMetricGroups(g))
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	return dto.Ok(result)
}

func GetDashboardPerfMetricsSummary(c *gin.Context) {
	hours, _ := strconv.Atoi(c.Query("hours"))
	groups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	slices.Sort(groups)
	result, err := perfSummaryForGroups(hours, c.QueryArray("recent_model"), groups)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func perfSummaryForGroups(hours int, recentModels []string, groups []string) (perfmetrics.SummaryAllResult, error) {
	if hours <= 0 {
		hours = 24
	}
	if len(recentModels) > perfmetrics.MaxRecentModels {
		return perfmetrics.SummaryAllResult{}, errors.New("Invalid parameters")
	}
	sorted := slices.Clone(recentModels)
	slices.Sort(sorted)
	key := strconv.Itoa(hours) + "|" + strings.Join(sorted, ",") + "|" + strings.Join(groups, ",")
	return cachedPublicPerfSummary(key, func() (perfmetrics.SummaryAllResult, error) {
		return perfmetrics.QuerySummaryAll(hours, groups, recentModels...)
	})
}

type publicPerfSummaryItem struct {
	expiresAt time.Time
	data      perfmetrics.SummaryAllResult
}

var (
	publicPerfSummaryMu     sync.Mutex
	publicPerfSummaryCache  = map[string]publicPerfSummaryItem{}
	publicPerfSummaryBuilds singleflight.Group
)

// cachedPublicPerfSummary serves the public summary for a minute per query: each call
// was up to 51 uncached aggregations, open to anonymous callers.
func cachedPublicPerfSummary(key string, build func() (perfmetrics.SummaryAllResult, error)) (perfmetrics.SummaryAllResult, error) {
	publicPerfSummaryMu.Lock()
	if item, ok := publicPerfSummaryCache[key]; ok && time.Now().Before(item.expiresAt) {
		publicPerfSummaryMu.Unlock()
		return item.data, nil
	}
	publicPerfSummaryMu.Unlock()
	built, err, _ := publicPerfSummaryBuilds.Do(key, func() (any, error) {
		data, err := build()
		if err != nil {
			return nil, err
		}
		publicPerfSummaryMu.Lock()
		// Keys are caller-chosen model lists; drop everything rather than grow forever.
		if len(publicPerfSummaryCache) >= 512 {
			clear(publicPerfSummaryCache)
		}
		publicPerfSummaryCache[key] = publicPerfSummaryItem{expiresAt: time.Now().Add(time.Minute), data: data}
		publicPerfSummaryMu.Unlock()
		return data, nil
	})
	if err != nil {
		return perfmetrics.SummaryAllResult{}, err
	}
	data, ok := built.(perfmetrics.SummaryAllResult)
	if !ok {
		return perfmetrics.SummaryAllResult{}, fmt.Errorf("unexpected perf summary result %T", built)
	}
	return data, nil
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

	groups := usablePerfMetricGroups(dto.GinCtx(c))
	build := func() (perfmetrics.QueryResult, error) {
		result, err := perfmetrics.Query(perfmetrics.QueryParams{
			Model:         p.Model,
			Group:         p.Group,
			Hours:         hours,
			AllowedGroups: groups,
		})
		if err != nil {
			return result, err
		}
		attachGroupUptime(&result, p.Model, hours)
		return result, nil
	}
	// Model, group and hours are free input, so only the model page's own shape is cached:
	// a known model, every group, the default window.
	_, known := model.GetPricingByModelName(p.Model)
	var result perfmetrics.QueryResult
	if known && p.Group == "" && hours == 24 {
		result, err = memoryCached("perf:"+p.Model+"|"+strings.Join(groups, ","), time.Minute, build)
	} else {
		result, err = build()
	}
	if err != nil {
		return dto.Fail[perfmetrics.QueryResult](err.Error())
	}
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

// GetSelfPerfMetrics answers in the public summary shape, from the caller's own
// request logs instead of the site-wide metrics.
func GetSelfPerfMetrics(c fuego.ContextWithParams[dto.GetPerfMetricsSummaryParams]) (*dto.Response[perfmetrics.SummaryAllResult], error) {
	p, err := dto.ParseParams[dto.GetPerfMetricsSummaryParams](c)
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	hours := p.Hours
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	end := time.Now().Unix()
	start := end - int64(hours)*3600
	rows, err := model.GetUserModelPerf(dto.UserID(c), start)
	if err != nil {
		return dto.Fail[perfmetrics.SummaryAllResult](err.Error())
	}
	result := perfmetrics.SummaryAllResult{WindowStart: start, WindowEnd: end, Models: []perfmetrics.ModelSummary{}}
	var requests, successes, okRows, okSeconds, okTokens int64
	for _, r := range rows {
		m := perfmetrics.ModelSummary{ModelName: r.ModelName, RequestCount: r.Requests}
		if r.Requests > 0 {
			m.SuccessRate = float64(r.Successes) * 100 / float64(r.Requests)
		}
		if r.OkRows > 0 {
			m.AvgLatencyMs = r.OkSeconds * 1000 / r.OkRows
		}
		if r.OkSeconds > 0 {
			m.AvgTps = float64(r.OkTokens) / float64(r.OkSeconds)
		}
		result.Models = append(result.Models, m)
		requests += r.Requests
		successes += r.Successes
		okRows += r.OkRows
		okSeconds += r.OkSeconds
		okTokens += r.OkTokens
	}
	if requests > 0 {
		summary := &perfmetrics.Summary{SuccessRate: float64(successes) * 100 / float64(requests)}
		if okRows > 0 {
			summary.AvgLatencyMs = okSeconds * 1000 / okRows
		}
		if okSeconds > 0 {
			summary.AvgTps = float64(okTokens) / float64(okSeconds)
		}
		result.Summary = summary
	}
	return dto.Ok(result)
}
