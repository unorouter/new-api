package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"gorm.io/gorm"
)

const oauthAuthFlowTTL = 10 * time.Minute

type oauthStateRequest struct {
	Provider string          `json:"provider"`
	Intent   string          `json:"intent"`
	Aff      string          `json:"aff,omitempty"`
	Scope    string          `json:"scope,omitempty"`
	Context  json.RawMessage `json:"context,omitempty"`
}

type oauthFlowPayload struct {
	AffiliateCode   string                         `json:"affiliate_code,omitempty"`
	Verification    *service.OAuthVerificationFlow `json:"verification,omitempty"`
	Telegram        *oauth.TelegramOAuthFlow       `json:"telegram,omitempty"`
	SessionIdentity *service.AuthIdentity          `json:"session_identity,omitempty"`
	Authorization   *model.AuthFlowAuthorization   `json:"authorization,omitempty"`
	// RedirectURI is set by the external frontend (BFF) flow: the callback
	// redirects back there instead of answering JSON.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// providerParams returns map with Provider key for i18n templates
func providerParams(name string) map[string]any {
	return map[string]any{"Provider": name}
}

// GenerateOAuthCode is the built-in frontend's entry: POST with a JSON body.
func GenerateOAuthCode(c *gin.Context) {
	var request oauthStateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	state, expiresAt, flowPayload, ok := generateOAuthState(c, request, "")
	if !ok {
		return
	}
	data := gin.H{"flow_token": state, "expires_at": expiresAt.Unix()}
	if flowPayload.Telegram != nil {
		data["authorization_url"] = flowPayload.Telegram.AuthorizationURL(state)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

// GenerateOAuthCodeQuery is the external frontend's entry (GET, query params).
// It answers the bare state string in data, which is what the BFF unwraps, and
// carries redirect_uri so the callback can send the browser back cross-domain.
func GenerateOAuthCodeQuery(c fuego.ContextWithParams[dto.GenerateOAuthCodeParams]) (*dto.Response[string], error) {
	ginCtx := dto.GinCtx(c)
	p, _ := dto.ParseParams[dto.GenerateOAuthCodeParams](c)
	intent := strings.TrimSpace(p.Intent)
	if intent == "" {
		if p.Action == "bind" {
			intent = model.AuthFlowIntentBind
		} else {
			intent = model.AuthFlowIntentLogin
		}
	}
	redirectURI := strings.TrimSpace(p.RedirectURI)
	if redirectURI != "" && !common.IsAllowedRedirectURI(redirectURI) {
		return dto.Fail[string]("The redirect URI is not in the list of allowed origins")
	}
	request := oauthStateRequest{Provider: p.Provider, Intent: intent, Aff: p.Aff}
	state, _, _, ok := generateOAuthState(ginCtx, request, redirectURI)
	if !ok {
		// The core already wrote the error response on the gin context.
		return nil, nil
	}
	return dto.Ok(state)
}

// generateOAuthState validates the request, binds the flow to the caller's
// identity where the intent needs one, and stores the flow. On failure the
// response has been written and ok is false.
func generateOAuthState(c *gin.Context, request oauthStateRequest, redirectURI string) (string, time.Time, oauthFlowPayload, bool) {
	request.Provider = strings.TrimSpace(request.Provider)
	request.Intent = strings.TrimSpace(request.Intent)
	request.Aff = strings.TrimSpace(request.Aff)
	if oauth.GetProvider(request.Provider) == nil ||
		(request.Intent != model.AuthFlowIntentLogin && request.Intent != model.AuthFlowIntentBind && request.Intent != model.AuthFlowIntentVerify) ||
		len(request.Aff) > 32 ||
		(request.Intent != model.AuthFlowIntentLogin && request.Aff != "") ||
		(request.Intent != model.AuthFlowIntentVerify && (request.Scope != "" || len(request.Context) != 0)) {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return "", time.Time{}, oauthFlowPayload{}, false
	}

	userID := 0
	sessionID := ""
	flowPayload := oauthFlowPayload{AffiliateCode: request.Aff, RedirectURI: redirectURI}
	bindingStarted := false
	if request.Provider == "telegram" {
		telegramFlow, err := oauth.NewTelegramOAuthFlow()
		if err != nil {
			writeSecurityOperationError(c, err)
			return "", time.Time{}, oauthFlowPayload{}, false
		}
		flowPayload.Telegram = telegramFlow
	}
	if request.Intent == model.AuthFlowIntentBind && redirectURI != "" {
		// External frontend bind: the dashboard session cookie does not survive
		// the cross-domain redirect, so the caller proves identity with its
		// Authorization header instead. The identity MUST come from a verified
		// credential: this route is public (only CORS + rate limiting), so
		// trusting a plain New-Api-User header here let anyone mint a bind state
		// for an arbitrary account and attach their own OAuth identity to it.
		//
		// A session token in that header resolves; a PAT does not. Binding adds a
		// login method and OAuth login checks no second factor, so accepting a PAT
		// here would let a bearer secret attach an identity it can then log in as.
		// The flow carries no session id and no proof; the callback recognises
		// that shape and binds to the user recorded here.
		user, err := middleware.ResolveDashboardSessionCredential(c)
		if err != nil || user == nil || user.Status != common.UserStatusEnabled {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required for bind"})
			return "", time.Time{}, oauthFlowPayload{}, false
		}
		userID = user.Id
		defer func() {
			recordUserSecurityAudit(c, userID, "user.binding_start", map[string]any{"provider": request.Provider, "success": bindingStarted, "external": true})
		}()
	} else if request.Intent == model.AuthFlowIntentBind || request.Intent == model.AuthFlowIntentVerify {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required for bind"})
			return "", time.Time{}, oauthFlowPayload{}, false
		}
		userID = identity.UserID
		sessionID = identity.SessionID
		if request.Intent == model.AuthFlowIntentBind {
			defer func() {
				recordUserSecurityAudit(c, userID, "user.binding_start", map[string]any{"provider": request.Provider, "success": bindingStarted})
			}()
			context, err := common.Marshal(service.AccountBindingContext{Provider: request.Provider})
			if err != nil {
				writeSecurityOperationError(c, err)
				return "", time.Time{}, oauthFlowPayload{}, false
			}
			flowPayload.Authorization = middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: context})
			if flowPayload.Authorization == nil {
				return "", time.Time{}, oauthFlowPayload{}, false
			}
			flowPayload.SessionIdentity = &identity
		}
		if flowPayload.Telegram != nil {
			if _, _, err := service.ValidateLoginSession(identity); err != nil {
				writeSecurityOperationError(c, err)
				return "", time.Time{}, oauthFlowPayload{}, false
			}
			flowPayload.SessionIdentity = &identity
		}
		if request.Intent == model.AuthFlowIntentVerify {
			verification, err := service.StartOAuthVerification(identity, service.VerificationOperation{Scope: request.Scope, Context: request.Context}, request.Provider)
			if err != nil {
				writeSecurityOperationError(c, err)
				return "", time.Time{}, oauthFlowPayload{}, false
			}
			flowPayload.Verification = verification
		}
	}
	payload, err := common.Marshal(flowPayload)
	if err != nil {
		writeSecurityOperationError(c, err)
		return "", time.Time{}, oauthFlowPayload{}, false
	}
	expiresAt := time.Now().Add(oauthAuthFlowTTL)
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  request.Provider,
		Intent:    request.Intent,
		UserId:    userID,
		SessionId: sessionID,
		Payload:   string(payload),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		writeSecurityOperationError(c, err)
		return "", time.Time{}, oauthFlowPayload{}, false
	}
	bindingStarted = request.Intent == model.AuthFlowIntentBind
	return state, expiresAt, flowPayload, true
}

