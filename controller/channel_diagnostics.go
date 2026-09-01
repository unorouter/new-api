package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-fuego/fuego"
)

// GetChannelDiagnostics returns a paginated, filtered list of channel diagnostic
// rows (status transitions + recurring probe failures, newest first). Admin-only.
func GetChannelDiagnostics(c fuego.ContextWithParams[dto.GetChannelDiagnosticsParams]) (*dto.Response[dto.PageData[*model.ChannelDiagnostic]], error) {
	pageInfo := dto.PageInfo(c)
	p, _ := dto.ParseParams[dto.GetChannelDiagnosticsParams](c)
	filter := model.ChannelDiagnosticFilter{
		ChannelId:      p.ChannelId,
		ToStatus:       p.ToStatus,
		TriggerSource:  p.TriggerSource,
		StatusCode:     p.StatusCode,
		ModelName:      p.ModelName,
		Keyword:        p.Keyword,
		RowType:        p.RowType,
		StartTimestamp: p.StartTimestamp,
		EndTimestamp:   p.EndTimestamp,
		SortBy:         p.SortBy,
		SortOrder:      p.SortOrder,
	}
	rows, total, err := model.GetChannelDiagnostics(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.ChannelDiagnostic](err.Error())
	}
	return dto.OkPage(pageInfo, rows, int(total))
}

// GetChannelDiagnosticStats returns per-channel transition / uptime aggregates over
// a time window, sorted by the requested dimension. Powers the "flappiest /
// worst-uptime / most-downtime" admin view. Admin-only.
func GetChannelDiagnosticStats(c fuego.ContextWithParams[dto.GetChannelDiagnosticStatsParams]) (*dto.Response[[]*model.ChannelDiagnosticStatRow], error) {
	p, _ := dto.ParseParams[dto.GetChannelDiagnosticStatsParams](c)
	since := p.StartTimestamp
	if since == 0 {
		since = common.GetTimestamp() - 7*24*3600
	}
	rows, err := model.ChannelDiagnosticStats(since, p.OrderBy, p.Limit)
	if err != nil {
		return dto.Fail[[]*model.ChannelDiagnosticStatRow](err.Error())
	}
	return dto.Ok(rows)
}

// PruneChannelDiagnostics deletes rows older than a cutoff. Admin-only.
func PruneChannelDiagnostics(c fuego.ContextWithParams[dto.PruneChannelDiagnosticsParams]) (*dto.Response[int64], error) {
	p, _ := dto.ParseParams[dto.PruneChannelDiagnosticsParams](c)
	if p.BeforeTimestamp == 0 {
		return dto.Fail[int64]("before_timestamp is required")
	}
	deleted, err := model.PruneChannelDiagnosticsBefore(p.BeforeTimestamp)
	if err != nil {
		return dto.Fail[int64](err.Error())
	}
	return dto.Ok(deleted)
}
