package controller

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

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

// LanePromptCapacityRow is one lane's learned long-prompt state. RejectsFrom is the
// prompt size from which the picker passes the lane over, 0 when nothing is learned.
type LanePromptCapacityRow struct {
	ChannelId    int    `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	Status       int    `json:"status"`
	ProvenTokens int    `json:"proven_tokens"`
	RejectsFrom  int    `json:"rejects_from"`
}

// GetLanePromptCapacity answers "which lanes serve long context for this model":
// every channel listing the model with the largest prompt it completed and the
// size it is passed over from. Largest proven first.
func GetLanePromptCapacity(c fuego.ContextWithParams[dto.GetLanePromptCapacityParams]) (*dto.Response[[]LanePromptCapacityRow], error) {
	p, _ := dto.ParseParams[dto.GetLanePromptCapacityParams](c)
	if p.ModelName == "" {
		return dto.Fail[[]LanePromptCapacityRow]("model_name is required")
	}
	var channelIds []int
	if err := model.DB.Model(&model.Ability{}).Where("model = ?", p.ModelName).Distinct().Pluck("channel_id", &channelIds).Error; err != nil {
		return dto.Fail[[]LanePromptCapacityRow](err.Error())
	}
	rows := make([]LanePromptCapacityRow, 0, len(channelIds))
	for _, id := range channelIds {
		channel, err := model.CacheGetChannel(id)
		if err != nil {
			continue
		}
		proven, rejectsFrom := service.LanePromptCapacity(id)
		rows = append(rows, LanePromptCapacityRow{ChannelId: id, ChannelName: channel.Name, Status: channel.Status, ProvenTokens: proven, RejectsFrom: rejectsFrom})
	}
	slices.SortFunc(rows, func(a, b LanePromptCapacityRow) int { return b.ProvenTokens - a.ProvenTokens })
	return dto.Ok(rows)
}