func HandleOAuth(c *gin.Context) {
	providerName := c.Param("provider")
	provider := oauth.GetProvider(providerName)
	if provider == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthUnknownProvider),
		})
		return
	}

	// 1. Validate state (CSRF protection)
	state := c.Query("state")
	pendingFlow, err := model.GetAuthFlow(state, model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
	})
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
		})
		return
	}

	// Extract the external-frontend redirect target stored when the flow was created.
	var pendingPayload oauthFlowPayload
	if err := common.UnmarshalJsonStr(pendingFlow.Payload, &pendingPayload); err != nil {
		common.ApiError(c, err)
		return
	}
	redirectURI := pendingPayload.RedirectURI

	consumeMatch := model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
		Intent:   pendingFlow.Intent,
	}
	bindSucceeded, notificationFailed := false, false
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		defer func() {
			recordUserSecurityAudit(c, pendingFlow.UserId, "user.binding_bind", map[string]any{"provider": providerName, "success": bindSucceeded, "notification_failed": notificationFailed, "external": redirectURI != ""})
		}()
	}
	// External-frontend binds (redirect_uri present, no session id) are bound to
	// the header-resolved user recorded at state creation: the browser reaching
	// this callback carries no dashboard session for this origin.
	externalBind := pendingFlow.Intent == model.AuthFlowIntentBind && redirectURI != "" && pendingFlow.SessionId == ""
	if externalBind {
		consumeMatch.UserId = pendingFlow.UserId
	} else if pendingFlow.Intent == model.AuthFlowIntentBind || pendingFlow.Intent == model.AuthFlowIntentVerify {
		// Bind and verification callbacks must use the dashboard session that started them.
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || identity.UserID != pendingFlow.UserId || identity.SessionID != pendingFlow.SessionId {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
			})
			return
		}
		consumeMatch.UserId = identity.UserID
		consumeMatch.SessionId = identity.SessionID
		if pendingFlow.Intent == model.AuthFlowIntentBind {
			context, err := common.Marshal(service.AccountBindingContext{Provider: providerName})
			if err != nil {
				writeSecurityOperationError(c, err)
				return
			}
			if err := service.ValidateFlowAuthorization(identity, service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: context}, pendingPayload.Authorization); err != nil {
				writeSecurityOperationError(c, err)
				return
			}
		}
	} else if pendingFlow.Intent != model.AuthFlowIntentLogin {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}

	// 3. Check if provider is enabled
	var telegramPayload oauthFlowPayload
	if providerName == "telegram" {
		if err := oauth.TelegramConfigurationError(); err != nil {
			writeSecurityOperationError(c, err)
			return
		}
		if err := common.UnmarshalJsonStr(pendingFlow.Payload, &telegramPayload); err != nil || telegramPayload.Telegram == nil {
			writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
			return
		}
		if pendingFlow.Intent != model.AuthFlowIntentLogin {
			identity, _ := middleware.GetSessionAuthIdentity(c)
			if telegramPayload.SessionIdentity == nil || *telegramPayload.SessionIdentity != identity {
				writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
				return
			}
			if _, _, err := service.ValidateLoginSession(identity); err != nil {
				writeSecurityOperationError(c, err)
				return
			}
		}
		c.Set(oauth.TelegramOAuthFlowContextKey, telegramPayload.Telegram)
	}
	if !provider.IsEnabled() {
		common.ApiErrorI18n(c, i18n.MsgOAuthNotEnabled, providerParams(provider.GetName()))
		return
	}

	// 4. Handle error from provider
	errorCode := c.Query("error")
	if errorCode != "" {
		if _, err := model.ConsumeAuthFlow(state, consumeMatch); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
			return
		}
		errorDescription := c.Query("error_description")
		if errorDescription == "" {
			errorDescription = errorCode
		}
		if setupOAuthErrorRedirect(c, redirectURI, errorDescription) {
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": errorDescription,
		})
		return
	}
	// 5. Exchange code for token
	code := c.Query("code")
	token, err := provider.ExchangeToken(c.Request.Context(), code, c)
	if err != nil {
		if providerName == "telegram" {
			writeSecurityOperationError(c, err)
			return
		}
		handleOAuthError(c, err)
		return
	}

	// 6. Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		if providerName == "telegram" {
			writeSecurityOperationError(c, err)
			return
		}
		handleOAuthError(c, err)
		return
	}
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		bindSucceeded, notificationFailed = handleOAuthBind(c, providerName, provider, oauthUser, pendingFlow, state, consumeMatch, redirectURI)
		return
	}
	flow, err := model.ConsumeAuthFlow(state, consumeMatch)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
		return
	}

	switch flow.Intent {
	case model.AuthFlowIntentLogin:
		handleOAuthLogin(c, provider, oauthUser, flow, redirectURI)
	case model.AuthFlowIntentVerify:
		handleOAuthVerification(c, providerName, oauthUser, flow)
	}
}

