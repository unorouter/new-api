package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/service"
	"github.com/go-fuego/fuego"
)

func GetChannelAffinityCacheStats(c fuego.ContextNoBody) (*dto.Response[service.ChannelAffinityCacheStats], error) {
	stats := service.GetChannelAffinityCacheStats()
	return dto.Ok(stats)
}

func ClearChannelAffinityCache(c fuego.ContextWithParams[dto.ClearChannelAffinityCacheParams]) (*dto.Response[dto.AffinityCacheClearData], error) {
	p, _ := dto.ParseParams[dto.ClearChannelAffinityCacheParams](c)
	all := strings.TrimSpace(p.All)
	ruleName := strings.TrimSpace(p.RuleName)

	if all == "true" {
		deleted := service.ClearChannelAffinityCacheAll()
		return dto.Ok(dto.AffinityCacheClearData{Deleted: deleted})
	}

	if ruleName == "" {
		return dto.Fail[dto.AffinityCacheClearData](common.TranslateMessage(dto.GinCtx(c), "affinity_cache.missing_param"))
	}

	deleted, err := service.ClearChannelAffinityCacheByRuleName(ruleName)
	if err != nil {
		return dto.Fail[dto.AffinityCacheClearData](err.Error())
	}

	return dto.Ok(dto.AffinityCacheClearData{Deleted: deleted})
}

func GetChannelAffinityUsageCacheStats(c fuego.ContextWithParams[dto.GetChannelAffinityUsageCacheStatsParams]) (*dto.Response[service.ChannelAffinityUsageCacheStats], error) {
	p, _ := dto.ParseParams[dto.GetChannelAffinityUsageCacheStatsParams](c)
	ruleName := strings.TrimSpace(p.RuleName)
	usingGroup := strings.TrimSpace(p.UsingGroup)
	keyFp := strings.TrimSpace(p.KeyFp)

	if ruleName == "" {
		return dto.Fail[service.ChannelAffinityUsageCacheStats](i18n.Translate("ctrl.missing_param_rule_name"))
	}
	if keyFp == "" {
		return dto.Fail[service.ChannelAffinityUsageCacheStats](i18n.Translate("ctrl.missing_param_key_fp"))
	}

	stats := service.GetChannelAffinityUsageCacheStats(ruleName, usingGroup, keyFp)
	return dto.Ok(stats)
}
