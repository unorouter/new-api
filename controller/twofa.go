package controller

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

func Setup2FA(c fuego.ContextNoBody) (*dto.Response[dto.Setup2FAResponse], error) {
	userId := dto.UserID(c)

	// 检查用户是否已经启用2FA
	existing, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.Fail[dto.Setup2FAResponse](err.Error())
	}
	if existing != nil && existing.IsEnabled {
		return dto.Fail[dto.Setup2FAResponse](common.TranslateMessage(dto.GinCtx(c), "twofa.disable_first"))
	}

	// 如果存在已禁用的2FA记录，先删除它
	if existing != nil && !existing.IsEnabled {
		if err := existing.Delete(); err != nil {
			return dto.Fail[dto.Setup2FAResponse](err.Error())
		}
		existing = nil // 重置为nil，后续将创建新记录
	}

	// 获取用户信息
	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.Fail[dto.Setup2FAResponse](err.Error())
	}

	// 生成TOTP密钥
	key, err := common.GenerateTOTPSecret(user.Username)
	if err != nil {
		common.SysLog(i18n.Translate("log.twofa_gen_secret_failed", map[string]any{"Error": err.Error()}))
		return dto.Fail[dto.Setup2FAResponse](common.TranslateMessage(dto.GinCtx(c), "twofa.gen_key_failed"))
	}

	// 生成备用码
	backupCodes, err := common.GenerateBackupCodes()
	if err != nil {
		common.SysLog(i18n.Translate("log.twofa_gen_backup_failed", map[string]any{"Error": err.Error()}))
		return dto.Fail[dto.Setup2FAResponse](common.TranslateMessage(dto.GinCtx(c), "twofa.gen_backup_failed"))
	}

	// 生成二维码数据
	qrCodeData := common.GenerateQRCodeData(key.Secret(), user.Username)

	// 创建或更新2FA记录（暂未启用）
	twoFA := &model.TwoFA{
		UserId:    userId,
		Secret:    key.Secret(),
		IsEnabled: false,
	}

	if existing != nil {
		// 更新现有记录
		twoFA.Id = existing.Id
		err = twoFA.Update()
	} else {
		// 创建新记录
		err = twoFA.Create()
	}

	if err != nil {
		return dto.Fail[dto.Setup2FAResponse](err.Error())
	}

	// 创建备用码记录
	if err := model.CreateBackupCodes(userId, backupCodes); err != nil {
		common.SysLog(i18n.Translate("log.twofa_save_backup_failed", map[string]any{"Error": err.Error()}))
		return dto.Fail[dto.Setup2FAResponse](common.TranslateMessage(dto.GinCtx(c), "twofa.save_backup_failed"))
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, i18n.Translate("log.twofa_started_setup"))

	return dto.OkMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.setup_init"), dto.Setup2FAResponse{
		Secret:      key.Secret(),
		QRCodeData:  qrCodeData,
		BackupCodes: backupCodes,
	})
}

func Enable2FA(c fuego.ContextWithBody[dto.Setup2FARequest]) (dto.MessageResponse, error) {
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}

	userId := dto.UserID(c)

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	if twoFA == nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.setup_required"))
	}
	if twoFA.IsEnabled {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.already_enabled"))
	}

	// 验证TOTP验证码
	cleanCode, err := common.ValidateNumericCode(req.Code)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	if !common.ValidateTOTPCode(twoFA.Secret, cleanCode) {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.code_invalid"))
	}

	// 启用2FA
	if err := twoFA.Enable(); err != nil {
		return dto.FailMsg(err.Error())
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, i18n.Translate("log.twofa_enabled"))

	return dto.Msg(common.TranslateMessage(dto.GinCtx(c), "twofa.enable_success"))
}

func Disable2FA(c fuego.ContextWithBody[dto.Verify2FARequest]) (dto.MessageResponse, error) {
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}

	userId := dto.UserID(c)

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	if twoFA == nil || !twoFA.IsEnabled {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.not_enabled"))
	}

	// 验证TOTP验证码或备用码
	cleanCode, err := common.ValidateNumericCode(req.Code)
	isValidTOTP := false
	isValidBackup := false

	if err == nil {
		// 尝试验证TOTP
		isValidTOTP, _ = twoFA.ValidateTOTPAndUpdateUsage(cleanCode)
	}

	if !isValidTOTP {
		// 尝试验证备用码
		isValidBackup, err = twoFA.ValidateBackupCodeAndUpdateUsage(req.Code)
		if err != nil {
			return dto.FailMsg(err.Error())
		}
	}

	if !isValidTOTP && !isValidBackup {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.code_invalid"))
	}

	// 禁用2FA
	if err := model.DisableTwoFA(userId); err != nil {
		return dto.FailMsg(err.Error())
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, i18n.Translate("log.twofa_disabled"))

	return dto.Msg(common.TranslateMessage(dto.GinCtx(c), "twofa.disable_success"))
}

func Get2FAStatus(c fuego.ContextNoBody) (*dto.Response[dto.TwoFAStatusData], error) {
	userId := dto.UserID(c)

	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.Fail[dto.TwoFAStatusData](err.Error())
	}

	status := dto.TwoFAStatusData{
		Enabled: false,
		Locked:  false,
	}

	if twoFA != nil {
		status.Enabled = twoFA.IsEnabled
		status.Locked = twoFA.IsLocked()
		if twoFA.IsEnabled {
			// 获取剩余备用码数量
			backupCount, err := model.GetUnusedBackupCodeCount(userId)
			if err != nil {
				common.SysLog(i18n.Translate("log.twofa_get_backup_count_failed", map[string]any{"Error": err.Error()}))
			} else {
				status.BackupCodesRemaining = backupCount
			}
		}
	}

	return dto.Ok(status)
}

