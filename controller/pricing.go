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

// applyUserGroupRatio overlays the caller's per-group ratio overrides onto a
// group-ratio map and returns the group they belong to. Anonymous callers get the
// public ratios and an empty group, which is what every pricing surface then
// filters by.
func applyUserGroupRatio(c fuego.ContextNoBody, groupRatio map[string]float64) string {
	userId, exists := dto.GinCtx(c).Get("id")
	if !exists {
		return ""
	}
	user, err := model.GetUserCache(userId.(int))
	if err != nil {
		return ""
	}
	for g := range groupRatio {
		if ratio, ok := ratio_setting.GetGroupGroupRatio(user.Group, g); ok {
			groupRatio[g] = ratio
		}
	}
	return user.Group
}

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
	groupRatio := ratio_setting.GetGroupRatioCopy()
	group := applyUserGroupRatio(c, groupRatio)
	usableGroup := service.GetUserUsableGroups(group)
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

// A model is free when every price input is zero. Mirrors the two billing
// shapes: fixed-price (sticker x cheapest group ratio) and ratio-priced
// (model_price <= 0 AND either the model ratio or some group's ratio is 0).
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
	if m.ModelPrice > 0 {
		return false
	}
	// Group-independent: a zero model ratio prices every group at zero, so the
	// answer cannot depend on which groups are currently live. EnableGroup is
	// built from ENABLED abilities only, so a model whose channels are all
	// rate-limit-disabled arrives here with no groups at all, and iterating it
	// reported a genuinely free model as paid (which then read as "not free" to
	// every caller gating guest access on this flag).
	if m.ModelRatio == 0 {
		return true
	}
	for _, g := range m.EnableGroup {
		if r, ok := groupRatio[g]; ok && r == 0 {
			return true
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
