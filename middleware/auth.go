package middleware

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const authIdentityContextKey = "auth_identity"

type dashboardCredentialKind int

const (
	dashboardCredentialUnmatched dashboardCredentialKind = iota
	dashboardCredentialInternal
	dashboardCredentialPAT
)

func validUserInfo(username string, role int) bool {
	// check username is empty
	if strings.TrimSpace(username) == "" {
		return false
	}
	if !common.IsValidateRole(role) {
		return false
	}
	return true
}

// enforceRoleAndStatus validates that the user set on the gin context meets
// the minimum role and is not disabled. Used by authHelper after the OAuth
// bearer path populates the context; the legacy access-token/session path
// keeps its inline checks for historical reasons.
//
// On failure the response is written, the request is aborted, and a non-nil
// error is returned so the caller can bail immediately.
func enforceRoleAndStatus(c *gin.Context, minRole int) error {
	username, _ := c.Get("username")
	role, _ := c.Get("role")

	roleInt, ok := role.(int)
	if !ok || !common.IsValidateRole(roleInt) {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid),
		})
		c.Abort()
		return errAuthInvalid
	}
	if roleInt < minRole {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
		c.Abort()
		return errAuthInvalid
	}
	if name, ok := username.(string); !ok || strings.TrimSpace(name) == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid),
		})
		c.Abort()
		return errAuthInvalid
	}
	return nil
}

var errAuthInvalid = errors.New("auth: invalid user")

func authHelper(c *gin.Context, minRole int) {
	user, identity, useAccessToken, err := authenticateDashboardRequest(c)
	if err != nil {
		writeDashboardAuthError(c, err)
		return
	}
	if user.Status != common.UserStatusEnabled {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_DISABLED", "message": common.TranslateMessage(c, i18n.MsgAuthUserBanned)})
		return
	}
	if user.Role < minRole {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "AUTH_INSUFFICIENT_PRIVILEGE", "message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege)})
		return
	}
	if !validUserInfo(user.Username, user.Role) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_USER_INVALID", "message": common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)})
		return
	}
	setDashboardAuthContext(c, user, identity, useAccessToken)

	// 管理/root 写操作审计兜底：内聚在鉴权链路里，保证任何经过 AdminAuth/RootAuth
	// 的写接口都会自动留痕（无需在路由上单独挂审计中间件，避免漏挂）。
	// handler 内手动埋点者会设置 ContextKeyAuditLogged，finishAdminAudit 据此跳过。
	var auditWriter *auditResponseWriter
	if minRole >= common.RoleAdminUser {
		auditWriter = beginAdminAudit(c)
	}

	c.Next()

	finishAdminAudit(c, auditWriter)
}

func TryUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		user, identity, credentialKind, err := classifyDashboardCredential(c)
		if err != nil {
			writeDashboardAuthError(c, err)
			return
		}
		if credentialKind != dashboardCredentialUnmatched {
			setDashboardAuthContext(c, user, identity, credentialKind == dashboardCredentialPAT)
		}
		c.Next()
	}
}

func UserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleCommonUser)
	}
}

func ModAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleModUser)
	}
}

func AdminAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleAdminUser)
	}
}

func RootAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleRootUser)
	}
}

// SessionOnly rejects personal access tokens on routes that change the
// credentials guarding an account.
//
// A PAT is a bearer credential with no second factor and no expiry short enough
// to matter, so treating it as equivalent to a live browser session means the
// weakest credential can rewrite the strongest. On 2026-08-26 a stolen PAT
// changed the root password through the admin update route: UpdateSelf refuses
// exactly that (it demands the current password AND a real session), but the
// admin route accepts any caller who is already admin, so pointing it at your
// own account skipped every check.
//
// A credential must never be able to change what revokes it.
func SessionOnly() func(c *gin.Context) {
	return func(c *gin.Context) {
		if !c.GetBool("use_access_token") {
			c.Next()
			return
		}
		recordSecurityDenial(c, auditActionPermissionDenied, "PAT_NOT_ALLOWED", map[string]interface{}{
			"reason": "credential-changing route requires an interactive session",
		})
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "PAT_NOT_ALLOWED",
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
	}
}

// BotServiceUsername identifies the Discord bot in audit rows. It is not a user
// account, so it can never own tokens, log in, or be granted quota.
const BotServiceUsername = "discord-bot"