func RegenerateBackupCodes(c fuego.ContextWithBody[dto.Verify2FARequest]) (*dto.Response[dto.BackupCodesData], error) {
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.BackupCodesData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}

	userId := dto.UserID(c)

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.Fail[dto.BackupCodesData](err.Error())
	}
	if twoFA == nil || !twoFA.IsEnabled {
		return dto.Fail[dto.BackupCodesData](common.TranslateMessage(dto.GinCtx(c), "twofa.not_enabled"))
	}

	// 验证TOTP验证码
	cleanCode, err := common.ValidateNumericCode(req.Code)
	if err != nil {
		return dto.Fail[dto.BackupCodesData](err.Error())
	}

	valid, err := twoFA.ValidateTOTPAndUpdateUsage(cleanCode)
	if err != nil {
		return dto.Fail[dto.BackupCodesData](err.Error())
	}
	if !valid {
		return dto.Fail[dto.BackupCodesData](common.TranslateMessage(dto.GinCtx(c), "twofa.code_invalid"))
	}

	// 生成新的备用码
	backupCodes, err := common.GenerateBackupCodes()
	if err != nil {
		common.SysLog(i18n.Translate("log.twofa_gen_backup_failed", map[string]any{"Error": err.Error()}))
		return dto.Fail[dto.BackupCodesData](common.TranslateMessage(dto.GinCtx(c), "twofa.gen_backup_failed"))
	}

	// 保存新的备用码
	if err := model.CreateBackupCodes(userId, backupCodes); err != nil {
		common.SysLog(i18n.Translate("log.twofa_save_backup_failed", map[string]any{"Error": err.Error()}))
		return dto.Fail[dto.BackupCodesData](common.TranslateMessage(dto.GinCtx(c), "twofa.save_backup_failed"))
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, i18n.Translate("log.twofa_regen_backup"))

	return dto.OkMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.backup_regen_success"), dto.BackupCodesData{
		BackupCodes: backupCodes,
	})
}

func Verify2FALogin(c *gin.Context) {
	var req dto.Verify2FARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, dto.ApiResponse{Message: common.TranslateMessage(c, "common.invalid_params")})
		return
	}

	// 从会话中获取pending用户信息
	session := sessions.Default(c)
	pendingUserId := session.Get("pending_user_id")
	if pendingUserId == nil {
		c.JSON(200, dto.ApiResponse{Message: common.TranslateMessage(c, "twofa.session_expired")})
		return
	}
	userId, ok := pendingUserId.(int)
	if !ok {
		c.JSON(200, dto.ApiResponse{Message: common.TranslateMessage(c, "twofa.session_invalid")})
		return
	}
	// 获取用户信息
	user, err := model.GetUserById(userId, false)
	if err != nil {
		c.JSON(200, dto.ApiResponse{Message: common.TranslateMessage(c, "user.not_exists")})
		return
	}

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(user.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if twoFA == nil || !twoFA.IsEnabled {
		c.JSON(200, dto.ApiResponse{Message: common.TranslateMessage(c, "twofa.not_enabled")})
		return
	}

	// 验证TOTP验证码或备用码
	cleanCode, err := common.ValidateNumericCode(req.Code)
	isValidTOTP := false
	isValidBackup := false

	if err == nil {
		// 尝试验证TOTP
		isValidTOTP, _ = twoFA.ValidateTOTPAndUpdateUsage(cleanCode)
	}

	if !isValidTOTP {
		// 尝试验证备用码
		isValidBackup, err = twoFA.ValidateBackupCodeAndUpdateUsage(req.Code)
		if err != nil {
			c.JSON(200, dto.ApiResponse{Message: err.Error()})
			return
		}
	}

	if !isValidTOTP && !isValidBackup {
		c.JSON(200, dto.ApiResponse{Message: common.TranslateMessage(c, "twofa.code_invalid")})
		return
	}

	// 2FA验证成功，清理pending会话信息并完成登录
	session.Delete("pending_username")
	session.Delete("pending_user_id")
	session.Save()

	setupLogin(user, c)
}

func Admin2FAStats(c fuego.ContextNoBody) (*dto.Response[*model.TwoFAStats], error) {
	stats, err := model.GetTwoFAStats()
	if err != nil {
		return dto.Fail[*model.TwoFAStats](err.Error())
	}

	return dto.Ok(stats)
}

func AdminDisable2FA(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	userId, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.user_id_format_error"))
	}

	// 检查目标用户权限
	targetUser, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	myRole := dto.UserRole(c)
	if myRole <= targetUser.Role && myRole != common.RoleRootUser {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.no_permission"))
	}

	// 禁用2FA
	if err := model.DisableTwoFA(userId); err != nil {
		if errors.Is(err, model.ErrTwoFANotEnabled) {
			return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "twofa.not_enabled"))
		}
		return dto.FailMsg(err.Error())
	}

	// 记录操作日志
	adminId := dto.UserID(c)
	model.RecordLog(userId, model.LogTypeManage,
		i18n.Translate("log.twofa_admin_force_disable", map[string]any{"AdminId": adminId}))

	return dto.Msg(common.TranslateMessage(dto.GinCtx(c), "twofa.admin_force_disable"))
}
