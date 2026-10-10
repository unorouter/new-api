package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupScopedAccessTokenAudit extends setupAccessTokenAudit for scoped tokens.
// Its owner is an ordinary user because prod refuses every PAT on an admin account.
func setupScopedAccessTokenAudit(t *testing.T) (*model.User, string) {
	t.Helper()
	user, legacy := setupAccessTokenAudit(t)
	require.NoError(t, i18n.Init())
	require.NoError(t, model.DB.AutoMigrate(&model.UserAccessToken{}, &model.Option{}, &model.AuthFlow{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.PasskeyCredential{}, &model.UserOAuthBinding{}))
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(time.Now().Unix()))
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleCommonUser).Error)
	user.Role = common.RoleCommonUser
	return user, legacy
}

// route rules keyed by gin full path apply unchanged.
func newAccessTokenTestRouter() *gin.Engine {
	router := gin.New()
	router.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	api := router.Group("/api")
	api.GET("/verify/methods", middleware.UserAuth(), GetVerificationMethods)
	api.POST("/verify", middleware.UserAuth(), UniversalVerify)
	userRoute := api.Group("/user")
	selfRoute := userRoute.Group("/", middleware.UserAuth())
	selfRoute.GET("/self", func(c *gin.Context) { common.ApiSuccess(c, gin.H{"id": c.GetInt("id")}) })
	selfRoute.POST("/2fa/setup", Setup2FA)
	selfRoute.POST("/2fa/disable", Disable2FA)
	tokenRoute := selfRoute.Group("/access_tokens")
	tokenRoute.GET("", ListAccessTokens)
	tokenRoute.GET("/catalog", GetAccessTokenCatalog)
	tokenRoute.GET("/scopes", GetAccessTokenScopes)
	tokenRoute.POST("", CreateAccessToken)
	tokenRoute.PATCH("/:id", UpdateAccessToken)
	tokenRoute.DELETE("/:id", DeleteAccessToken)
	tokenRoute.DELETE("/legacy", RevokeLegacyAccessToken)
	adminRoute := userRoute.Group("/", middleware.AdminAuth())
	adminRoute.DELETE("/:id", fuegoNoBody(DeleteUser))
	return router
}

func accessTokenRequest(router http.Handler, method, path, credential, proof, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	if proof != "" {
		request.Header.Set("X-Security-Proof", proof)
	}
	request.RemoteAddr = "192.0.2.8:4567"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func createScopedAccessToken(t *testing.T, userID int, expiresAt int64, scopes ...string) (string, *model.UserAccessToken) {
	t.Helper()
	suffix, err := common.GenerateRandomCharsKey(43)
	require.NoError(t, err)
	raw := model.AccessTokenPrefix + suffix
	token := &model.UserAccessToken{Name: "test token", TokenHash: model.AccessTokenFingerprint(raw), TokenHint: model.AccessTokenHint(raw), ExpiresAt: expiresAt}
	require.NoError(t, token.SetScopes(scopes))
	require.NoError(t, model.CreateUserAccessToken(userID, token, service.AccessTokenMaxPerUser))
	return raw, token
}

// createAccessTokenTestSession returns a browser session identity and the
// dashboard JWT that carries it.
func createAccessTokenTestSession(t *testing.T, userID int, sid string) (service.AuthIdentity, string) {
	t.Helper()
	now := time.Now().Unix()
	require.NoError(t, model.CreateUserSession(&model.UserSession{SID: sid, UserID: userID, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password", LastActiveAt: now, ExpiresAt: now + 3600}))
	identity := service.AuthIdentity{UserID: userID, SessionID: sid, UserAuthVersion: 1, SessionVersion: 1}
	jwt, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	return identity, jwt
}

func issueAccessTokenGenerateProof(t *testing.T, identity service.AuthIdentity, expiresAt int64, scopes ...string) string {
	t.Helper()
	context, err := common.Marshal(service.AccessTokenGenerateContext{Scopes: scopes, ExpiresAt: expiresAt})
	require.NoError(t, err)
	return issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccessTokenGenerate, Context: context}, "password")
}

func issueAccessTokenRevokeProof(t *testing.T, identity service.AuthIdentity, target service.AccessTokenRevokeContext) string {
	t.Helper()
	context, err := common.Marshal(target)
	require.NoError(t, err)
	return issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccessTokenRevoke, Context: context}, "password")
}

