package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"gorm.io/gorm"
)

const oauthAuthFlowTTL = 10 * time.Minute

type oauthStateRequest struct {
	Provider string `json:"provider"`
	Intent   string `json:"intent"`
	Aff      string `json:"aff,omitempty"`
	// Only for a login that may create the account; ignored for existing users.
	CustomerType string `json:"customer_type,omitempty"`
	// The terms notice the signup page showed, recorded on an account this login creates.
	Terms   model.TermsShown `json:"terms"`
	Scope   string           `json:"scope,omitempty"`
	Context json.RawMessage  `json:"context,omitempty"`
}

type oauthFlowPayload struct {
	AffiliateCode   string                         `json:"affiliate_code,omitempty"`
	CustomerType    string                         `json:"customer_type,omitempty"`
	Terms           model.TermsShown               `json:"terms"`
	Verification    *service.OAuthVerificationFlow `json:"verification,omitempty"`
	Telegram        *oauth.TelegramOAuthFlow       `json:"telegram,omitempty"`
	SessionIdentity *service.AuthIdentity          `json:"session_identity,omitempty"`
	Authorization   *model.AuthFlowAuthorization   `json:"authorization,omitempty"`
	// RedirectURI is set by the external frontend (BFF) flow: the callback
	// redirects back there instead of answering JSON.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// providerParams returns the params of messages that name the OAuth provider.
func providerParams(name string) map[string]any {
	return map[string]any{"provider": name}
}

// GenerateOAuthCode is the built-in frontend's entry: POST with a JSON body.
func GenerateOAuthCode(c *gin.Context) {
	var request oauthStateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
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
	request := oauthStateRequest{Provider: p.Provider, Intent: intent, Aff: p.Aff, CustomerType: p.CustomerType, Scope: strings.TrimSpace(p.Scope)}
	request.Terms = model.TermsShown{Version: strings.TrimSpace(p.TermsVersion), Locale: strings.TrimSpace(p.TermsLocale), Text: strings.TrimSpace(p.TermsText)}
	if p.Context != "" {
		request.Context = json.RawMessage(p.Context)
	}
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
	request.CustomerType = strings.TrimSpace(request.CustomerType)
	if oauth.GetProvider(request.Provider) == nil ||
		(request.Intent != model.AuthFlowIntentLogin && request.Intent != model.AuthFlowIntentBind && request.Intent != model.AuthFlowIntentVerify) ||
		len(request.Aff) > 32 ||
		(request.Intent != model.AuthFlowIntentLogin && request.Aff != "") ||
		(request.CustomerType != "" && (request.Intent != model.AuthFlowIntentLogin || !model.IsValidCustomerType(request.CustomerType))) ||
		(request.Terms != model.TermsShown{} && (request.Intent != model.AuthFlowIntentLogin || !request.Terms.Valid())) ||
		(request.Intent != model.AuthFlowIntentVerify && (request.Scope != "" || len(request.Context) != 0)) {
		common.ApiError(c, common.NewMessage("Invalid parameters"))
		return "", time.Time{}, oauthFlowPayload{}, false
	}

	userID := 0
	sessionID := ""
	flowPayload := oauthFlowPayload{AffiliateCode: request.Aff, CustomerType: request.CustomerType, Terms: request.Terms, RedirectURI: redirectURI}
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
			writeAccountStatusError(c, http.StatusUnauthorized, "Sign in to bind an account")
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
			if redirectURI != "" {
				flowPayload.SessionIdentity = &identity
			}
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
		writeAccountStatusError(c, http.StatusBadRequest, "Unknown OAuth provider")
		return
	}

	// 1. Validate state (CSRF protection)
	state := c.Query("state")
	pendingFlow, err := model.GetAuthFlow(state, model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
	})
	if err != nil {
		msg := "State parameter is empty or mismatched"
		// An expired or replayed state has no redirect target of its own; showing this
		// host's sign-in instead stranded users on the API host after they retried there.
		if setupOAuthErrorRedirect(c, setting.OAuthExternalCallbackUrl, msg) {
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": msg})
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
	bindSucceeded := false
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		defer func() {
			recordUserSecurityAudit(c, pendingFlow.UserId, "user.binding_bind", map[string]any{"provider": providerName, "success": bindSucceeded, "external": redirectURI != ""})
		}()
	}
	// External-frontend binds (redirect_uri present, no session id) are bound to
	// the header-resolved user recorded at state creation: the browser reaching
	// this callback carries no dashboard session for this origin.
	externalBind := pendingFlow.Intent == model.AuthFlowIntentBind && redirectURI != "" && pendingFlow.SessionId == ""
	// An external-frontend verification reaches this callback without the session
	// that started it, so it is checked against the session recorded in the flow.
	externalVerify := pendingFlow.Intent == model.AuthFlowIntentVerify && redirectURI != ""
	if externalBind {
		// The bind completes here, before the frontend sees anything, so this is the
		// only place to tie it to the browser that started it: an authorize URL built
		// for the attacker's bind state, opened by a victim, bound the victim's identity
		// to the attacker's account.
		if flowCookie, err := c.Cookie("oauth_bind_flow"); err != nil || flowCookie != state {
			if setupOAuthErrorRedirect(c, redirectURI, "This link was not started from your browser. Start the binding again from your account settings.") {
				return
			}
			writeAccountStatusError(c, http.StatusForbidden, "State parameter is empty or mismatched")
			return
		}
		consumeMatch.UserId = pendingFlow.UserId
	} else if externalVerify {
		identity := pendingPayload.SessionIdentity
		if identity == nil || identity.UserID != pendingFlow.UserId || identity.SessionID != pendingFlow.SessionId {
			writeAccountStatusError(c, http.StatusForbidden, "State parameter is empty or mismatched")
			return
		}
		consumeMatch.UserId = identity.UserID
		consumeMatch.SessionId = identity.SessionID
	} else if pendingFlow.Intent == model.AuthFlowIntentBind || pendingFlow.Intent == model.AuthFlowIntentVerify {
		// Bind and verification callbacks must use the dashboard session that started them.
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || identity.UserID != pendingFlow.UserId || identity.SessionID != pendingFlow.SessionId {
			writeAccountStatusError(c, http.StatusForbidden, "State parameter is empty or mismatched")
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
		common.ApiErrorT(c, "Invalid parameters")
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
		common.ApiErrorT(c, "{{provider}} login and registration has not been enabled by administrator", providerParams(provider.GetName()))
		return
	}

	// 4. Handle error from provider
	errorCode := c.Query("error")
	if errorCode != "" {
		if _, err := model.ConsumeAuthFlow(state, consumeMatch); err != nil {
			writeAccountStatusError(c, http.StatusForbidden, "State parameter is empty or mismatched")
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
		bindSucceeded = handleOAuthBind(c, providerName, provider, oauthUser, pendingFlow, state, consumeMatch, redirectURI)
		return
	}
	flow, err := model.ConsumeAuthFlow(state, consumeMatch)
	if err != nil {
		writeAccountStatusError(c, http.StatusForbidden, "State parameter is empty or mismatched")
		return
	}

	switch flow.Intent {
	case model.AuthFlowIntentLogin:
		handleOAuthLogin(c, provider, oauthUser, token, flow, redirectURI)
	case model.AuthFlowIntentVerify:
		handleOAuthVerification(c, providerName, oauthUser, flow, redirectURI)
	}
}

func handleOAuthVerification(c *gin.Context, provider string, oauthUser *oauth.OAuthUser, flow *model.AuthFlow, redirectURI string) {
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	identity, _ := middleware.GetSessionAuthIdentity(c)
	if redirectURI != "" && payload.SessionIdentity != nil {
		identity = *payload.SessionIdentity
	}
	proof, err := service.FinishOAuthVerification(identity, provider, oauthUser.ProviderUserID, payload.Verification)
	if err != nil {
		if redirectURI != "" {
			_ = c.Error(err)
			message := service.ErrVerificationFailed.Error()
			if errors.Is(err, service.ErrOAuthAccountMismatch) {
				message = service.ErrOAuthAccountMismatch.Error()
			}
			if setupOAuthErrorRedirect(c, redirectURI, message) {
				return
			}
		}
		writeSecurityOperationError(c, err)
		return
	}
	recordUserSecurityAudit(c, identity.UserID, "user.security_verify", map[string]any{"method": proof.Method, "scope": proof.Scope, "provider": provider})
	if redirectURI != "" {
		setupVerifyAndRedirect(identity.UserID, proof, c, redirectURI)
		return
	}
	common.ApiSuccess(c, proof)
}

func handleOAuthLogin(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, token *oauth.OAuthToken, flow *model.AuthFlow, redirectURI string) {
	// 7. Find or create user
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	user, migration, err := findOrCreateOAuthUser(c, provider, oauthUser, token, payload.AffiliateCode, payload.CustomerType, payload.Terms)
	if err != nil {
		var msg string
		switch err.(type) {
		case *types.OAuthUserDeletedError:
			msg = "User has been deleted"
		case *types.OAuthRegistrationDisabledError:
			msg = "New user registration has been disabled by administrator"
		case *OAuthEmailAlreadyTakenError:
			msg = "Email address is already in use"
		case *OAuthLegacyBindingNotConfirmedError:
			msg = common.NewMessage("This {{provider}} account cannot be linked to an existing account automatically, please sign in or register another way and then link {{provider}} in account settings", providerParams(provider.GetName())).Error()
		}
		if errors.Is(err, model.ErrEmailAlreadyTaken) {
			msg = "Email address is already in use"
		}
		if msg == "" {
			writeSecurityOperationError(c, err)
			return
		}
		// Send the user back to the frontend that started the login, not the API host's own sign-in page.
		if setupOAuthErrorRedirect(c, redirectURI, msg) {
			return
		}
		common.ApiErrorMsg(c, msg)
		return
	}

	// 9. Check user status
	if user.Status != common.UserStatusEnabled {
		msg := "User has been banned"
		if setupOAuthErrorRedirect(c, redirectURI, msg) {
			return
		}
		common.ApiErrorMsg(c, msg)
		return
	}

	// 10. External redirect or same-origin login
	if redirectURI != "" {
		setupLoginAndRedirect(user, migration, c, redirectURI)
		return
	}
	setupLogin(user, migration, c)
}

// handleOAuthBind attaches the provider identity to the account that started
// the flow. Same-origin binds run under the dashboard session and the security
// proof recorded in the flow; external-frontend binds (redirectURI set, no
// session id) were verified by session token at state creation and answer with
// a redirect instead of JSON.
func handleOAuthBind(c *gin.Context, providerName string, provider oauth.Provider, oauthUser *oauth.OAuthUser, flow *model.AuthFlow, state string, match model.AuthFlowMatch, redirectURI string) bool {
	external := redirectURI != "" && flow.SessionId == ""
	var identity service.AuthIdentity
	if !external {
		var ok bool
		identity, ok = middleware.GetSessionAuthIdentity(c)
		if !ok {
			writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
			return false
		}
	}
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		writeSecurityOperationError(c, model.ErrAuthFlowInvalid)
		return false
	}
	if !external {
		context, err := common.Marshal(service.AccountBindingContext{Provider: providerName})
		if err != nil {
			writeSecurityOperationError(c, err)
			return false
		}
		// Recheck after the external provider round trip, then validate the session
		// under the transaction's locks before consuming the flow and writing.
		if err := service.ValidateFlowAuthorization(identity, service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: context}, payload.Authorization); err != nil {
			writeSecurityOperationError(c, err)
			return false
		}
	}
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		alreadyBound := common.NewMessage("This {{provider}} account has already been bound", providerParams(provider.GetName())).Error()
		if setupOAuthErrorRedirect(c, redirectURI, alreadyBound) {
			return false
		}
		common.ApiError(c, common.NewMessage("This {{provider}} account has already been bound", providerParams(provider.GetName())))
		return false
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
				return false
			}
		}
		writeSecurityOperationError(c, err)
		return false
	}
	// A login that does not vouch for a person (see model.identityVouched) leaves the ban in place.
	if provider.ProviderUserIDColumn() == "github_id" {
		tooNew := gitHubAccountTooNew(oauthUser)
		if setting, settingErr := model.GetUserSetting(userId, true); settingErr == nil && setting.UnvouchedGitHub != tooNew {
			setting.UnvouchedGitHub = tooNew
			if err := model.UpdateUserSetting(userId, setting); err != nil {
				common.SysError(common.LogText("[OAuth] failed to record GitHub account age for user %d: %s", userId, err.Error()))
			}
		}
		if !tooNew {
			model.LiftShadowBan(userId, "linked "+provider.GetName())
		}
	} else if provider.ProviderUserIDColumn() != "google_id" || common.MainstreamEmailDomain(oauthUser.Email) {
		model.LiftShadowBan(userId, "linked "+provider.GetName())
	}
	user, err := model.GetUserById(userId, false)
	if err != nil {
		writeSecurityOperationError(c, err)
		return true
	}
	backfillOAuthEmail(user, oauthUser)

	// Cross-domain bind: redirect back with exchange code
	if redirectURI != "" {
		setupBindAndRedirect(user, c, redirectURI)
		return true
	}
	common.ApiSuccessT(c, "Binding successful", gin.H{"action": "bind"})
	return true
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

