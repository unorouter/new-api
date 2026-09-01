package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// generateOAuthCodeRequest drives prod's fuego GenerateOAuthCode handler through a
// real registered gin route, so the query-param parsing and auth-flow creation
// contract is exercised exactly as in production. sessionSetup, when set, seeds the
// dashboard session identity the bind intent resolves via middleware.GetSessionAuthIdentity.
func generateOAuthCodeRequest(t *testing.T, query url.Values, sessionSetup gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if sessionSetup != nil {
		engine.Use(sessionSetup)
	}
	router := dto.NewRouter(nil, engine, "test")
	dto.GetP(router, "/api/oauth/state", GenerateOAuthCode)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/state?"+query.Encode(), nil)
	engine.ServeHTTP(recorder, request)
	return recorder
}

type authFlowTestOAuthProvider struct {
	exchangeErr   error
	userInfoErr   error
	exchangeCalls int
	userInfoCalls int
}

func (*authFlowTestOAuthProvider) GetName() string { return "Auth Flow Test" }
func (*authFlowTestOAuthProvider) IsEnabled() bool { return true }
func (provider *authFlowTestOAuthProvider) ExchangeToken(context.Context, string, *gin.Context) (*oauth.OAuthToken, error) {
	provider.exchangeCalls++
	if provider.exchangeErr != nil {
		return nil, provider.exchangeErr
	}
	return &oauth.OAuthToken{}, nil
}
func (provider *authFlowTestOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	provider.userInfoCalls++
	if provider.userInfoErr != nil {
		return nil, provider.userInfoErr
	}
	return &oauth.OAuthUser{ProviderUserID: "external-user"}, nil
}
func (*authFlowTestOAuthProvider) IsUserIDTaken(string) bool                      { return false }
func (*authFlowTestOAuthProvider) FillUserByProviderID(*model.User, string) error { return nil }
func (*authFlowTestOAuthProvider) SetProviderUserID(*model.User, string)          {}
func (*authFlowTestOAuthProvider) GetProviderPrefix() string                      { return "flow_" }
func (*authFlowTestOAuthProvider) ProviderUserIDColumn() string                   { return "" }

func setupAuthFlowControllerTest(t *testing.T) *authFlowTestOAuthProvider {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	provider := &authFlowTestOAuthProvider{}
	oauth.Register("auth-flow-test", provider)
	t.Cleanup(func() {
		oauth.Unregister("auth-flow-test")
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return provider
}

func TestGenerateOAuthCodeCarriesAffiliateInLoginFlow(t *testing.T) {
	setupAuthFlowControllerTest(t)

	recorder := generateOAuthCodeRequest(t, url.Values{
		"provider": {"auth-flow-test"},
		"intent":   {"login"},
		"aff":      {"invite-code"},
	}, nil)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response dto.Response[string]
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
	})
	require.NoError(t, err)
	var payload oauthFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, "invite-code", payload.AffiliateCode)
	assert.Zero(t, flow.UserId)
	assert.Empty(t, flow.SessionId)
}

func TestGenerateOAuthCodeBindsFlowToAuthenticatedSession(t *testing.T) {
	setupAuthFlowControllerTest(t)

	recorder := generateOAuthCodeRequest(t, url.Values{
		"provider": {"auth-flow-test"},
		"intent":   {"bind"},
	}, func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("session_id", "session-42")
		c.Set("auth_version", int64(3))
		c.Set("session_version", int64(2))
	})

	require.Equal(t, http.StatusOK, recorder.Code)
	var response dto.Response[string]
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42",
	})
	require.NoError(t, err)
	assert.Equal(t, 42, flow.UserId)
	assert.Equal(t, "session-42", flow.SessionId)
}

func TestOAuthLoginConsumesFlowOnlyAfterProviderIdentity(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	tests := []struct {
		name        string
		exchangeErr error
		userInfoErr error
	}{
		{name: "exchange failure", exchangeErr: errors.New("exchange failed")},
		{name: "user info failure", userInfoErr: errors.New("user info failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider.exchangeErr = test.exchangeErr
			provider.userInfoErr = test.userInfoErr
			token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
				Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
			})
			require.NoError(t, err)

			router := gin.New()
			router.GET("/api/oauth/:provider", HandleOAuth)
			request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+token+"&code=test", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			flow, err := model.GetAuthFlow(token, model.AuthFlowMatch{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
			})
			require.NoError(t, err)
			assert.Nil(t, flow.ConsumedAt)
		})
	}
}

func TestOAuthLoginConsumesFlowAfterProviderIdentityAndOnProviderError(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	provider.exchangeErr = nil
	provider.userInfoErr = nil
	successToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
		Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+successToken+"&code=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(successToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)

	providerErrorToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
		Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	request = httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+providerErrorToken+"&error=access_denied", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(providerErrorToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)
}

func TestOAuthBindProviderErrorConsumesSessionBoundFlow(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	flowToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42", Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("session_id", "session-42")
		c.Set("auth_version", int64(1))
		c.Set("session_version", int64(1))
		c.Next()
	})
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+flowToken+"&error=access_denied&error_description=cancelled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	_, err = model.GetAuthFlow(flowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Zero(t, provider.exchangeCalls)
	assert.Zero(t, provider.userInfoCalls)
}