// BotServiceUserID is the audit actor id for the bot. Deliberately 0 rather
// than a real user id: the bot authenticates as itself, so attributing its
// writes to a human account would falsify the audit trail.
const BotServiceUserID = 0

const botAuthContextKey = "authenticated_via_bot_token"

// AuthenticatedViaBotToken reports whether BotAuth accepted this request, so a
// handler can restrict what a service credential may do beyond route access.
func AuthenticatedViaBotToken(c *gin.Context) bool {
	return c.GetBool(botAuthContextKey)
}

// BotAuth accepts the Discord bot's service token on the handful of routes it
// needs, and otherwise defers to AdminAuth so the dashboard is unaffected.
//
// The bot previously held root's access token, which authorized every admin
// route (channel writes, user deletion, upstream key reads) to perform four
// operations. A service credential that leaks should cost what the service can
// do, not what the platform can do.
//
// The token is NOT a user: it resolves to no account, so it cannot be promoted,
// cannot own tokens, and cannot be reused to log in. It only sets the context
// keys the downstream handlers and the admin auditor already read.
func BotAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		secret := system_setting.BotServiceToken()
		raw, ok := AuthorizationToken(c.GetHeader("Authorization"))
		if !ok || secret == "" || subtle.ConstantTimeCompare([]byte(raw), []byte(secret)) != 1 {
			authHelper(c, common.RoleAdminUser)
			return
		}

		c.Set("id", BotServiceUserID)
		c.Set("username", BotServiceUsername)
		c.Set("role", common.RoleAdminUser)
		c.Set("use_access_token", true)
		c.Set(botAuthContextKey, true)

		// Bot writes are audited on the same path as admin writes; skipping this
		// would make the one credential that runs unattended the one that leaves
		// no trace.
		auditWriter := beginAdminAudit(c)
		c.Next()
		finishAdminAudit(c, auditWriter)
	}
}

// SyncServiceUsername identifies new-api-sync in audit rows. Like the bot's
// credential it is not a user account, so it can never own tokens, log in, or be
// granted quota.
const SyncServiceUsername = "new-api-sync"

// SyncServiceUserID is the audit actor id for the sync. It owns no row in
// `users`; the id exists so the sync has a stable casbin subject
// (`user:900000001`) that can hold an explicit ChannelSensitiveWrite grant.
//
// Creating a channel means supplying its upstream key, so the sync genuinely
// needs that permission, and it cannot come from the admin role: the boot
// reconciler rebuilds casbin from each action's DefaultRoles, so a role grant
// added by hand is deleted on the next restart, and adding it in code would
// hand sensitive_write to every admin -- which router/channel_permissions_test.go
// exists to forbid. Per-user grants are the supported path and survive restarts,
// which is how user:1 holds the same permission today.
//
// Far above the users sequence (~24.7k) so it can never collide with a real
// account, and positive because id is read as an int in paths that assume a
// non-negative user.
const SyncServiceUserID = 900000001

const syncAuthContextKey = "authenticated_via_sync_token"

// AuthenticatedViaSyncToken reports whether SyncAuth accepted this request, so a
// handler can restrict what the sync credential may do beyond route access.
func AuthenticatedViaSyncToken(c *gin.Context) bool {
	return c.GetBool(syncAuthContextKey)
}

// SyncAuth accepts new-api-sync's service token on the routes it needs, and
// otherwise defers to normal admin auth so the dashboard is unaffected.
//
// The sync previously held root's access token, which authorized every admin
// route. What it actually needs is channel/model/vendor CRUD plus eighteen
// pricing and routing keys in the options table -- none of them the
// auth-hardening options a 2026-08-26 intruder used a stolen root PAT to
// disable. Root was three orders of magnitude more authority than the job.
//
// Role is RoleAdminUser, NOT root: the option route is the only thing that ever
// required root, and UpdateOption gates the sync to its own key list instead.
// The token resolves to no account, so it cannot be promoted, cannot own tokens,
// and cannot be reused to log in.
func SyncAuth(minRole int) func(c *gin.Context) {
	return func(c *gin.Context) {
		secret := system_setting.SyncServiceToken()
		raw, ok := AuthorizationToken(c.GetHeader("Authorization"))
		if !ok || secret == "" || subtle.ConstantTimeCompare([]byte(raw), []byte(secret)) != 1 {
			authHelper(c, minRole)
			return
		}

		c.Set("id", SyncServiceUserID)
		c.Set("username", SyncServiceUsername)
		c.Set("role", common.RoleAdminUser)
		c.Set("use_access_token", true)
		c.Set(syncAuthContextKey, true)

		// Sync writes are audited on the same path as admin writes; skipping this
		// would make the one credential that runs unattended the one that leaves
		// no trace.
		auditWriter := beginAdminAudit(c)
		c.Next()
		finishAdminAudit(c, auditWriter)
	}
}

