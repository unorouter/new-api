package controller

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"gorm.io/gorm"
)

// providerParams returns map with Provider key for i18n templates
func providerParams(name string) map[string]any {
	return map[string]any{"Provider": name}
}

// GenerateOAuthCode generates a state code for OAuth CSRF protection.
// When redirect_uri is provided (external frontend flow), the state is a self-contained
// signed token so it doesn't depend on the session (which won't survive cross-domain redirects).
func GenerateOAuthCode(c fuego.ContextWithParams[dto.GenerateOAuthCodeParams]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	p, _ := dto.ParseParams[dto.GenerateOAuthCodeParams](c)

	if p.RedirectURI != "" {
		// External redirect flow: store state in Redis
		if !common.IsAllowedRedirectURI(p.RedirectURI) {
			return dto.Fail[string](i18n.T(ginCtx, "oauth.redirect_uri_not_allowed"))
		}
		stateData := &common.OAuthStateData{
			RedirectURI: p.RedirectURI,
			Aff:         p.Aff,
		}
		// Bind flow: identify the logged-in user to bind to
		if p.Action == "bind" {
			var userID int
			// Try Authorization header (access token)
			if authHeader := ginCtx.GetHeader("Authorization"); authHeader != "" {
				if user, err := model.ValidateAccessToken(authHeader); err == nil && user != nil {
					userID = user.Id
				}
			}
			// Fall back to New-Api-User header (set by frontend proxy from session cookie)
			if userID == 0 {
				if idStr := ginCtx.GetHeader("New-Api-User"); idStr != "" {
					if id, err := strconv.Atoi(idStr); err == nil && id > 0 {
						user := &model.User{Id: id}
						if user.FillUserById() == nil && user.Status == common.UserStatusEnabled {
							userID = id
						}
					}
				}
			}
			if userID == 0 {
				return dto.Fail[string]("authentication required for bind")
			}
			stateData.UserID = userID
			stateData.Action = "bind"
		}
		state, err := common.CreateOAuthState(stateData)
		if err != nil {
			return nil, err
		}
		return dto.Ok(state)
	}

	// Same-origin flow: use session-based state
	session := sessions.Default(ginCtx)
	state := common.GetRandomString(12)
	if p.Aff != "" {
		session.Set("aff", p.Aff)
	}
	session.Set("oauth_state", state)
	if err := session.Save(); err != nil {
		return dto.Fail[string](err.Error())
	}
	return dto.Ok(state)
}

// HandleOAuth handles OAuth callback for all standard OAuth providers
func HandleOAuth(c *gin.Context) {
	providerName := c.Param("provider")
	provider := oauth.GetProvider(providerName)
	if provider == nil {
		c.JSON(http.StatusBadRequest, dto.ApiResponse{Message: i18n.T(c, "oauth.unknown_provider")})
		return
	}

	session := sessions.Default(c)
	state := c.Query("state")

	// 1. Validate state: try Redis-backed state first, then session-based
	var redirectURI, affCode string
	stateResult := common.RedeemOAuthState(state)
	if stateResult != nil {
		redirectURI = stateResult.RedirectURI
		affCode = stateResult.Aff
	} else if savedState, ok := session.Get("oauth_state").(string); !ok || state == "" || state != savedState {
		c.JSON(http.StatusForbidden, dto.ApiResponse{Message: i18n.T(c, "oauth.state_invalid")})
		return
	}

	// 2. Check for bind flow
	if stateResult != nil && stateResult.Action == "bind" && stateResult.UserID > 0 {
		// Cross-domain bind: load user from Redis state
		user := &model.User{Id: stateResult.UserID}
		if err := user.FillUserById(); err != nil {
			common.ApiError(c, err)
			return
		}
		handleOAuthBind(c, provider, user, redirectURI)
		return
	}
	if redirectURI == "" {
		// Same-origin bind: check session for logged-in user
		username := session.Get("username")
		if username != nil {
			handleOAuthBind(c, provider, nil, "")
			return
		}
	}

	// 3. Check if provider is enabled
	if !provider.IsEnabled() {
		common.ApiErrorI18n(c, "oauth.not_enabled", providerParams(provider.GetName()))
		return
	}

	// 4. Handle error from provider
	errorCode := c.Query("error")
	if errorCode != "" {
		errorDescription := c.Query("error_description")
		c.JSON(http.StatusOK, dto.ApiResponse{Message: errorDescription})
		return
	}

	// 5. Exchange code for token
	code := c.Query("code")
	token, err := provider.ExchangeToken(c.Request.Context(), code, c)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// 6. Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// 7. Handle affiliate code from signed state
	if affCode != "" {
		session.Set("aff", affCode)
		_ = session.Save()
	}

	// 8. Find or create user
	user, err := findOrCreateOAuthUser(c, provider, oauthUser, session)
	if err != nil {
		switch err.(type) {
		case *types.OAuthUserDeletedError:
			common.ApiErrorI18n(c, "oauth.user_deleted")
		case *types.OAuthRegistrationDisabledError:
			common.ApiErrorI18n(c, "user.register_disabled")
		default:
			common.ApiError(c, err)
		}
		return
	}

	// 9. Check user status
	if user.Status != common.UserStatusEnabled {
		common.ApiErrorI18n(c, "oauth.user_banned")
		return
	}

	// 10. External redirect or same-origin login
	if redirectURI != "" {
		setupLoginAndRedirect(user, c, redirectURI)
		return
	}
	setupLogin(user, c)
}

