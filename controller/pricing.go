package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
)

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

	usableGroup = service.GetUserUsableGroups(group)
	for group := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}

	showOriginalPrice := operation_setting.ShowOriginalPriceEnabled

	return dto.PricingData{
		Success:           true,
		Data:              pricing,
		Vendors:           model.GetVendors(),
		GroupRatio:        groupRatio,
		UsableGroup:       usableGroup,
		SupportedEndpoint: model.GetSupportedEndpointMap(),
		AutoGroups:        service.GetUserAutoGroup(group),
		ShowOriginalPrice: showOriginalPrice,
	}, nil
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
	return dto.Msg("重置模型倍率成功")
}
