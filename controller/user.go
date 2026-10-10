package controller

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"gorm.io/gorm"
)

type LoginRequest struct {
	Username          string `json:"username"`
	Password          string `json:"password"`
	PasswordEncrypted string `json:"password_encrypted"`
	EncryptionKeyID   string `json:"encryption_key_id"`
}

func GetPasswordEncryptionKey(c *gin.Context) {
	if !common.PasswordLoginEncryptionEnabled {
		common.ApiSuccess(c, gin.H{"enabled": false})
		return
	}
	keyID, publicKey := common.PasswordEncryptionPublicKey()
	if keyID == "" || publicKey == "" {
		common.ApiErrorT(c, "Database error, please contact the administrator")
		return
	}
	common.ApiSuccess(c, gin.H{
		"enabled":    true,
		"kid":        keyID,
		"public_key": publicKey,
	})
}

// Login uses *gin.Context because setupLogin writes session + JSON directly
func Login(c *gin.Context) {
	if !common.PasswordLoginEnabled {
		common.ApiError(c, common.NewMessage("Password login has been disabled by administrator"))
		return
	}
	var loginRequest dto.LoginRequest
	err := common.DecodeJson(c.Request.Body, &loginRequest)
	if err != nil {
		common.ApiError(c, common.NewMessage("Invalid parameters"))
		return
	}
	username := loginRequest.Username
	password := loginRequest.Password
	if common.PasswordLoginEncryptionEnabled {
		if loginRequest.PasswordEncrypted == "" || loginRequest.EncryptionKeyID == "" {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		password, err = common.DecryptPassword(loginRequest.PasswordEncrypted, loginRequest.EncryptionKeyID)
		if err != nil {
			common.ApiErrorT(c, "Username or password is incorrect, or user has been banned")
			return
		}
	}
	if username == "" || password == "" {
		common.ApiError(c, common.NewMessage("Invalid parameters"))
		return
	}
	user := model.User{
		Username: username,
		Password: password,
	}
	err = user.ValidateAndFill()
	if err != nil {
		switch {
		case errors.Is(err, model.ErrDatabase):
			common.SysLog(common.LogText("Login database error for user %s: %v", username, err))
			common.ApiErrorT(c, "Database error, please contact the administrator")
		case errors.Is(err, model.ErrUserEmptyCredentials):
			common.ApiError(c, common.NewMessage("Invalid parameters"))
		case errors.Is(err, model.ErrUserDisabled):
			common.ApiError(c, common.NewMessage("This account is suspended. Contact support@unorouter.com if you think this is a mistake."))
		default:
			common.ApiErrorT(c, "Username or password is incorrect, or user has been banned")
		}
		return
	}

	setupLogin(&user, nil, c)
}

// loginMethodFromContext 根据请求路径推导登录方式，用于登录审计日志。
func loginMethodFromContext(c *gin.Context) string {
	if method := c.GetString("login_method"); method != "" {
		return method
	}
	switch c.FullPath() {
	case "/api/user/login":
		return "password"
	case "/api/user/login/2fa":
		return "2fa"
	case "/api/user/passkey/login/finish":
		return "passkey"
	case "/api/oauth/wechat":
		return "wechat"
	case "/api/oauth/telegram/login":
		return "telegram"
	case "/api/oauth/:provider":
		if provider := c.Param("provider"); provider != "" {
			return "oauth:" + provider
		}
		return "oauth"
	default:
		return "unknown"
	}
}

// recordLoginAudit 记录登录成功审计日志（对所有用户启用，仅记录成功，不记录失败）。
func recordLoginAudit(user *model.User, c *gin.Context) {
	method := loginMethodFromContext(c)
	ip := c.ClientIP()
	extra := model.AuditOther{
		LoginMethod: method,
		UserAgent:   c.Request.UserAgent(),
	}
	content := fmt.Sprintf("Logged in successfully via %s", method)
	params := map[string]any{
		"method": method,
	}
	if verifiedMethod := c.GetString("login_verification_method"); verifiedMethod != "" {
		params["verification_method"] = verifiedMethod
	}
	model.RecordLoginLog(user.Id, user.Role, user.Username, content, ip, "login", params, extra, c)
}

// publicClientIp returns the client address ONLY when a trusted proxy actually
// forwarded one, and it is a routable public address.
//
// The socket peer is never a usable identity: every request arrives from the
// tunnel or an internal caller, so falling back to it stamps our own egress
// address on unrelated accounts and collapses them into one register-IP
// identity. An absent header must read as "unknown", not as a wrong address that
// downstream uniqueness checks would treat as authoritative.
func publicClientIp(c *gin.Context) string {
	forwarded, ok := forwardedClientIp(c)
	if !ok {
		return ""
	}
	if forwarded.IsLoopback() || forwarded.IsPrivate() || forwarded.IsUnspecified() ||
		forwarded.IsLinkLocalUnicast() || forwarded.IsLinkLocalMulticast() {
		return ""
	}
	return forwarded.String()
}

// forwardedClientIp reads the client address from the forwarding headers, but
// only when gin resolved the request through a TRUSTED proxy. gin's ClientIP()
// validates the proxy chain against SetTrustedProxies and silently degrades to
// the socket peer when the chain is untrusted or no header was sent, returning no
// indication of which happened. Requiring a header to be present, and its result
// to differ from the peer, distinguishes a real forwarded address from that
// fallback.
func forwardedClientIp(c *gin.Context) (net.IP, bool) {
	hasHeader := false
	for _, name := range middleware.ClientIPHeaders() {
		if strings.TrimSpace(c.GetHeader(name)) != "" {
			hasHeader = true
			break
		}
	}
	if !hasHeader {
		return nil, false
	}
	parsed := net.ParseIP(c.ClientIP())
	if parsed == nil {
		return nil, false
	}
	// Equal to the socket peer means the header was rejected as untrusted and gin
	// fell back. Compare via RemoteIP() so both sides are normalized the same way.
	if peer := net.ParseIP(c.RemoteIP()); peer != nil && parsed.Equal(peer) {
		return nil, false
	}
	return parsed, true
}

// backfillRegisterIp stores the login IP for accounts created before register IPs were recorded.
func backfillRegisterIp(user *model.User, c *gin.Context) {
	if user.RegisterIp != "" {
		return
	}
	ip := publicClientIp(c)
	if ip == "" {
		return
	}
	err := model.DB.Model(&model.User{}).
		Where("id = ? AND (register_ip = '' OR register_ip IS NULL)", user.Id).
		Updates(map[string]any{
			"register_ip":      ip,
			"register_ip_hash": common.RegisterIpHash(ip),
		}).Error
	if err != nil {
		common.SysError("failed to backfill register ip: " + err.Error())
	}
}

// setupLogin evaluates the shared login policy after primary authentication.
// Only a completed Passkey ceremony may go directly to session issuance. A
// pending legacy GitHub binding rewrite travels inside the challenge and is
// written only when the verification completes.
func setupLogin(user *model.User, migration *service.LegacyGitHubMigration, c *gin.Context) {
	challenge, err := service.StartLoginVerification(user, loginMethodFromContext(c), migration)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if challenge != nil {
		setAuthNoStore(c)
		common.ApiSuccess(c, challenge)
		return
	}
	setupLoginAtAuthVersion(user, user.AuthVersion, c)
}

func setupLoginAtAuthVersion(user *model.User, expectedAuthVersion int64, c *gin.Context) {
	if user == nil || user.Id <= 0 || user.Status != common.UserStatusEnabled {
		common.ApiErrorT(c, "User has been banned")
		return
	}
	backfillRegisterIp(user, c)
	currentUser, err := model.GetUserById(user.Id, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var bundle *service.AuthBundle
	if expectedAuthVersion > 0 {
		bundle, err = service.CreateLoginSessionAtAuthVersion(
			user.Id,
			expectedAuthVersion,
			loginMethodFromContext(c),
			c.ClientIP(),
			c.Request.UserAgent(),
		)
	} else {
		bundle, err = service.CreateLoginSession(
			user.Id,
			loginMethodFromContext(c),
			c.ClientIP(),
			c.Request.UserAgent(),
		)
	}
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	writeLoginResponse(c, currentUser, bundle)
}

func writeLoginResponse(c *gin.Context, user *model.User, bundle *service.AuthBundle) {
	c.Set("login_method", bundle.Session.LoginMethod)
	model.UpdateUserLastLoginAt(user.Id)
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	setAuthNoStore(c)
	recordLoginAudit(user, c)
	c.JSON(http.StatusOK, gin.H{
		"message": "",
		"success": true,
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"session":           bundle.Session,
			"user":              buildSelfUserData(user),
		},
	})
}

// setupLoginAndRedirect generates a one-time exchange code and returns a redirect URL.
// The external frontend exchanges the code for user data via POST /api/oauth/exchange.
func setupLoginAndRedirect(user *model.User, c *gin.Context, redirectURI string) {
	backfillRegisterIp(user, c)

	// OAuth login issues the same 30-day access token the password path issues and
	// reports its expiry, so the external frontend can size its cookie to the
	// token's real lifetime. Handing out user.GetAccessToken() (the API key) left
	// the BFF with no expiry to read, so it fell back to a 15-minute cookie and
	// logged OAuth users out every few minutes.
	bundle, err := service.CreateLoginSession(
		user.Id,
		loginMethodFromContext(c),
		c.ClientIP(),
		c.Request.UserAgent(),
	)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	model.UpdateUserLastLoginAt(user.Id)
	setAuthNoStore(c)
	recordLoginAudit(user, c)

	code, err := common.StoreOAuthExchangeCode(&common.OAuthExchangeData{
		AccessToken:     bundle.AccessToken,
		AccessExpiresAt: bundle.AccessExpiresAt,
		UserID:          user.Id,
		Username:        user.Username,
		DisplayName:     user.DisplayName,
		Role:            user.Role,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	parsed, _ := url.Parse(redirectURI)
	q := parsed.Query()
	q.Set("code", code)
	parsed.RawQuery = q.Encode()

	c.JSON(http.StatusOK, dto.ApiResponse{
		Success: true,
		Message: "redirect",
		Data: dto.LoginData{
			RedirectURL: parsed.String(),
		},
	})
}

// setupBindAndRedirect generates a one-time exchange code with action=bind and returns a redirect URL.
func setupBindAndRedirect(user *model.User, c *gin.Context, redirectURI string) {
	code, err := common.StoreOAuthExchangeCode(&common.OAuthExchangeData{
		UserID:      user.Id,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Action:      "bind",
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	parsed, _ := url.Parse(redirectURI)
	q := parsed.Query()
	q.Set("code", code)
	parsed.RawQuery = q.Encode()

	c.JSON(http.StatusOK, dto.ApiResponse{
		Success: true,
		Message: "redirect",
		Data: dto.LoginData{
			RedirectURL: parsed.String(),
		},
	})
}

// setupOAuthErrorRedirect sends the external frontend a redirect URL carrying
// the error, so a failed bind/login (e.g. already_bound) bounces back to the
// originating frontend instead of stranding the user on the API host. Returns
// true when the external-redirect flow was handled.
func setupOAuthErrorRedirect(c *gin.Context, redirectURI, errMsg string) bool {
	if redirectURI == "" {
		return false
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}
	q := parsed.Query()
	q.Set("error", errMsg)
	parsed.RawQuery = q.Encode()

	c.JSON(http.StatusOK, dto.ApiResponse{
		Success: true,
		Message: "redirect",
		Data: dto.LoginData{
			RedirectURL: parsed.String(),
		},
	})
	return true
}

// ExchangeOAuthCode exchanges a one-time OAuth code for user data and access token.
func ExchangeOAuthCode(c fuego.ContextWithBody[dto.OAuthExchangeRequest]) (*dto.Response[dto.OAuthExchangeData], error) {
	body, err := c.Body()
	if err != nil || body.Code == "" {
		return dto.Fail[dto.OAuthExchangeData]("Invalid authorization code")
	}

	data := common.RedeemOAuthExchangeCode(body.Code)
	if data == nil {
		return dto.Fail[dto.OAuthExchangeData]("Invalid or expired code")
	}

	return dto.Ok(dto.OAuthExchangeData{
		AccessToken:     data.AccessToken,
		AccessExpiresAt: data.AccessExpiresAt,
		UserID:          data.UserID,
		Username:        data.Username,
		DisplayName:     data.DisplayName,
		Role:            data.Role,
		Action:          data.Action,
	})
}

func Logout(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	setAuthNoStore(ginCtx)
	if rawRefreshToken, err := ginCtx.Cookie(service.RefreshCookieName); err == nil && rawRefreshToken != "" {
		if sid, ok := service.RefreshTokenSID(rawRefreshToken); ok {
			_ = service.RevokeByRefreshToken(rawRefreshToken, sid, "logout")
		}
	}
	service.ClearRefreshCookie(ginCtx)
	return dto.Msg("")
}

// registerIpLimited reports whether ip already created REGISTER_IP_MAX_ACCOUNTS accounts (<=0 disables).
func registerIpLimited(ip string) (bool, error) {
	// The per-IP cap only ever sees one address, so a farm renting a /24 rotates
	// through it and stays under the limit on every individual IP. Refusing the
	// whole allocation is the only version of this check it cannot outspend.
	if service.RegistrationNetworkFlagged(ip) {
		return true, nil
	}
	limit := common.GetEnvOrDefault("REGISTER_IP_MAX_ACCOUNTS", 1)
	if limit <= 0 || ip == "" {
		return false, nil
	}
	count, err := model.CountUsersByRegisterIp(ip)
	if err != nil {
		return false, err
	}
	return count >= int64(limit), nil
}

func Register(c fuego.ContextWithBody[dto.RegisterRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	if !common.RegisterEnabled {
		return dto.FailMsg("New user registration has been disabled by administrator")
	}
	if !common.PasswordRegisterEnabled {
		return dto.FailMsg("Password registration has been disabled by administrator, please use third-party account verification")
	}
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg("Invalid parameters")
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		return dto.FailMsg("Invalid parameters")
	}
	if err := common.Validate.Struct(&req); err != nil {
		return dto.FailMsg(common.NewMessage("Invalid input {{Error}}", map[string]any{"Error": err.Error()}).Error())
	}
	if common.EmailVerificationEnabled {
		if req.Email == "" || req.VerificationCode == "" {
			return dto.FailMsg("Email verification is enabled, please enter email address and verification code")
		}
		if !common.VerifyCodeWithKey(req.Email, req.VerificationCode, common.EmailVerificationPurpose) {
			return dto.FailMsg("Verification code is incorrect or has expired")
		}
	}
	exist, err := model.CheckUserExistOrDeleted(req.Username, req.Email)
	if err != nil {
		common.SysLog(fmt.Sprintf("CheckUserExistOrDeleted error: %v", err))
		return dto.FailMsg("Database error, please contact the administrator")
	}
	if exist {
		return dto.FailMsg("Username already exists or has been deleted")
	}
	registerIp := publicClientIp(ginCtx)
	limited, err := registerIpLimited(registerIp)
	if err != nil {
		return dto.FailMsg("Database error, please contact the administrator")
	}
	if limited {
		return dto.FailMsg("An account has already been registered from this IP address")
	}
	affCode := req.AffCode
	inviterId, _ := model.GetUserIdByAffCode(affCode)
	cleanUser := model.User{
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.Username,
		InviterId:   inviterId,
		Role:        common.RoleCommonUser,
		RegisterIp:  registerIp,
		// Written together, always. The address is cleared after 30 days; the marker is
		// what the per-IP cap and the Discord reward check compare on from then on.
		RegisterIpHash: common.RegisterIpHash(registerIp),
	}
	if common.EmailVerificationEnabled {
		cleanUser.Email = req.Email
	}
	if err := cleanUser.Insert(inviterId); err != nil {
		return dto.FailMsg(err.Error())
	}

	var insertedUser model.User
	if err := model.DB.Where("username = ?", cleanUser.Username).First(&insertedUser).Error; err != nil {
		return dto.FailMsg("User registration failed or user ID retrieval failed")
	}
	if constant.GenerateDefaultToken {
		key, err := common.GenerateKey()
		if err != nil {
			common.SysLog("failed to generate token key: " + err.Error())
			return dto.FailMsg("Failed to generate default token")
		}
		token := model.Token{
			UserId:             insertedUser.Id,
			Name:               cleanUser.Username + "'s initial token",
			Key:                key,
			CreatedTime:        common.GetTimestamp(),
			AccessedTime:       common.GetTimestamp(),
			ExpiredTime:        -1,
			RemainQuota:        500000,
			UnlimitedQuota:     true,
			ModelLimitsEnabled: false,
		}
		if setting.DefaultUseAutoGroup {
			token.Group = "auto"
		}
		if err := token.Insert(); err != nil {
			return dto.FailMsg("Failed to create default token")
		}
	}

	return dto.Msg("")
}

func GetAllUsers(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.User]], error) {
	ginCtx := dto.GinCtx(c)
	pageInfo := dto.PageInfo(c)
	sortOptions := model.NewUserSortOptions(ginCtx.Query("sort_by"), ginCtx.Query("sort_order"))
	users, total, err := model.GetAllUsers(pageInfo, sortOptions)
	if err != nil {
		return dto.FailPage[*model.User](err.Error())
	}
	recordSensitiveRead(ginCtx, "read.user_list", map[string]interface{}{
		"page":  pageInfo.GetPage(),
		"count": len(users),
	})
	return dto.OkPage(pageInfo, users, int(total))
}

func SearchUsers(c fuego.ContextWithParams[dto.SearchUsersParams]) (*dto.Response[dto.PageData[*model.User]], error) {
	p, _ := dto.ParseParams[dto.SearchUsersParams](c)
	pageInfo := dto.PageInfo(c)
	sortOptions := model.NewUserSortOptions(p.SortBy, p.SortOrder)
	users, total, err := model.SearchUsers(p.Keyword, p.Group, p.Role, p.Status, p.NegativeQuota, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), sortOptions)
	if err != nil {
		return dto.FailPage[*model.User](err.Error())
	}
	recordSensitiveRead(dto.GinCtx(c), "read.user_search", map[string]interface{}{
		"keyword": p.Keyword,
		"count":   len(users),
	})
	return dto.OkPage(pageInfo, users, int(total))
}

func canManageTargetRole(myRole int, targetRole int) bool {
	return myRole == common.RoleRootUser || myRole > targetRole
}

// patDeniedManageActions are the /user/manage actions a personal access token
// must never perform. Each of them changes who can log in or what they may do,
// which is exactly what the 2026-08-26 intruder drove through this route with a
// stolen admin PAT: promote to gain reach, disable and delete to lock the owner
// out. The route cannot move behind SessionOnly() because the Discord bot needs
// it, so the refusal lives here instead.
//
// Read actions and the bot's rate-limit action are unaffected; a real dashboard
// session still performs all of these normally.
var patDeniedManageActions = map[string]bool{
	"delete":                         true,
	"promote":                        true,
	"demote":                         true,
	"disable":                        true,
	"enable":                         true,
	"add_quota":                      true,
	"set_block_free":                 true,
	"set_unlimited_free":             true,
	"set_moderation_exempt":          true,
	"set_free_rate_limit_window_pct": true,
	"set_usable_groups":              true,
}

// botAllowedManageAction is the only /user/manage action the Discord bot's
// service token may invoke.
const botAllowedManageAction = "set_free_rate_limit_window_pct"

// maxDiscordGrantQuota bounds one discord_grant. The bot hands out cents for
// votes and server tags (the largest legitimate grant on record is $1), so a
// single call has no reason to move more than $10; a stolen bot token granted
// $4000 in one request on 2026-09-08.
const maxDiscordGrantQuota = 10 * 500 * 1000

// GetUserBotView returns only the fields the Discord bot reads, instead of the
// whole user record. GetUser serves email, Discord id, register IP and referral
// data, none of which the bot uses, so pointing a service credential at it
// would let a compromised bot enumerate that for any user id.
func GetUserBotView(c fuego.ContextNoBody) (*dto.Response[dto.UserBotViewData], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[dto.UserBotViewData](err.Error())
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.Fail[dto.UserBotViewData](err.Error())
	}
	return dto.Ok(dto.UserBotViewData{Username: user.Username, Quota: user.Quota, Setting: user.Setting})
}

func GetUser(c fuego.ContextNoBody) (*dto.Response[model.User], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[model.User](err.Error())
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.Fail[model.User](err.Error())
	}
	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, user.Role) {
		return dto.Fail[model.User]("No permission to access users of same or higher level")
	}
	user.AdminPermissions = authz.Capabilities(user.Id, user.Role)
	return dto.Ok(*user)
}

