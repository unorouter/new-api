package relay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/samber/lo"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

// Clients that derive max output from the CONTEXT window send values no
// provider accepts (a JanitorAI-class client sent 958318 against Gemini's 65536
// cap), and the upstream 400 is retried across every sibling channel with the
// same doomed payload, so one request burns a minute before surfacing. We
// already publish the real cap on /v1/models; clamp instead of trusting the
// client to read it.
//
// Only trims a value ABOVE the model's own limit, and ignores absent or
// implausible metadata (492 of 1161 models carry no maxOutputTokens, and a
// couple record 0 or 1), so a missing limit can never zero out a live request.
const minCredibleOutputLimit = 256

func clampMaxTokensToModelLimit(modelName string, request *dto.GeneralOpenAIRequest) {
	applyOutputLimit(model.GetModelLimits(modelName).MaxOutputTokens, request)
}

func applyOutputLimit(limit int, request *dto.GeneralOpenAIRequest) {
	if limit < minCredibleOutputLimit {
		return
	}
	capped := uint(limit)
	for _, field := range []**uint{&request.MaxTokens, &request.MaxCompletionTokens} {
		if *field != nil && **field > capped {
			*field = &capped
		}
	}
}

func TextHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	textReq, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.GeneralOpenAIRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(textReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to GeneralOpenAIRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if request.WebSearchOptions != nil {
		c.Set("chat_completion_web_search_context_size", request.WebSearchOptions.SearchContextSize)
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	clampMaxTokensToModelLimit(info.OriginModelName, request)

	// 强制上游流式：客户端发送 stream=false 且请求合格时，将 stream 改写为 true 让上游走 SSE，
	// 响应层会把 SSE 聚合成一次性 JSON 返回给客户端。用来规避上游 reseller 网关对长响应的 30s header timeout。
	if !info.ClientWantsStream &&
		operation_setting.IsForceUpstreamStreamingEnabled() &&
		isForceStreamEligibleOpenAI(request, info) {
		request.Stream = lo.ToPtr(true)
		info.IsStream = true
		info.ForceUpstreamStream = true
	}

	includeUsage := true
	// 判断用户是否需要返回使用情况
	if request.StreamOptions != nil {
		includeUsage = request.StreamOptions.IncludeUsage
		// PROD-ONLY (fork): remember the client asked for usage explicitly.
		info.ClientRequestedStreamUsage = request.StreamOptions.IncludeUsage
	}

	// 如果不支持StreamOptions，将StreamOptions设置为nil
	if !info.SupportStreamOptions || !lo.FromPtrOr(request.Stream, false) {
		request.StreamOptions = nil
	} else {
		// 如果支持StreamOptions，且请求中没有设置StreamOptions，根据配置文件设置StreamOptions
		if constant.ForceStreamOption {
			request.StreamOptions = &dto.StreamOptions{
				IncludeUsage: true,
			}
		}
	}

	info.ShouldIncludeUsage = includeUsage

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	passThroughGlobal := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	if info.RelayMode == relayconstant.RelayModeChatCompletions &&
		!passThroughGlobal &&
		!info.ChannelSetting.PassThroughBodyEnabled &&
		service.ShouldChatCompletionsUseResponsesGlobal(info.ChannelId, info.ChannelType, info.OriginModelName) {
		applySystemPromptIfNeeded(c, info, request)
		usage, newApiErr := chatCompletionsViaResponses(c, info, adaptor, request)
		if newApiErr != nil {
			return newApiErr
		}

		var containAudioTokens = usage.CompletionTokenDetails.AudioTokens > 0 || usage.PromptTokensDetails.AudioTokens > 0
		var containsAudioRatios = ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)

		if containAudioTokens && containsAudioRatios {
			service.PostAudioConsumeQuota(c, info, usage, "")
		} else {
			service.PostTextConsumeQuota(c, info, usage, nil)
		}
		return nil
	}

	var requestBody io.Reader

	if passThroughGlobal || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		// Even in pass-through, the upstream must receive the model-mapped name, not
		// the published alias (e.g. "gemini-embedding-2", not "gemini-embedding-2:free"
		// which Google 404s). Rewrite only the top-level model field, leaving every
		// other provider-specific field byte-identical.
		body, err := io.ReadAll(common.NewReplayableBodyReader(storage))
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if info.IsModelMapped && info.UpstreamModelName != "" {
			if mapped, mErr := sjson.SetBytes(body, "model", info.UpstreamModelName); mErr == nil {
				body = mapped
			}
		}
		if common.DebugEnabled {
			logger.LogDebug(c, "requestBody: %s", common.ElideBase64(string(body)))
		}
		requestBody = bytes.NewReader(body)
	} else {
		convertedRequest, err := adaptor.ConvertOpenAIRequest(c, info, request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

		if info.ChannelSetting.SystemPrompt != "" {
			// 如果有系统提示，则将其添加到请求中
			request, ok := convertedRequest.(*dto.GeneralOpenAIRequest)
			if ok {
				containSystemPrompt := false
				for _, message := range request.Messages {
					if message.Role == request.GetSystemRoleName() {
						containSystemPrompt = true
						break
					}
				}
				if !containSystemPrompt {
					// 如果没有系统提示，则添加系统提示
					systemMessage := dto.Message{
						Role:    request.GetSystemRoleName(),
						Content: info.ChannelSetting.SystemPrompt,
					}
					request.Messages = append([]dto.Message{systemMessage}, request.Messages...)
				} else if info.ChannelSetting.SystemPromptOverride {
					common.SetContextKey(c, constant.ContextKeySystemPromptOverride, true)
					// 如果有系统提示，且允许覆盖，则拼接到前面
					for i, message := range request.Messages {
						if message.Role == request.GetSystemRoleName() {
							if message.IsStringContent() {
								request.Messages[i].SetStringContent(info.ChannelSetting.SystemPrompt + "\n" + message.StringContent())
							} else {
								contents := message.ParseContent()
								contents = append([]dto.MediaContent{
									{
										Type: dto.ContentTypeText,
										Text: info.ChannelSetting.SystemPrompt,
									},
								}, contents...)
								request.Messages[i].Content = contents
							}
							break
						}
					}
				}
			}
		}

		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeJsonMarshalFailed, types.ErrOptionWithSkipRetry())
		}

		// Detect adapter-level silent drops (e.g. Claude rejecting min_p / top_a /
		// penalties, Gemini rejecting top_k on Google direct). Diff the inbound
		// request struct against the converted struct and emit the same
		// x-newapi-dropped-params header that ApplyParamOverride uses, so the
		// downstream BFF (unorouter) can surface a single toast regardless of
		// whether the drop came from config or from the adaptor.
		if originalJSON, oErr := common.Marshal(request); oErr == nil {
			if dropped := relaycommon.DetectSilentAdapterDrops(originalJSON, jsonData); len(dropped) > 0 {
				relaycommon.EmitDroppedParamsHeader(c.Writer.Header(), dropped)
			}
		}

		// remove disabled fields for OpenAI API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override (also emits x-newapi-dropped-params for stripped knobs)
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info, c.Writer.Header())
			if err != nil {
				return newAPIErrorFromParamOverride(err)
			}
		}

		logger.LogDebug(c, "text request body: %s", common.ElideBase64(string(jsonData)))

		// Guard against forwarding a chat-completions request with no messages. A
		// malformed/empty messages array is the client's fault and deterministic,
		// so every channel returns the same "field messages is required" error;
		// fail once with skip_retry instead of burning the whole retry chain (and
		// tripping auto_ban on every channel). Check the INBOUND request, not the
		// converted body: a Gemini channel (type 24) has already turned `messages`
		// into `contents`, so grepping jsonData for "messages" would false-positive
		// on a perfectly valid converted request.
		if info.RelayMode == relayconstant.RelayModeChatCompletions &&
			len(request.Messages) == 0 {
			return types.NewErrorWithStatusCode(
				errors.New("field messages is required"),
				types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}

		body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		jsonData = nil
		requestBody = body
	}

	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			newApiErr := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newApiErr, statusCodeMappingStr)
			return newApiErr
		}
	}

	usage, newApiErr := adaptor.DoResponse(c, httpResp, info)
	if newApiErr != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newApiErr, statusCodeMappingStr)
		return newApiErr
	}

	var containAudioTokens = usage.(*dto.Usage).CompletionTokenDetails.AudioTokens > 0 || usage.(*dto.Usage).PromptTokensDetails.AudioTokens > 0
	var containsAudioRatios = ratio_setting.ContainsAudioRatio(info.OriginModelName) || ratio_setting.ContainsAudioCompletionRatio(info.OriginModelName)

	if containAudioTokens && containsAudioRatios {
		service.PostAudioConsumeQuota(c, info, usage.(*dto.Usage), "")
	} else {
		service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	}
	return nil
}

// isForceStreamEligibleOpenAI decides whether a non-streaming OpenAI-format
// request is safe to transparently upgrade to upstream SSE + aggregation.
// Text, reasoning, and tool calls are all handled by the aggregator, so the
// only gate is the thinking-model blacklist (response shape for those models
// is still evolving upstream and best left untouched).
func isForceStreamEligibleOpenAI(request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) bool {
	if request == nil {
		return false
	}
	for _, m := range model_setting.GetGlobalSettings().ThinkingModelBlacklist {
		if m == info.OriginModelName {
			return false
		}
	}
	return true
}