// GetAuthIdentity returns a dashboard session identity. PAT-authenticated
// requests intentionally have no SessionID and cannot manage browser sessions.
func GetAuthIdentity(c *gin.Context) (service.AuthIdentity, bool) {
	value, ok := c.Get(authIdentityContextKey)
	if !ok {
		return service.AuthIdentity{}, false
	}
	identity, ok := value.(service.AuthIdentity)
	return identity, ok
}

// GetSessionAuthIdentity returns only identities backed by a live dashboard
// session. PAT-authenticated requests intentionally fail this check.
func GetSessionAuthIdentity(c *gin.Context) (service.AuthIdentity, bool) {
	identity, ok := GetAuthIdentity(c)
	if !ok {
		identity = service.AuthIdentity{
			UserID:          c.GetInt("id"),
			SessionID:       c.GetString("session_id"),
			UserAuthVersion: c.GetInt64("auth_version"),
			SessionVersion:  c.GetInt64("session_version"),
		}
	}
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 {
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func authenticateDashboardRequest(c *gin.Context) (*model.UserBase, service.AuthIdentity, bool, error) {
	user, identity, credentialKind, err := classifyDashboardCredential(c)
	if err != nil {
		return nil, service.AuthIdentity{}, credentialKind == dashboardCredentialPAT, err
	}
	if credentialKind == dashboardCredentialUnmatched {
		return nil, service.AuthIdentity{}, false, service.ErrAuthTokenInvalid
	}
	return user, identity, credentialKind == dashboardCredentialPAT, nil
}

// ResolveDashboardCredential returns the user behind the request's
// Authorization header, or nil when it carries none that validates. It accepts
// both credential kinds the dashboard issues: a login session token and a
// personal access token.
//
// Exported for routes that must authenticate a caller themselves because they
// cannot sit behind UserAuth, such as the OAuth bind flow, whose cross-domain
// redirect drops the session cookie. Those routes must NOT re-implement this:
// checking only model.ValidateAccessToken silently rejects every user without a
// PAT, which is most of them.
func ResolveDashboardCredential(c *gin.Context) (*model.UserBase, error) {
	user, _, kind, err := classifyDashboardCredential(c)
	if err != nil {
		return nil, err
	}
	if kind == dashboardCredentialUnmatched {
		return nil, nil
	}
	return user, nil
}

func classifyDashboardCredential(c *gin.Context) (*model.UserBase, service.AuthIdentity, dashboardCredentialKind, error) {
	raw, ok := AuthorizationToken(c.GetHeader("Authorization"))
	if !ok {
		return nil, service.AuthIdentity{}, dashboardCredentialUnmatched, nil
	}
	identity, internal, err := service.ParseDashboardAccessToken(raw)
	if internal {
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		_, user, err := service.ValidateLoginSession(identity)
		if err != nil {
			return nil, service.AuthIdentity{}, dashboardCredentialInternal, err
		}
		return user, identity, dashboardCredentialInternal, nil
	}
	patUser, err := model.ValidateAccessToken(raw)
	if err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	if patUser == nil || patUser.Id <= 0 {
		return nil, service.AuthIdentity{}, dashboardCredentialUnmatched, nil
	}
	user, err := model.GetUserCache(patUser.Id)
	if err != nil {
		return nil, service.AuthIdentity{}, dashboardCredentialPAT, err
	}
	return user, service.AuthIdentity{UserID: user.Id, UserAuthVersion: user.AuthVersion}, dashboardCredentialPAT, nil
}

// AuthorizationToken extracts a bearer credential from an Authorization header.
func AuthorizationToken(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}
	parts := strings.Fields(header)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		header = parts[1]
	} else if len(parts) != 1 {
		return "", false
	}
	return header, header != ""
}

