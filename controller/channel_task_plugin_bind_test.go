package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-fuego/fuego"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTaskPluginBindChannelTest(t *testing.T) {
	t.Helper()
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	originalDB, originalLogDB := model.DB, model.LOG_DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.Log{}, &model.AuditLog{}, &model.User{}))
	model.DB = database
	model.LOG_DB = database
	require.NoError(t, authz.Init(database))
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
		common.RedisEnabled = previousRedisEnabled
		model.DB = originalDB
		model.LOG_DB = originalLogDB
	})
}

func postAddChannel(t *testing.T, userID, role int, body string) dto.MessageResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("id", userID)
	ginCtx.Set("role", role)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/api/channel", nil)

	var request AddChannelRequest
	require.NoError(t, common.UnmarshalJsonStr(body, &request))
	ctx := fuego.NewMockContext[AddChannelRequest, any](request, nil)
	ctx.CommonCtx = ginCtx

	payload, err := AddChannel(ctx)
	require.NoError(t, err)
	return payload
}

func TestAddChannelTaskPluginRequiresBindPermission(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	const key = "channel-bind"
	source := `
export const meta = {apiVersion: 1, key: "channel-bind", name: "Bind", version: "1.0.0", author: {name: "Test"}, models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	taskPluginBody := `{"mode":"single","channel":{"type":61,"name":"plugin-channel","key":"sk","models":"doc","group":"default","base_url":"https://example.com","setting":"{\"task_plugin_key\":\"channel-bind\"}"}}`
	openaiBody := `{"mode":"single","channel":{"type":1,"name":"openai-channel","key":"sk","models":"gpt","group":"default"}}`

	adminDenied := postAddChannel(t, 2, common.RoleAdminUser, taskPluginBody)
	assert.False(t, adminDenied.Success)
	assert.Contains(t, adminDenied.Message, "task plugin channels require the task_plugin.bind permission")

	rootAllowed := postAddChannel(t, 1, common.RoleRootUser, taskPluginBody)
	assert.True(t, rootAllowed.Success)
	assert.NotContains(t, rootAllowed.Message, "task_plugin.bind")

	adminOtherType := postAddChannel(t, 2, common.RoleAdminUser, openaiBody)
	assert.True(t, adminOtherType.Success)
	assert.NotContains(t, adminOtherType.Message, "task_plugin.bind")
}

func TestUpdateChannelTaskPluginRequiresBindPermission(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	const key = "channel-bind-update"
	source := `
export const meta = {apiVersion: 1, key: "channel-bind-update", name: "Bind", version: "1.0.0", author: {name: "Test"}, models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	baseURL := "https://example.com"
	setting := `{"task_plugin_key":"channel-bind-update"}`
	channel := model.Channel{
		Type:    constant.ChannelTypeTaskPlugin,
		Status:  common.ChannelStatusEnabled,
		Name:    "existing-plugin",
		Models:  "doc",
		Group:   "default",
		Key:     "sk",
		BaseURL: &baseURL,
		Setting: &setting,
	}
	require.NoError(t, channel.Insert())

	payload := fmt.Sprintf(
		`{"id":%d,"type":61,"name":"existing-plugin","key":"sk","models":"doc","group":"default","base_url":"https://example.com","setting":"{\"task_plugin_key\":\"channel-bind-update\"}"}`,
		channel.Id,
	)
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("id", 2)
	ginCtx.Set("role", common.RoleAdminUser)
	ginCtx.Request = httptest.NewRequest(http.MethodPut, "/api/channel", nil)

	var patch PatchChannel
	require.NoError(t, common.UnmarshalJsonStr(payload, &patch))
	ctx := fuego.NewMockContext[PatchChannel, any](patch, nil)
	ctx.CommonCtx = ginCtx

	response, err := UpdateChannel(ctx)
	require.NoError(t, err)
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "task plugin channels require the task_plugin.bind permission")
}

