package relay

import (
	"bytes"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	openaichannel "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// responsesViaChatCompletions converts a Responses API request to a Chat
// Completions request, sends it upstream via /v1/chat/completions, and
// converts the response back to Responses format. This is the inverse of
// textRequestViaResponses and serves every OpenAI-chat upstream that does not
// speak the Responses API natively. The response handler is chosen by what
// the CLIENT asked for, not by what the upstream sent back: a client that
// asked to stream gets Responses SSE even if the upstream answered with one
// JSON body, and a client that did not gets one JSON body even if the
// upstream streamed.
func responsesViaChatCompletions(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, request *dto.OpenAIResponsesRequest) (*dto.Usage, *types.NewAPIError) {
	chatReq, err := service.ResponsesRequestToChatCompletionsRequest(request)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	info.AppendRequestConversion(types.RelayFormatOpenAI)

	savedRelayMode := info.RelayMode
	savedRequestURLPath := info.RequestURLPath
	defer func() {
		info.RelayMode = savedRelayMode
		info.RequestURLPath = savedRequestURLPath
	}()

	info.RelayMode = relayconstant.RelayModeChatCompletions
	info.RequestURLPath = "/v1/chat/completions"

	convertedRequest, err := adaptor.ConvertOpenAIRequest(c, info, chatReq)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings, info.ChannelSetting.PassThroughBodyEnabled)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info, c.Writer.Header())
		if err != nil {
			return nil, newAPIErrorFromParamOverride(err)
		}
	}

	var requestBody io.Reader = bytes.NewBuffer(jsonData)

	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	if resp == nil {
		return nil, types.NewOpenAIError(nil, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	httpResp := resp.(*http.Response)
	clientStream := info.IsStream
	upstreamStream := isResponsesEventStreamContentType(httpResp.Header.Get("Content-Type"))
	info.IsStream = clientStream || upstreamStream
	if httpResp.StatusCode != http.StatusOK {
		newApiErr := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
		service.ResetStatusCode(newApiErr, statusCodeMappingStr)
		return nil, newApiErr
	}

	var usage *dto.Usage
	var newApiErr *types.NewAPIError
	switch {
	case upstreamStream && clientStream:
		usage, newApiErr = openaichannel.OaiChatToResponsesStreamHandler(c, info, httpResp)
	case upstreamStream:
		info.IsStream = false
		usage, newApiErr = openaichannel.OaiChatToResponsesBufferedStreamHandler(c, info, httpResp)
	case clientStream:
		usage, newApiErr = openaichannel.OaiChatToResponsesReplayHandler(c, info, httpResp)
	default:
		usage, newApiErr = openaichannel.OaiChatToResponsesHandler(c, info, httpResp)
	}
	if newApiErr != nil {
		service.ResetStatusCode(newApiErr, statusCodeMappingStr)
		return nil, newApiErr
	}
	return usage, nil
}

// shouldResponsesUseChatCompletions returns true when a /v1/responses request
// should be internally converted to /v1/chat/completions.
//
// Converting is the DEFAULT because most upstreams behind an OpenAI-shaped
// channel serve chat completions only. Such an upstream answers /v1/responses
// with a router-level 404 whose body is plain text, so it parses into an error
// with an empty message that no keyword or status rule can classify, and the
// request fails over across every sibling collecting the same 404.
//
// The opposite default was tried and does not hold: it required marking every
// chat-only channel, and the sync rebuilds channel settings from scratch on each
// run, so those marks were wiped and the 404s came back. Far fewer channels
// serve Responses natively than do not, so the burden belongs on them.
//
// A channel opts out with capabilities.responses=true, and channel types that
// are Responses-native by definition (Codex, and relays that proxy the whole
// OpenAI surface) never convert. Adaptors that do not speak OpenAI chat on the
// wire never convert either: the detour would parse a Claude or Gemini body as
// OpenAI chat, and those adaptors already own Responses through their own
// ConvertOpenAIResponsesRequest and DoResponse.
func shouldResponsesUseChatCompletions(info *relaycommon.RelayInfo) bool {
	switch info.ApiType {
	case constant.APITypeAnthropic, constant.APITypeAws, constant.APITypeGemini, constant.APITypeVertexAi:
		return false
	}
	modelToCheck := info.UpstreamModelName
	if modelToCheck == "" {
		modelToCheck = info.OriginModelName
	}
	if common.IsImageGenerationModel(modelToCheck) {
		return true
	}
	if caps := info.ChannelSetting.Capabilities; caps != nil && caps.Responses != nil {
		return !*caps.Responses
	}
	return !channelTypeServesResponses(info.ChannelType)
}

// channelTypeServesResponses reports whether a channel type speaks the Responses
// API natively, so an unmarked channel of that type is left alone. Derived from
// GetEndpointTypesByChannelType, which is the same declaration the catalog uses.
func channelTypeServesResponses(channelType int) bool {
	for _, ep := range common.GetEndpointTypesByChannelType(channelType, "") {
		if ep == types.EndpointTypeOpenAIResponse {
			return true
		}
	}
	return false
}