func GenerateAccessToken(c fuego.ContextNoBody) (*dto.Response[string], error) {
	id := dto.UserID(c)
	// Privileged accounts administer through a console session. Minting a PAT here
	// once left root with a standing credential that outlived the operator who
	// created it and was replayed from Tor; rotate such a token with a scoped
	// database UPDATE instead, so it never exists as a mintable value.
	if dto.UserRole(c) >= common.RoleAdminUser {
		return dto.Fail[string]("Unauthorized, insufficient privileges")
	}
	randI := common.GetRandomInt(4)
	key, err := common.GenerateRandomKey(29 + randI)
	if err != nil {
		common.SysLog("failed to generate key: " + err.Error())
		return dto.Fail[string]("Generation failed")
	}
	if model.DB.Where("access_token = ?", key).First(&model.User{}).RowsAffected != 0 {
		return dto.Fail[string]("Please retry, the system generated a duplicate UUID!")
	}

	if err := model.UpdateUserLegacyAccessToken(id, key); err != nil {
		return dto.Fail[string](err.Error())
	}

	return dto.Ok(key)
}

func TransferAffQuota(c fuego.ContextWithBody[dto.TransferAffQuotaRequest]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.FailMsg("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	id := dto.UserID(c)
	user, err := model.GetUserById(id, true)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	tran, err := c.Body()
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	err = user.TransferAffQuotaToQuota(tran.Quota)
	var message *common.Message
	if errors.As(err, &message) {
		return dto.FailMsg(message.Error())
	}
	if err != nil {
		return dto.FailMsg(common.NewMessage("Transfer failed {{Error}}", map[string]any{"Error": err.Error()}).Error())
	}
	// Converts commission into spendable balance. Lower risk than a partner grant
	// because the money stays in the account, but it is still a balance change a
	// PAT can trigger, and it recorded nothing at all.
	recordUserSecurityAudit(ginCtx, id, "user.aff_transfer", map[string]interface{}{
		"quota": tran.Quota,
	})
	return dto.Msg("Transfer successful")
}

func GetAffCode(c fuego.ContextNoBody) (*dto.Response[string], error) {
	id := dto.UserID(c)
	user, err := model.GetUserById(id, true)
	if err != nil {
		return dto.Fail[string](err.Error())
	}
	if user.AffCode == "" {
		user.AffCode = common.GetRandomString(4)
		if err := user.Update(false); err != nil {
			return dto.Fail[string](err.Error())
		}
	}
	return dto.Ok(user.AffCode)
}

func GetInvitedUsers(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.InvitedUser]], error) {
	id := dto.UserID(c)
	pageInfo := common.GetPageQuery(dto.GinCtx(c))
	users, total, err := model.GetInvitedUsers(id, pageInfo)
	if err != nil {
		return dto.FailPage[*model.InvitedUser](err.Error())
	}
	return dto.OkPage(pageInfo, users, int(total))
}

