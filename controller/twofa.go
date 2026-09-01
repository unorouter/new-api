package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

type twoFALoginFlowPayload struct {
	AuthVersion int64 `json:"auth_version"`
}

func Setup2FA(c fuego.ContextNoBody) (*dto.Response[dto.Setup2FAResponse], error) {
	userId := dto.UserID(c)

	// 检查用户是否已经启用2FA
	existing, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.Fail[dto.Setup2FAResponse](err.Error())
	}
	if existing != nil && existing.IsEnabled {
		return dto.Fail[dto.Setup2FAResponse]("User has enabled 2FA, please disable it first before reconfiguring")
	}

	// 如果存在已禁用的2FA记录，先删除它
	if existing != nil && !existing.IsEnabled {
		if err := existing.DeletePendingTwoFASetup(); err != nil {
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
		common.SysLog("failed to generate TOTP secret: " + err.Error())
		return dto.Fail[dto.Setup2FAResponse]("Failed to generate 2FA key")
	}

	// 生成备用码
	backupCodes, err := common.GenerateBackupCodes()
	if err != nil {
		common.SysLog("failed to generate backup codes: " + err.Error())
		return dto.Fail[dto.Setup2FAResponse]("Failed to generate backup codes")
	}

	// 生成二维码数据
	qrCodeData := common.GenerateQRCodeData(key.Secret(), user.Username)

	// 创建或更新2FA记录（暂未启用）
	twoFA := &model.TwoFA{
		UserId:    userId,
		Secret:    key.Secret(),
		IsEnabled: false,
	}

	if err := twoFA.CreatePendingTwoFASetup(); err != nil {
		return dto.Fail[dto.Setup2FAResponse](err.Error())
	}

	// 创建备用码记录
	if err := model.CreatePendingTwoFASetupBackupCodes(userId, backupCodes); err != nil {
		common.SysLog("failed to save backup codes: " + err.Error())
		return dto.Fail[dto.Setup2FAResponse]("Failed to save backup codes")
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, "started 2FA setup")

	return dto.OkMsg("2FA setup initialized, please scan the QR code with your authenticator app and enter the verification code to complete setup", dto.Setup2FAResponse{
		Secret:      key.Secret(),
		QRCodeData:  qrCodeData,
		BackupCodes: backupCodes,
	})
}

func Enable2FA(c fuego.ContextWithBody[dto.Setup2FARequest]) (dto.ApiResponse, error) {
	req, err := c.Body()
	if err != nil {
		return dto.FailAny("Invalid parameters")
	}

	userId := dto.UserID(c)

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.FailAny(err.Error())
	}
	if twoFA == nil {
		return dto.FailAny("Please complete 2FA initialization setup first")
	}
	if twoFA.IsEnabled {
		return dto.FailAny("2FA is already enabled")
	}

	// 验证TOTP验证码
	cleanCode, err := common.ValidateNumericCode(req.Code)
	if err != nil {
		return dto.FailAny(err.Error())
	}

	if !common.ValidateTOTPCode(twoFA.Secret, cleanCode) {
		return dto.FailAny("Verification code or backup code is incorrect")
	}

	identity, ok := middleware.GetSessionAuthIdentity(dto.GinCtx(c))
	if !ok {
		return dto.FailAny("The current authentication method does not support security verification")
	}
	// 启用2FA并原子推进用户鉴权版本
	if err := twoFA.EnableWithAuthVersion(); err != nil {
		return dto.FailAny(err.Error())
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "twofa_enabled")
	if err != nil {
		return dto.FailAny(err.Error())
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, "2FA enabled successfully")

	return dto.OkMsgAny("Two-factor authentication enabled successfully", authRotationData(bundle))
}

func Disable2FA(c fuego.ContextWithBody[dto.Verify2FARequest]) (dto.ApiResponse, error) {
	req, err := c.Body()
	if err != nil {
		return dto.FailAny("Invalid parameters")
	}

	userId := dto.UserID(c)

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.FailAny(err.Error())
	}
	if twoFA == nil || !twoFA.IsEnabled {
		return dto.FailAny("User has not enabled 2FA")
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
			return dto.FailAny(err.Error())
		}
	}

	if !isValidTOTP && !isValidBackup {
		return dto.FailAny("Verification code or backup code is incorrect")
	}

	identity, ok := middleware.GetSessionAuthIdentity(dto.GinCtx(c))
	if !ok {
		return dto.FailAny("The current authentication method does not support security verification")
	}
	// 禁用2FA并原子推进用户鉴权版本
	if err := model.DisableTwoFAWithAuthVersion(userId); err != nil {
		return dto.FailAny(err.Error())
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "twofa_disabled")
	if err != nil {
		return dto.FailAny(err.Error())
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, "2FA disabled")

	return dto.OkMsgAny("Two-factor authentication has been disabled", authRotationData(bundle))
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
				common.SysLog("failed to get backup code count: " + err.Error())
			} else {
				status.BackupCodesRemaining = backupCount
			}
		}
	}

	return dto.Ok(status)
}

