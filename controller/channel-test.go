package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/samber/lo"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

type testResult struct {
	context     *gin.Context
	localErr    error
	newAPIError *types.NewAPIError
}

func normalizeChannelTestEndpoint(channel *model.Channel, modelName, endpointType string) string {
	normalized := strings.TrimSpace(endpointType)
	if normalized != "" {
		return normalized
	}
	if strings.HasSuffix(modelName, ratio_setting.CompactModelSuffix) {
		return string(constant.EndpointTypeOpenAIResponseCompact)
	}
	if channel != nil && channel.Type == constant.ChannelTypeCodex {
		return string(constant.EndpointTypeOpenAIResponse)
	}
	return normalized
}

func resolveChannelTestUserID(c *gin.Context) (int, error) {
	if c != nil {
		if userID := c.GetInt("id"); userID > 0 {
			return userID, nil
		}
	}

	var rootUser model.User
	if err := model.DB.Select("id").Where("role = ?", common.RoleRootUser).First(&rootUser).Error; err != nil {
		return 0, fmt.Errorf("failed to resolve channel test user: %w", err)
	}
	if rootUser.Id == 0 {
		return 0, errors.New("failed to resolve channel test user")
	}
	return rootUser.Id, nil
}

// isTaskChannel reports whether a channel routes through an async task adaptor
// (submit -> poll), i.e. it has no sync chat/image endpoint. Uses the SAME
// dispatch the real relay uses (GetTaskAdaptor by channel type), so any new task
// channel type is recognized automatically.
func isTaskChannel(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	return relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channel.Type))) != nil
}

// testTaskChannelSubmit probes an async task channel by SUBMITTING a minimal task
// and passing when the upstream accepts it and returns a task id. It does NOT poll
// to completion (a valid submit proves auth + endpoint + model), and it skips all
// billing/moderation/refund (this is a bare submit, not a real relay). Free task
// channels only; the caller gates on that.
func testTaskChannelSubmit(ctx context.Context, channel *model.Channel, testUserID int, testModel string) testResult {
	platform := constant.TaskPlatform(strconv.Itoa(channel.Type))
	adaptor := relay.GetTaskAdaptor(platform)
	if adaptor == nil {
		return testResult{localErr: fmt.Errorf("no task adaptor for channel type %d", channel.Type)}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	probeCtx, cancel := context.WithTimeout(ctx, channelProbeTimeout)
	defer cancel()

	// Minimal task submit body. GetTaskRequest reads this off the request body.
	submit := relaycommon.TaskSubmitReq{Prompt: "test", Model: testModel, Size: "512x512"}
	raw, _ := common.Marshal(submit)
	c.Request = httptest.NewRequestWithContext(probeCtx, http.MethodPost, "/v1/task/submit", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")

	cache, err := model.GetUserCache(testUserID)
	if err != nil {
		return testResult{localErr: err}
	}
	cache.WriteContext(c)
	c.Set("id", testUserID)
	c.Set("channel", channel.Type)
	c.Set("base_url", channel.GetBaseURL())
	c.Set("platform", string(platform))
	group, _ := model.GetUserGroup(testUserID, false)
	c.Set("group", group)

	if newAPIError := middleware.SetupContextForSelectedChannel(c, channel, testModel); newAPIError != nil {
		return testResult{context: c, localErr: newAPIError, newAPIError: newAPIError}
	}

	info, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		return testResult{context: c, localErr: err, newAPIError: types.NewError(err, types.ErrorCodeGenRelayInfoFailed)}
	}
	info.InitChannelMeta(c)
	info.OriginModelName = testModel
	info.UpstreamModelName = testModel

	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		return taskResultErr(c, taskErr)
	}
	body, buildErr := adaptor.BuildRequestBody(c, info)
	if buildErr != nil {
		return testResult{context: c, localErr: buildErr, newAPIError: types.NewError(buildErr, types.ErrorCodeConvertRequestFailed)}
	}
	resp, reqErr := adaptor.DoRequest(c, info, body)
	if reqErr != nil {
		return testResult{context: c, localErr: reqErr, newAPIError: types.NewError(reqErr, types.ErrorCodeDoRequestFailed)}
	}
	taskID, _, taskErr := adaptor.DoResponse(c, resp, info)
	if taskErr != nil {
		return taskResultErr(c, taskErr)
	}
	if taskID == "" {
		err := errors.New("task submit returned empty task id")
		return testResult{context: c, localErr: err, newAPIError: types.NewError(err, types.ErrorCodeBadResponse)}
	}
	return testResult{context: c}
}

// taskResultErr maps a *dto.TaskError to a testResult so the normal disable/enable
// gate treats a bad task submit like any other failed probe.
func taskResultErr(c *gin.Context, taskErr *dto.TaskError) testResult {
	err := taskErr.Error
	if err == nil {
		err = errors.New(taskErr.Message)
	}
	return testResult{
		context:     c,
		localErr:    err,
		newAPIError: types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponse, taskErr.StatusCode),
	}
}