func GetReferralCommissions(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*model.ReferralCommissionWithUser]], error) {
	id := dto.UserID(c)
	pageInfo := common.GetPageQuery(dto.GinCtx(c))
	commissions, total, err := model.GetUserReferralCommissions(id, pageInfo)
	if err != nil {
		return dto.FailPage[*model.ReferralCommissionWithUser](err.Error())
	}
	return dto.OkPage(pageInfo, commissions, int(total))
}

func GetSelf(c fuego.ContextNoBody) (*dto.Response[dto.UserSelfData], error) {
	id := dto.UserID(c)
	userRole := dto.UserRole(c)
	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.Fail[dto.UserSelfData](err.Error())
	}
	user.Remark = ""
	backfillRegisterIp(user, dto.GinCtx(c))

	// The authenticated role is loaded from GetUserCache. It should equal the
	// row role, but use it for capabilities so GetSelf and login/refresh remain
	// consistent with the authorization decision made for this request.
	permissions := calculateUserPermissions(userRole)
	permissions["admin_permissions"] = authz.Capabilities(id, userRole)

	userSetting := user.GetSetting()

	hasPassword, err := model.UserHasPassword(id)
	if err != nil {
		return dto.Fail[dto.UserSelfData](err.Error())
	}

	data := dto.UserSelfData{
		Id:                        user.Id,
		Username:                  user.Username,
		DisplayName:               user.DisplayName,
		Role:                      user.Role,
		Status:                    user.Status,
		Email:                     user.Email,
		GitHubId:                  user.GitHubId,
		DiscordId:                 user.DiscordId,
		GoogleId:                  user.GoogleId,
		OidcId:                    user.OidcId,
		WeChatId:                  user.WeChatId,
		TelegramId:                user.TelegramId,
		Group:                     user.Group,
		Quota:                     user.Quota,
		UsedQuota:                 user.UsedQuota,
		RequestCount:              user.RequestCount,
		AffCode:                   user.AffCode,
		AffCount:                  user.AffCount,
		AffQuota:                  user.AffQuota,
		AffHistoryQuota:           user.AffHistoryQuota,
		AffCommissionRate:         effectiveCommissionRate(user.ReferralCommissionPercent),
		TopUpBonusPercent:         effectiveTopUpBonus(user.TopUpBonusPercent),
		AffCommissionMaxRecharges: common.ReferralCommissionMaxRecharges,
		InviterId:                 user.InviterId,
		LinuxDOId:                 user.LinuxDOId,
		Setting:                   user.Setting,
		StripeCustomer:            user.StripeCustomer,
		SidebarModules:            userSetting.SidebarModules,
		Permissions:               permissions,
		HasPassword:               hasPassword,
	}

	// Hand back a fresh token once the current one is past half its life. The
	// session slides on use, but the JWT expiry is fixed at issuance, so without
	// this a long-lived client is logged out mid-request on a live session.
	if ginCtx := dto.GinCtx(c); ginCtx != nil && ginCtx.GetBool("use_access_token") {
		if identity, ok := middleware.GetSessionAuthIdentity(ginCtx); ok {
			if raw, found := middleware.AuthorizationToken(ginCtx.GetHeader("Authorization")); found &&
				service.AccessTokenPastHalfLife(raw) {
				if token, expiresAt, err := service.IssueAccessToken(identity); err == nil {
					data.AccessToken = token
					data.AccessExpiresAt = expiresAt
				}
			}
		}
	}

	return dto.Ok(data)
}