// gitHubMinAccountAge is how old a GitHub account must be to count as identity.
const gitHubMinAccountAge = 30 * 24 * time.Hour

// gitHubAccountTooNew reads the account creation date only GitHub reports.
func gitHubAccountTooNew(oauthUser *oauth.OAuthUser) bool {
	created, ok := oauthUser.Extra["account_created_at"].(int64)
	return ok && created > 0 && time.Since(time.Unix(created, 0)) < gitHubMinAccountAge
}

// findOrCreateOAuthUser finds the existing user or creates a new one. For a
// legacy GitHub binding that still waits for the login verification, it also
// returns the rewrite to carry into the challenge.
func findOrCreateOAuthUser(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, token *oauth.OAuthToken, affiliateCode string, customerType string, terms model.TermsShown) (*model.User, *service.LegacyGitHubMigration, error) {
	user := &model.User{}
	if provider.ProviderUserIDColumn() == "telegram_id" {
		err := provider.FillUserByProviderID(user, oauthUser.ProviderUserID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, oauth.ErrTelegramAccountNotBound
		}
		return user, nil, err
	}

	// Check if user already exists with new ID
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		err := provider.FillUserByProviderID(user, oauthUser.ProviderUserID)
		if err != nil {
			return nil, nil, err
		}
		// Check if user has been deleted
		if user.Id == 0 {
			return nil, nil, &types.OAuthUserDeletedError{}
		}
		backfillOAuthEmail(user, oauthUser)
		return user, nil, nil
	}

	// Legacy GitHub bindings stored the login name, which only points at a
	// candidate account. Values that are all digits are already numeric account
	// IDs and never take part in the comparison.
	legacyID, _ := oauthUser.Extra["legacy_id"].(string)
	if strings.ContainsFunc(legacyID, func(r rune) bool { return r < '0' || r > '9' }) && provider.IsUserIDTaken(legacyID) {
		if err := provider.FillUserByProviderID(user, legacyID); err != nil {
			return nil, nil, err
		}
		if user.Id != 0 {
			state, err := model.GetUserVerificationState(user.Id)
			if err != nil {
				return nil, nil, err
			}
			if state.HasTwoFA || state.HasPasskey {
				// The rewrite is written in the transaction that issues the session
				// once the login verification completes.
				return user, &service.LegacyGitHubMigration{GitHubID: oauthUser.ProviderUserID, LegacyID: legacyID}, nil
			}
			// Without a second factor, one of the addresses the provider has
			// confirmed must match the account email. The list is fetched only here
			// and never recorded.
			reason, matched := "no_matching_evidence", false
			if emailProvider, ok := provider.(oauth.VerifiedEmailProvider); ok && user.Email != "" {
				emails, err := emailProvider.GetVerifiedEmails(c.Request.Context(), token)
				if err != nil {
					common.SysError(common.LogText("[OAuth] Failed to load verified emails for user %d: %s", user.Id, err.Error()))
					reason = "verified_emails_unavailable"
				}
				accountEmail := model.NormalizeEmail(user.Email)
				matched = slices.ContainsFunc(emails, func(email string) bool { return model.NormalizeEmail(email) == accountEmail })
			}
			if !matched {
				recordLegacyGitHubBindingAudit(c, user, false, map[string]any{"legacy_id": legacyID, "provider_user_id": oauthUser.ProviderUserID, "reason": reason})
				return nil, nil, &OAuthLegacyBindingNotConfirmedError{}
			}
			written := false
			err = model.DB.Transaction(func(tx *gorm.DB) error {
				var err error
				written, err = model.MigrateLegacyGitHubBindingWithTx(tx, user.Id, legacyID, oauthUser.ProviderUserID)
				return err
			})
			if err != nil {
				return nil, nil, err
			}
			if written {
				user.GitHubId = oauthUser.ProviderUserID
				recordLegacyGitHubBindingAudit(c, user, true, map[string]any{
					"legacy_id": legacyID, "provider_user_id": oauthUser.ProviderUserID,
					"verified_email_matched": true,
				})
			}
			backfillOAuthEmail(user, oauthUser)
			return user, nil, nil
		}
	}

	// User doesn't exist, create new user if registration is enabled
	if !common.RegisterEnabled {
		return nil, nil, &types.OAuthRegistrationDisabledError{}
	}

	registerIp := publicClientIp(c)
	limited, err := registerIpLimited(registerIp)
	if err != nil {
		return nil, nil, err
	}
	if limited {
		return nil, nil, &oauth.AccessDeniedError{Message: "An account has already been registered from this IP address"}
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
				return nil, nil, &OAuthEmailAlreadyTakenError{}
			}
			return nil, nil, err
		}
	}
	user.Role = common.RoleCommonUser
	user.Status = common.UserStatusEnabled
	if gitHubAccountTooNew(oauthUser) {
		setting := user.GetSetting()
		setting.UnvouchedGitHub = true
		user.SetSetting(setting)
	}
	user.RegisterIp = registerIp
	user.RegisterIpHash = common.RegisterIpHash(registerIp)
	if model.IsValidCustomerType(customerType) {
		user.SetCustomerType(customerType)
	}

	// Handle affiliate code
	inviterId := 0
	if affiliateCode != "" {
		inviterId, _ = model.GetUserIdByAffCode(affiliateCode)
	}
	user.InviterId = inviterId

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
			return nil, nil, err
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
				"google_id":   user.GoogleId,
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
			return nil, nil, err
		}

		// Perform post-transaction tasks
		user.FinalizeOAuthUserCreation(inviterId)
	}
	if terms.Valid() {
		if err := model.RecordTermsAcceptance(user.Id, model.TermsSourceOAuth, terms); err != nil {
			common.SysError(common.LogText("failed to record terms acceptance for user %d: %s", user.Id, err.Error()))
		}
	}

	return user, nil, nil
}