func handleOAuthVerification(c *gin.Context, provider string, oauthUser *oauth.OAuthUser, flow *model.AuthFlow) {
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	identity, _ := middleware.GetSessionAuthIdentity(c)
	proof, err := service.FinishOAuthVerification(identity, provider, oauthUser.ProviderUserID, payload.Verification)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "user.security_verify", map[string]any{"method": proof.Method, "scope": proof.Scope, "provider": provider})
	common.ApiSuccess(c, proof)
}

func handleOAuthLogin(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, flow *model.AuthFlow, redirectURI string) {
	// 7. Find or create user
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	user, err := findOrCreateOAuthUser(c, provider, oauthUser, payload.AffiliateCode)
	if err != nil {
		if errors.Is(err, model.ErrEmailAlreadyTaken) {
			common.ApiErrorI18n(c, i18n.MsgUserEmailAlreadyTaken)
			return
		}
		switch err.(type) {
		case *types.OAuthUserDeletedError:
			common.ApiErrorI18n(c, i18n.MsgOAuthUserDeleted)
		case *types.OAuthRegistrationDisabledError:
			common.ApiErrorI18n(c, i18n.MsgUserRegisterDisabled)
		case *OAuthEmailAlreadyTakenError:
			common.ApiErrorI18n(c, i18n.MsgUserEmailAlreadyTaken)
		default:
			writeSecurityOperationError(c, err)
		}
		return
	}

	// 9. Check user status
	if user.Status != common.UserStatusEnabled {
		common.ApiErrorI18n(c, i18n.MsgOAuthUserBanned)
		return
	}

	// 10. External redirect or same-origin login
	if redirectURI != "" {
		setupLoginAndRedirect(user, c, redirectURI)
		return
	}
	setupLogin(user, c)
}