func effectiveCommissionRate(perUser *float64) float64 {
	if perUser != nil {
		return *perUser
	}
	return common.ReferralCommissionPercent
}

// buildSelfUserData is the single safe dashboard-user DTO used by GetSelf,
// login and refresh. It intentionally excludes password, management PAT and
// administrator-only remarks.
func buildSelfUserData(user *model.User) map[string]any {
	userSetting := user.GetSetting()
	permissions := calculateUserPermissions(user.Role)
	permissions["admin_permissions"] = authz.Capabilities(user.Id, user.Role)
	return map[string]any{
		"id":                user.Id,
		"username":          user.Username,
		"display_name":      user.DisplayName,
		"has_password":      user.HasPassword,
		"role":              user.Role,
		"status":            user.Status,
		"email":             user.Email,
		"github_id":         user.GitHubId,
		"discord_id":        user.DiscordId,
		"google_id":         user.GoogleId,
		"oidc_id":           user.OidcId,
		"wechat_id":         user.WeChatId,
		"telegram_id":       user.TelegramId,
		"group":             user.Group,
		"quota":             user.Quota,
		"used_quota":        user.UsedQuota,
		"request_count":     user.RequestCount,
		"aff_code":          user.AffCode,
		"aff_count":         user.AffCount,
		"aff_quota":         user.AffQuota,
		"aff_history_quota": user.AffHistoryQuota,
		"inviter_id":        user.InviterId,
		"linux_do_id":       user.LinuxDOId,
		"setting":           user.Setting,
		"stripe_customer":   user.StripeCustomer,
		"sidebar_modules":   userSetting.SidebarModules, // 正确提取sidebar_modules字段
		"permissions":       permissions,
	}
}

func calculateUserPermissions(userRole int) map[string]interface{} {
	permissions := map[string]interface{}{}

	if userRole == common.RoleRootUser {
		permissions["sidebar_settings"] = false
		permissions["sidebar_modules"] = map[string]any{}
	} else if userRole == common.RoleAdminUser {
		permissions["sidebar_settings"] = true
		permissions["sidebar_modules"] = map[string]interface{}{
			"admin": map[string]interface{}{
				"setting": false,
			},
		}
	} else if userRole == common.RoleModUser {
		permissions["sidebar_settings"] = true
		permissions["sidebar_modules"] = map[string]interface{}{
			"admin": map[string]interface{}{
				"enabled":    true,
				"channel":    true,
				"models":     false,
				"redemption": false,
				"user":       true,
				"setting":    false,
			},
		}
	} else {
		permissions["sidebar_settings"] = true
		permissions["sidebar_modules"] = map[string]interface{}{
			"admin": false,
		}
	}

	return permissions
}

func GetUserModels(c fuego.ContextNoBody) (*dto.Response[[]string], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		id = dto.UserID(c)
	}
	user, err := model.GetUserCache(id)
	if err != nil {
		return dto.Fail[[]string](err.Error())
	}
	groups := service.GetUserUsableGroups(user.Group)
	group := dto.GinCtx(c).Query("group")
	var groupsToQuery []string
	switch {
	case group == "":
		for g := range groups {
			groupsToQuery = append(groupsToQuery, g)
		}
	case group == "auto":
		if _, ok := groups[group]; ok {
			groupsToQuery = service.GetUserAutoGroup(user.Group)
		}
	default:
		if _, ok := groups[group]; ok {
			groupsToQuery = []string{group}
		}
	}
	return dto.Ok(service.GetGroupsEnabledModels(groupsToQuery))
}

func UpdateUser(c fuego.ContextWithBody[model.User]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	updatedUser, err := c.Body()
	if err != nil || updatedUser.Id == 0 {
		return dto.FailMsg("Invalid parameters")
	}
	// Password is optional on update: empty = keep unchanged (the `omitempty`
	// validate tag lets an empty password bind + pass validation).
	if err := common.Validate.Struct(&updatedUser); err != nil {
		return dto.FailMsg(common.NewMessage("Invalid input {{Error}}", map[string]any{"Error": err.Error()}).Error())
	}
	originUser, err := model.GetUserById(updatedUser.Id, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, originUser.Role) {
		return dto.FailMsg("No permission to update users of same or higher permission level")
	}
	if !canManageTargetRole(myRole, updatedUser.Role) {
		return dto.FailMsg("Cannot create users with permission level equal to or higher than yourself")
	}
	updatePassword := updatedUser.Password != ""
	// Changing your OWN password belongs on /user/self, which requires the current
	// password and an interactive session. Allowing it here let the 2026-08-26
	// intruder rewrite the root password from a stolen token without knowing it.
	if updatePassword && updatedUser.Id == dto.UserID(c) {
		return dto.FailMsg("No permission to update users of same or higher permission level")
	}
	auditParams := map[string]any{
		"username": originUser.Username,
		"id":       updatedUser.Id,
	}
	// Resetting a password or rewriting the admin permission matrix changes what
	// the managed account can do or who can sign in to it.
	if updatePassword || updatedUser.AdminPermissions != nil {
		authorization, failure := checkAdminUserProof(ginCtx, service.VerificationScopeAdminUserUpdate, service.AdminUserContext{UserID: updatedUser.Id})
		if authorization == nil {
			return dto.FailMsg(failure)
		}
		auditParams["verification_method"] = authorization.Method
		auditParams["password_reset"] = updatePassword
		auditParams["admin_permissions_updated"] = updatedUser.AdminPermissions != nil
	}
	authzTouched := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := updatedUser.EditWithTx(tx, updatePassword); err != nil {
			return err
		}
		touched, err := updateAdminPermissionsForUserInTx(ginCtx, tx, updatedUser.Id, originUser.Role, updatedUser.AdminPermissions)
		authzTouched = touched
		return err
	}); err != nil {
		return dto.FailMsg(err.Error())
	}
	if authzTouched {
		if err := authz.ReloadPolicy(); err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	if updatedUser.AuthVersion > originUser.AuthVersion {
		if _, err := model.RevokeAllUserSessions(updatedUser.Id, "admin_user_update"); err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	if err := model.PublishUserAuthCache(updatedUser.Id); err != nil {
		return dto.FailMsg(err.Error())
	}
	recordManageAuditFor(ginCtx, updatedUser.Id, "user.update", auditParams)
	return dto.Msg("")
}

func AdminClearUserBinding(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.FailMsg("Invalid parameters")
	}

	bindingType := strings.ToLower(strings.TrimSpace(c.PathParam("binding_type")))
	if bindingType == "" {
		return dto.FailMsg("Invalid parameters")
	}

	user, err := model.GetUserById(id, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, user.Role) {
		return dto.FailMsg("No permission to access users of same or higher level")
	}
	authorization, failure := checkAdminUserProof(ginCtx, service.VerificationScopeAdminUserBindingClear, service.AdminUserBindingContext{UserID: user.Id, BindingType: bindingType})
	if authorization == nil {
		return dto.FailMsg(failure)
	}

	if err := user.ClearBinding(bindingType); err != nil {
		return dto.FailMsg(err.Error())
	}

	recordManageAuditFor(ginCtx, user.Id, "user.binding_clear", map[string]interface{}{
		"bindingType":         bindingType,
		"username":            user.Username,
		"verification_method": authorization.Method,
	})

	return dto.Msg("success")
}

// selfUnbindableTypes lists the OAuth binding types a user may remove from their own account.
var selfUnbindableTypes = map[string]bool{
	"github":   true,
	"discord":  true,
	"google":   true,
	"oidc":     true,
	"wechat":   true,
	"telegram": true,
	"linuxdo":  true,
}