// handleOAuthBind handles binding OAuth account to existing user.
// When user is nil, it falls back to loading from session (same-origin flow).
// When redirectURI is non-empty, it redirects back to the external frontend.
func handleOAuthBind(c *gin.Context, provider oauth.Provider, user *model.User, redirectURI string) {
	if !provider.IsEnabled() {
		common.ApiErrorI18n(c, "oauth.not_enabled", providerParams(provider.GetName()))
		return
	}

	// Exchange code for token
	code := c.Query("code")
	token, err := provider.ExchangeToken(c.Request.Context(), code, c)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// Check if this OAuth account is already bound (check both new ID and legacy ID)
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		common.ApiErrorI18n(c, "oauth.already_bound", providerParams(provider.GetName()))
		return
	}
	// Also check legacy ID to prevent duplicate bindings during migration period
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" {
		if provider.IsUserIDTaken(legacyID) {
			common.ApiErrorI18n(c, "oauth.already_bound", providerParams(provider.GetName()))
			return
		}
	}

	// Load user from session if not provided (same-origin flow)
	if user == nil {
		session := sessions.Default(c)
		id := session.Get("id")
		user = &model.User{Id: id.(int)}
		if err = user.FillUserById(); err != nil {
			common.ApiError(c, err)
			return
		}
	}

	// Handle binding based on provider type
	if genericProvider, ok := provider.(*oauth.GenericOAuthProvider); ok {
		// Custom provider: use user_oauth_bindings table
		err = model.UpdateUserOAuthBinding(user.Id, genericProvider.GetProviderId(), oauthUser.ProviderUserID)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	} else {
		// Built-in provider: update user record directly
		provider.SetProviderUserID(user, oauthUser.ProviderUserID)
		err = user.Update(false)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}

	// Cross-domain bind: redirect back with exchange code
	if redirectURI != "" {
		setupBindAndRedirect(user, c, redirectURI)
		return
	}

	common.ApiSuccessI18n(c, i18n.MsgOAuthBindSuccess, gin.H{
		"action": "bind",
	})
}