func issueAccessTokenUpdateProof(t *testing.T, identity service.AuthIdentity, tokenID int, scopes ...string) string {
	t.Helper()
	context, err := common.Marshal(service.AccessTokenUpdateContext{TokenID: tokenID, Scopes: scopes})
	require.NoError(t, err)
	return issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeAccessTokenUpdate, Context: context}, "password")
}

// setLegacyAccessTokenRetireAt moves the stored transition deadline the way an
// operator restoring the option row would, then reloads it.
func setLegacyAccessTokenRetireAt(t *testing.T, retireAt int64) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: "LegacyAccessTokenRetireAt"}).Update("value", fmt.Sprint(retireAt)).Error)
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(time.Now().Unix()))
	require.Equal(t, retireAt, model.LegacyAccessTokenRetireAt())
}

type accessTokenListResponse struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Data    struct {
		Items []struct {
			Id       int      `json:"id"`
			Name     string   `json:"name"`
			TokenRef string   `json:"token_ref"`
			Scopes   []string `json:"scopes"`
		} `json:"items"`
		Legacy *struct {
			TokenRef string `json:"token_ref"`
			RetireAt int64  `json:"retire_at"`
		} `json:"legacy"`
	} `json:"data"`
}

func TestAccessTokenLifecycleAndLateRequests(t *testing.T) {
	user, legacy := setupScopedAccessTokenAudit(t)
	router := newAccessTokenTestRouter()
	identity, browser := createAccessTokenTestSession(t, user.Id, "lifecycle-session")
	scoped, scopedToken := createScopedAccessToken(t, user.Id, 0, "profile:read")
	tokenPath := fmt.Sprintf("/api/user/access_tokens/%d", scopedToken.Id)
	for _, credential := range []string{scoped, legacy} {
		for _, endpoint := range []struct{ method, path string }{
			{"GET", "/api/user/access_tokens"}, {"GET", "/api/user/access_tokens/catalog"}, {"GET", "/api/user/access_tokens/scopes"}, {"POST", "/api/user/access_tokens"},
			{"PATCH", tokenPath}, {"DELETE", tokenPath}, {"DELETE", "/api/user/access_tokens/legacy"},
		} {
			response := accessTokenRequest(router, endpoint.method, endpoint.path, credential, "", `{"name":"renamed","scopes":["profile:read"],"expires_at":0}`)
			assert.Equal(t, http.StatusForbidden, response.Code, endpoint.method+" "+endpoint.path)
			assert.Contains(t, response.Body.String(), `"code":"AUTH_SESSION_REQUIRED"`)
		}
	}
	tokens, err := model.ListUserAccessTokens(user.Id)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, "test token", tokens[0].Name, "an access token cannot manage access tokens")

	response := accessTokenRequest(router, "GET", "/api/user/access_tokens", browser, "", "")
	require.Equal(t, http.StatusOK, response.Code)
	for _, secret := range []string{scoped, legacy} {
		assert.NotContains(t, response.Body.String(), secret)
	}
	var listed accessTokenListResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &listed))
	require.Len(t, listed.Data.Items, 1)
	assert.Equal(t, scopedToken.TokenHash, listed.Data.Items[0].TokenRef)
	assert.Equal(t, []string{"profile:read"}, listed.Data.Items[0].Scopes)
	require.NotNil(t, listed.Data.Legacy)
	assert.Equal(t, model.AccessTokenFingerprint(legacy), listed.Data.Legacy.TokenRef)
	assert.Equal(t, model.LegacyAccessTokenRetireAt(), listed.Data.Legacy.RetireAt)

	// A token id only resolves among the caller's own tokens.
	other := &model.User{Username: "audit-other", Password: "placeholder", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "audit-other"}
	require.NoError(t, model.DB.Create(other).Error)
	otherRaw, otherToken := createScopedAccessToken(t, other.Id, 0, "profile:read")
	otherProof := issueAccessTokenRevokeProof(t, identity, service.AccessTokenRevokeContext{TokenID: otherToken.Id})
	response = accessTokenRequest(router, "DELETE", fmt.Sprintf("/api/user/access_tokens/%d", otherToken.Id), browser, otherProof, "")
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"ACCESS_TOKEN_NOT_FOUND"`)
	assert.Equal(t, http.StatusOK, accessTokenRequest(router, "GET", "/api/user/self", otherRaw, "", "").Code)

	// A revoke proof names one target and is not spent on another one.
	proof := issueAccessTokenRevokeProof(t, identity, service.AccessTokenRevokeContext{TokenID: scopedToken.Id})
	response = accessTokenRequest(router, "DELETE", "/api/user/access_tokens/legacy", browser, proof, "")
	assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_CONTEXT_MISMATCH"`)
	assert.Equal(t, http.StatusOK, accessTokenRequest(router, "GET", "/api/user/self", legacy, "", "").Code)
	response = accessTokenRequest(router, "DELETE", tokenPath, browser, proof, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, http.StatusUnauthorized, accessTokenRequest(router, "GET", "/api/user/self", scoped, "", "").Code)
	response = accessTokenRequest(router, "DELETE", tokenPath, browser, proof, "")
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"ACCESS_TOKEN_NOT_FOUND"`)

	legacyProof := issueAccessTokenRevokeProof(t, identity, service.AccessTokenRevokeContext{Legacy: true})
	response = accessTokenRequest(router, "DELETE", "/api/user/access_tokens/legacy", browser, legacyProof, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, http.StatusUnauthorized, accessTokenRequest(router, "GET", "/api/user/self", legacy, "", "").Code)
	response = accessTokenRequest(router, "GET", "/api/user/access_tokens", browser, "", "")
	listed = accessTokenListResponse{}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &listed))
	assert.Empty(t, listed.Data.Items)
	assert.Nil(t, listed.Data.Legacy)

	// The BFF still issues legacy tokens, so a passed deadline neither hides
	// nor rejects them.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("access_token", legacy).Error)
	retireAt := model.LegacyAccessTokenRetireAt()
	setLegacyAccessTokenRetireAt(t, time.Now().Unix()-1)
	t.Cleanup(func() { setLegacyAccessTokenRetireAt(t, retireAt) })
	response = accessTokenRequest(router, "GET", "/api/user/access_tokens", browser, "", "")
	listed = accessTokenListResponse{}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &listed))
	assert.NotNil(t, listed.Data.Legacy)
	assert.Equal(t, http.StatusOK, accessTokenRequest(router, "GET", "/api/user/self", legacy, "", "").Code)

	var revoked []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "access_token.revoke").Order("id").Find(&revoked).Error)
	require.Len(t, revoked, 2)
	for i, want := range []string{
		fmt.Sprintf(`"token_ref":"%s"`, scopedToken.TokenHash),
		fmt.Sprintf(`"legacy":true,"token_ref":"%s"`, model.AccessTokenFingerprint(legacy)),
	} {
		require.NotNil(t, revoked[i].Other.Op)
		params, err := common.Marshal(revoked[i].Other.Op.Params)
		require.NoError(t, err)
		assert.Contains(t, string(params), want)
	}
	var history []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&history).Error)
	encoded, err := common.Marshal(history)
	require.NoError(t, err)
	for _, secret := range []string{scoped, legacy, browser, proof, legacyProof, otherRaw, otherProof, "Authorization"} {
		assert.NotContains(t, string(encoded), secret)
	}
}

// The scope dictionary labels stored grants, so it must not shrink to what the
// viewer can grant today.
func TestAccessTokenScopeDictionaryIgnoresTheViewersGrants(t *testing.T) {
	user, _ := setupScopedAccessTokenAudit(t)
	router := newAccessTokenTestRouter()
	_, browser := createAccessTokenTestSession(t, user.Id, "dictionary-session")
	response := accessTokenRequest(router, "GET", "/api/user/access_tokens/scopes", browser, "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var dictionary struct {
		Data struct {
			Resources []struct {
				Resource string `json:"resource"`
				LabelKey string `json:"label_key"`
				Actions  []struct {
					Action   string `json:"action"`
					LabelKey string `json:"label_key"`
				} `json:"actions"`
			} `json:"resources"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &dictionary))
	labels := map[string]string{}
	for _, resource := range dictionary.Data.Resources {
		for _, action := range resource.Actions {
			labels[resource.Resource+":"+action.Action] = resource.LabelKey + " / " + action.LabelKey
		}
	}
	assert.Equal(t, "Profile / View", labels["profile:read"])
	assert.Equal(t, "API keys / Reveal full keys", labels["api_key:reveal"])
	assert.Equal(t, "Users / Edit", labels["user:write"])
	assert.Equal(t, "System settings / Edit", labels["option:write"])
	for _, permission := range authz.AllPermissions() {
		assert.Contains(t, labels, service.AccessTokenScopeOf(permission))
	}

	response = accessTokenRequest(router, "GET", "/api/user/access_tokens/catalog", browser, "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), `"resource":"user"`, "the grant catalog stays filtered")
}