func SelfClearBinding(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	userId := dto.UserID(c)
	if userId == 0 {
		return dto.FailMsg("Not logged in")
	}

	bindingType := strings.ToLower(strings.TrimSpace(c.PathParam("binding_type")))
	if !selfUnbindableTypes[bindingType] {
		return dto.FailMsg("Invalid parameters")
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		return dto.FailMsg(err.Error())
	}

	if err := user.ClearBinding(bindingType); err != nil {
		return dto.FailMsg(err.Error())
	}

	// Detaching an identity frees it to be attached elsewhere, so the removal
	// belongs on the record as much as the bind does. The admin path already
	// audits; this is the self-service one.
	recordUserSecurityAudit(ginCtx, userId, "user.oauth_unbind", map[string]interface{}{
		"provider": bindingType,
	})

	return dto.Msg("Unbound successfully")
}

func UpdateSelf(c fuego.ContextNoBody) (dto.ApiResponse, error) {
	ginCtx := dto.GinCtx(c)
	var requestData map[string]interface{}
	err := dto.Decode(c, &requestData)
	if err != nil {
		return dto.FailAny("Invalid parameters")
	}

	if sidebarModules, sidebarExists := requestData["sidebar_modules"]; sidebarExists {
		userId := dto.UserID(c)
		user, err := model.GetUserById(userId, false)
		if err != nil {
			return dto.FailAny(err.Error())
		}

		currentSetting := user.GetSetting()

		if sidebarModulesStr, ok := sidebarModules.(string); ok {
			currentSetting.SidebarModules = sidebarModulesStr
		}

		if err := model.UpdateUserSetting(user.Id, currentSetting); err != nil {
			return dto.FailAny("Update failed")
		}

		return dto.OkMsgAny("Update successful", nil)
	}

	if language, langExists := requestData["language"]; langExists {
		userId := dto.UserID(c)
		user, err := model.GetUserById(userId, false)
		if err != nil {
			return dto.FailAny(err.Error())
		}

		currentSetting := user.GetSetting()

		if langStr, ok := language.(string); ok {
			currentSetting.Language = langStr
		}

		if err := model.UpdateUserSetting(user.Id, currentSetting); err != nil {
			return dto.FailAny("Update failed")
		}

		return dto.OkMsgAny("Update successful", nil)
	}

	var user model.User
	requestDataBytes, err := common.Marshal(requestData)
	if err != nil {
		return dto.FailAny("Invalid parameters")
	}
	if err = common.Unmarshal(requestDataBytes, &user); err != nil {
		return dto.FailAny("Invalid parameters")
	}

	if user.Password == "" {
		user.Password = "$I_LOVE_U"
	}
	if err := common.Validate.Struct(&user); err != nil {
		return dto.FailAny("Invalid input")
	}

	cleanUser := model.User{
		Id:          dto.UserID(c),
		Username:    user.Username,
		Password:    user.Password,
		DisplayName: user.DisplayName,
	}
	if user.Password == "$I_LOVE_U" {
		user.Password = ""
		cleanUser.Password = ""
	}
	updatePassword, err := checkUpdatePassword(ginCtx, user.OriginalPassword, user.Password, cleanUser.Id)
	if err != nil {
		return dto.FailAny(err.Error())
	}
	if updatePassword {
		identity, ok := middleware.GetSessionAuthIdentity(ginCtx)
		if !ok {
			return dto.FailAny("The current authentication method does not support security verification")
		}
		if err := model.DB.Transaction(func(tx *gorm.DB) error {
			return cleanUser.UpdateWithTx(tx, true)
		}); err != nil {
			return dto.FailAny(err.Error())
		}
		if err := model.PublishUserAuthCache(cleanUser.Id); err != nil {
			return dto.FailAny(err.Error())
		}
		bundle, err := service.AdvanceCurrentSessionToUserVersion(identity, "password_changed")
		if err != nil {
			return dto.FailAny(err.Error())
		}
		// Taking over an account permanently means changing its password. The
		// checks above make that hard (current password plus a real session, which
		// a PAT cannot satisfy), but nothing recorded a success, so a change made
		// with stolen credentials left the same trace as a routine one: none.
		recordUserSecurityAudit(ginCtx, cleanUser.Id, "user.password_change", nil)
		return dto.OkMsgAny("", gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"session":           bundle.Session,
		})
	}
	if err := cleanUser.Update(false); err != nil {
		return dto.FailAny(err.Error())
	}

	return dto.OkMsgAny("", nil)
}

func checkUpdatePassword(ginCtx *gin.Context, originalPassword string, newPassword string, userId int) (updatePassword bool, err error) {
	var currentUser *model.User
	currentUser, err = model.GetUserById(userId, true)
	if err != nil {
		return
	}

	if !common.ValidatePasswordAndHash(originalPassword, currentUser.Password) && currentUser.Password != "" {
		err = fmt.Errorf("%s", "Original password is incorrect")
		return
	}
	if newPassword == "" {
		return
	}
	updatePassword = true
	return
}

