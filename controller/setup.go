package controller

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/go-fuego/fuego"
)

func GetSetup(c fuego.ContextNoBody) (*dto.Response[dto.SetupData], error) {
	setup := dto.SetupData{
		Status: constant.Setup,
	}
	if constant.Setup {
		return dto.Ok(setup)
	}
	setup.RootInit = model.RootUserExists()
	if common.UsingMySQL {
		setup.DatabaseType = "mysql"
	}
	if common.UsingPostgreSQL {
		setup.DatabaseType = "postgres"
	}
	if common.UsingSQLite {
		setup.DatabaseType = "sqlite"
	}
	return dto.Ok(setup)
}

func PostSetup(c fuego.ContextWithBody[dto.SetupRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	if constant.Setup {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.already_done"))
	}

	rootExists := model.RootUserExists()

	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if !rootExists {
		if len(req.Username) > 12 {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.username_max_len"))
		}
		if req.Password != req.ConfirmPassword {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.password_mismatch"))
		}
		if len(req.Password) < 8 {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.password_min_len"))
		}

		hashedPassword, err := common.Password2Hash(req.Password)
		if err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "common.system_error"))
		}
		rootUser := model.User{
			Username:    req.Username,
			Password:    hashedPassword,
			Role:        common.RoleRootUser,
			Status:      common.UserStatusEnabled,
			DisplayName: "Root User",
			AccessToken: nil,
			Quota:       100000000,
		}
		err = model.DB.Create(&rootUser).Error
		if err != nil {
			return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.create_admin_failed"))
		}
	}

	operation_setting.SelfUseModeEnabled = req.SelfUseModeEnabled
	operation_setting.DemoSiteEnabled = req.DemoSiteEnabled

	err = model.UpdateOption("SelfUseModeEnabled", boolToString(req.SelfUseModeEnabled))
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.self_use_failed"))
	}

	err = model.UpdateOption("DemoSiteEnabled", boolToString(req.DemoSiteEnabled))
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.demo_mode_failed"))
	}

	constant.Setup = true

	setup := model.Setup{
		Version:       common.Version,
		InitializedAt: time.Now().Unix(),
	}
	err = model.DB.Create(&setup).Error
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "setup.init_failed"))
	}

	return dto.Msg(common.TranslateMessage(ginCtx, "setup.init_success"))
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