func testChannel(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult {
	if ctx == nil {
		ctx = context.Background()
	}
	tik := time.Now()
	// Async task channels with a task adaptor (AI Horde, Suno, Kling, Jimeng, Doubao
	// video, Vidu, Sora, ...) are probed via a submit-only task probe below. Only the
	// task types that have NO adaptor (Midjourney) stay unsupported.
	var unsupportedTestChannelTypes = []int{
		constant.ChannelTypeMidjourney,
		constant.ChannelTypeMidjourneyPlus,
	}
	if lo.Contains(unsupportedTestChannelTypes, channel.Type) {
		channelTypeName := constant.GetChannelTypeName(channel.Type)
		return testResult{
			localErr: fmt.Errorf("%s channel test is not supported", channelTypeName),
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	testModel = strings.TrimSpace(testModel)
	if testModel == "" {
		if channel.TestModel != nil && *channel.TestModel != "" {
			testModel = strings.TrimSpace(*channel.TestModel)
		} else {
			models := channel.GetModels()
			if len(models) > 0 {
				testModel = strings.TrimSpace(models[0])
			}
			if testModel == "" {
				testModel = "gpt-4o-mini"
			}
		}
	}

	// Submit-only probe for async task channels, gated on the MODEL being non-text:
	// a media/task model (image/video) has no sync endpoint and must be submit-probed.
	// A text model always takes the sync path below - crucial because dual channel types
	// (OpenAI/Gemini/xAI have a Sora/video task adaptor too) would otherwise route every
	// text model through the task probe and fail with "task_id is empty". Free only.
	if isTaskChannel(channel) && isNonTextModel(testModel) {
		if !isFreeChannel(channel) {
			return testResult{localErr: fmt.Errorf("%s paid task channel test is skipped", constant.GetChannelTypeName(channel.Type))}
		}
		return testTaskChannelSubmit(ctx, channel, testUserID, testModel)
	}

	endpointType = normalizeChannelTestEndpoint(channel, testModel, endpointType)

	requestPath := "/v1/chat/completions"

	// 如果指定了端点类型，使用指定的端点类型
	if endpointType != "" {
		if endpointInfo, ok := common.GetDefaultEndpointInfo(constant.EndpointType(endpointType)); ok {
			requestPath = endpointInfo.Path
		}
	} else {
		// 如果没有指定端点类型，使用原有的自动检测逻辑

		if strings.Contains(strings.ToLower(testModel), "rerank") {
			requestPath = "/v1/rerank"
		}

		// 先判断是否为 Embedding 模型
		if strings.Contains(strings.ToLower(testModel), "embedding") ||
			strings.HasPrefix(testModel, "m3e") || // m3e 系列模型
			strings.Contains(testModel, "bge-") || // bge 系列模型
			strings.Contains(testModel, "embed") ||
			channel.Type == constant.ChannelTypeMokaAI { // 其他 embedding 模型
			requestPath = "/v1/embeddings" // 修改请求路径
		}

		// VolcEngine 图像生成模型
		if channel.Type == constant.ChannelTypeVolcEngine && strings.Contains(testModel, "seedream") {
			requestPath = "/v1/images/generations"
		}

		// responses-only models
		if strings.Contains(strings.ToLower(testModel), "codex") {
			requestPath = "/v1/responses"
		}

	}
	// Gemini 原生流式通过 URL action（:streamGenerateContent）表达而非请求体字段，
	// GeminiChatRequest.IsStream 依据请求 URL 判定，合成请求路径需与生产入口保持一致
	if isStream && constant.EndpointType(endpointType) == constant.EndpointTypeGemini {
		requestPath = strings.Replace(requestPath, ":generateContent", ":streamGenerateContent", 1)
	}
	if strings.HasPrefix(requestPath, "/v1/responses/compact") {
		testModel = ratio_setting.WithCompactModelSuffix(testModel)
	}

	// PROD-ONLY (fork): bound the probe independently of the shared relay client so
	// one hanging upstream cannot starve the test loop. Surfaces as
	// context.DeadlineExceeded, which doRequest reclassifies as a channel timeout.
	//
	// A non-streaming probe has no partial output to wait for, so the whole call is
	// effectively time-to-first-byte and the shorter deadline applies. A streaming
	// probe legitimately spends minutes generating AFTER headers arrive, so it keeps
	// the long ceiling; the shared client's ResponseHeaderTimeout still caps its
	// first byte.
	probeTimeout := channelProbeHeaderTimeout
	if isStream {
		probeTimeout = channelProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	c.Request = httptest.NewRequestWithContext(probeCtx, http.MethodPost, requestPath, nil)

	cache, err := model.GetUserCache(testUserID)
	if err != nil {
		return testResult{
			localErr:    err,
			newAPIError: nil,
		}
	}
	cache.WriteContext(c)
	c.Set("id", testUserID)

	//c.Request.Header.Set("Authorization", "Bearer "+channel.Key)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("channel", channel.Type)
	c.Set("base_url", channel.GetBaseURL())
	group, _ := model.GetUserGroup(testUserID, false)
	c.Set("group", group)

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, testModel)
	if newAPIError != nil {
		return testResult{
			context:     c,
			localErr:    newAPIError,
			newAPIError: newAPIError,
		}
	}

	// Determine relay format based on endpoint type or request path
	var relayFormat types.RelayFormat
	if endpointType != "" {
		// 根据指定的端点类型设置 relayFormat
		switch constant.EndpointType(endpointType) {
		case constant.EndpointTypeOpenAI:
			relayFormat = types.RelayFormatOpenAI
		case constant.EndpointTypeOpenAIResponse:
			relayFormat = types.RelayFormatOpenAIResponses
		case constant.EndpointTypeOpenAIResponseCompact:
			relayFormat = types.RelayFormatOpenAIResponsesCompaction
		case constant.EndpointTypeAnthropic:
			relayFormat = types.RelayFormatClaude
		case constant.EndpointTypeGemini:
			relayFormat = types.RelayFormatGemini
		case constant.EndpointTypeJinaRerank:
			relayFormat = types.RelayFormatRerank
		case constant.EndpointTypeImageGeneration:
			relayFormat = types.RelayFormatOpenAIImage
		case constant.EndpointTypeEmbeddings:
			relayFormat = types.RelayFormatEmbedding
		default:
			relayFormat = types.RelayFormatOpenAI
		}
	} else {
		// 根据请求路径自动检测
		relayFormat = types.RelayFormatOpenAI
		if c.Request.URL.Path == "/v1/embeddings" {
			relayFormat = types.RelayFormatEmbedding
		}
		if c.Request.URL.Path == "/v1/images/generations" {
			relayFormat = types.RelayFormatOpenAIImage
		}
		if c.Request.URL.Path == "/v1/messages" {
			relayFormat = types.RelayFormatClaude
		}
		if strings.Contains(c.Request.URL.Path, "/v1beta/models") {
			relayFormat = types.RelayFormatGemini
		}
		if c.Request.URL.Path == "/v1/rerank" || c.Request.URL.Path == "/rerank" {
			relayFormat = types.RelayFormatRerank
		}
		if c.Request.URL.Path == "/v1/responses" {
			relayFormat = types.RelayFormatOpenAIResponses
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v1/responses/compact") {
			relayFormat = types.RelayFormatOpenAIResponsesCompaction
		}
	}

	request := buildTestRequest(testModel, endpointType, channel, isStream)

	info, err := relaycommon.GenRelayInfo(c, relayFormat, request, nil)

	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeGenRelayInfoFailed),
		}
	}

	info.IsChannelTest = true
	info.InitChannelMeta(c)

	err = attachTestBillingRequestInput(info, request)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeJsonMarshalFailed),
		}
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeChannelModelMappedError),
		}
	}

	testModel = info.UpstreamModelName
	// 更新请求中的模型名称
	request.SetModelName(testModel)

	apiType, _ := common.ChannelType2APIType(channel.Type)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact &&
		!common.SupportsResponsesCompact(channel.Type, apiType) {
		return testResult{
			context:     c,
			localErr:    fmt.Errorf("responses compaction test is not supported for api type %d", apiType),
			newAPIError: types.NewError(fmt.Errorf("unsupported api type: %d", apiType), types.ErrorCodeInvalidApiType),
		}
	}
	adaptor := relay.GetAdaptor(apiType)
	if adaptor == nil {
		return testResult{
			context:     c,
			localErr:    fmt.Errorf("invalid api type: %d, adaptor is nil", apiType),
			newAPIError: types.NewError(fmt.Errorf("invalid api type: %d, adaptor is nil", apiType), types.ErrorCodeInvalidApiType),
		}
	}

	//// 创建一个用于日志的 info 副本，移除 ApiKey
	//logInfo := info
	//logInfo.ApiKey = ""
	common.SysLog(fmt.Sprintf("testing channel %d with model %s , info %+v ", channel.Id, testModel, info.ToString()))

	priceData, err := helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest)),
		}
	}

	adaptor.Init(info)

	var convertedRequest any
	// 根据 RelayMode 选择正确的转换函数
	switch info.RelayMode {
	case relayconstant.RelayModeEmbeddings:
		// Embedding 请求 - request 已经是正确的类型
		if embeddingReq, ok := request.(*relaydto.EmbeddingRequest); ok {
			convertedRequest, err = adaptor.ConvertEmbeddingRequest(c, info, *embeddingReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid embedding request type"),
				newAPIError: types.NewError(errors.New("invalid embedding request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeImagesGenerations:
		// 图像生成请求 - request 已经是正确的类型
		if imageReq, ok := request.(*relaydto.ImageRequest); ok {
			convertedRequest, err = adaptor.ConvertImageRequest(c, info, *imageReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid image request type"),
				newAPIError: types.NewError(errors.New("invalid image request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeRerank:
		// Rerank 请求 - request 已经是正确的类型
		if rerankReq, ok := request.(*relaydto.RerankRequest); ok {
			convertedRequest, err = adaptor.ConvertRerankRequest(c, info.RelayMode, *rerankReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid rerank request type"),
				newAPIError: types.NewError(errors.New("invalid rerank request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeResponses:
		// Response 请求 - request 已经是正确的类型
		if responseReq, ok := request.(*relaydto.OpenAIResponsesRequest); ok {
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, *responseReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid response request type"),
				newAPIError: types.NewError(errors.New("invalid response request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeResponsesCompact:
		// Response compaction request - convert to OpenAIResponsesRequest before adapting
		switch req := request.(type) {
		case *relaydto.OpenAIResponsesCompactionRequest:
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, relaydto.OpenAIResponsesRequest{
				Model:              req.Model,
				Input:              req.Input,
				Instructions:       req.Instructions,
				PreviousResponseID: req.PreviousResponseID,
			})
		case *relaydto.OpenAIResponsesRequest:
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, *req)
		default:
			return testResult{
				context:     c,
				localErr:    errors.New("invalid response compaction request type"),
				newAPIError: types.NewError(errors.New("invalid response compaction request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	default:
		// Chat/Completion 等其他请求类型
		switch req := request.(type) {
		case *relaydto.GeneralOpenAIRequest:
			convertedRequest, err = adaptor.ConvertOpenAIRequest(c, info, req)
		case *relaydto.ClaudeRequest:
			convertedRequest, err = adaptor.ConvertClaudeRequest(c, info, req)
		case *relaydto.GeminiChatRequest:
			convertedRequest, err = adaptor.ConvertGeminiRequest(c, info, req)
		default:
			return testResult{
				context:     c,
				localErr:    errors.New("invalid chat request type"),
				newAPIError: types.NewError(errors.New("invalid chat request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	}

	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed),
		}
	}
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeJsonMarshalFailed),
		}
	}

	//jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings)
	//if err != nil {
	//	return testResult{
	//		context:     c,
	//		localErr:    err,
	//		newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed),
	//	}
	//}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info, nil)
		if err != nil {
			if fixedErr, ok := relaycommon.AsParamOverrideReturnError(err); ok {
				return testResult{
					context:     c,
					localErr:    fixedErr,
					newAPIError: relaycommon.NewAPIErrorFromParamOverride(fixedErr),
				}
			}
			return testResult{
				context:     c,
				localErr:    err,
				newAPIError: types.NewError(err, types.ErrorCodeChannelParamOverrideInvalid),
			}
		}
	}

	requestBody := bytes.NewBuffer(jsonData)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(jsonData))
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError),
		}
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		if httpResp.StatusCode != http.StatusOK {
			err := service.RelayErrorHandler(c.Request.Context(), httpResp, true)
			common.SysError(fmt.Sprintf(
				"channel test bad response: channel_id=%d name=%s type=%d model=%s endpoint_type=%s status=%d err=%v",
				channel.Id,
				channel.Name,
				channel.Type,
				testModel,
				endpointType,
				httpResp.StatusCode,
				err,
			))
			return testResult{
				context:     c,
				localErr:    err,
				newAPIError: types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError),
			}
		}
	}
	usageA, respErr := adaptor.DoResponse(c, httpResp, info)
	if respErr != nil {
		return testResult{
			context:     c,
			localErr:    respErr,
			newAPIError: respErr,
		}
	}
	usage, usageErr := coerceTestUsage(usageA, isStream, info.GetEstimatePromptTokens())
	if usageErr != nil {
		return testResult{
			context:     c,
			localErr:    usageErr,
			newAPIError: types.NewOpenAIError(usageErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError),
		}
	}
	result := w.Result()
	respBody, err := readTestResponseBody(result.Body, isStream)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError),
		}
	}
	if bodyErr := validateTestResponseBody(respBody, isStream); bodyErr != nil {
		return testResult{
			context:     c,
			localErr:    bodyErr,
			newAPIError: types.NewOpenAIError(bodyErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError),
		}
	}
	if operation_setting.GetMonitorSetting().DisableOnEmptyResponse && isEmptyTestResponseBody(respBody, isStream) {
		emptyErr := errors.New("channel: empty response (upstream returned 200 with no content)")
		return testResult{
			context:     c,
			localErr:    emptyErr,
			newAPIError: types.NewOpenAIError(emptyErr, types.ErrorCodeChannelEmptyResponse, http.StatusBadGateway),
		}
	}
	info.SetEstimatePromptTokens(usage.PromptTokens)

	quota, tieredResult := settleTestQuota(info, priceData, usage)
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	consumedTime := float64(milliseconds) / 1000.0
	other := buildTestLogOther(c, info, priceData, usage, tieredResult)
	model.RecordConsumeLog(c, testUserID, model.RecordConsumeLogParams{
		ChannelId:        channel.Id,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ModelName:        info.OriginModelName,
		TokenName:        "Model test",
		Quota:            quota,
		Content:          "Model test",
		UseTimeSeconds:   int(consumedTime),
		IsStream:         info.IsStream,
		Group:            info.UsingGroup,
		Other:            other,
	})
	common.SysLog(fmt.Sprintf("testing channel #%d, response: \n%s", channel.Id, string(respBody)))
	return testResult{
		context:     c,
		localErr:    nil,
		newAPIError: nil,
	}
}

func attachTestBillingRequestInput(info *relaycommon.RelayInfo, request relaydto.Request) error {
	if info == nil {
		return nil
	}

	input, err := helper.BuildBillingExprRequestInputFromRequest(request, info.RequestHeaders)
	if err != nil {
		return err
	}
	info.BillingRequestInput = &input
	return nil
}

func settleTestQuota(info *relaycommon.RelayInfo, priceData hosttypes.PriceData, usage *relaydto.Usage) (int, *billingexpr.TieredResult) {
	if usage != nil && info != nil && info.TieredBillingSnapshot != nil {
		isClaudeUsageSemantic := usage.UsageSemantic == "anthropic" || info.GetFinalRequestRelayFormat() == types.RelayFormatClaude
		usedVars := billingexpr.UsedVars(info.TieredBillingSnapshot.ExprString)
		if ok, quota, result := service.TryTieredSettle(info, service.BuildTieredTokenParams(usage, isClaudeUsageSemantic, usedVars)); ok {
			return quota, result
		}
	}

	quota := 0
	if !priceData.UsePrice {
		quota = usage.PromptTokens + int(math.Round(float64(usage.CompletionTokens)*priceData.CompletionRatio))
		quota = int(math.Round(float64(quota) * priceData.ModelRatio))
		if priceData.ModelRatio != 0 && quota <= 0 {
			quota = 1
		}
		return quota, nil
	}

	return int(priceData.ModelPrice * common.QuotaPerUnit), nil
}

func buildTestLogOther(c *gin.Context, info *relaycommon.RelayInfo, priceData hosttypes.PriceData, usage *relaydto.Usage, tieredResult *billingexpr.TieredResult) map[string]interface{} {
	other := service.GenerateTextOtherInfo(c, info, priceData.ModelRatio, priceData.GroupRatioInfo.GroupRatio, priceData.CompletionRatio,
		usage.PromptTokensDetails.CachedTokens, priceData.CacheRatio, priceData.ModelPrice, priceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		service.InjectTieredBillingInfo(other, info, tieredResult)
	}
	return other
}

func coerceTestUsage(usageAny any, isStream bool, estimatePromptTokens int) (*relaydto.Usage, error) {
	switch u := usageAny.(type) {
	case *relaydto.Usage:
		return u, nil
	case relaydto.Usage:
		return &u, nil
	case nil:
		if !isStream {
			return nil, errors.New("usage is nil")
		}
		usage := &relaydto.Usage{
			PromptTokens: estimatePromptTokens,
		}
		usage.TotalTokens = usage.PromptTokens
		return usage, nil
	default:
		if !isStream {
			return nil, fmt.Errorf("invalid usage type: %T", usageAny)
		}
		usage := &relaydto.Usage{
			PromptTokens: estimatePromptTokens,
		}
		usage.TotalTokens = usage.PromptTokens
		return usage, nil
	}
}

func readTestResponseBody(body io.ReadCloser, isStream bool) ([]byte, error) {
	defer func() { _ = body.Close() }()
	const maxStreamLogBytes = 8 << 10
	if isStream {
		return io.ReadAll(io.LimitReader(body, maxStreamLogBytes))
	}
	return io.ReadAll(body)
}

func detectErrorFromTestResponseBody(respBody []byte) error {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return nil
	}
	if message := detectErrorMessageFromJSONBytes(b); message != "" {
		return fmt.Errorf("upstream error: %s", message)
	}

	for _, line := range bytes.Split(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if message := detectErrorMessageFromJSONBytes(payload); message != "" {
			return fmt.Errorf("upstream error: %s", message)
		}
	}

	return nil
}

func validateStreamTestResponseBody(respBody []byte) error {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return errors.New("stream response body is empty")
	}

	for _, line := range bytes.Split(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		return nil
	}

	return errors.New("stream response body does not contain a valid stream event")
}

func validateTestResponseBody(respBody []byte, isStream bool) error {
	if bodyErr := detectErrorFromTestResponseBody(respBody); bodyErr != nil {
		return bodyErr
	}
	if isStream {
		return validateStreamTestResponseBody(respBody)
	}
	return nil
}

// isEmptyTestResponseBody reports whether a 200 test response carries no usable
// model output (content, reasoning or tool calls). Non-chat payloads (embeddings,
// rerank, images) have no choices array and are never flagged.
func isEmptyTestResponseBody(respBody []byte, isStream bool) bool {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return true
	}

	if !isStream {
		if b[0] != '{' {
			return false
		}
		choices := gjson.GetBytes(b, "choices")
		if !choices.Exists() || !choices.IsArray() {
			return false
		}
		return !chatChoiceHasOutput(gjson.GetBytes(b, "choices.0.message"), gjson.GetBytes(b, "choices.0.finish_reason").String()) &&
			strings.TrimSpace(gjson.GetBytes(b, "choices.0.text").String()) == ""
	}

	sawChatChunk := false
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || payload[0] != '{' {
			continue
		}
		choices := gjson.GetBytes(payload, "choices")
		if !choices.Exists() || !choices.IsArray() {
			continue
		}
		sawChatChunk = true
		if chatChoiceHasOutput(gjson.GetBytes(payload, "choices.0.delta"), gjson.GetBytes(payload, "choices.0.finish_reason").String()) ||
			strings.TrimSpace(gjson.GetBytes(payload, "choices.0.text").String()) != "" {
			return false
		}
	}
	return sawChatChunk
}

func chatChoiceHasOutput(message gjson.Result, finishReason string) bool {
	if !message.Exists() {
		return false
	}
	if strings.TrimSpace(message.Get("content").String()) != "" {
		return true
	}
	if toolCalls := message.Get("tool_calls"); toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		return true
	}
	if message.Get("function_call").Exists() {
		return true
	}
	// Reasoning-only counts as output only when the turn completed (Qwen-style
	// thinking where reasoning is the answer). finish_reason=length with empty
	// content means the model ran out of budget mid-reasoning and never produced
	// an answer (GLM with thinking left on) - blank to the user, so the probe
	// treats it as empty and disables the channel.
	return strings.TrimSpace(message.Get("reasoning_content").String()) != "" &&
		finishReason != constant.FinishReasonLength
}

func shouldUseStreamForAutomaticChannelTest(channel *model.Channel) bool {
	return channel != nil && channel.Type == constant.ChannelTypeCodex
}

func detectErrorMessageFromJSONBytes(jsonBytes []byte) string {
	if len(jsonBytes) == 0 {
		return ""
	}
	if jsonBytes[0] != '{' && jsonBytes[0] != '[' {
		return ""
	}
	errVal := gjson.GetBytes(jsonBytes, "error")
	if !errVal.Exists() || errVal.Type == gjson.Null {
		return ""
	}

	message := gjson.GetBytes(jsonBytes, "error.message").String()
	if message == "" {
		message = gjson.GetBytes(jsonBytes, "error.error.message").String()
	}
	if message == "" && errVal.Type == gjson.String {
		message = errVal.String()
	}
	if message == "" {
		message = errVal.Raw
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "upstream returned error payload"
	}
	return message
}

func buildTestRequest(model string, endpointType string, channel *model.Channel, isStream bool) relaydto.Request {
	testResponsesInput := json.RawMessage(`[{"role":"user","content":"hi"}]`)

	// 根据端点类型构建不同的测试请求
	if endpointType != "" {
		switch constant.EndpointType(endpointType) {
		case constant.EndpointTypeEmbeddings:
			// 返回 EmbeddingRequest
			return &relaydto.EmbeddingRequest{
				Model: model,
				Input: []any{"hello world"},
			}
		case constant.EndpointTypeImageGeneration:
			// 返回 ImageRequest
			return &relaydto.ImageRequest{
				Model:  model,
				Prompt: "a cute cat",
				N:      lo.ToPtr(uint(1)),
				Size:   "1024x1024",
			}
		case constant.EndpointTypeJinaRerank:
			// 返回 RerankRequest
			return &relaydto.RerankRequest{
				Model:     model,
				Query:     "What is Deep Learning?",
				Documents: []any{"Deep Learning is a subset of machine learning.", "Machine learning is a field of artificial intelligence."},
				TopN:      lo.ToPtr(2),
			}
		case constant.EndpointTypeOpenAIResponse:
			// 返回 OpenAIResponsesRequest
			return &relaydto.OpenAIResponsesRequest{
				Model:  model,
				Input:  json.RawMessage(`[{"role":"user","content":"hi"}]`),
				Stream: lo.ToPtr(isStream),
			}
		case constant.EndpointTypeOpenAIResponseCompact:
			// 返回 OpenAIResponsesCompactionRequest
			return &relaydto.OpenAIResponsesCompactionRequest{
				Model: model,
				Input: testResponsesInput,
			}
		case constant.EndpointTypeAnthropic:
			return &relaydto.ClaudeRequest{
				Model:     model,
				Stream:    lo.ToPtr(isStream),
				MaxTokens: lo.ToPtr(uint(reasoningProbeMaxTokens(model, 16))),
				Messages: []relaydto.ClaudeMessage{
					{
						Role:    "user",
						Content: "hi",
					},
				},
			}
		case constant.EndpointTypeGemini:
			return &relaydto.GeminiChatRequest{
				Contents: []relaydto.GeminiChatContent{
					{
						Role:  "user",
						Parts: []relaydto.GeminiPart{{Text: "hi"}},
					},
				},
				GenerationConfig: relaydto.GeminiChatGenerationConfig{
					MaxOutputTokens: lo.ToPtr(uint(3000)),
				},
			}
		case constant.EndpointTypeOpenAI:
			req := &relaydto.GeneralOpenAIRequest{
				Model:  model,
				Stream: lo.ToPtr(isStream),
				Messages: []relaydto.Message{
					{
						Role:    "user",
						Content: "hi",
					},
				},
				MaxTokens: lo.ToPtr(uint(reasoningProbeMaxTokens(model, 16))),
			}
			if isStream {
				req.StreamOptions = &relaydto.StreamOptions{IncludeUsage: true}
			}
			return req
		}
	}

	// 自动检测逻辑（保持原有行为）
	if strings.Contains(strings.ToLower(model), "rerank") {
		return &relaydto.RerankRequest{
			Model:     model,
			Query:     "What is Deep Learning?",
			Documents: []any{"Deep Learning is a subset of machine learning.", "Machine learning is a field of artificial intelligence."},
			TopN:      lo.ToPtr(2),
		}
	}

	// 先判断是否为 Embedding 模型
	if strings.Contains(strings.ToLower(model), "embedding") ||
		strings.HasPrefix(model, "m3e") ||
		strings.Contains(model, "bge-") {
		// 返回 EmbeddingRequest
		return &relaydto.EmbeddingRequest{
			Model: model,
			Input: []any{"hello world"},
		}
	}

	// Responses compaction models (must use /v1/responses/compact)
	if strings.HasSuffix(model, ratio_setting.CompactModelSuffix) {
		return &relaydto.OpenAIResponsesCompactionRequest{
			Model: model,
			Input: testResponsesInput,
		}
	}

	// Responses-only models (e.g. codex series)
	if strings.Contains(strings.ToLower(model), "codex") {
		return &relaydto.OpenAIResponsesRequest{
			Model:  model,
			Input:  json.RawMessage(`[{"role":"user","content":"hi"}]`),
			Stream: lo.ToPtr(isStream),
		}
	}

	// Chat/Completion 请求 - 返回 GeneralOpenAIRequest
	testRequest := &relaydto.GeneralOpenAIRequest{
		Model:  model,
		Stream: lo.ToPtr(isStream),
		Messages: []relaydto.Message{
			{
				Role:    "user",
				Content: "hi",
			},
		},
	}
	if isStream {
		testRequest.StreamOptions = &relaydto.StreamOptions{IncludeUsage: true}
	}

	if relaydto.IsOpenAIReasoningOModel(model) {
		testRequest.MaxCompletionTokens = lo.ToPtr(uint(16))
	} else if strings.Contains(model, "thinking") {
		if !strings.Contains(model, "claude") {
			// PROD-ONLY (fork): 50 is too tight for CoT models; give room for content.
			testRequest.MaxTokens = lo.ToPtr(uint(3000))
		}
	} else if strings.Contains(model, "gemini") {
		testRequest.MaxTokens = lo.ToPtr(uint(3000))
	} else {
		// PROD-ONLY (fork): default-reasoning models (glm-5/deepseek-r1/qwen3/...)
		// have no "thinking" in the name; without a bump their 16-token probe returns
		// empty content and DisableOnEmptyResponse false-disables the channel.
		testRequest.MaxTokens = lo.ToPtr(reasoningProbeMaxTokens(model, 16))
	}

	return testRequest
}

func TestChannel(c fuego.ContextWithParams[dto.TestChannelParams]) (dto.TestChannelResponse, error) {
	p, _ := dto.ParseParams[dto.TestChannelParams](c)
	ginCtx := dto.GinCtx(c)
	channelId, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.TestChannelResponse{Success: false, Message: err.Error()}, nil
	}
	channel, err := model.CacheGetChannel(channelId)
	if err != nil {
		channel, err = model.GetChannelById(channelId, true)
		if err != nil {
			return dto.TestChannelResponse{Success: false, Message: err.Error()}, nil
		}
	}
	testModel := p.Model
	endpointType := p.EndpointType
	isStream := p.Stream
	testUserID, err := resolveChannelTestUserID(ginCtx)
	if err != nil {
		return dto.TestChannelResponse{Success: false, Message: err.Error()}, nil
	}
	tik := time.Now()
	requestCtx := context.Background()
	if ginCtx != nil && ginCtx.Request != nil {
		requestCtx = ginCtx.Request.Context()
	}
	result := testChannel(requestCtx, channel, testUserID, testModel, endpointType, isStream)
	if result.localErr != nil {
		resp := dto.TestChannelResponse{Success: false, Message: result.localErr.Error(), Time: 0.0}
		if result.newAPIError != nil {
			resp.ErrorCode = result.newAPIError.GetErrorCode()
		}
		return resp, nil
	}
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	consumedTime := float64(milliseconds) / 1000.0
	if result.newAPIError != nil {
		return dto.TestChannelResponse{Success: false, Message: result.newAPIError.Error(), Time: consumedTime, ErrorCode: result.newAPIError.GetErrorCode()}, nil
	}
	go channel.UpdateResponseTime(milliseconds)
	// An operator testing a channel expects a passing test to bring it back. Only
	// auto-disabled channels qualify: a manual disable is an explicit decision.
	if channel.Status == common.ChannelStatusAutoDisabled {
		common.SysLog(fmt.Sprintf("channel-test: manual test passed for channel #%d (%s), re-enabling", channel.Id, channel.Name))
		service.EnableChannel(channel.Id, common.GetContextKeyString(result.context, constant.ContextKeyChannelKey), channel.Name,
			model.WithChannelStatusTrigger(model.ChannelStatusTriggerManual),
			model.WithChannelStatusModel(testModel),
			model.WithChannelStatusResponseTime(int(milliseconds)))
	}
	return dto.TestChannelResponse{Success: true, Message: "", Time: consumedTime}, nil
}