func DeleteUser(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	originUser, err := model.GetUserByIdUnscoped(id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	myRole := dto.UserRole(c)
	if myRole <= originUser.Role {
		return dto.FailMsg("No permission to update users of same or higher permission level")
	}
	authorization, failure := checkAdminUserProof(dto.GinCtx(c), service.VerificationScopeAdminUserDelete, service.AdminUserContext{UserID: originUser.Id})
	if authorization == nil {
		return dto.FailMsg(failure)
	}
	revokedAccessTokens, err := model.HardDeleteUserById(id)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	recordManageAuditFor(dto.GinCtx(c), originUser.Id, "user.delete", map[string]interface{}{
		"username":              originUser.Username,
		"id":                    originUser.Id,
		"verification_method":   authorization.Method,
		"revoked_access_tokens": revokedAccessTokens,
	})
	return dto.Msg("")
}

func DeleteSelf(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	id := dto.UserID(c)
	user, _ := model.GetUserById(id, false)

	if user.Role == common.RoleRootUser {
		return dto.FailMsg("Cannot delete super administrator account")
	}

	if _, err := model.DeleteUserById(id); err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

func CreateUser(c fuego.ContextWithBody[model.User]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	user, err := c.Body()
	user.Username = strings.TrimSpace(user.Username)
	if err != nil || user.Username == "" || user.Password == "" {
		return dto.FailMsg("Invalid parameters")
	}
	if err := common.Validate.Struct(&user); err != nil {
		return dto.FailMsg(common.NewMessage("Invalid input {{Error}}", map[string]any{"Error": err.Error()}).Error())
	}
	if !common.IsValidateRole(user.Role) {
		return dto.FailMsg("Invalid parameters")
	}
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	myRole := dto.UserRole(c)
	if user.Role >= myRole {
		return dto.FailMsg("Cannot create users with permission level equal to or higher than yourself")
	}
	auditParams := map[string]any{"role": user.Role}
	// Creating an administrator grants privilege just like promoting one.
	if user.Role >= common.RoleAdminUser {
		authorization, failure := checkAdminUserProof(ginCtx, service.VerificationScopeAdminUserCreate, service.AdminUserCreateContext{Role: user.Role})
		if authorization == nil {
			return dto.FailMsg(failure)
		}
		auditParams["verification_method"] = authorization.Method
	}
	cleanUser := model.User{
		Username:    user.Username,
		Password:    user.Password,
		DisplayName: user.DisplayName,
		Role:        user.Role,
	}
	authzTouched := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := cleanUser.InsertWithTx(tx, 0); err != nil {
			return err
		}
		touched, err := updateAdminPermissionsForUserInTx(ginCtx, tx, cleanUser.Id, cleanUser.Role, user.AdminPermissions)
		authzTouched = touched
		return err
	}); err != nil {
		return dto.FailMsg(err.Error())
	}
	if authzTouched {
		if err := authz.ReloadPolicy(); err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	cleanUser.FinishInsert(0)

	auditParams["username"] = cleanUser.Username
	recordManageAuditFor(ginCtx, cleanUser.Id, "user.create", auditParams)
	return dto.Msg("")
}

func updateAdminPermissionsForUserInTx(c *gin.Context, tx *gorm.DB, userID int, userRole int, permissions map[string]map[string]bool) (bool, error) {
	if permissions == nil {
		if userRole < common.RoleAdminUser && c.GetInt("role") == common.RoleRootUser {
			return true, authz.ClearUserAuthorizationInTx(tx, userID)
		}
		return false, nil
	}
	if c.GetInt("role") != common.RoleRootUser {
		return false, fmt.Errorf("only root can update admin permissions")
	}
	if userRole < common.RoleAdminUser {
		return true, authz.ClearUserAuthorizationInTx(tx, userID)
	}
	return true, authz.SetUserPermissionsInTx(tx, userID, permissions)
}

// ManageUser handles user management actions (enable/disable/delete/promote/demote)
func ManageUser(c fuego.ContextWithBody[dto.ManageRequest]) (*dto.Response[dto.ManageUserData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()

	if err != nil {
		return dto.Fail[dto.ManageUserData]("Invalid parameters")
	}
	// A zero id turns the struct lookup below into an unconditioned query that
	// resolves to whichever row sorts first.
	if req.Id <= 0 {
		return dto.Fail[dto.ManageUserData]("Invalid parameters")
	}
	var user model.User
	if err := model.DB.Unscoped().Where("id = ?", req.Id).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.Fail[dto.ManageUserData]("User does not exist")
		}
		return dto.Fail[dto.ManageUserData]("Database error, please contact the administrator")
	}
	// Balance changes need a live account; the unscoped lookup exists so
	// enable/delete can still reach soft-deleted rows.
	if req.Action == "add_quota" && user.DeletedAt.Valid {
		return dto.Fail[dto.ManageUserData]("User does not exist")
	}
	myRole := dto.UserRole(c)
	if !canManageTargetRole(myRole, user.Role) {
		return dto.Fail[dto.ManageUserData]("No permission to update users of same or higher permission level")
	}
	// The bot's service credential reaches this route for exactly one action.
	// Without this the token would still be able to delete or promote accounts,
	// which is most of what taking the token off root was meant to prevent.
	if middleware.AuthenticatedViaBotToken(ginCtx) && req.Action != botAllowedManageAction {
		return dto.Fail[dto.ManageUserData]("No permission to update users of same or higher permission level")
	}
	// A bearer secret with no second factor must not decide who keeps their
	// account. AuthenticatedViaPAT excludes the bot and sync service tokens, so
	// this narrows human PATs only.
	if patDeniedManageActions[req.Action] && middleware.AuthenticatedViaPAT(ginCtx) {
		return dto.Fail[dto.ManageUserData]("Unauthorized, insufficient privileges")
	}
	switch req.Action {
	case "disable":
		user.Status = common.UserStatusDisabled
		if user.Role == common.RoleRootUser {
			return dto.Fail[dto.ManageUserData]("Cannot disable super administrator user")
		}
	case "enable":
		user.Status = common.UserStatusEnabled
	case "delete":
		if user.Role == common.RoleRootUser {
			return dto.Fail[dto.ManageUserData]("Cannot delete super administrator account")
		}
		authorization, failure := checkAdminUserProof(ginCtx, service.VerificationScopeAdminUserDelete, service.AdminUserContext{UserID: user.Id})
		if authorization == nil {
			return dto.Fail[dto.ManageUserData](failure)
		}
		revokedAccessTokens, err := user.Delete()
		if err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		// Cached relay tokens outlive the row until their TTL; drop them now so a
		// deleted account cannot keep relaying.
		if err := model.InvalidateUserTokensCache(user.Id); err != nil {
			common.SysLog(common.LogText("failed to invalidate tokens cache for user %d: %s", user.Id, err.Error()))
		}
		recordManageAuditFor(ginCtx, user.Id, "user.manage", map[string]interface{}{
			"action":                req.Action,
			"username":              user.Username,
			"id":                    user.Id,
			"verification_method":   authorization.Method,
			"revoked_access_tokens": revokedAccessTokens,
		})
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "promote":
		if user.Role >= common.RoleAdminUser {
			return dto.Fail[dto.ManageUserData]("This user is already an administrator")
		}
		nextRole := common.RoleModUser
		if user.Role >= common.RoleModUser {
			nextRole = common.RoleAdminUser
		}
		if !canManageTargetRole(myRole, nextRole) {
			return dto.Fail[dto.ManageUserData]("Cannot create users with permission level equal to or higher than yourself")
		}
		user.Role = nextRole
	case "demote":
		if user.Role == common.RoleRootUser {
			return dto.Fail[dto.ManageUserData]("Cannot demote super administrator user")
		}
		if user.Role <= common.RoleCommonUser {
			return dto.Fail[dto.ManageUserData]("This user is already a common user")
		}
		if user.Role >= common.RoleAdminUser {
			user.Role = common.RoleModUser
		} else {
			user.Role = common.RoleCommonUser
		}
	case "add_quota":
		switch req.Mode {
		case "add":
			if req.Value <= 0 {
				return dto.Fail[dto.ManageUserData]("Quota change amount cannot be zero")
			}
			if err := common.ValidateWalletQuota(req.Value); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			if err := common.ValidateWalletQuota(user.Quota + req.Value); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			if err := model.IncreaseUserQuota(user.Id, req.Value, true); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			recordManageAuditFor(ginCtx, user.Id, "user.quota_add", map[string]interface{}{
				"quota": logger.LogQuota(req.Value),
			})
		case "subtract":
			if req.Value <= 0 {
				return dto.Fail[dto.ManageUserData]("Quota change amount cannot be zero")
			}
			if err := common.ValidateWalletQuota(req.Value); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			if user.Quota-req.Value < -common.MaxWalletQuota {
				return dto.Fail[dto.ManageUserData](fmt.Sprintf("wallet quota below -%d", common.MaxWalletQuota))
			}
			if err := model.DecreaseUserQuota(user.Id, req.Value, true); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			recordManageAuditFor(ginCtx, user.Id, "user.quota_subtract", map[string]interface{}{
				"quota": logger.LogQuota(req.Value),
			})
		case "override":
			if err := common.ValidateWalletQuota(req.Value); err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			oldQuota := user.Quota
			if err := model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", req.Value).Error; err != nil {
				return dto.Fail[dto.ManageUserData](err.Error())
			}
			recordManageAuditFor(ginCtx, user.Id, "user.quota_override", map[string]interface{}{
				"from": logger.LogQuota(oldQuota),
				"to":   logger.LogQuota(req.Value),
			})
		default:
			return dto.Fail[dto.ManageUserData]("Invalid parameters")
		}
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_block_free":
		s := user.GetSetting()
		s.BlockFreeWhenNoQuota = req.Value == 1
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			common.NewMessage("admin ({{admin}}) set block-free-models-when-balance-zero to {{value}} for user", map[string]any{"admin": adminName, "value": req.Value == 1}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_unlimited_free":
		s := user.GetSetting()
		s.UnlimitedFreeModels = req.Value == 1
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			common.NewMessage("admin ({{admin}}) set unlimited-free-models to {{value}} for user", map[string]any{"admin": adminName, "value": req.Value == 1}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_moderation_exempt":
		s := user.GetSetting()
		s.ModerationExempt = req.Value == 1
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			common.NewMessage("admin ({{admin}}) set moderation-exempt to {{value}} for user", map[string]any{"admin": adminName, "value": req.Value == 1}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_free_rate_limit_window_pct":
		// Carries a real number, unlike the boolean grants above: the value is the
		// percentage shaved off the free-model window.
		pct := types.ClampFreeRateLimitWindowPct(req.Value)
		s := user.GetSetting()
		s.FreeRateLimitWindowPct = pct
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			common.NewMessage("admin ({{admin}}) set free-rate-limit-window-pct to {{value}} for user", map[string]any{"admin": adminName, "value": pct}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	case "set_usable_groups":
		// Per-user usable-group grants (private routing groups). Keep only
		// non-empty groups that exist in GroupRatio so we never store junk.
		seen := make(map[string]bool)
		groups := make([]string, 0, len(req.Groups))
		for _, g := range req.Groups {
			if g == "" || seen[g] || !ratio_setting.ContainsGroupRatio(g) {
				continue
			}
			seen[g] = true
			groups = append(groups, g)
		}
		s := user.GetSetting()
		s.UsableGroups = groups
		user.SetSetting(s)
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		adminName := ginCtx.GetString("username")
		model.RecordLog(user.Id, model.LogTypeManage,
			common.NewMessage("admin ({{admin}}) set usable groups to [{{groups}}] for user", map[string]any{"admin": adminName, "groups": strings.Join(groups, ", ")}))
		return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
	default:
		return dto.Fail[dto.ManageUserData]("Invalid parameters")
	}

	authorization, failure := checkAdminUserProof(ginCtx, service.VerificationScopeAdminUserManage, service.AdminUserManageContext{UserID: user.Id, Action: req.Action})
	if authorization == nil {
		return dto.Fail[dto.ManageUserData](failure)
	}
	if req.Action == "demote" {
		if err := model.DB.Transaction(func(tx *gorm.DB) error {
			if err := user.UpdateWithTx(tx, false); err != nil {
				return err
			}
			return authz.ClearUserAuthorizationInTx(tx, user.Id)
		}); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		if err := authz.ReloadPolicy(); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		if err := model.PublishUserAuthCache(user.Id); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
		if _, err := model.RevokeAllUserSessions(user.Id, "admin_demote"); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
	} else {
		if err := user.Update(false); err != nil {
			return dto.Fail[dto.ManageUserData](err.Error())
		}
	}
	// Update/UpdateWithTx has already published the new user hash and revoked
	// browser sessions exactly once. Only PAT/relay token caches still need an
	// explicit invalidation; deleting the user hash here would discard the
	// freshly published auth-version floor.
	if err := model.InvalidateUserTokensCache(user.Id); err != nil {
		common.SysLog(common.LogText("failed to invalidate tokens cache for user %d: %s", user.Id, err.Error()))
	}
	recordManageAuditFor(ginCtx, user.Id, "user.manage", map[string]interface{}{
		"action":              req.Action,
		"username":            user.Username,
		"id":                  user.Id,
		"verification_method": authorization.Method,
	})
	return dto.Ok(dto.ManageUserData{Role: user.Role, Status: user.Status})
}

func grantLabel(req dto.GrantDiscordQuotaRequest) string {
	clean := func(v string, max int) string {
		v = strings.Join(strings.Fields(v), " ")
		if r := []rune(v); len(r) > max {
			v = string(r[:max])
		}
		return v
	}
	source, reason, by := clean(req.Source, 32), clean(req.Reason, 120), clean(req.GrantedBy, 32)
	if source == "" && reason == "" {
		return ""
	}
	label := " [" + source
	if reason != "" {
		label += ": " + reason
	}
	if by != "" && by != "system" {
		label += ", by Discord user " + by
	}
	return label + "]"
}

// GrantDiscordQuota grants quota to the user linked to a Discord ID.
// Repeatable: it always adds quota when the Discord account is linked. The caller
// (the Discord bot) owns any idempotency/audit. Returns Linked=false when no user
// has bound the given Discord ID.
func GrantDiscordQuota(c fuego.ContextWithBody[dto.GrantDiscordQuotaRequest]) (*dto.Response[dto.GrantDiscordQuotaData], error) {
	ginCtx := dto.GinCtx(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.GrantDiscordQuotaData]("Invalid parameters")
	}
	if req.DiscordId == "" || req.Quota <= 0 || req.Quota > maxDiscordGrantQuota {
		return dto.Fail[dto.GrantDiscordQuotaData]("Invalid parameters")
	}

	if !model.IsDiscordIdAlreadyTaken(req.DiscordId) {
		return dto.Ok(dto.GrantDiscordQuotaData{Linked: false})
	}

	user := model.User{DiscordId: req.DiscordId}
	if err := user.FillUserByDiscordId(); err != nil {
		return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
	}
	if user.Id == 0 {
		return dto.Ok(dto.GrantDiscordQuotaData{Linked: false})
	}

	// The shared-IP check is a gate for new accounts, not a state an established
	// one can fall back into: a VPN exit someone else used, a hash backfill or an
	// older account surfacing later would otherwise cut off a member who was being
	// paid for weeks. Passing once, or holding an earlier bot grant, clears it for good.
	if req.CheckIpUnique && user.RegisterIpHash != "" && !user.GetSetting().RewardIpCleared {
		duplicate, err := model.HasEarlierUserWithRegisterIpHash(user.RegisterIpHash, user.Id)
		if err != nil {
			return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
		}
		if duplicate {
			granted, err := model.HasDiscordBotGrant(user.Id)
			if err != nil {
				return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
			}
			if !granted {
				return dto.Ok(dto.GrantDiscordQuotaData{UserId: user.Id, Linked: true, IpDuplicate: true})
			}
		}
		setting := user.GetSetting()
		setting.RewardIpCleared = true
		if err := model.UpdateUserSetting(user.Id, setting); err != nil {
			return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
		}
	}

	if err := model.IncreaseUserQuota(user.Id, req.Quota, true); err != nil {
		return dto.Fail[dto.GrantDiscordQuotaData](err.Error())
	}

	adminName := ginCtx.GetString("username")
	model.RecordLog(user.Id, model.LogTypeManage,
		common.NewMessage("admin ({{admin}}) granted quota {{quota}} to a Discord-linked user{{label}}", map[string]any{"admin": adminName, "quota": logger.LogQuota(req.Quota), "label": grantLabel(req)}))

	return dto.Ok(dto.GrantDiscordQuotaData{UserId: user.Id, Linked: true})
}

// TransferDiscordQuota moves quota from one Discord-linked user to another in a
// single atomic transaction. Both sides must be linked; the sender's balance is
// checked under a row lock so concurrent transfers cannot overspend. Like
// GrantDiscordQuota, the caller (the Discord bot) owns idempotency/audit.
func TransferDiscordQuota(c fuego.ContextWithBody[dto.TransferDiscordQuotaRequest]) (*dto.Response[dto.TransferDiscordQuotaData], error) {
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.TransferDiscordQuotaData]("Invalid parameters")
	}
	if req.FromDiscordId == "" || req.ToDiscordId == "" || req.Quota <= 0 || req.FromDiscordId == req.ToDiscordId {
		return dto.Fail[dto.TransferDiscordQuotaData]("Invalid parameters")
	}

	if !model.IsDiscordIdAlreadyTaken(req.FromDiscordId) {
		return dto.Ok(dto.TransferDiscordQuotaData{FromLinked: false, ToLinked: model.IsDiscordIdAlreadyTaken(req.ToDiscordId)})
	}
	if !model.IsDiscordIdAlreadyTaken(req.ToDiscordId) {
		return dto.Ok(dto.TransferDiscordQuotaData{FromLinked: true, ToLinked: false})
	}

	fromUser := model.User{DiscordId: req.FromDiscordId}
	if err := fromUser.FillUserByDiscordId(); err != nil {
		return dto.Fail[dto.TransferDiscordQuotaData](err.Error())
	}
	toUser := model.User{DiscordId: req.ToDiscordId}
	if err := toUser.FillUserByDiscordId(); err != nil {
		return dto.Fail[dto.TransferDiscordQuotaData](err.Error())
	}
	if fromUser.Id == 0 {
		return dto.Ok(dto.TransferDiscordQuotaData{FromLinked: false, ToLinked: toUser.Id != 0})
	}
	if toUser.Id == 0 {
		return dto.Ok(dto.TransferDiscordQuotaData{FromUserId: fromUser.Id, FromLinked: true, ToLinked: false})
	}

	fromBalance, err := model.TransferQuotaBetweenUsers(fromUser.Id, toUser.Id, req.Quota)
	if err != nil {
		if errors.Is(err, model.ErrInsufficientQuota) {
			return dto.Ok(dto.TransferDiscordQuotaData{
				FromUserId: fromUser.Id, ToUserId: toUser.Id,
				FromLinked: true, ToLinked: true, Insufficient: true,
				FromBalance: fromUser.Quota,
			})
		}
		return dto.Fail[dto.TransferDiscordQuotaData](err.Error())
	}

	model.RecordLog(fromUser.Id, model.LogTypeManage,
		common.NewMessage("transferred quota {{quota}} to a Discord-linked user", map[string]any{"quota": logger.LogQuota(req.Quota)}))
	model.RecordLog(toUser.Id, model.LogTypeManage,
		common.NewMessage("received quota {{quota}} from a Discord-linked user", map[string]any{"quota": logger.LogQuota(req.Quota)}))

	return dto.Ok(dto.TransferDiscordQuotaData{
		FromUserId: fromUser.Id, ToUserId: toUser.Id,
		FromLinked: true, ToLinked: true, FromBalance: fromBalance,
	})
}

