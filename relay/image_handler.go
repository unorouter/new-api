package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

func ImageHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	imageReq, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.ImageRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(imageReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ImageRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	var requestBody io.Reader

	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		// Even in pass-through, the upstream must receive the model-mapped name, not
		// the published alias (e.g. "z-image-turbo", not "z-image-turbo:free" which
		// DashScope 404s). Rewrite only the top-level model field, leaving every
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
		requestBody = bytes.NewReader(body)
	} else {
		convertedRequest, err := adaptor.ConvertImageRequest(c, info, *request)
		if err != nil {
			// Request conversion is deterministic (e.g. model does not support image
			// generation); every channel would fail identically. Skip retry so a bad
			// request fails fast instead of thrashing the whole pool.
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

		switch convertedRequest.(type) {
		case *bytes.Buffer:
			requestBody = convertedRequest.(io.Reader)
		default:
			jsonData, err := common.Marshal(convertedRequest)
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

			// Unconditional: this is the ONLY view of what the provider actually
			// receives after conversion and param override. A knob the caller sent
			// that was rewritten or stripped here is otherwise indistinguishable
			// from one the provider accepted and ignored.
			logger.LogInfo(c, fmt.Sprintf("image outbound body: channel=%d model=%s body=%s",
				info.ChannelId, info.OriginModelName, common.ElideBase64(string(jsonData))))
			body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			defer closer.Close()
			jsonData = nil
			requestBody = body
		}
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		// Pairs with the outbound-body line above: an upstream that accepted the
		// request but ignored a knob answers 200 like any other success, so the
		// status is the only marker separating that from a rejected param.
		logger.LogInfo(c, fmt.Sprintf("image upstream response: channel=%d model=%s status=%d",
			info.ChannelId, info.OriginModelName, httpResp.StatusCode))
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			if httpResp.StatusCode == http.StatusCreated && info.ApiType == constant.APITypeReplicate {
				// replicate channel returns 201 Created when using Prefer: wait, treat it as success.
				httpResp.StatusCode = http.StatusOK
			} else {
				newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
				// reset status code 重置状态码
				service.ResetStatusCode(newAPIError, statusCodeMappingStr)
				return newAPIError
			}
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	imageN := uint(1)
	if request.N != nil {
		imageN = *request.N
	}

	// Image upstreams often report no usage at all. Stamping a literal 1 made the
	// prompt cost one token however long it was, so token stats and per-channel
	// cost were meaningless for every image channel. Count the prompt locally
	// instead, the same way the chat path does when an upstream omits usage.
	imageUsage := usage.(*dto.Usage)
	if service.IsStubTokenCount(imageUsage.PromptTokens) {
		imageUsage.PromptTokens = service.CountTextToken(request.Prompt, request.Model)
	}
	if imageUsage.TotalTokens == 0 {
		imageUsage.TotalTokens = imageUsage.PromptTokens + imageUsage.CompletionTokens
	}

	quality := request.Quality
	if quality == "" {
		quality = "standard"
	}

	var logContent []string

	if len(request.Size) > 0 {
		logContent = append(logContent, fmt.Sprintf("Size %s", request.Size))
	}
	if len(quality) > 0 {
		logContent = append(logContent, fmt.Sprintf("Quality %s", quality))
	}
	if imageN > 0 {
		logContent = append(logContent, fmt.Sprintf("Count %d", imageN))
	}

	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), logContent)
	return nil
}