func setDashboardAuthContext(c *gin.Context, user *model.UserBase, identity service.AuthIdentity, useAccessToken bool) {
	c.Header("Auth-Version", "864b7076dbcd0a3c01b5520316720ebf")
	c.Set("username", user.Username)
	c.Set("role", user.Role)
	c.Set("id", user.Id)
	c.Set("group", user.Group)
	c.Set("user_group", user.Group)
	c.Set("use_access_token", useAccessToken)
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	c.Set(authIdentityContextKey, identity)
	user.WriteContext(c)
}

func writeDashboardAuthError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrAuthTokenExpired) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_TOKEN_EXPIRED", "message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn)})
		return
	}
	if errors.Is(err, service.ErrLoginSessionRevoked) || errors.Is(err, gorm.ErrRecordNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_SESSION_REVOKED", "message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn)})
		return
	}
	if errors.Is(err, service.ErrAuthTokenInvalid) {
		// A rejected credential is the loudest signal we get that someone is
		// probing with something they should not have. Previously this returned
		// 401 and left no trace outside the edge log.
		recordSecurityDenial(c, auditActionAuthRejected, "AUTH_UNAUTHORIZED", nil)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_UNAUTHORIZED", "message": common.TranslateMessage(c, i18n.MsgAuthAccessTokenInvalid)})
		return
	}
	common.SysLog("dashboard authentication error: " + err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"success": false, "code": "AUTH_INTERNAL_ERROR", "message": common.TranslateMessage(c, i18n.MsgDatabaseError)})
}

func RequirePermission(permission authz.Permission) func(c *gin.Context) {
	return func(c *gin.Context) {
		role := c.GetInt("role")
		userID := c.GetInt("id")
		if authz.Can(userID, role, permission) {
			c.Next()
			return
		}
		// Privilege escalation attempt: the caller authenticated but reached for
		// something their role does not grant. Records which permission was
		// wanted, so a credential probing beyond its owner's normal scope is
		// visible rather than just a 403.
		recordSecurityDenial(c, auditActionPermissionDenied, "INSUFFICIENT_PRIVILEGE", map[string]interface{}{
			"permission": permission.Resource + ":" + permission.Action,
		})
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
		c.Abort()
	}
}

func WssAuth(c *gin.Context) {

}

// TokenOrUserAuth allows either session-based user auth or API token auth.
// Used for endpoints that need to be accessible from both the dashboard and API clients.
func TokenOrUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		raw, ok := AuthorizationToken(c.GetHeader("Authorization"))
		if ok {
			identity, internal, err := service.ParseDashboardAccessToken(raw)
			if !internal {
				TokenAuth()(c)
				return
			}
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			_, user, err := service.ValidateLoginSession(identity)
			if err != nil {
				writeDashboardAuthError(c, err)
				return
			}
			setDashboardAuthContext(c, user, identity, false)
			c.Next()
			return
		}
		// Opaque credentials are relay API keys here, never dashboard PATs.
		TokenAuth()(c)
	}
}

// TokenAuthReadOnly 宽松版本的令牌认证中间件，用于只读查询接口。
// 只验证令牌 key 是否存在，不检查令牌状态、过期时间和额度。
// 即使令牌已过期、已耗尽或已禁用，也允许访问。
// 仍然检查用户是否被封禁。
func TokenAuthReadOnly() func(c *gin.Context) {
	return func(c *gin.Context) {
		key := c.Request.Header.Get("Authorization")
		if key == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenNotProvided),
			})
			c.Abort()
			return
		}
		if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
			key = strings.TrimSpace(key[7:])
		}
		key = strings.TrimPrefix(key, "sk-")
		parts := strings.Split(key, "-")
		key = parts[0]

		token, err := model.GetTokenByKey(key, false)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgTokenInvalid),
				})
			} else {
				common.SysLog("TokenAuthReadOnly GetTokenByKey database error: " + err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
				})
			}
			c.Abort()
			return
		}

		// TokenAuthReadOnly must keep allowing other token states to query read-only
		// data, such as token usage logs; only explicitly disabled tokens are denied.
		if token.Status == common.TokenStatusDisabled {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenStatusUnavailable),
			})
			c.Abort()
			return
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuthReadOnly GetUserCache error for user %d: %v", token.UserId, err))
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
			})
			c.Abort()
			return
		}
		if userCache.Status != common.UserStatusEnabled {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthUserBanned),
			})
			c.Abort()
			return
		}

		c.Set("id", token.UserId)
		c.Set("token_id", token.Id)
		c.Set("token_key", token.Key)
		c.Next()
	}
}

