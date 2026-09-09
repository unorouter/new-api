package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
)

func GetRatioConfig(c fuego.ContextNoBody) (*dto.Response[map[string]any], error) {
	if !ratio_setting.IsExposeRatioEnabled() {
		return dto.Fail[map[string]any]("Ratio configuration API is not enabled")
	}

	return dto.Ok(billing_setting.GetPricingSyncData(ratio_setting.GetExposedData().ToMap()))
}