func EmailBind(c fuego.ContextWithParams[dto.EmailBindParams]) (dto.MessageResponse, error) {
	ginCtx := dto.GinCtx(c)
	p, _ := dto.ParseParams[dto.EmailBindParams](c)
	if !common.VerifyCodeWithKey(p.Email, p.Code, common.EmailVerificationPurpose) {
		return dto.FailMsg("Verification code is incorrect or has expired")
	}
	id := ginCtx.GetInt("id")
	if id == 0 {
		return dto.FailMsg("Not logged in")
	}
	user := model.User{
		Id: id,
	}
	if err := user.FillUserById(); err != nil {
		return dto.FailMsg(err.Error())
	}
	previousEmail := user.Email
	user.Email = p.Email
	if err := user.Update(false); err != nil {
		return dto.FailMsg(err.Error())
	}
	// The address is the password-reset channel, so repointing it is a way back
	// into an account that survives revoking every session and credential. The
	// old value is recorded because only the transition proves a redirect: after
	// the write the prior address exists nowhere.
	recordUserSecurityAudit(ginCtx, id, "user.email_bind", map[string]interface{}{
		"from": previousEmail,
		"to":   p.Email,
	})
	return dto.Msg("")
}

var topUpLocks sync.Map
var topUpCreateLock sync.Mutex

type topUpTryLock struct {
	ch chan struct{}
}

func newTopUpTryLock() *topUpTryLock {
	return &topUpTryLock{ch: make(chan struct{}, 1)}
}

