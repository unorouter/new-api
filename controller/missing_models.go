package controller

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-fuego/fuego"
)

func GetMissingModels(c fuego.ContextNoBody) (*dto.Response[[]string], error) {
	missing, err := model.GetMissingModels()
	if err != nil {
		return dto.Fail[[]string](err.Error())
	}

	return dto.Ok(missing)
}
