package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func GetPricing(c fuego.ContextNoBody) (dto.PricingData, error) {
	pricing := model.GetPricing()
	userId, exists := dto.GinCtx(c).Get("id")
	usableGroup := map[string]string{}
	groupRatio := map[string]float64{}
	for s, f := range ratio_setting.GetGroupRatioCopy() {
		groupRatio[s] = f
	}
	var group string
	if exists {
		user, err := model.GetUserCache(userId.(int))
		if err == nil {
			group = user.Group
			for g := range groupRatio {
				ratio, ok := ratio_setting.GetGroupGroupRatio(group, g)
				if ok {
					groupRatio[g] = ratio
				}
			}
		}
	}

	// Per-user usable groups (incl. private routing groups granted by user id) so
	// private groups + the models they serve flow through the pricing payload; the
	// client matches group->models via each model's enable_groups.
	if exists {
		usableGroup = service.GetUserUsableGroups(group)
	} else {
		usableGroup = service.GetUserUsableGroups(group)
	}
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	for group := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}

	showOriginalPrice := operation_setting.ShowOriginalPriceEnabled

	return dto.PricingData{
		Success:           true,
		Data:              toPricingModels(pricing, groupRatio),
		Vendors:           toPricingVendors(model.GetVendors()),
		GroupRatio:        groupRatio,
		UsableGroup:       usableGroup,
		SupportedEndpoint: toEndpointInfoMap(model.GetSupportedEndpointMap()),
		AutoGroups:        service.GetUserAutoGroup(group),
		ShowOriginalPrice: showOriginalPrice,
	}, nil
}

// GetPricingModel returns ONE model's pricing by name, always - even when every
// channel is disabled or deleted. The model-detail page must render a known model
// regardless of routability, so this applies NO usable-group filter (unlike
// GetPricing). Group ratios are the full public set so the detail page can still
// show group pricing for a dark model. 404s only when the name is unknown to both
// the pricing cache and the models table.
func GetPricingModel(c fuego.ContextNoBody) (dto.PricingData, error) {
	modelName := dto.GinCtx(c).Query("model")
	pricing, ok := model.GetPricingByModelName(modelName)
	if !ok {
		return dto.PricingData{Success: false}, fuego.NotFoundError{Title: "model not found"}
	}

	// Only the groups THIS model is served by. The full copy is every group the
	// gateway knows (1600+ with per-channel routing groups), which dwarfs the
	// model itself and is useless to a single-model caller.
	all := ratio_setting.GetGroupRatioCopy()
	groupRatio := make(map[string]float64, len(pricing.EnableGroup))
	if common.StringsContains(pricing.EnableGroup, "all") {
		groupRatio = all
	} else {
		for _, g := range pricing.EnableGroup {
			if f, ok := all[g]; ok {
				groupRatio[g] = f
			}
		}
	}
	if userId, exists := dto.GinCtx(c).Get("id"); exists {
		if user, err := model.GetUserCache(userId.(int)); err == nil {
			for g := range groupRatio {
				if ratio, ok := ratio_setting.GetGroupGroupRatio(user.Group, g); ok {
					groupRatio[g] = ratio
				}
			}
		}
	}

	return dto.PricingData{
		Success:           true,
		Data:              toPricingModels([]model.Pricing{pricing}, groupRatio),
		Vendors:           toPricingVendors(model.GetVendors()),
		GroupRatio:        groupRatio,
		SupportedEndpoint: toEndpointInfoMap(model.GetSupportedEndpointMap()),
		ShowOriginalPrice: operation_setting.ShowOriginalPriceEnabled,
	}, nil
}

// A model is free when every price input is zero for some group it is served
// by. Mirrors the three billing shapes: fixed-price (sticker x cheapest group
// ratio), and ratio-priced (model_price <= 0 AND either the group ratio or the
// model ratio is 0).
func modelIsFree(m model.Pricing, groupRatio map[string]float64) bool {
	if m.QuotaType == 1 || m.QuotaType == 3 || m.QuotaType == 4 {
		minRatio := 1.0
		found := false
		for _, g := range m.EnableGroup {
			if r, ok := groupRatio[g]; ok && (!found || r < minRatio) {
				minRatio = r
				found = true
			}
		}
		return m.ModelPrice*minRatio == 0
	}
	for _, g := range m.EnableGroup {
		if m.ModelPrice <= 0 {
			r, ok := groupRatio[g]
			if (ok && r == 0) || m.ModelRatio == 0 {
				return true
			}
		}
	}
	return false
}

func toPricingModels(src []model.Pricing, groupRatio map[string]float64) []dto.PricingModel {
	out := make([]dto.PricingModel, len(src))
	for i, m := range src {
		out[i] = dto.PricingModel{
			ModelName:              m.ModelName,
			Description:            m.Description,
			Icon:                   m.Icon,
			Tags:                   m.Tags,
			Metadata:               m.Metadata,
			VendorID:               m.VendorID,
			QuotaType:              m.QuotaType,
			ModelRatio:             m.ModelRatio,
			ModelPrice:             m.ModelPrice,
			OwnerBy:                m.OwnerBy,
			CompletionRatio:        m.CompletionRatio,
			CacheRatio:             m.CacheRatio,
			CreateCacheRatio:       m.CreateCacheRatio,
			ImageRatio:             m.ImageRatio,
			AudioRatio:             m.AudioRatio,
			AudioCompletionRatio:   m.AudioCompletionRatio,
			EnableGroup:            m.EnableGroup,
			SupportedEndpointTypes: m.SupportedEndpointTypes,
			PricingVersion:         m.PricingVersion,
			GridPricing:            m.GridPricing,
			BillingMode:            m.BillingMode,
			BillingExpr:            m.BillingExpr,
			Online:                 m.Online,
			IsFree:                 modelIsFree(m, groupRatio),
		}
	}
	return out
}

func toPricingVendors(src []model.PricingVendor) []dto.PricingVendor {
	out := make([]dto.PricingVendor, len(src))
	for i, v := range src {
		out[i] = dto.PricingVendor{
			ID:          v.ID,
			Name:        v.Name,
			Description: v.Description,
			Icon:        v.Icon,
		}
	}
	return out
}

func toEndpointInfoMap(src map[string]common.EndpointInfo) map[string]dto.EndpointInfo {
	out := make(map[string]dto.EndpointInfo, len(src))
	for k, v := range src {
		out[k] = dto.EndpointInfo{Path: v.Path, Method: v.Method}
	}
	return out
}

func ResetModelRatio(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg(common.TranslateMessage(dto.GinCtx(c), "model.reset_success"))
}