// recordLegacyGitHubBindingAudit records the outcome of a legacy GitHub binding
// rewrite as an account binding event. No session exists yet on the login path,
// so the row carries the user's own role instead of the request context.
func recordLegacyGitHubBindingAudit(c *gin.Context, user *model.User, success bool, params map[string]any) {
	params["provider"], params["legacy_migration"], params["success"] = "github", true, success
	model.RecordOperationAuditLog(user.Id, user.Role, auditContentEN("user.binding_bind", params), c.ClientIP(), "user.binding_bind", params, nil, &model.AuditRequestInfo{
		Method: c.Request.Method, Route: c.FullPath(), Path: c.FullPath(), Status: c.Writer.Status(), Success: success,
	}, c)
}

// OAuthEmailAlreadyTakenError is returned when an OAuth signup email collides
// with an existing account. The other OAuth error types live in the types package.
type OAuthEmailAlreadyTakenError struct{}

func (e *OAuthEmailAlreadyTakenError) Error() string {
	return "email is already in use"
}

// OAuthLegacyBindingNotConfirmedError reports a legacy GitHub binding match that
// neither a second factor nor a confirmed provider email backed.
type OAuthLegacyBindingNotConfirmedError struct{}

func (e *OAuthLegacyBindingNotConfirmedError) Error() string {
	return "legacy binding was not confirmed"
}

// handleOAuthError handles OAuth errors and returns a web console message
func handleOAuthError(c *gin.Context, err error) {
	switch e := err.(type) {
	case *oauth.OAuthError:
		common.ApiError(c, e.Message)
	case *oauth.AccessDeniedError:
		common.ApiErrorMsg(c, e.Message)
	case *oauth.TrustLevelError:
		common.ApiErrorT(c, "Linux DO trust level does not meet the minimum required by administrator")
	default:
		writeSecurityOperationError(c, err)
	}
}