// OptionalTokenAuth runs TokenAuth only when the request carries a credential.
// A caller with no key reaches the handler unauthenticated (user id 0), which
// is how /v1/models answers anonymously the way other aggregators do; a caller
// WITH a key is validated exactly as before, so a revoked or malformed one is
// still rejected rather than silently downgraded to the public list.
func OptionalTokenAuth() func(c *gin.Context) {
	authed := TokenAuth()
	return func(c *gin.Context) {
		hasCredential := c.Request.Header.Get("Authorization") != "" ||
			c.Request.Header.Get("x-api-key") != "" ||
			c.Request.Header.Get("x-goog-api-key") != "" ||
			c.Request.Header.Get("Sec-WebSocket-Protocol") != "" ||
			c.Query("key") != ""
		if !hasCredential {
			c.Next()
			return
		}
		authed(c)
	}
}

func TokenAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		// OAuth 2.1 bearer JWT first. If the request carries a valid OAuth
		// access token, the gin context is populated and we let it through;
		// other branches still fall through to the legacy `sk-` path.
		if matched, ok := tryOAuthBearerAuth(c); matched {
			if !ok {
				return
			}
			c.Next()
			return
		}
		// 先检测是否为ws
		if c.Request.Header.Get("Sec-WebSocket-Protocol") != "" {
			// Sec-WebSocket-Protocol: realtime, openai-insecure-api-key.sk-xxx, openai-beta.realtime-v1
			// read sk from Sec-WebSocket-Protocol
			key := c.Request.Header.Get("Sec-WebSocket-Protocol")
			parts := strings.Split(key, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "openai-insecure-api-key") {
					key = strings.TrimPrefix(part, "openai-insecure-api-key.")
					break
				}
			}
			c.Request.Header.Set("Authorization", "Bearer "+key)
		}
		// 检查path包含/v1/messages 或 /v1/models
		if strings.Contains(c.Request.URL.Path, "/v1/messages") || strings.Contains(c.Request.URL.Path, "/v1/models") {
			anthropicKey := c.Request.Header.Get("x-api-key")
			if anthropicKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+anthropicKey)
			}
		}
		// gemini api 从query中获取key
		if c.Request.URL.Path == "/v1/models" ||
			strings.HasPrefix(c.Request.URL.Path, "/v1beta/models") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1beta/openai/models") ||
			strings.HasPrefix(c.Request.URL.Path, "/v1/models/") {
			skKey := c.Query("key")
			if skKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+skKey)
			}
			// 从x-goog-api-key header中获取key
			xGoogKey := c.Request.Header.Get("x-goog-api-key")
			if xGoogKey != "" {
				c.Request.Header.Set("Authorization", "Bearer "+xGoogKey)
			}
		}
		key := c.Request.Header.Get("Authorization")
		parts := make([]string, 0)
		if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
			key = strings.TrimSpace(key[7:])
		}
		if key == "" || key == "midjourney-proxy" {
			key = c.Request.Header.Get("mj-api-secret")
			if strings.HasPrefix(key, "Bearer ") || strings.HasPrefix(key, "bearer ") {
				key = strings.TrimSpace(key[7:])
			}
			key = strings.TrimPrefix(key, "sk-")
			parts = strings.Split(key, "-")
			key = parts[0]
		} else {
			key = strings.TrimPrefix(key, "sk-")
			parts = strings.Split(key, "-")
			key = parts[0]
		}
		token, err := model.ValidateUserToken(key)
		if token != nil {
			id := c.GetInt("id")
			if id == 0 {
				c.Set("id", token.UserId)
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, model.ErrDatabase):
				common.SysLog("TokenAuth ValidateUserToken database error: " + err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError,
					common.TranslateMessage(c, i18n.MsgDatabaseError))
			case errors.Is(err, model.ErrTokenExhausted):
				abortWithOpenAiMessage(c, http.StatusPaymentRequired,
					common.TranslateMessage(c, i18n.MsgQuotaInsufficient))
			case errors.Is(err, model.ErrTokenExpired):
				abortWithOpenAiMessage(c, http.StatusUnauthorized,
					common.TranslateMessage(c, i18n.MsgTokenExpired))
			case errors.Is(err, model.ErrTokenDisabled):
				abortWithOpenAiMessage(c, http.StatusUnauthorized,
					common.TranslateMessage(c, i18n.MsgTokenStatusUnavailable))
			default:
				abortWithOpenAiMessage(c, http.StatusUnauthorized,
					common.TranslateMessage(c, i18n.MsgTokenInvalid))
			}
			return
		}

		allowIps := token.GetIpLimits()
		if len(allowIps) > 0 {
			clientIp := c.ClientIP()
			logger.LogDebug(c, "Token has IP restrictions, checking client IP %s", clientIp)
			ip := net.ParseIP(clientIp)
			if ip == nil {
				abortWithOpenAiMessage(c, http.StatusForbidden, "unable to parse client IP address")
				return
			}
			if common.IsIpInCIDRList(ip, allowIps) == false {
				abortWithOpenAiMessage(c, http.StatusForbidden, "your IP is not in the token's allowed access list", types.ErrorCodeAccessDenied)
				return
			}
			logger.LogDebug(c, "Client IP %s passed the token IP restrictions check", clientIp)
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuth GetUserCache error for user %d: %v", token.UserId, err))
			abortWithOpenAiMessage(c, http.StatusInternalServerError,
				common.TranslateMessage(c, i18n.MsgDatabaseError))
			return
		}
		userEnabled := userCache.Status == common.UserStatusEnabled
		if !userEnabled {
			abortWithOpenAiMessage(c, http.StatusForbidden, common.TranslateMessage(c, i18n.MsgAuthUserBanned))
			return
		}

		userCache.WriteContext(c)

		userGroup := userCache.Group
		tokenGroup := token.Group
		if tokenGroup != "" {
			// A composite pinned group ("vip,discount") validates per element; the
			// channel-select engine later resolves the concrete group per request.
			for _, g := range service.ParseTokenGroups(tokenGroup) {
				if !service.GroupInUserUsableGroups(userGroup, g) {
					abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("No permission to access group %s", g))
					return
				}
				// check group in common.GroupRatio
				if !ratio_setting.ContainsGroupRatio(g) {
					if g != "auto" {
						abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("Group %s has been deprecated", g))
						return
					}
				}
			}
			userGroup = tokenGroup
		}
		common.SetContextKey(c, constant.ContextKeyUsingGroup, userGroup)

		err = SetupContextForToken(c, token, parts...)
		if err != nil {
			return
		}
		c.Next()
	}
}