// handleOAuthBind attaches the provider identity to the account that started
// the flow. Same-origin binds run under the dashboard session and the security
// proof recorded in the flow; external-frontend binds (redirectURI set, no
// session id) were verified by session token at state creation and answer with
// a redirect instead of JSON.
func handleOAuthBind(c *gin.Context, providerName string, provider oauth.Provider, oauthUser *oauth.OAuthUser, flow *model.AuthFlow, state string, match model.AuthFlowMatch, redirectURI string) (bool, bool) {
	external := redirectURI != "" && flow.SessionId == ""
	var identity service.AuthIdentity
	if !external {
		var ok bool
		identity, ok = middleware.GetSessionAuthIdentity(c)
		if !ok {
			writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
			return false, false
		}
	}
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return false, false
	}
	if !external {
		context, err := common.Marshal(service.AccountBindingContext{Provider: providerName})
		if err != nil {
			writeSecurityOperationError(c, err)
			return false, false
		}
		// Recheck after the external provider round trip, then validate the session
		// under the transaction's locks before consuming the flow and writing.
		if err := service.ValidateFlowAuthorization(identity, service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: context}, payload.Authorization); err != nil {
			writeSecurityOperationError(c, err)
			return false, false
		}
	}
	// Check if this OAuth account is already bound (check both new ID and legacy ID)
	taken := provider.IsUserIDTaken(oauthUser.ProviderUserID)
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" && provider.IsUserIDTaken(legacyID) {
		taken = true
	}
	if taken {
		alreadyBound := common.TranslateMessage(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
		if setupOAuthErrorRedirect(c, redirectURI, alreadyBound) {
			return false, false
		}
		common.ApiErrorI18n(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
		return false, false
	}
	userId := flow.UserId
	_, err := model.ConsumeAuthFlowWithAction(state, match, func(tx *gorm.DB, _ *model.AuthFlow) error {
		if external {
			if custom, ok := provider.(*oauth.GenericOAuthProvider); ok {
				return model.UpdateUserOAuthBinding(userId, custom.GetProviderId(), oauthUser.ProviderUserID)
			}
			return model.UpdateUserBindColumn(userId, provider.ProviderUserIDColumn(), oauthUser.ProviderUserID)
		}
		if providerName == "telegram" {
			return model.BindTelegramForSessionWithTx(tx, identity, oauthUser.ProviderUserID)
		}
		if custom, ok := provider.(*oauth.GenericOAuthProvider); ok {
			return model.UpdateUserOAuthBindingForSessionWithTx(tx, identity, custom.GetProviderId(), oauthUser.ProviderUserID)
		}
		return model.UpdateUserBindColumnForSessionWithTx(tx, identity, provider.ProviderUserIDColumn(), oauthUser.ProviderUserID)
	})
	if err != nil {
		if external {
			if setupOAuthErrorRedirect(c, redirectURI, err.Error()) {
				return false, false
			}
		}
		writeSecurityOperationError(c, err)
		return false, false
	}
	user, err := model.GetUserById(userId, false)
	if err != nil {
		writeSecurityOperationError(c, err)
		return true, true
	}
	notificationFailed := service.NotifyAccountSecurityChange(user.Email, "Login account linked: "+provider.GetName()) != nil

	// Cross-domain bind: redirect back with exchange code
	if redirectURI != "" {
		setupBindAndRedirect(user, c, redirectURI)
		return true, notificationFailed
	}
	common.ApiSuccessI18n(c, i18n.MsgOAuthBindSuccess, gin.H{"action": "bind", "notification_warning": notificationFailed})
	return true, notificationFailed
}

// backfillOAuthEmail adopts the provider's email for an account that has none.
// The address is only captured at signup, so an account created before that (or
// by a provider that withheld it) can never recover a password. An existing
// address is never overwritten, and a collision is skipped rather than failing
// the login, since the user came here to sign in, not to bind an email.
func backfillOAuthEmail(user *model.User, oauthUser *oauth.OAuthUser) {
	if user.Id == 0 || user.Email != "" || oauthUser.Email == "" {
		return
	}
	email := model.NormalizeEmail(oauthUser.Email)
	if email == "" || model.EnsureEmailAvailable(email, user.Id) != nil {
		return
	}
	if err := user.UpdateEmail(email); err != nil {
		common.SysError(fmt.Sprintf("[OAuth] failed to backfill email for user %d: %s", user.Id, err.Error()))
		return
	}
	user.Email = email
}

// findOrCreateOAuthUser finds existing user or creates new user
func findOrCreateOAuthUser(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, affiliateCode string) (*model.User, error) {
	user := &model.User{}
	if provider.ProviderUserIDColumn() == "telegram_id" {
		err := provider.FillUserByProviderID(user, oauthUser.ProviderUserID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, oauth.ErrTelegramAccountNotBound
		}
		return user, err
	}

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
		backfillOAuthEmail(user, oauthUser)
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
				common.SysLog(fmt.Sprintf("[OAuth] Migrating user %d from legacy_id=%s to new_id=%s",
					user.Id, legacyID, oauthUser.ProviderUserID))
				if err := user.UpdateGitHubId(oauthUser.ProviderUserID); err != nil {
					common.SysError(fmt.Sprintf("[OAuth] Failed to migrate user %d: %s", user.Id, err.Error()))
					// Continue with login even if migration fails
				}
				backfillOAuthEmail(user, oauthUser)
				return user, nil
			}
		}
	}

	// User doesn't exist, create new user if registration is enabled
	if !common.RegisterEnabled {
		return nil, &types.OAuthRegistrationDisabledError{}
	}

	registerIp := publicClientIp(c)
	limited, err := registerIpLimited(registerIp)
	if err != nil {
		return nil, err
	}
	if limited {
		return nil, &oauth.AccessDeniedError{Message: "An account has already been registered from this IP address"}
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
		user.Email = model.NormalizeEmail(oauthUser.Email)
		if err := model.EnsureEmailAvailable(user.Email, 0); err != nil {
			if errors.Is(err, model.ErrEmailAlreadyTaken) {
				return nil, &OAuthEmailAlreadyTakenError{}
			}
			return nil, err
		}
	}
	user.Role = common.RoleCommonUser
	user.Status = common.UserStatusEnabled
	user.RegisterIp = registerIp

	// Handle affiliate code
	inviterId := 0
	if affiliateCode != "" {
		inviterId, _ = model.GetUserIdByAffCode(affiliateCode)
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
			if err := tx.Model(user).Updates(map[string]any{
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

// OAuthEmailAlreadyTakenError is returned when an OAuth signup email collides
// with an existing account. The other OAuth error types live in the types package.
type OAuthEmailAlreadyTakenError struct{}

func (e *OAuthEmailAlreadyTakenError) Error() string {
	return "email is already in use"
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
		common.ApiErrorI18n(c, i18n.MsgOAuthTrustLevelLow)
	default:
		writeSecurityOperationError(c, err)
	}
}