func TestAccessTokenCreationChecksGrantBeforeConsumingProof(t *testing.T) {
	user, legacy := setupScopedAccessTokenAudit(t)
	router := newAccessTokenTestRouter()
	identity, browser := createAccessTokenTestSession(t, user.Id, "create-session")
	response := accessTokenRequest(router, "POST", "/api/user/access_tokens", browser, "", `{"name":"ci","scopes":["profile:read"],"expires_at":0}`)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_REQUIRED"`)
	expiresAt := time.Now().Unix() + 7*24*60*60
	for _, test := range []struct {
		name, body, code string
		role, status     int
		proofScopes      []string
		proofExpiry      int64
	}{
		{"common user cannot grant user management", `{"name":"ci","scopes":["user:write"],"expires_at":0}`, "ACCESS_TOKEN_SCOPE_FORBIDDEN", common.RoleCommonUser, http.StatusBadRequest, []string{"user:write"}, 0},
		{"administrator is refused a personal access token", `{"name":"ci","scopes":["profile:read"],"expires_at":0}`, "PAT_NOT_ALLOWED", common.RoleAdminUser, http.StatusForbidden, []string{"profile:read"}, 0},
		{"unknown scope", `{"name":"ci","scopes":["profile:admin"],"expires_at":0}`, "ACCESS_TOKEN_SCOPE_INVALID", common.RoleCommonUser, http.StatusBadRequest, []string{"profile:admin"}, 0},
		{"empty grant", `{"name":"ci","scopes":[],"expires_at":0}`, "ACCESS_TOKEN_SCOPE_INVALID", common.RoleCommonUser, http.StatusBadRequest, []string{"profile:read"}, 0},
		{"expiry under one hour", fmt.Sprintf(`{"name":"ci","scopes":["profile:read"],"expires_at":%d}`, time.Now().Unix()+60), "ACCESS_TOKEN_EXPIRY_INVALID", common.RoleCommonUser, http.StatusBadRequest, []string{"profile:read"}, time.Now().Unix() + 60},
		{"blank name", `{"name":"  ","scopes":["profile:read"],"expires_at":0}`, "ACCESS_TOKEN_NAME_INVALID", common.RoleCommonUser, http.StatusBadRequest, []string{"profile:read"}, 0},
		{"proof for other scopes", `{"name":"ci","scopes":["profile:read","usage:read"],"expires_at":0}`, "SECURITY_PROOF_CONTEXT_MISMATCH", common.RoleCommonUser, http.StatusForbidden, []string{"profile:read"}, 0},
		{"proof for other expiry", fmt.Sprintf(`{"name":"ci","scopes":["profile:read"],"expires_at":%d}`, expiresAt), "SECURITY_PROOF_CONTEXT_MISMATCH", common.RoleCommonUser, http.StatusForbidden, []string{"profile:read"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, model.DB.Model(user).Update("role", test.role).Error)
			proof := issueAccessTokenGenerateProof(t, identity, test.proofExpiry, test.proofScopes...)
			response := accessTokenRequest(router, "POST", "/api/user/access_tokens", browser, proof, test.body)
			assert.Equal(t, test.status, response.Code)
			assert.Contains(t, response.Body.String(), `"code":"`+test.code+`"`)
			var consumed int64
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("consumed_at IS NOT NULL").Count(&consumed).Error)
			assert.Zero(t, consumed, "a rejected grant must not spend the verification")
		})
	}
	tokens, err := model.ListUserAccessTokens(user.Id)
	require.NoError(t, err)
	assert.Empty(t, tokens)

	require.NoError(t, model.DB.Model(user).Update("role", common.RoleCommonUser).Error)
	proof := issueAccessTokenGenerateProof(t, identity, 0, "usage:read", "profile:read")
	response = accessTokenRequest(router, "POST", "/api/user/access_tokens", browser, proof, `{"name":" ci ","scopes":["profile:read","usage:read","profile:read"],"expires_at":0}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
			Item  struct {
				Id        int      `json:"id"`
				Name      string   `json:"name"`
				TokenRef  string   `json:"token_ref"`
				TokenHint string   `json:"token_hint"`
				ExpiresAt int64    `json:"expires_at"`
				Scopes    []string `json:"scopes"`
			} `json:"item"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &created))
	raw := created.Data.Token
	require.True(t, strings.HasPrefix(raw, model.AccessTokenPrefix))
	assert.Len(t, raw, len(model.AccessTokenPrefix)+43)
	assert.Equal(t, "ci", created.Data.Item.Name)
	assert.Equal(t, []string{"profile:read", "usage:read"}, created.Data.Item.Scopes)
	assert.Zero(t, created.Data.Item.ExpiresAt, "expires_at 0 creates a token that never expires")
	assert.Equal(t, model.AccessTokenFingerprint(raw), created.Data.Item.TokenRef)
	assert.Equal(t, raw[len(raw)-4:], created.Data.Item.TokenHint)
	var columns []map[string]any
	require.NoError(t, model.DB.Table("user_access_tokens").Find(&columns).Error)
	require.Len(t, columns, 1)
	encodedRow, err := common.Marshal(columns[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encodedRow), raw, "the database stores only the token hash")
	response = accessTokenRequest(router, "POST", "/api/user/access_tokens", browser, proof, `{"name":"ci","scopes":["profile:read","usage:read"],"expires_at":0}`)
	assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_CONSUMED"`)
	assert.Equal(t, http.StatusOK, accessTokenRequest(router, "GET", "/api/user/self", raw, "", "").Code)
	stored, err := model.GetUserById(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, legacy, stored.GetAccessToken(), "creating a token never replaces the legacy token")

	var generated model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "access_token.generate").First(&generated).Error)
	require.NotNil(t, generated.Other.Op)
	params, err := common.Marshal(generated.Other.Op.Params)
	require.NoError(t, err)
	assert.Contains(t, string(params), fmt.Sprintf(`"token_ref":"%s"`, created.Data.Item.TokenRef))
	assert.Contains(t, string(params), `"scopes":["profile:read","usage:read"]`)
	var history []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&history).Error)
	encoded, err := common.Marshal(history)
	require.NoError(t, err)
	for _, secret := range []string{raw, browser, proof} {
		assert.NotContains(t, string(encoded), secret)
	}
}