// findOrCreateOAuthUser finds existing user or creates new user
func findOrCreateOAuthUser(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, session sessions.Session) (*model.User, error) {
	user := &model.User{}

	// Check if user already exists with new ID
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		err := provider.FillUserByProviderID(user, oauthUser.ProviderUserID)
		if err != nil {
			return nil, err
		}
		// Check if user has been deleted
		if user.Id == 0 {
			return nil, &types.OAuthUserDeletedError{}
		}
		return user, nil
	}

	// Try to find user with legacy ID (for GitHub migration from login to numeric ID)
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" {
		if provider.IsUserIDTaken(legacyID) {
			err := provider.FillUserByProviderID(user, legacyID)
			if err != nil {
				return nil, err
			}
			if user.Id != 0 {
				// Found user with legacy ID, migrate to new ID
				common.SysLog(fmt.Sprintf(i18n.Translate("ctrl.oauth_migrating_user_rom_legacy_id_o_new"),
					user.Id, legacyID, oauthUser.ProviderUserID))
				if err := user.UpdateGitHubId(oauthUser.ProviderUserID); err != nil {
					common.SysError(fmt.Sprintf(i18n.Translate("ctrl.oauth_failed_to_migrate_user"), user.Id, err.Error()))
					// Continue with login even if migration fails
				}
				return user, nil
			}
		}
	}

	// User doesn't exist, create new user if registration is enabled
	if !common.RegisterEnabled {
		return nil, &types.OAuthRegistrationDisabledError{}
	}

	// Set up new user
	user.Username = provider.GetProviderPrefix() + strconv.Itoa(model.GetMaxUserId()+1)

	if oauthUser.Username != "" {
		if exists, err := model.CheckUserExistOrDeleted(oauthUser.Username, ""); err == nil && !exists {
			// 防止索引退化
			if len(oauthUser.Username) <= model.UserNameMaxLength {
				user.Username = oauthUser.Username
			}
		}
	}

	if oauthUser.DisplayName != "" {
		user.DisplayName = oauthUser.DisplayName
	} else if oauthUser.Username != "" {
		user.DisplayName = oauthUser.Username
	} else {
		user.DisplayName = provider.GetName() + " User"
	}
	if oauthUser.Email != "" {
		user.Email = oauthUser.Email
	}
	user.Role = common.RoleCommonUser
	user.Status = common.UserStatusEnabled

	// Handle affiliate code
	affCode := session.Get("aff")
	inviterId := 0
	if affCode != nil {
		inviterId, _ = model.GetUserIdByAffCode(affCode.(string))
	}

	// Use transaction to ensure user creation and OAuth binding are atomic
	if genericProvider, ok := provider.(*oauth.GenericOAuthProvider); ok {
		// Custom provider: create user and binding in a transaction
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			// Create user
			if err := user.InsertWithTx(tx, inviterId); err != nil {
				return err
			}

			// Create OAuth binding
			binding := &model.UserOAuthBinding{
				UserId:         user.Id,
				ProviderId:     genericProvider.GetProviderId(),
				ProviderUserId: oauthUser.ProviderUserID,
			}
			if err := model.CreateUserOAuthBindingWithTx(tx, binding); err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return nil, err
		}

		// Perform post-transaction tasks (logs, sidebar config, inviter rewards)
		user.FinalizeOAuthUserCreation(inviterId)
	} else {
		// Built-in provider: create user and update provider ID in a transaction
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			// Create user
			if err := user.InsertWithTx(tx, inviterId); err != nil {
				return err
			}

			// Set the provider user ID on the user model and update
			provider.SetProviderUserID(user, oauthUser.ProviderUserID)
			if err := tx.Model(user).Updates(map[string]interface{}{
				"github_id":   user.GitHubId,
				"discord_id":  user.DiscordId,
				"oidc_id":     user.OidcId,
				"linux_do_id": user.LinuxDOId,
				"wechat_id":   user.WeChatId,
				"telegram_id": user.TelegramId,
			}).Error; err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return nil, err
		}

		// Perform post-transaction tasks
		user.FinalizeOAuthUserCreation(inviterId)
	}

	return user, nil
}

// handleOAuthError handles OAuth errors and returns translated message
func handleOAuthError(c *gin.Context, err error) {
	switch e := err.(type) {
	case *oauth.OAuthError:
		if e.Params != nil {
			common.ApiErrorI18n(c, e.MsgKey, e.Params)
		} else {
			common.ApiErrorI18n(c, e.MsgKey)
		}
	case *oauth.AccessDeniedError:
		common.ApiErrorMsg(c, e.Message)
	case *oauth.TrustLevelError:
		common.ApiErrorI18n(c, "oauth.trust_level_low")
	default:
		common.ApiError(c, err)
	}
}