func RegenerateBackupCodes(c fuego.ContextWithBody[dto.Verify2FARequest]) (dto.ApiResponse, error) {
	req, err := c.Body()
	if err != nil {
		return dto.FailAny("Invalid parameters")
	}

	userId := dto.UserID(c)

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(userId)
	if err != nil {
		return dto.FailAny(err.Error())
	}
	if twoFA == nil || !twoFA.IsEnabled {
		return dto.FailAny("User has not enabled 2FA")
	}

	// 验证TOTP验证码
	cleanCode, err := common.ValidateNumericCode(req.Code)
	if err != nil {
		return dto.FailAny(err.Error())
	}

	valid, err := twoFA.ValidateTOTPAndUpdateUsage(cleanCode)
	if err != nil {
		return dto.FailAny(err.Error())
	}
	if !valid {
		return dto.FailAny("Verification code or backup code is incorrect")
	}

	// 生成新的备用码
	backupCodes, err := common.GenerateBackupCodes()
	if err != nil {
		common.SysLog("failed to generate backup codes: " + err.Error())
		return dto.FailAny("Failed to generate backup codes")
	}

	identity, ok := middleware.GetSessionAuthIdentity(dto.GinCtx(c))
	if !ok {
		return dto.FailAny("The current authentication method does not support security verification")
	}
	// 保存新的备用码并原子推进用户鉴权版本
	if err := model.ReplaceBackupCodesWithAuthVersion(userId, backupCodes); err != nil {
		common.SysLog("failed to save backup codes: " + err.Error())
		return dto.FailAny("Failed to save backup codes")
	}
	bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "twofa_backup_codes_regenerated")
	if err != nil {
		return dto.FailAny(err.Error())
	}

	// 记录操作日志
	model.RecordLog(userId, model.LogTypeSystem, "regenerated 2FA backup codes")

	data := authRotationData(bundle)
	data["backup_codes"] = backupCodes
	return dto.OkMsgAny("Backup codes regenerated successfully", data)
}

func Verify2FALogin(c *gin.Context) {
	var req dto.Verify2FARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, dto.ApiResponse{Message: "Invalid parameters"})
		return
	}

	flow, err := model.GetAuthFlow(req.FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeTwoFALogin})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "Session has expired, please log in again",
		})
		return
	}
	// 获取用户信息
	user, err := model.GetUserById(flow.UserId, false)
	if err != nil {
		c.JSON(200, dto.ApiResponse{Message: "User does not exist"})
		return
	}
	if user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "User has been disabled",
		})
		return
	}
	var flowPayload twoFALoginFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &flowPayload); err != nil || flowPayload.AuthVersion <= 0 || flowPayload.AuthVersion != user.AuthVersion {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "Session has expired, please log in again",
		})
		return
	}

	// 获取2FA记录
	twoFA, err := model.GetTwoFAByUserId(user.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if twoFA == nil || !twoFA.IsEnabled {
		c.JSON(200, dto.ApiResponse{Message: "User has not enabled 2FA"})
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
		c.JSON(200, dto.ApiResponse{Message: "Verification code or backup code is incorrect"})
		return
	}

	if _, err := model.ConsumeAuthFlow(req.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeTwoFALogin,
		UserId:  user.Id,
	}); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "Session has expired, please log in again",
		})
		return
	}

	setupLoginAtAuthVersion(user, flowPayload.AuthVersion, c)
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
		return dto.FailMsg("User ID format error")
	}

	// 检查目标用户权限
	targetUser, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, targetUser.Role) {
		return dto.FailMsg("No permission to manage 2FA settings of users at the same or higher level")
	}

	// 禁用2FA
	if err := model.DisableTwoFAWithAuthVersion(userId); err != nil {
		if errors.Is(err, model.ErrTwoFANotEnabled) {
			return dto.FailMsg("User has not enabled 2FA")
		}
		return dto.FailMsg(err.Error())
	}
	if _, err := model.RevokeAllUserSessions(userId, "admin_twofa_disabled"); err != nil {
		return dto.FailMsg(err.Error())
	}

	recordManageAuditFor(dto.GinCtx(c), userId, "user.2fa_disable", nil)

	return dto.Msg("User 2FA has been forcefully disabled")
}