func TestAccessTokenUpdateRenamesFreelyAndChangesGrantOnlyWithProof(t *testing.T) {
	user, _ := setupScopedAccessTokenAudit(t)
	router := newAccessTokenTestRouter()
	identity, browser := createAccessTokenTestSession(t, user.Id, "update-session")
	raw, token := createScopedAccessToken(t, user.Id, 0, "profile:read")
	tokenPath := fmt.Sprintf("/api/user/access_tokens/%d", token.Id)
	other := &model.User{Username: "audit-other", Password: "placeholder", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "audit-other"}
	require.NoError(t, model.DB.Create(other).Error)
	_, otherToken := createScopedAccessToken(t, other.Id, 0, "profile:read")

	response := accessTokenRequest(router, "PATCH", tokenPath, browser, "", `{"name":" renamed "}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	stored, err := model.GetUserAccessToken(user.Id, token.Id)
	require.NoError(t, err)
	assert.Equal(t, "renamed", stored.Name)
	assert.Equal(t, []string{"profile:read"}, stored.GetScopes(), "a rename keeps the grant")

	response = accessTokenRequest(router, "PATCH", tokenPath, browser, "", `{"name":"ci","scopes":["usage:read"]}`)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_REQUIRED"`)

	generateProof := issueAccessTokenGenerateProof(t, identity, 0, "usage:read")
	for _, test := range []struct {
		name, path, body, proof, code string
		status                        int
	}{
		{"permission the owner lacks", tokenPath, `{"name":"ci","scopes":["channel:sensitive_write"]}`, issueAccessTokenUpdateProof(t, identity, token.Id, "channel:sensitive_write"), "ACCESS_TOKEN_SCOPE_FORBIDDEN", http.StatusBadRequest},
		{"empty grant", tokenPath, `{"name":"ci","scopes":[]}`, issueAccessTokenUpdateProof(t, identity, token.Id, "profile:read"), "ACCESS_TOKEN_SCOPE_INVALID", http.StatusBadRequest},
		{"another user's token", fmt.Sprintf("/api/user/access_tokens/%d", otherToken.Id), `{"name":"ci","scopes":["usage:read"]}`, issueAccessTokenUpdateProof(t, identity, otherToken.Id, "usage:read"), "ACCESS_TOKEN_NOT_FOUND", http.StatusNotFound},
		{"proof for another token", tokenPath, `{"name":"ci","scopes":["usage:read"]}`, issueAccessTokenUpdateProof(t, identity, otherToken.Id, "usage:read"), "SECURITY_PROOF_CONTEXT_MISMATCH", http.StatusForbidden},
		{"proof for another grant", tokenPath, `{"name":"ci","scopes":["usage:read"]}`, issueAccessTokenUpdateProof(t, identity, token.Id, "profile:read"), "SECURITY_PROOF_CONTEXT_MISMATCH", http.StatusForbidden},
		{"creation proof", tokenPath, `{"name":"ci","scopes":["usage:read"]}`, generateProof, "SECURITY_PROOF_SCOPE_MISMATCH", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := accessTokenRequest(router, "PATCH", test.path, browser, test.proof, test.body)
			assert.Equal(t, test.status, response.Code)
			assert.Contains(t, response.Body.String(), `"code":"`+test.code+`"`)
			var consumed int64
			require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("consumed_at IS NOT NULL").Count(&consumed).Error)
			assert.Zero(t, consumed, "a rejected change must not spend the verification")
		})
	}
	stored, err = model.GetUserAccessToken(user.Id, token.Id)
	require.NoError(t, err)
	assert.Equal(t, "renamed", stored.Name, "a rejected grant change does not apply the rename either")
	assert.Equal(t, []string{"profile:read"}, stored.GetScopes())

	assert.Equal(t, http.StatusOK, accessTokenRequest(router, "GET", "/api/user/self", raw, "", "").Code)
	// The proof binds the grant as a set, so its order and repeats do not matter.
	widenProof := issueAccessTokenUpdateProof(t, identity, token.Id, "usage:read", "profile:read", "profile:read")
	response = accessTokenRequest(router, "PATCH", tokenPath, browser, widenProof, `{"name":"ci","scopes":["profile:read","usage:read"]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	stored, err = model.GetUserAccessToken(user.Id, token.Id)
	require.NoError(t, err)
	assert.Equal(t, []string{"profile:read", "usage:read"}, stored.GetScopes())

	proof := issueAccessTokenUpdateProof(t, identity, token.Id, "usage:read")
	response = accessTokenRequest(router, "PATCH", tokenPath, browser, proof, `{"name":"ci","scopes":["usage:read"," usage:read"]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated struct {
		Data struct {
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &updated))
	assert.Equal(t, "ci", updated.Data.Name)
	assert.Equal(t, []string{"usage:read"}, updated.Data.Scopes)
	response = accessTokenRequest(router, "GET", "/api/user/self", raw, "", "")
	assert.Equal(t, http.StatusForbidden, response.Code, "a narrowed grant applies to the next request")
	assert.Contains(t, response.Body.String(), `"code":"ACCESS_TOKEN_SCOPE_DENIED"`)
	response = accessTokenRequest(router, "PATCH", tokenPath, browser, proof, `{"name":"ci","scopes":["usage:read"]}`)
	assert.Contains(t, response.Body.String(), `"code":"SECURITY_PROOF_CONSUMED"`)

	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action IN ?", []string{"access_token.rename", "access_token.update"}).Order("id").Find(&audits).Error)
	require.Len(t, audits, 3)
	assert.Equal(t, "access_token.rename", audits[0].Action)
	assert.Equal(t, "access_token.update", audits[1].Action)
	assert.Equal(t, "access_token.update", audits[2].Action)
	require.NotNil(t, audits[2].Other.Op)
	params, err := common.Marshal(audits[2].Other.Op.Params)
	require.NoError(t, err)
	assert.Contains(t, string(params), `"previous_scopes":["profile:read","usage:read"]`)
	assert.Contains(t, string(params), `"scopes":["usage:read"]`)
	assert.Contains(t, string(params), fmt.Sprintf(`"token_ref":"%s"`, token.TokenHash))
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	for _, secret := range []string{raw, browser, widenProof, proof, generateProof} {
		assert.NotContains(t, string(encoded), secret)
	}
}