func TestAddChannelTaskPluginPersistsPluginDefaultBaseURLAndAuditsSource(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	for key, baseURLField := range map[string]string{"bind-default-url": `baseUrl: "http://10.0.0.5:8000/",`, "bind-no-default": ""} {
		source := fmt.Sprintf(`
export const meta = {apiVersion: 1, key: %q, name: "Bind", version: "1.0.0", author: {name: "Test"}, %s models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, key, baseURLField)
		_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })
	}
	body := func(pluginKey string) string {
		return fmt.Sprintf(`{"mode":"single","channel":{"type":61,"name":"%s","key":"sk","models":"doc","group":"default","setting":"{\"task_plugin_key\":\"%s\"}"}}`, pluginKey, pluginKey)
	}

	noDefault := postAddChannel(t, 1, common.RoleRootUser, body("bind-no-default"))
	assert.Contains(t, noDefault.Message, "base URL is required for task plugin channels")

	filled := postAddChannel(t, 1, common.RoleRootUser, body("bind-default-url"))
	require.True(t, filled.Success, filled.Message)
	var created model.Channel
	require.NoError(t, model.DB.Where("name = ?", "bind-default-url").First(&created).Error)
	require.NotNil(t, created.BaseURL)
	assert.Equal(t, "http://10.0.0.5:8000", *created.BaseURL, "the normalized plugin default is stored on the channel row")

	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "channel.create").Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"base_url_source":"plugin_default"`)
}

func putUpdateChannel(t *testing.T, userID, role int, body string) *dto.Response[PatchChannel] {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("id", userID)
	ginCtx.Set("role", role)
	ginCtx.Request = httptest.NewRequest(http.MethodPut, "/api/channel", nil)

	var patch PatchChannel
	require.NoError(t, common.UnmarshalJsonStr(body, &patch))
	ctx := fuego.NewMockContext[PatchChannel, any](patch, nil)
	ctx.CommonCtx = ginCtx

	response, err := UpdateChannel(ctx)
	require.NoError(t, err)
	return response
}

func TestNewAPIChannelPluginBindingsRequireBindPermission(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	const key = "gateway-bind"
	source := `
export const meta = {apiVersion: 1, key: "gateway-bind", name: "Gateway", version: "1.0.0", author: {name: "Test"}, models: ["gateway-doc"], fetchMode: "per_task", upstreams: ["vendor", "new_api"]};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	bound := `{"mode":"single","channel":{"type":60,"name":"gateway","key":"sk","models":"gateway-doc","group":"default","base_url":"https://gateway.example","setting":"{\"task_extend_plugin_keys\":[\"gateway-bind\"]}"}}`
	unbound := `{"mode":"single","channel":{"type":60,"name":"plain-gateway","key":"sk","models":"gpt","group":"default","base_url":"https://gateway.example"}}`
	adminDenied := postAddChannel(t, 2, common.RoleAdminUser, bound)
	assert.Contains(t, adminDenied.Message, "task plugin channels require the task_plugin.bind permission")
	rootAllowed := postAddChannel(t, 1, common.RoleRootUser, bound)
	assert.True(t, rootAllowed.Success)
	adminUnbound := postAddChannel(t, 2, common.RoleAdminUser, unbound)
	assert.True(t, adminUnbound.Success, "a gateway channel without plugin bindings needs no bind permission")

	baseURL := "https://gateway.example"
	setting := `{"task_extend_plugin_keys":["gateway-bind"]}`
	channel := model.Channel{Type: constant.ChannelTypeNewAPI, Status: common.ChannelStatusEnabled, Name: "existing-gateway", Models: "gateway-doc", Group: "default", Key: "sk", BaseURL: &baseURL, Setting: &setting}
	require.NoError(t, channel.Insert())
	update := func(name, setting string) string {
		return fmt.Sprintf(`{"id":%d,"type":60,"name":%q,"key":"sk","models":"gateway-doc","group":"default","base_url":"https://gateway.example","setting":%q}`, channel.Id, name, setting)
	}
	unchanged := putUpdateChannel(t, 2, common.RoleAdminUser, update("renamed-gateway", setting))
	assert.True(t, unchanged.Success, "resubmitting the stored bindings does not need the bind permission")
	assert.NotContains(t, unchanged.Message, "task_plugin.bind")
	rebound := putUpdateChannel(t, 2, common.RoleAdminUser, update("renamed-gateway", `{"task_extend_plugin_keys":[]}`))
	assert.Contains(t, rebound.Message, "task plugin channels require the task_plugin.bind permission")
	rootRebound := putUpdateChannel(t, 1, common.RoleRootUser, update("renamed-gateway", `{"task_extend_plugin_keys":[]}`))
	assert.True(t, rootRebound.Success)

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("setting", setting).Error)
	copyChannel := func(userID, role int) *dto.Response[dto.CopyChannelData] {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Set("id", userID)
		ginCtx.Set("role", role)
		ginCtx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
		ginCtx.Request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/channel/copy/%d", channel.Id), nil)
		ctx := fuego.NewMockContext[any, dto.CopyChannelParams](nil, dto.CopyChannelParams{})
		ctx.CommonCtx = ginCtx
		response, err := CopyChannel(ctx)
		require.NoError(t, err)
		return response
	}
	assert.Contains(t, copyChannel(2, common.RoleAdminUser).Message, "task plugin channels require the task_plugin.bind permission")
	assert.True(t, copyChannel(1, common.RoleRootUser).Success)
}
