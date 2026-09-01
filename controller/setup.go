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
	setup.DatabaseType = string(common.MainDatabaseType())
	return dto.Ok(setup)
}

func PostSetup(c fuego.ContextWithBody[dto.SetupRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	if constant.Setup {
		return dto.FailMsg("System has already been initialized")
	}

	rootExists := model.RootUserExists()

	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(ginCtx, "common.invalid_params"))
	}

	if !rootExists {
		if len(req.Username) > 12 {
			return dto.FailMsg("Username cannot exceed 12 characters")
		}
		if req.Password != req.ConfirmPassword {
			return dto.FailMsg("Passwords do not match")
		}
		if len(req.Password) < 8 {
			return dto.FailMsg("Password must be at least 8 characters")
		}

		hashedPassword, err := common.Password2Hash(req.Password)
		if err != nil {
			return dto.FailMsg("System error: {{.Error}}")
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
			return dto.FailMsg("Failed to create admin account: {{.Error}}")
		}
	}

	operation_setting.SelfUseModeEnabled = req.SelfUseModeEnabled
	operation_setting.DemoSiteEnabled = req.DemoSiteEnabled

	err = model.UpdateOption("SelfUseModeEnabled", boolToString(req.SelfUseModeEnabled))
	if err != nil {
		return dto.FailMsg("Failed to save self-use mode settings: {{.Error}}")
	}

	err = model.UpdateOption("DemoSiteEnabled", boolToString(req.DemoSiteEnabled))
	if err != nil {
		return dto.FailMsg("Failed to save demo site mode settings: {{.Error}}")
	}

	constant.Setup = true

	setup := model.Setup{
		Version:       common.Version,
		InitializedAt: time.Now().Unix(),
	}
	err = model.DB.Create(&setup).Error
	if err != nil {
		return dto.FailMsg("System initialization failed: {{.Error}}")
	}

	return dto.Msg("System initialization successful")
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
