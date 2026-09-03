package controller

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureTokenAutoGroupsTest(t *testing.T, maxCount string, autoGroups string) {
	t.Helper()
	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateMaxTokenAutoGroups(maxCount))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(autoGroups))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(stringInt(originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
}

func stringInt(value int) string {
	return fmt.Sprintf("%d", value)
}

func setupTokenAutoGroupsControllerTest(t *testing.T) *model.User {
	t.Helper()
	db := setupTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	user := &model.User{
		Id:       101,
		Username: "token-auto-user",
		Password: "password",
		Group:    "default",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func baseAutoTokenRequest(name string) dto.CreateTokenRequest {
	return dto.CreateTokenRequest{
		Name:            name,
		ExpiredTime:     -1,
		RemainQuota:     0,
		UnlimitedQuota:  true,
		Group:           "auto",
		CrossGroupRetry: common.GetPointer(true),
	}
}

// newTokenAutoGroupsGinContext builds the gin context prod's fuego handlers reach
// through dto.GinCtx for the authenticated user id and group.
func newTokenAutoGroupsGinContext(userID int) *gin.Context {
	ctx, _ := gin.CreateTestContext(nil)
	ctx.Set("id", userID)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	return ctx
}

func newAutoGroupsAddContext(userID int, body dto.CreateTokenRequest) *fuego.MockContext[dto.CreateTokenRequest, any] {
	ctx := fuego.NewMockContext[dto.CreateTokenRequest, any](body, nil)
	ctx.CommonCtx = newTokenAutoGroupsGinContext(userID)
	return ctx
}

func TestAddTokenEmptyAutoGroupsInheritGlobalAuto(t *testing.T) {
	tests := []struct {
		name  string
		value *[]string
	}{
		{name: "omitted", value: nil},
		{name: "empty array", value: &[]string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			request := baseAutoTokenRequest("create-" + test.name)
			request.AutoGroups = test.value

			response, err := AddToken(newAutoGroupsAddContext(user.Id, request))
			require.NoError(t, err)
			require.True(t, response.Success, response.Message)

			var token model.Token
			require.NoError(t, model.DB.Where("name = ?", request.Name).First(&token).Error)
			assert.Empty(t, token.AutoGroups)
			assert.True(t, token.CrossGroupRetry)

			payload, err := common.Marshal(buildMaskedTokenResponse(&token))
			require.NoError(t, err)
			var responseData map[string]any
			require.NoError(t, common.Unmarshal(payload, &responseData))
			assert.Nil(t, responseData["auto_groups"])
		})
	}
}

func TestAddTokenPersistsOrderedAutoGroupsSnapshot(t *testing.T) {
	configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
	user := setupTokenAutoGroupsControllerTest(t)
	request := baseAutoTokenRequest("ordered-snapshot")
	request.AutoGroups = &[]string{"vip", "default"}

	response, err := AddToken(newAutoGroupsAddContext(user.Id, request))
	require.NoError(t, err)
	require.True(t, response.Success, response.Message)

	var token model.Token
	require.NoError(t, model.DB.Where("name = ?", "ordered-snapshot").First(&token).Error)
	assert.JSONEq(t, `["vip","default"]`, token.AutoGroups)

	getCtx := fuego.NewMockContext[any, any](nil, nil)
	getCtx.CommonCtx = newTokenAutoGroupsGinContext(user.Id)
	getCtx.PathParams = map[string]string{"id": stringInt(token.Id)}
	getResponse, err := GetToken(getCtx)
	require.NoError(t, err)
	require.True(t, getResponse.Success, getResponse.Message)
	assert.Equal(t, []string{"vip", "default"}, getResponse.Data.AutoGroups)
}

func TestUpdateTokenAutoGroupsTriStateAndNonAutoCleanup(t *testing.T) {
	tests := []struct {
		name               string
		value              *[]string
		group              string
		expectedAutoGroups string
		expectedRetry      bool
	}{
		{name: "omitted preserves", group: "auto", expectedAutoGroups: `["vip","default"]`, expectedRetry: true},
		{name: "empty inherits", value: &[]string{}, group: "auto", expectedRetry: true},
		{name: "non auto clears and disables retry", value: &[]string{"vip"}, group: "default"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			token := seedToken(t, model.DB, user.Id, "update-auto", "update-auto-key")
			token.Group = "auto"
			token.CrossGroupRetry = true
			require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
			require.NoError(t, model.DB.Save(token).Error)

			request := dto.UpdateTokenRequest{
				Id:              token.Id,
				Status:          common.TokenStatusEnabled,
				Name:            "updated-auto",
				ExpiredTime:     -1,
				UnlimitedQuota:  true,
				Group:           test.group,
				CrossGroupRetry: common.GetPointer(true),
				AutoGroups:      test.value,
			}

			ctx := fuego.NewMockContext[dto.UpdateTokenRequest, dto.StatusOnlyParams](request, dto.StatusOnlyParams{})
			ctx.CommonCtx = newTokenAutoGroupsGinContext(user.Id)
			response, err := UpdateToken(ctx)
			require.NoError(t, err)
			require.True(t, response.Success, response.Message)

			var updated model.Token
			require.NoError(t, model.DB.First(&updated, token.Id).Error)
			if test.expectedAutoGroups == "" {
				assert.Empty(t, updated.AutoGroups)
			} else {
				assert.JSONEq(t, test.expectedAutoGroups, updated.AutoGroups)
			}
			assert.Equal(t, test.expectedRetry, updated.CrossGroupRetry)
		})
	}
}

func TestAddTokenRejectsInvalidAutoGroups(t *testing.T) {
	tests := []struct {
		name     string
		maxCount string
		groups   []string
	}{
		{name: "over limit", maxCount: "1", groups: []string{"default", "vip"}},
		{name: "duplicate", maxCount: "5", groups: []string{"default", "default"}},
		{name: "auto pseudo group", maxCount: "5", groups: []string{"auto"}},
		{name: "unavailable", maxCount: "5", groups: []string{"missing"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, test.maxCount, `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			request := baseAutoTokenRequest("invalid-" + test.name)
			request.AutoGroups = &test.groups

			response, err := AddToken(newAutoGroupsAddContext(user.Id, request))
			require.NoError(t, err)
			assert.False(t, response.Success)

			var count int64
			require.NoError(t, model.DB.Model(&model.Token{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestGetTokenAutoGroupsReturnsFullFilteredGlobalOrderAndLimit(t *testing.T) {
	configureTokenAutoGroupsTest(t, "1", `["vip","missing","default"]`)
	user := setupTokenAutoGroupsControllerTest(t)

	ctx := fuego.NewMockContext[any, any](nil, nil)
	ctx.CommonCtx = newTokenAutoGroupsGinContext(user.Id)
	response, err := GetTokenAutoGroups(ctx)
	require.NoError(t, err)
	require.True(t, response.Success, response.Message)
	assert.Equal(t, []string{"vip", "default"}, response.Data.Groups)
	assert.Equal(t, 1, response.Data.MaxCount)
}
