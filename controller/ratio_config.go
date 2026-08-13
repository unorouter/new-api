package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
)

func GetRatioConfig(c fuego.ContextNoBody) (*dto.Response[ratio_setting.ExposedRatioData], error) {
	if !ratio_setting.IsExposeRatioEnabled() {
		return dto.Fail[ratio_setting.ExposedRatioData]("Ratio configuration API is not enabled")
	}

	return dto.Ok(ratio_setting.GetExposedData())
}