// channelTestSummary records the outcome of one channel test cycle so the
// system task can persist a per-run result for history.
type channelTestSummary struct {
	Tested    int `json:"tested"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Disabled  int `json:"disabled"`
	Enabled   int `json:"enabled"`
}

// channelProbeTimeout bounds a single probe. Reasoning models and congested
// upstreams legitimately take minutes, and failing them for that alone strands a
// working channel; the pool keeps a long ceiling affordable because a slow probe
// costs its own wall-clock rather than every other channel's turn.
const channelProbeTimeout = 5 * time.Minute

// channelProbeHeaderTimeout bounds the wait for the FIRST BYTE, which is a
// different question from how long generation takes. A wedged upstream accepts
// the connection and then never writes, so it consumes the whole
// channelProbeTimeout while looking merely slow. Healthy shards answer in single
// digit seconds even for reasoning models, since slow tokens are not slow
// headers, so a minute here only ever cuts short something already dead.
const channelProbeHeaderTimeout = 45 * time.Second

// channelTestConcurrency resolves the probe-pool width. The env override keeps
// the deployment able to widen the pool without an admin round-trip; otherwise
// the admin-configured monitor setting decides. Probes are almost entirely
// network wait, so the cycle finishes within the scheduling tick instead of one
// slow upstream serializing every channel behind it.
func channelTestConcurrency(configured int) int {
	if n, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_CONCURRENCY")); err == nil && n > 0 {
		return n
	}
	return operation_setting.NormalizeChannelTestConcurrency(configured)
}

// runChannelTestWorkers executes independent channel tests with bounded
// concurrency. Results and progress are reduced by the caller goroutine, so
// summary counts and the progress reporter remain serialized.
func runChannelTestWorkers(
	ctx context.Context,
	channels []*model.Channel,
	concurrency int,
	run func(context.Context, *model.Channel) channelTestSummary,
	report func(processed, total int),
) channelTestSummary {
	if ctx == nil {
		ctx = context.Background()
	}
	total := len(channels)
	if report != nil {
		report(0, total)
	}
	if total == 0 {
		return channelTestSummary{}
	}

	workerCount := min(operation_setting.NormalizeChannelTestConcurrency(concurrency), total)
	jobs := make(chan *model.Channel)
	results := make(chan channelTestSummary)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case channel, ok := <-jobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}

					result := channelTestSummary{}
					if channel != nil && channel.Status != common.ChannelStatusManuallyDisabled {
						result = run(ctx, channel)
					}

					results <- result

					// Spread probes when configured, so a pool-wide burst does not
					// look like an attack to a shared upstream.
					if common.RequestInterval > 0 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(common.RequestInterval):
						}
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, channel := range channels {
			select {
			case <-ctx.Done():
				return
			case jobs <- channel:
			}
		}
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	summary := channelTestSummary{}
	processed := 0
	for result := range results {
		summary.Tested += result.Tested
		summary.Succeeded += result.Succeeded
		summary.Failed += result.Failed
		summary.Disabled += result.Disabled
		summary.Enabled += result.Enabled
		processed++
		if report != nil && ctx.Err() == nil {
			report(processed, total)
		}
	}
	return summary
}

// testChannelForCycle probes one channel and applies the disable/enable decision.
func testChannelForCycle(ctx context.Context, channel *model.Channel, testUserID int, allowDisable bool) channelTestSummary {
	summary := channelTestSummary{}
	// Skip channels whose only models are image/video/audio (non-text). Testing
	// them just spams bad-response errors every scheduled run. Free embedding/
	// image-only channels still get a testable model via pickAutoTestModel.
	testModel := pickAutoTestModel(channel)
	if testModel == "" {
		return summary
	}
	isChannelEnabled := channel.Status == common.ChannelStatusEnabled
	tik := time.Now()
	result := testChannel(ctx, channel, testUserID, testModel, "", shouldUseStreamForAutomaticChannelTest(channel))
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	if ctx.Err() != nil {
		return summary
	}

	shouldBanChannel := false
	newAPIError := result.newAPIError
	// request error disables the channel
	if newAPIError != nil {
		shouldBanChannel = service.ShouldDisableChannel(result.newAPIError)
		// PROD-ONLY (fork): mirror the relay-side skip for non-recoverable modalities.
		if shouldBanChannel && shouldSkipDisableForModality(testModel, result.newAPIError) {
			common.SysLog(fmt.Sprintf("PROD-ONLY(fork): skip auto-disable channel #%d non-recoverable modality model=%s code=%s",
				channel.Id, testModel, result.newAPIError.GetErrorCode()))
			shouldBanChannel = false
		}
	}

	summary.Tested++
	if newAPIError == nil {
		summary.Succeeded++
	} else {
		summary.Failed++
	}

	// disable channel
	if allowDisable && isChannelEnabled && shouldBanChannel && channel.GetAutoBan() {
		processChannelError(result.context, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(result.context, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError,
			model.WithChannelStatusTrigger(model.ChannelStatusTriggerScheduledTest),
			model.WithChannelStatusModel(testModel),
			model.WithChannelStatusResponseTime(int(milliseconds)))
		summary.Disabled++
	}

	// enable channel
	if result.localErr == nil && !isChannelEnabled && service.ShouldEnableChannel(newAPIError, channel.Status) {
		// A flapping channel passes the tiny recovery probe but dies again under
		// real traffic; hold it disabled with exponential cooldown instead of
		// re-enabling it every probe cycle (each flap leaks user-visible errors).
		if wait := model.FlapCooldownRemainingSeconds(channel.Id); wait > 0 {
			common.SysLog(fmt.Sprintf("channel-test: probe passed but channel #%d (%s) is flapping; keeping disabled for %ds more", channel.Id, channel.Name, wait))
		} else {
			service.EnableChannel(channel.Id, common.GetContextKeyString(result.context, constant.ContextKeyChannelKey), channel.Name,
				model.WithChannelStatusTrigger(model.ChannelStatusTriggerScheduledTest),
				model.WithChannelStatusModel(testModel),
				model.WithChannelStatusResponseTime(int(milliseconds)))
			summary.Enabled++
		}
	}

	// probe of an already-disabled channel failed again (no status flip): the
	// disable/enable branches above wrote nothing, so record the recurring
	// failure as a self-transition row (upsert on error signature) to make
	// always-failing channels visible.
	if newAPIError != nil && !isChannelEnabled {
		model.RecordChannelProbeFailure(channel, newAPIError.StatusCode, string(newAPIError.GetErrorCode()),
			newAPIError.ErrorWithStatusCode(), model.ChannelStatusTriggerScheduledTest, testModel, int(milliseconds))
	}

	channel.UpdateResponseTime(milliseconds)
	return summary
}

// performChannelTests probes the given channels through a bounded worker pool,
// honoring ctx cancellation so a system-task runner that loses its lease stops
// promptly. When report is non-nil it is called after each channel with
// (processed, total) so the system task can surface progress.
func performChannelTests(ctx context.Context, channels []*model.Channel, testUserID int, allowDisable bool, concurrency int, report func(processed, total int)) channelTestSummary {
	if ctx == nil {
		ctx = context.Background()
	}
	return runChannelTestWorkers(
		ctx,
		channels,
		concurrency,
		func(ctx context.Context, channel *model.Channel) channelTestSummary {
			return testChannelForCycle(ctx, channel, testUserID, allowDisable)
		},
		report,
	)
}

// runChannelTestTask runs one synchronous channel test cycle for the system task
// runner (both the scheduled job and the manual "test all channels" trigger go
// through here). It honors ctx cancellation so a runner that loses its lease
// stops promptly. mode selects the channel set: an empty mode falls back to the
// configured monitor ChannelTestMode (scheduled behavior), while a manual
// trigger passes ChannelTestModeScheduledAll to test every channel. When notify
// is set the root user is notified on completion. Cross-instance execution is
// guarded by the system task per-type lock, so no process-local guard is needed.
func runChannelTestTask(ctx context.Context, mode string, notify bool, report func(processed, total int)) (channelTestSummary, error) {
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return channelTestSummary{}, err
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return channelTestSummary{}, err
	}
	if strings.TrimSpace(mode) == "" {
		mode = operation_setting.GetMonitorSetting().ChannelTestMode
	}
	selected := selectChannelsForAutomaticTest(channels, mode)
	allowDisable := mode != operation_setting.ChannelTestModePassiveRecovery
	concurrency := channelTestConcurrency(operation_setting.GetMonitorSetting().ChannelTestConcurrency)
	cycleStart := time.Now()
	summary := performChannelTests(ctx, selected, testUserID, allowDisable, concurrency, report)
	// Always-on run summary so recovery throughput is visible without DEBUG. The
	// duration is what tells us the cycle still fits inside the scheduling tick.
	common.SysLog(fmt.Sprintf(
		"channel test: tested=%d succeeded=%d enabled=%d disabled=%d selected=%d concurrency=%d took=%.1fs",
		summary.Tested, summary.Succeeded, summary.Enabled, summary.Disabled,
		len(selected), concurrency, time.Since(cycleStart).Seconds()))
	if notify && (ctx == nil || ctx.Err() == nil) {
		service.NotifyRootUser(relaydto.NotifyTypeChannelTest, "Channel test complete", "All channel tests have completed")
	}
	return summary, nil
}

func selectChannelsForAutomaticTest(channels []*model.Channel, mode string) []*model.Channel {
	// Either upstream's passive_recovery mode or our AutoTestDisabledChannelsOnly
	// toggle restricts the scheduled probe to auto-disabled channels, leaving
	// healthy channels alone (avoids probe-induced 429s and quota burn). The
	// toggle field survived an upstream merge that rewrote this loop, so honor it
	// here explicitly.
	disabledOnly := mode == operation_setting.ChannelTestModePassiveRecovery ||
		operation_setting.GetMonitorSetting().AutoTestDisabledChannelsOnly
	selected := make([]*model.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Status == common.ChannelStatusManuallyDisabled {
			continue
		}
		if mode == operation_setting.ChannelTestModeAutoBanOnly && !channel.GetAutoBan() {
			continue
		}
		if disabledOnly && channel.Status != common.ChannelStatusAutoDisabled {
			continue
		}
		if !channelDueForScheduledTest(channel) {
			continue
		}
		selected = append(selected, channel)
	}
	return selected
}

// PROD-ONLY (fork): a channel carrying its own interval is skipped until that long
// has passed since its last test, so one global tick can serve upstreams with very
// different probe costs. An upstream metered per exit IP gives only a handful of
// requests per address, and a scheduled probe spends that budget against the users
// the channel exists to serve.
//
// TestTime is stamped by UpdateResponseTime at the end of every cycle test, pass or
// fail, so it is a reliable "last probed" marker. The manual test paths do not go
// through this selector and stay immediate.
func channelDueForScheduledTest(channel *model.Channel) bool {
	setting := channel.GetSetting()
	minutes := setting.AutoTestIntervalMinutes
	if minutes <= 0 || channel.TestTime <= 0 {
		return true
	}
	return common.GetTimestamp()-channel.TestTime >= int64(channelTestIntervalMinutes(channel.Id, minutes, setting.AutoTestIntervalMaxMinutes))*60
}

// channelTestIntervalMinutes places a channel at a fixed offset inside [min,max].
// The offset comes from the channel id rather than rand so a channel keeps the
// same slot across restarts: a random draw per cycle would re-roll every channel
// every run and reconverge on the mean, which is the clumping the window exists
// to break. Siblings on one upstream have different ids, so they spread.
func channelTestIntervalMinutes(channelId, minMinutes, maxMinutes int) int {
	if maxMinutes <= minMinutes {
		return minMinutes
	}
	span := maxMinutes - minMinutes + 1
	// FNV-1a over the id: adjacent ids land far apart, unlike id % span.
	h := uint32(2166136261)
	for v := uint32(channelId); ; v >>= 8 {
		h = (h ^ (v & 0xff)) * 16777619
		if v < 0x100 {
			break
		}
	}
	return minMinutes + int(h%uint32(span))
}

// TestAllChannels enqueues a channel_test system task instead of running the
// test loop inline. If any channel_test task is already active, the manual run is
// rejected so the caller does not mistake a scheduled run for this manual one.
func TestAllChannels(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	_, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelTest, channelTestTaskPayload{
		Mode:   operation_setting.ChannelTestModeScheduledAll,
		Notify: true,
	})
	if err != nil {
		return dto.MessageResponse{}, err
	}
	if !created {
		return dto.MessageResponse{}, fmt.Errorf("a channel test task is already running or pending")
	}
	return dto.Msg("")
}

var autoSnapshotModelStatusOnce sync.Once

// AutomaticallySnapshotModelStatus runs once per minute on the master node and
// records a per-model up/down snapshot derived from the current channel table.
// A model is up iff at least one channel listing it has Status == enabled.
func AutomaticallySnapshotModelStatus() {
	if !common.IsMasterNode {
		return
	}
	autoSnapshotModelStatusOnce.Do(func() {
		for {
			if !operation_setting.GetMonitorSetting().SnapshotModelStatusEnabled {
				sleepUntilNextMinute()
				continue
			}
			for {
				runModelStatusSnapshot()
				sleepUntilNextMinute()
				if !operation_setting.GetMonitorSetting().SnapshotModelStatusEnabled {
					break
				}
			}
		}
	})
}

// sleepUntilNextMinute blocks until the next wall-clock minute boundary, plus
// a small skew so the snapshot writes for the just-finished minute reliably.
// Using `time.Sleep(60s)` after a variable-duration snapshot accumulates drift
// and skips minutes when the snapshot crosses a minute boundary.
func sleepUntilNextMinute() {
	now := time.Now()
	next := now.Truncate(time.Minute).Add(time.Minute + 500*time.Millisecond)
	time.Sleep(time.Until(next))
}

func runModelStatusSnapshot() {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		common.SysLog("model status snapshot: failed to load channels: " + err.Error())
		return
	}

	// Record the snapshot against the minute that just finished. Channel state
	// is current ("now"), traffic metrics are for the prior 60s window, and
	// both get keyed to the same minute timestamp so the row reads as "what
	// the system looked like during minute N".
	minuteIndex := time.Now().Unix()/60 - 1
	timestamp := minuteIndex * 60
	windowStart := timestamp
	windowEnd := windowStart + 60

	perModel := map[string]*model.ModelStatusPing{}

	// 1. Structural verdict from channel table. Disabled channels count only
	// while they flipped within the hide window: a lane dead for over a week
	// is catalog baggage, not lost capacity, and would otherwise pin every
	// healthy sibling model at "degraded" forever.
	recentFlip := model.ChannelIdsWithRecentTransition(timestamp - model.StatusHideAfterSeconds)
	for _, ch := range channels {
		if ch.Status != common.ChannelStatusEnabled && !recentFlip[ch.Id] {
			continue
		}
		for _, m := range strings.Split(ch.Models, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			row, ok := perModel[m]
			if !ok {
				row = &model.ModelStatusPing{Model: m, Timestamp: timestamp}
				perModel[m] = row
			}
			row.TotalChannels++
			if ch.Status == common.ChannelStatusEnabled {
				row.UpChannels++
				if ch.ResponseTime > 0 && (row.LatencyMs == 0 || ch.ResponseTime < row.LatencyMs) {
					row.LatencyMs = ch.ResponseTime
				}
			}
		}
	}

	// 2. Real-traffic metadata from log table for the just-finished minute.
	traffic, err := model.CollectModelTrafficMetrics(windowStart, windowEnd)
	if err != nil {
		common.SysLog("model status snapshot: traffic metrics failed: " + err.Error())
	} else {
		for m, t := range traffic {
			row, ok := perModel[m]
			if !ok {
				// Model has traffic but no configured channel — record a row
				// anyway so the history shows the activity. TotalChannels
				// stays 0 → status "empty".
				row = &model.ModelStatusPing{Model: m, Timestamp: timestamp}
				perModel[m] = row
			}
			row.RequestCount = t.RequestCount
			row.ErrorCount = t.ErrorCount
			row.P50LatencyMs = t.P50LatencyMs
			row.P95LatencyMs = t.P95LatencyMs
		}
	}

	// 3. Pre-compute status enum for every row.
	rows := make([]*model.ModelStatusPing, 0, len(perModel))
	modelNames := make([]string, 0, len(perModel))
	upModels := make([]string, 0, len(perModel))
	for _, r := range perModel {
		r.Status = model.ComputeModelStatus(r.UpChannels, r.TotalChannels, r.RequestCount, r.ErrorCount)
		rows = append(rows, r)
		modelNames = append(modelNames, r.Model)
		if r.UpChannels > 0 {
			upModels = append(upModels, r.Model)
		}
	}

	if err := model.InsertModelStatusPings(rows); err != nil {
		common.SysLog("model status snapshot: insert failed: " + err.Error())
		return
	}

	// 4. Auto-create page components for any new models.
	if err := model.UpsertModelStatusComponents(modelNames); err != nil {
		common.SysLog("model status snapshot: component upsert failed: " + err.Error())
	}
	if err := model.BumpModelStatusComponentsLastUp(upModels, timestamp); err != nil {
		common.SysLog("model status snapshot: last-up bump failed: " + err.Error())
	}

	// 5. Incident state machine: open on error, close on recovery.
	reconcileIncidents(rows, timestamp)

	// 6. Drop models that no longer appear in any channel. Skipped when the
	// active set is empty (treated as a transient enumeration failure rather
	// than a real "all models gone" event).
	if len(modelNames) > 0 {
		if err := model.DeleteModelStatusComponentsNotIn(modelNames); err != nil {
			common.SysLog("model status snapshot: orphan component delete failed: " + err.Error())
		}
		if err := model.DeleteOrphanIncidents(); err != nil {
			common.SysLog("model status snapshot: orphan incident delete failed: " + err.Error())
		}
	}

	// 7. Heavy ping-table maintenance once per hour. The orphan delete is a
	// NOT IN over the full ping table (no index can serve a negation); run
	// per-minute it was a full scan of 17M+ rows every 60s and the biggest
	// standing load on the DB.
	//
	// Gate on the minute this run STARTED, not on `timestamp`, and not on
	// minuteIndex%60. Two ways to get this wrong, both shipped once:
	// minuteIndex is an absolute minute count (unix/60), so minuteIndex%60==0
	// almost never coincides with a run; and `timestamp` is the PREVIOUS minute
	// (the one being recorded) while sleepUntilNextMinute wakes at HH:MM:00.5,
	// so at 02:00:00.5 it reads 01:59 and Minute() is 59, never 0. Both left the
	// retention prune dead: 30 days of rows against a 7-day policy (18.6M rows
	// / 4.85GB, 2026-08-19), then still 2.0M stale rows after the first fix.
	if time.Now().UTC().Minute() == 0 {
		if len(modelNames) > 0 {
			if err := model.DeleteModelStatusPingsNotIn(modelNames); err != nil {
				common.SysLog("model status snapshot: orphan ping delete failed: " + err.Error())
			}
		}
		retentionDays := operation_setting.GetMonitorSetting().SnapshotModelStatusRetentionDays
		if retentionDays > 0 {
			cutoffTs := timestamp - int64(retentionDays)*24*60*60
			if err := model.PruneModelStatusPingsBefore(cutoffTs); err != nil {
				common.SysLog("model status snapshot: prune failed: " + err.Error())
			}
		}
	}
}

// reconcileIncidents drives the per-component incident state machine using
// each row's pre-computed status:
//
//   - status="error" + no open incident   -> open one
//   - status="success"|"degraded" + open  -> resolve it (recovery confirmed)
//   - status="error" + open incident      -> noop (still ongoing)
//   - status="empty" + open incident      -> noop (no signal, do not resolve)
func reconcileIncidents(rows []*model.ModelStatusPing, timestamp int64) {
	for _, r := range rows {
		comp, err := model.GetComponentByModel(r.Model)
		if err != nil || comp == nil {
			continue
		}
		open, err := model.GetOpenIncidentByComponent(comp.Id)
		if err != nil {
			common.SysLog("model status snapshot: open-incident lookup failed: " + err.Error())
			continue
		}
		switch r.Status {
		case model.ModelStatusError:
			if open == nil {
				title := "All channels for " + r.Model + " are disabled"
				if err := model.OpenIncident(comp.Id, title, timestamp); err != nil {
					common.SysLog("model status snapshot: open incident failed: " + err.Error())
				}
			}
		case model.ModelStatusSuccess, model.ModelStatusDegraded:
			if open != nil {
				if err := model.ResolveIncident(open.Id, timestamp); err != nil {
					common.SysLog("model status snapshot: resolve incident failed: " + err.Error())
				}
			}
		}
	}
}

// Model-type filtering for automatic channel tests: skip image/video/audio
// models when picking the test model so disabled non-text channels are not
// hammered with bad-response errors on every scheduled run.
var nonTextModelKeywords = []string{
	"image", "dall-e", "flux", "seedream", "stable-diffusion", "imagen", "recraft", "ideogram", "midjourney",
	"video", "sora", "kling", "veo", "vidu", "jimeng", "-i2v", "-t2v", "-i2i", "-t2i", "-i2v-", "-t2v-",
	"-r2v", "-vace", "-animate", "wan2.", "wanx", "hailuo", "happyhorse", // PROD-ONLY (fork): native-task media families
	"tts", "whisper", "audio", "speech", "transcribe", "suno", "music",
}

// Embedding models testChannel can probe cheaply via /v1/embeddings. Free channels
// that serve ONLY embeddings would otherwise never be auto-tested (no text model to
// pick), so dead embedding lanes sat broken. Allow them through for free channels.
var embeddingModelKeywords = []string{
	"embedding", "embed", "bge-", "m3e", "voyage", "rerank",
}

// OpenAI-shaped image-generation models testChannel can probe via /v1/images/generations.
// Free image lanes (flux/dall-e/gpt-image) are tested when disabled; the upstream image
// call is the cost, acceptable on a free lane at the scheduled interval.
var imageGenModelKeywords = []string{
	"dall-e", "gpt-image", "flux", "seedream", "stable-diffusion", "imagen",
	"recraft", "ideogram", "sdxl",
}

func isImageGenModel(modelName string) bool {
	name := strings.ToLower(modelName)
	for _, keyword := range imageGenModelKeywords {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	return false
}

func isNonTextModel(modelName string) bool {
	name := strings.ToLower(modelName)
	for _, keyword := range nonTextModelKeywords {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	return false
}

func isEmbeddingModel(modelName string) bool {
	name := strings.ToLower(modelName)
	for _, keyword := range embeddingModelKeywords {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	return false
}

// PROD-ONLY (fork): reasoningProbeKeywords match models that default to a
// chain-of-thought (reasoning_content) even WITHOUT "thinking" in the name (GLM
// 4.6+/5.x, DeepSeek V3.1+/R1, Qwen3, MiniMax M*, Kimi K2 thinking, gpt-oss,
// magistral, ernie-x/-thinking). The tiny 16-token channel-test budget is spent
// entirely on reasoning, so content comes back empty; with DisableOnEmptyResponse
// that auto-disables a healthy channel every cron cycle. Give these a generous
// probe budget so post-reasoning content still fits.
var reasoningProbeKeywords = []string{
	"thinking", "reasoning", "reasoner",
	"glm-4.6", "glm-4.7", "glm-5", "glm-z",
	"deepseek-v3.1", "deepseek-v3.2", "deepseek-r1", "deepseek-v4",
	"qwen3", "qwq", "qvq",
	"minimax-m", "kimi-k2", "gpt-oss", "magistral", "ernie-x", "hunyuan-t", "seed-thinking",
}

// PROD-ONLY (fork): modelIsReasoning reports whether the synced catalog marks
// this model as reasoning. The keyword list below can only recognize families
// somebody already added to it, so a new reasoning model (stealth/ox-alpha,
// every Claude 4.x) probes with the 16-token budget, spends all of it on
// chain-of-thought, returns empty content and is auto-disabled forever by its
// own recovery probe. The sync writes isReasoning for exactly this purpose.
// Reads the 1-minute-cached pricing snapshot, so this costs no query per probe.
func modelIsReasoning(modelName string) bool {
	if modelName == "" || model.DB == nil {
		return false
	}
	if v, ok := reasoningMetaCache.Load(modelName); ok {
		if e := v.(reasoningMetaEntry); time.Since(e.at) < time.Minute*5 {
			return e.reasoning
		}
	}
	// Read the models table directly rather than the pricing snapshot: pricing is
	// built from abilities, and a model whose every channel is disabled is
	// exactly the case this lookup exists to rescue.
	var meta struct{ Metadata string }
	err := model.DB.Table("models").Select("metadata").
		Where("model_name = ?", modelName).Limit(1).Scan(&meta).Error
	reasoning := false
	if err == nil && meta.Metadata != "" {
		var md dto.ModelMetadata
		if common.UnmarshalJsonStr(meta.Metadata, &md) == nil {
			reasoning = md.IsReasoning
		}
	}
	reasoningMetaCache.Store(modelName, reasoningMetaEntry{reasoning: reasoning, at: time.Now()})
	return reasoning
}

// Probes run per channel on a schedule, so the same few model names repeat
// constantly. Short-lived so a re-synced flag lands without a restart.
type reasoningMetaEntry struct {
	reasoning bool
	at        time.Time
}

var reasoningMetaCache sync.Map

// PROD-ONLY (fork): reasoningProbeMaxTokens returns a probe max_tokens generous
// enough for a default-reasoning model to emit visible content after its CoT.
// Mirrors the gemini branch (3000). Non-reasoning models keep the cheap 16.
func reasoningProbeMaxTokens(modelName string, fallback uint) uint {
	name := strings.ToLower(modelName)
	for _, kw := range reasoningProbeKeywords {
		if strings.Contains(name, kw) {
			return 3000
		}
	}
	// Catalog metadata second: the keyword list stays the cheap path and still
	// covers models the sync never priced.
	if modelIsReasoning(modelName) {
		return 3000
	}
	return fallback
}

// PROD-ONLY (fork): isCronRecoverableModel reports whether the scheduled channel-test
// (pickAutoTestModel) has a probe path for this model. Keep EXACTLY aligned with
// pickAutoTestModel's recovery branch: recoverable == text OR embedding OR imageGen.
func isCronRecoverableModel(modelName string) bool {
	if !isNonTextModel(modelName) {
		return true // text (or unrecognized) -> default relay probe recovers it
	}
	return isEmbeddingModel(modelName) || isImageGenModel(modelName)
}

// PROD-ONLY (fork): client-side request faults - a bot/scraper sent a payload the
// upstream rejected (bad image, missing param, bad url, wrong-endpoint 404). The
// channel itself is healthy; the SAME bad request fails on every sibling. These are
// the only codes we spare for non-recoverable modalities. 429 (capacity) and 5xx
// (upstream down) are genuine channel faults and MUST still disable.
var requestLevelDisableStatusCodes = map[int]struct{}{
	http.StatusBadRequest:                 {}, // 400
	http.StatusNotFound:                   {}, // 404
	http.StatusNotAcceptable:              {}, // 406
	http.StatusGone:                       {}, // 410
	http.StatusRequestEntityTooLarge:      {}, // 413
	http.StatusUnsupportedMediaType:       {}, // 415
	http.StatusUnprocessableEntity:        {}, // 422
	http.StatusUnavailableForLegalReasons: {}, // 451
}

// PROD-ONLY (fork): spare a channel that ShouldDisableChannel already flagged, iff the
// model is a non-recoverable modality (audio/video/native-image the cron cannot
// re-probe) AND the error is a client-side request fault. A genuine channel fault is
// already reclassified to channel:* in types/error.go (IsChannelError short-circuits),
// and 429/5xx are not in requestLevelDisableStatusCodes, so real faults STILL disable.
func shouldSkipDisableForModality(modelName string, err *types.NewAPIError) bool {
	if err == nil || types.IsChannelError(err) {
		return false
	}
	if _, ok := requestLevelDisableStatusCodes[err.StatusCode]; !ok {
		return false
	}
	return !isCronRecoverableModel(modelName)
}

// A free channel costs nothing per call, so autotest may probe its non-text
// (embedding) models too. Detected by the ":free" published-name convention or a
// group whose name carries "free".
func isFreeChannel(channel *model.Channel) bool {
	if strings.Contains(strings.ToLower(channel.Group), "free") {
		return true
	}
	for _, m := range channel.GetModels() {
		if strings.HasSuffix(strings.TrimSpace(strings.ToLower(m)), ":free") {
			return true
		}
	}
	return false
}

// pickAutoTestModel returns the model the scheduled autotest should use, or "" to skip the channel
func pickAutoTestModel(channel *model.Channel) string {
	if channel.TestModel != nil {
		testModel := strings.TrimSpace(*channel.TestModel)
		if testModel != "" && !isNonTextModel(testModel) {
			return testModel
		}
	}
	for _, m := range channel.GetModels() {
		m = strings.TrimSpace(m)
		if m != "" && !isNonTextModel(m) {
			return m
		}
	}
	// No text model. Fall back to an embedding or image model: testChannel routes
	// them to /v1/embeddings or /v1/images/generations. PROD-ONLY (fork):
	// recovery-probe branch, split by cost:
	//   - Embedding probes are ~free (a few tokens), so probe them on a free channel
	//     OR to recover a disabled one (else a dead embedding lane can never re-enable).
	//   - Image generation costs real money PER CALL (e.g. gpt-image-2 = $0.2/probe),
	//     so ONLY probe image models on a FREE channel. A disabled PAID image channel
	//     is NOT recovery-probed (it would bill us every cron cycle); it stays disabled
	//     until real traffic or a manual re-enable.
	free := isFreeChannel(channel)
	recoverDisabled := channel.Status == common.ChannelStatusAutoDisabled
	// Async task channels (AI Horde, Kling, Vidu, Sora, ...) have no sync endpoint;
	// testChannel probes them via a submit-only task probe. Free-only (a submit could
	// bill on paid). Return the first non-text model so testChannel takes the task path.
	taskChannel := isTaskChannel(channel)
	for _, m := range channel.GetModels() {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if isEmbeddingModel(m) && (free || recoverDisabled) {
			return m
		}
		if taskChannel && isNonTextModel(m) && free {
			return m
		}
		if isImageGenModel(m) && free {
			return m
		}
	}
	return ""
}