func SetupContextForToken(c *gin.Context, token *model.Token, parts ...string) error {
	if token == nil {
		return fmt.Errorf("token is nil")
	}
	c.Set("id", token.UserId)
	c.Set("token_id", token.Id)
	c.Set("token_key", token.Key)
	c.Set("token_name", token.Name)
	c.Set("token_unlimited_quota", token.UnlimitedQuota)
	if !token.UnlimitedQuota {
		c.Set("token_quota", token.RemainQuota)
	}
	if token.ModelLimitsEnabled {
		c.Set("token_model_limit_enabled", true)
		c.Set("token_model_limit", token.GetModelLimitsMap())
	} else {
		c.Set("token_model_limit_enabled", false)
	}
	common.SetContextKey(c, constant.ContextKeyTokenGroup, token.Group)
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, token.CrossGroupRetry)
	if token.AutoGroups != "" {
		autoGroups, err := token.GetAutoGroups()
		if err != nil {
			common.SysError(fmt.Sprintf("failed to parse auto groups for token %d: %v", token.Id, err))
			autoGroups = []string{}
			common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, autoGroups)
		} else if len(autoGroups) > 0 {
			common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, autoGroups)
		}
	}
	if token.GroupMapping != "" {
		common.SetContextKey(c, constant.ContextKeyTokenGroupMapping, token.GroupMapping)
	}
	if len(parts) > 1 {
		if model.IsAdmin(token.UserId) {
			c.Set("specific_channel_id", parts[1])
		} else {
			c.Header("specific_channel_version", "701e3ae1dc3f7975556d354e0675168d004891c8")
			abortWithOpenAiMessage(c, http.StatusForbidden, "regular users cannot specify a channel")
			return fmt.Errorf("regular users cannot specify a channel")
		}
	}
	return nil
}