func (l *topUpTryLock) TryLock() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *topUpTryLock) Unlock() {
	select {
	case <-l.ch:
	default:
	}
}

func getTopUpLock(userID int) *topUpTryLock {
	if v, ok := topUpLocks.Load(userID); ok {
		return v.(*topUpTryLock)
	}
	topUpCreateLock.Lock()
	defer topUpCreateLock.Unlock()
	if v, ok := topUpLocks.Load(userID); ok {
		return v.(*topUpTryLock)
	}
	l := newTopUpTryLock()
	topUpLocks.Store(userID, l)
	return l
}

func TopUp(c fuego.ContextWithBody[dto.TopUpRequest]) (*dto.Response[int], error) {
	ginCtx := dto.GinCtx(c)
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return dto.Fail[int]("Payment, redemption, subscription, and invitation reward features are disabled. The administrator must confirm compliance terms before enabling them.")
	}
	id := dto.UserID(c)
	lock := getTopUpLock(id)
	if !lock.TryLock() {
		return dto.Fail[int]("Top-up is processing, please try again later")
	}
	defer lock.Unlock()
	req, err := c.Body()
	if err != nil {
		return dto.Fail[int](err.Error())
	}
	quota, err := model.Redeem(req.Key, id)
	if err != nil {
		// 不向用户暴露兑换失败的细分原因，避免攻击者根据错误类型判断兑换码状态。
		logger.LogError(ginCtx, fmt.Sprintf("failed to redeem key %s for user %d: %s", req.Key, id, err.Error()))
		return dto.Fail[int]("Redemption failed, please try again later")
	}
	return dto.Ok(quota)
}

func UpdateUserSetting(c fuego.ContextWithBody[dto.UpdateUserSettingRequest]) (dto.MessageResponse, error) {
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg("Invalid parameters")
	}

	if req.QuotaWarningType != types.NotifyTypeEmail && req.QuotaWarningType != types.NotifyTypeWebhook && req.QuotaWarningType != types.NotifyTypeBark && req.QuotaWarningType != types.NotifyTypeGotify {
		return dto.FailMsg("Invalid warning type")
	}

	if req.QuotaWarningThreshold <= 0 {
		return dto.FailMsg("Warning threshold must be greater than 0")
	}

	if req.QuotaWarningType == types.NotifyTypeWebhook {
		if req.WebhookUrl == "" {
			return dto.FailMsg("Webhook URL cannot be empty")
		}
		if _, err := url.ParseRequestURI(req.WebhookUrl); err != nil {
			return dto.FailMsg("Invalid Webhook URL")
		}
	}

	if req.QuotaWarningType == types.NotifyTypeEmail && req.NotificationEmail != "" {
		if !strings.Contains(req.NotificationEmail, "@") {
			return dto.FailMsg("Invalid email address")
		}
	}

	if req.QuotaWarningType == types.NotifyTypeBark {
		if req.BarkUrl == "" {
			return dto.FailMsg("Bark push URL cannot be empty")
		}
		if _, err := url.ParseRequestURI(req.BarkUrl); err != nil {
			return dto.FailMsg("Invalid Bark push URL")
		}
		if !strings.HasPrefix(req.BarkUrl, "https://") && !strings.HasPrefix(req.BarkUrl, "http://") {
			return dto.FailMsg("URL must start with http:// or https://")
		}
	}

	if req.QuotaWarningType == types.NotifyTypeGotify {
		if req.GotifyUrl == "" {
			return dto.FailMsg("Gotify server URL cannot be empty")
		}
		if req.GotifyToken == "" {
			return dto.FailMsg("Gotify token cannot be empty")
		}
		if _, err := url.ParseRequestURI(req.GotifyUrl); err != nil {
			return dto.FailMsg("Invalid Gotify server URL")
		}
		if !strings.HasPrefix(req.GotifyUrl, "https://") && !strings.HasPrefix(req.GotifyUrl, "http://") {
			return dto.FailMsg("URL must start with http:// or https://")
		}
	}

	userId := dto.UserID(c)
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	existingSettings := user.GetSetting()
	upstreamModelUpdateNotifyEnabled := existingSettings.UpstreamModelUpdateNotifyEnabled
	if user.Role >= common.RoleAdminUser && req.UpstreamModelUpdateNotifyEnabled != nil {
		upstreamModelUpdateNotifyEnabled = *req.UpstreamModelUpdateNotifyEnabled
	}

	settings := types.UserSetting{
		QuotaWarningEnabled:              req.QuotaWarningEnabled,
		NotifyType:                       req.QuotaWarningType,
		QuotaWarningThreshold:            req.QuotaWarningThreshold,
		UpstreamModelUpdateNotifyEnabled: upstreamModelUpdateNotifyEnabled,
		AcceptUnsetRatioModel:            req.AcceptUnsetModelRatioModel,
		RecordIpLog:                      req.RecordIpLog,
		// Not part of this request DTO; rebuilding the struct without them
		// silently wiped admin grants and UI prefs on every notify-settings save.
		SidebarModules:            existingSettings.SidebarModules,
		BillingPreference:         existingSettings.BillingPreference,
		Language:                  existingSettings.Language,
		BlockFreeWhenNoQuota:      existingSettings.BlockFreeWhenNoQuota,
		UsableGroups:              existingSettings.UsableGroups,
		UnlimitedFreeModels:       existingSettings.UnlimitedFreeModels,
		ModerationExempt:          existingSettings.ModerationExempt,
		FreeRateLimitWindowPct:    existingSettings.FreeRateLimitWindowPct,
		MaxFirstTokenSeconds:      existingSettings.MaxFirstTokenSeconds,
		MaxChainFirstTokenSeconds: existingSettings.MaxChainFirstTokenSeconds,
	}

	if req.QuotaWarningType == types.NotifyTypeWebhook {
		settings.WebhookUrl = req.WebhookUrl
		if req.WebhookSecret != "" {
			settings.WebhookSecret = req.WebhookSecret
		}
	}

	if req.QuotaWarningType == types.NotifyTypeEmail && req.NotificationEmail != "" {
		settings.NotificationEmail = req.NotificationEmail
	}

	if req.QuotaWarningType == types.NotifyTypeBark {
		settings.BarkUrl = req.BarkUrl
	}

	if req.QuotaWarningType == types.NotifyTypeGotify {
		settings.GotifyUrl = req.GotifyUrl
		settings.GotifyToken = req.GotifyToken
		if req.GotifyPriority < 0 || req.GotifyPriority > 10 {
			settings.GotifyPriority = 5
		} else {
			settings.GotifyPriority = req.GotifyPriority
		}
	}

	// 更新用户设置
	if err := model.UpdateUserSetting(user.Id, settings); err != nil {
		return dto.FailMsg("Update failed")
	}

	return dto.Msg("Settings updated")
}

// UpdateTimeoutPreference stores the caller's opt-in first-token limits. Both
// bound only the wait for the upstream's FIRST byte, never a reply already
// streaming, so a long generation is unaffected by either. Zero disables.
//
// Read-modify-write on the stored settings rather than rebuilding the struct:
// UserSetting carries admin grants and UI prefs this request knows nothing
// about, and reconstructing it would silently clear them.
func UpdateTimeoutPreference(c fuego.ContextWithBody[dto.TimeoutPreferenceRequest]) (*dto.Response[dto.TimeoutPreferenceData], error) {
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.TimeoutPreferenceData]("Invalid parameters")
	}

	perAttempt := types.ClampFirstTokenSeconds(req.MaxFirstTokenSeconds)
	chain := types.ClampFirstTokenSeconds(req.MaxChainFirstTokenSeconds)
	// A chain budget under the per-attempt one would cut the first attempt short
	// of its own allowance, which reads as the per-attempt value being ignored.
	if chain > 0 && perAttempt > 0 && chain < perAttempt {
		chain = perAttempt
	}

	user, err := model.GetUserById(dto.UserID(c), true)
	if err != nil {
		return dto.Fail[dto.TimeoutPreferenceData](err.Error())
	}
	current := user.GetSetting()
	current.MaxFirstTokenSeconds = perAttempt
	current.MaxChainFirstTokenSeconds = chain
	if err := model.UpdateUserSetting(user.Id, current); err != nil {
		return dto.Fail[dto.TimeoutPreferenceData]("Update failed")
	}

	return dto.Ok(dto.TimeoutPreferenceData{
		MaxFirstTokenSeconds:      perAttempt,
		MaxChainFirstTokenSeconds: chain,
	})
}

// effectiveTopUpBonus flattens the nullable per-user bonus for the /self
// response: absent means no bonus, which the dashboard reads as "not a partner".
func effectiveTopUpBonus(perUser *float64) float64 {
	if perUser != nil && *perUser > 0 {
		return *perUser
	}
	return 0
}
