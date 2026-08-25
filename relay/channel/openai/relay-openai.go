package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAIResponseHasOutput reports whether a non-stream OpenAI chat response
// carries usable output in any choice: text content, reasoning, a tool call, or
// a legacy text completion. Mirrors chatChoiceHasOutput in the channel autotest
// so the live path and the scheduled test agree on "empty".
func openAIResponseHasOutput(resp *dto.OpenAITextResponse) bool {
	for i := range resp.Choices {
		msg := &resp.Choices[i].Message
		if strings.TrimSpace(msg.StringContent()) != "" {
			return true
		}
		if len(msg.ParseToolCalls()) > 0 {
			return true
		}
		// Reasoning-only counts as output only when the turn actually completed
		// (a thinking model whose reasoning IS the answer, e.g. Qwen). A
		// finish_reason=length with empty content means the model ran out of
		// budget mid-reasoning and never produced an answer (GLM with thinking
		// left on) - blank to the user, so treat it as empty and auto-disable.
		if strings.TrimSpace(msg.GetReasoningContent()) != "" &&
			resp.Choices[i].FinishReason != constant.FinishReasonLength {
			return true
		}
	}
	return false
}

// streamHadOutput is the streaming twin of openAIResponseHasOutput: reasoning
// counts as the answer only when the turn actually completed (Qwen-style models
// whose reasoning IS the reply). A stream that ends mid-reasoning - on a length
// ceiling, or with no finish_reason at all because the upstream just stopped -
// rendered blank to the reader, so it is an empty response no matter how many
// reasoning tokens it burned.
func streamHadOutput(stats *StreamOutputStats, toolCount int) bool {
	if toolCount > 0 {
		return true
	}
	if stats == nil {
		return false
	}
	if strings.TrimSpace(stats.Content.String()) != "" {
		return true
	}
	return stats.ReasoningChars > 0 && stats.FinishReason == constant.FinishReasonStop
}

func sendStreamData(c *gin.Context, info *relaycommon.RelayInfo, data string, forceFormat bool, thinkToContent bool) error {
	if data == "" {
		return nil
	}

	// PROD-ONLY (fork): some upstreams (yun's gemini-2.5-pro adaptor) always emit a
	// trailing usage-only chunk with `"choices":[]`, even when the client did NOT send
	// stream_options.include_usage. Fragile clients do chunk.choices[0].delta and crash
	// ("Cannot read properties of undefined (reading 'delta')"). Drop the choices-empty
	// usage chunk unless the client EXPLICITLY asked for usage (default is true but that
	// is new-api's own default, not the client's intent).
	if !info.ClientRequestedStreamUsage && gjson.Get(data, "choices").IsArray() &&
		len(gjson.Get(data, "choices").Array()) == 0 {
		return nil
	}

	if !forceFormat && !thinkToContent {
		return helper.StringData(c, data)
	}

	var lastStreamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &lastStreamResponse); err != nil {
		return err
	}

	if !thinkToContent {
		return helper.ObjectData(c, lastStreamResponse)
	}

	hasThinkingContent := false
	hasContent := false
	var thinkingContent strings.Builder
	for _, choice := range lastStreamResponse.Choices {
		if len(choice.Delta.GetReasoningContent()) > 0 {
			hasThinkingContent = true
			thinkingContent.WriteString(choice.Delta.GetReasoningContent())
		}
		if len(choice.Delta.GetContentString()) > 0 {
			hasContent = true
		}
	}

	// Handle think to content conversion
	if info.ThinkingContentInfo.IsFirstThinkingContent {
		if hasThinkingContent {
			response := lastStreamResponse.Copy()
			for i := range response.Choices {
				// send `think` tag with thinking content
				response.Choices[i].Delta.SetContentString("<think>\n" + thinkingContent.String())
				response.Choices[i].Delta.ReasoningContent = nil
				response.Choices[i].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.IsFirstThinkingContent = false
			info.ThinkingContentInfo.HasSentThinkingContent = true
			return helper.ObjectData(c, response)
		}
	}

	if lastStreamResponse.Choices == nil || len(lastStreamResponse.Choices) == 0 {
		return helper.ObjectData(c, lastStreamResponse)
	}

	// Process each choice
	for i, choice := range lastStreamResponse.Choices {
		// Handle transition from thinking to content
		// only send `</think>` tag when previous thinking content has been sent
		if hasContent && !info.ThinkingContentInfo.SendLastThinkingContent && info.ThinkingContentInfo.HasSentThinkingContent {
			response := lastStreamResponse.Copy()
			for j := range response.Choices {
				response.Choices[j].Delta.SetContentString("\n</think>\n")
				response.Choices[j].Delta.ReasoningContent = nil
				response.Choices[j].Delta.Reasoning = nil
			}
			info.ThinkingContentInfo.SendLastThinkingContent = true
			helper.ObjectData(c, response)
		}

		// Convert reasoning content to regular content if any
		if len(choice.Delta.GetReasoningContent()) > 0 {
			lastStreamResponse.Choices[i].Delta.SetContentString(choice.Delta.GetReasoningContent())
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
		} else if !hasThinkingContent && !hasContent {
			// flush thinking content
			lastStreamResponse.Choices[i].Delta.ReasoningContent = nil
			lastStreamResponse.Choices[i].Delta.Reasoning = nil
		}
	}

	return helper.ObjectData(c, lastStreamResponse)
}

// SendPendingThinkClose closes a `<think>` block left open when the stream died
// during reasoning (budget exhausted before any answer text). The closing tag is
// normally emitted on the reasoning-to-content transition, so a reasoning-only
// stream never sends it, and tag-stripping clients (JanitorAI, SillyTavern)
// cannot strip an unterminated block: the raw chain of thought renders as the
// bot's reply.
func SendPendingThinkClose(c *gin.Context, info *relaycommon.RelayInfo, responseId string, createAt int64) {
	if !info.ChannelSetting.ThinkingToContent ||
		!info.ThinkingContentInfo.HasSentThinkingContent ||
		info.ThinkingContentInfo.SendLastThinkingContent {
		return
	}
	response := dto.ChatCompletionsStreamResponse{
		Id:      responseId,
		Object:  "chat.completion.chunk",
		Created: createAt,
		Model:   info.UpstreamModelName,
		Choices: []dto.ChatCompletionsStreamResponseChoice{{}},
	}
	response.Choices[0].Delta.SetContentString("\n</think>\n")
	info.ThinkingContentInfo.SendLastThinkingContent = true
	_ = helper.ObjectData(c, &response)
}

func OaiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	defer service.CloseResponseBodyGracefully(resp)

	model := info.UpstreamModelName
	var responseId string
	var createAt int64 = 0
	var systemFingerprint string
	var containStreamUsage bool
	var responseTextBuilder strings.Builder
	var outputStats StreamOutputStats
	var toolCount int
	var usage = &dto.Usage{}
	var lastStreamData string
	var secondLastStreamData string // 存储倒数第二个stream data，用于音频模型
	seenStreamToolCalls := make(map[string]struct{})
	var streamFunctionCallNames []string

	// 检查是否为音频模型
	isAudioModel := strings.Contains(strings.ToLower(model), "audio")

	// Hold the leading role/empty opener chunks until the first real content (or
	// tool-call, or reasoning - responseTextBuilder counts all three) arrives, so a
	// stream that produces nothing usable never commits a 200 to the client and can
	// still fail over to a sibling channel. Once real content appears, the buffered
	// openers are flushed in order and streaming proceeds normally. Guarded by the
	// operator flag; disabled -> original passthrough behavior.
	bufferEmptyOpener := info.RelayFormat == types.RelayFormatOpenAI &&
		operation_setting.GetMonitorSetting().DisableOnEmptyResponse
	streamingStarted := !bufferEmptyOpener
	var pendingFlush []string
	var streamErrChunk string // PROD-ONLY (fork): raw mid-stream error chunk; "" = none
	sendChunk := func(sr *helper.StreamResult, data string) {
		if err := HandleStreamFormat(c, info, data, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent); err != nil {
			common.SysLog("error handling stream format: " + err.Error())
			sr.Error(err)
		}
	}

	if streamErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if lastStreamData != "" {
			if !streamingStarted && (responseTextBuilder.Len() > 0 || toolCount > 0) {
				// First real content has landed: release the held openers, then stream live.
				streamingStarted = true
				for _, d := range pendingFlush {
					sendChunk(sr, d)
				}
				pendingFlush = pendingFlush[:0]
			}
			if streamingStarted {
				sendChunk(sr, lastStreamData)
			} else {
				pendingFlush = append(pendingFlush, lastStreamData)
			}
		}
		if len(data) > 0 {
			// PROD-ONLY (fork): an upstream that rejects mid-stream (Aliyun content
			// moderation "data_inspection_failed", a provider 5xx after first byte)
			// sends a top-level {"error":{...}} SSE chunk. The success DTO has no
			// error field, so processTokenData would parse it to an empty struct and
			// the chunk would forward verbatim - leaking the raw upstream body/URL to
			// the client and never logging a type=5 row. Intercept it here, before it
			// becomes lastStreamData or is forwarded; the post-stream block returns it
			// as a real NewAPIError (masked + logged + failover).
			if gjson.Get(data, "error").Exists() {
				streamErrChunk = data
				sr.Stop(errors.New("upstream stream error chunk"))
				return
			}

			// 对音频模型，保存倒数第二个stream data
			if isAudioModel && lastStreamData != "" {
				secondLastStreamData = lastStreamData
			}

			lastStreamData = data
			collectStreamFunctionCallNames(data, seenStreamToolCalls, &streamFunctionCallNames)
			if err := processTokenData(info.RelayMode, data, &responseTextBuilder, &toolCount, &outputStats); err != nil {
				logger.LogError(c, "error processing stream token data: "+err.Error())
				sr.Error(err)
			}
		}
	}); streamErr != nil {
		return nil, streamErr
	}

	// PROD-ONLY (fork): a mid-stream error chunk was intercepted. Return it as a
	// real error BEFORE the empty-response classification and before any forward of
	// lastStreamData, so it flows through processChannelError (masking + type=5 log
	// + failover). usage is still the zero &dto.Usage{} - nothing billable.
	if streamErrChunk != "" {
		return usage, buildStreamErrorAPIError(streamErrChunk, streamingStarted)
	}

	// 对音频模型，从倒数第二个stream data中提取usage信息
	if isAudioModel && secondLastStreamData != "" {
		var streamResp struct {
			Usage *dto.Usage `json:"usage"`
		}
		err := common.Unmarshal([]byte(secondLastStreamData), &streamResp)
		if err == nil && streamResp.Usage != nil && service.ValidUsage(streamResp.Usage) {
			usage = streamResp.Usage
			containStreamUsage = true

			if common.DebugEnabled {
				logger.LogDebug(c, "Audio model usage extracted from second last SSE: PromptTokens=%d, CompletionTokens=%d, TotalTokens=%d, InputTokens=%d, OutputTokens=%d",
					usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
					usage.InputTokens, usage.OutputTokens)
			}
		}
	}

	// 处理最后的响应
	shouldSendLastResp := true
	if err := handleLastResponse(lastStreamData, &responseId, &createAt, &systemFingerprint, &model, &usage,
		&containStreamUsage, info, &shouldSendLastResp); err != nil {
		logger.LogError(c, fmt.Sprintf("error handling last response: %s, lastStreamData: [%s]", err.Error(), lastStreamData))
	}

	if info.RelayFormat == types.RelayFormatOpenAI {
		if shouldSendLastResp {
			_ = sendStreamData(c, info, lastStreamData, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
		}
		SendPendingThinkClose(c, info, responseId, createAt)
	}

	if !containStreamUsage {
		usage = service.ResponseText2Usage(c, responseTextBuilder.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		usage.CompletionTokens += toolCount * 7
	}

	applyUsagePostProcessing(info, usage, common.StringToByteSlice(lastStreamData))

	for _, name := range streamFunctionCallNames {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, name)
	}

	// A stream that finished with no usable output (empty content, no tool calls).
	// Two shapes: (a) the upstream opened the SSE and closed it having flushed real
	// content deltas that were all blank / reasoning-only (a dead reasoning channel,
	// e.g. GLM with thinking left on that burned its budget on reasoning_content) -
	// bytes are already on the wire, so we can't fail over; (b) the upstream accepted
	// the request and sent ZERO usable data (a silent free-tier reseller under load).
	// In case (b) nothing real was flushed, so we can still fail over to a sibling.
	//
	// Judged on outputStats, NOT responseTextBuilder: the builder also holds
	// reasoning because it feeds billing, so testing it for emptiness only ever
	// caught shape (b) and was blind to every reasoning-only stream.
	emptyResponse := info.RelayFormat == types.RelayFormatOpenAI &&
		operation_setting.GetMonitorSetting().DisableOnEmptyResponse &&
		!streamHadOutput(&outputStats, toolCount) &&
		!streamFinishedOnContentFilter(lastStreamData)

	// Case (b): the opener chunks were buffered (streamingStarted stayed false), so
	// nothing was flushed and the response is uncommitted. Return a retryable error -
	// no [DONE], no skip-retry - so the outer loop fails over to another channel.
	// 429, not 5xx: chat frontends replace any 5xx body with their own generic text,
	// so the reason never reaches the reader. 429 is rendered verbatim by those
	// clients and by Cloudflare, and it points at the correct action (retry).
	if emptyResponse && !streamingStarted {
		return usage, types.NewOpenAIError(
			fmt.Errorf("the upstream provider returned an empty reply. The request has already been failed over to any other provider serving this model; retrying usually clears it"),
			types.ErrorCodeChannelEmptyResponse, http.StatusTooManyRequests,
			emptyResponseDisableOption(info)...)
	}

	HandleFinalResponse(c, info, lastStreamData, responseId, createAt, model, systemFingerprint, usage, containStreamUsage)

	// Case (a): bytes were already streamed, so this can't fail over or re-write the
	// client response; skip retry and let the outer defer skip the JSON error write
	// (guarded on Written()). The error still runs processChannelError to disable.
	if emptyResponse {
		return usage, types.NewOpenAIError(
			fmt.Errorf("the upstream provider returned an empty reply after the response had already started, so it could not be retried automatically. Send the message again"),
			types.ErrorCodeChannelEmptyResponse, http.StatusServiceUnavailable,
			types.ErrOptionWithSkipRetry())
	}

	return usage, nil
}

// emptyResponseDisableOption returns ErrOptionWithSkipDisable while the channel is
// below the empty-response disable threshold: a lone transient empty fails over
// without banning the channel, but a dead channel still disables once it
// accumulates enough empties (counted in Redis across replicas - required because
// the autotest cron only probes DISABLED channels, so live disable is the only way
// a dead-but-enabled channel leaves rotation).
func emptyResponseDisableOption(info *relaycommon.RelayInfo) []types.NewAPIErrorOptions {
	if service.RecordEmptyResponseFailure(info.ChannelId) {
		return nil
	}
	return []types.NewAPIErrorOptions{types.ErrOptionWithSkipDisable()}
}

// buildStreamErrorAPIError turns an intercepted mid-stream {"error":{...}} chunk
// into a NewAPIError. PROD-ONLY (fork). The message is left RAW: masking happens
// downstream in processChannelError -> MaskSensitiveErrorWithStatusCode. A
// moderation reject (inappropriate content / data_inspection_failed) is mapped to
// 400 so IsUpstreamModerationError matches - failover to a sibling, do NOT disable
// the channel. WithOpenAIError marks ErrorTypeOpenAIError (required for those
// classifiers). When bytes were already committed (streamingStarted), skip retry
// to avoid a double-send; the type=5 log still runs.
func buildStreamErrorAPIError(errChunk string, streamingStarted bool) *types.NewAPIError {
	e := gjson.Get(errChunk, "error")
	msg := e.Get("message").String()
	if msg == "" {
		msg = "upstream returned an error mid-stream"
	}
	status := http.StatusBadGateway
	if n := e.Get("code"); n.Type == gjson.Number && n.Int() >= 100 && n.Int() <= 599 {
		status = int(n.Int())
	} else {
		lower := strings.ToLower(msg + " " + e.Get("code").String() + " " + e.Get("type").String())
		if strings.Contains(lower, "inappropriate") || strings.Contains(lower, "data_inspection") ||
			strings.Contains(lower, "datainspectionfailed") {
			status = http.StatusBadRequest
		}
	}
	oaiErr := types.OpenAIError{
		Message: msg,
		Type:    e.Get("type").String(),
		Code:    e.Get("code").Value(),
	}
	var opts []types.NewAPIErrorOptions
	if streamingStarted {
		opts = append(opts, types.ErrOptionWithSkipRetry())
	}
	return types.WithOpenAIError(oaiErr, status, opts...)
}

// streamFinishedOnContentFilter reports whether the final stream chunk carried a
// content_filter finish reason, i.e. a legitimate upstream refusal rather than a
// dead/empty channel.
func streamFinishedOnContentFilter(lastStreamData string) bool {
	if lastStreamData == "" {
		return false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lastStreamData), "data:"))
	if payload == "" || payload == "[DONE]" || payload[0] != '{' {
		return false
	}
	for _, choice := range gjson.Get(payload, "choices").Array() {
		if choice.Get("finish_reason").String() == constant.FinishReasonContentFilter {
			return true
		}
	}
	return false
}

func collectStreamFunctionCallNames(data string, seen map[string]struct{}, names *[]string) {
	var streamResponse dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
		return
	}
	for _, choice := range streamResponse.Choices {
		for i, tc := range choice.Delta.ToolCalls {
			name := tc.Function.Name
			if name == "" {
				continue
			}
			toolIdx := i
			if tc.Index != nil {
				toolIdx = *tc.Index
			}
			key := fmt.Sprintf("%d-%d", choice.Index, toolIdx)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			*names = append(*names, name)
		}
	}
}

func OpenaiHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	var simpleResponse dto.OpenAITextResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	logger.LogDebug(c, "upstream response body: %s", common.ElideBase64(string(responseBody)))
	// Unmarshal to simpleResponse
	if info.ChannelType == constant.ChannelTypeOpenRouter && info.ChannelOtherSettings.IsOpenRouterEnterprise() {
		// 尝试解析为 openrouter enterprise
		var enterpriseResponse openrouter.OpenRouterEnterpriseResponse
		err = common.Unmarshal(responseBody, &enterpriseResponse)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		if enterpriseResponse.Success {
			responseBody = enterpriseResponse.Data
		} else {
			logger.LogError(c, fmt.Sprintf("openrouter enterprise response success=false, data: %s", enterpriseResponse.Data))
			return nil, types.NewOpenAIError(fmt.Errorf("openrouter response success=false"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
	}

	err = common.Unmarshal(responseBody, &simpleResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	if oaiError := simpleResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	contentFiltered := false
	for _, choice := range simpleResponse.Choices {
		if choice.FinishReason == constant.FinishReasonContentFilter {
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "openai_finish_reason=content_filter")
			contentFiltered = true
			break
		}
	}

	for _, choice := range simpleResponse.Choices {
		for _, tc := range choice.Message.ParseToolCalls() {
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, tc.Function.Name)
		}
	}

	// A 200 with no usable output (empty choices / blank content, no tool call)
	// means the channel is effectively dead (e.g. an upstream quota wall that still
	// returns 200). Classify it as a channel-empty-response fault so the request fails
	// over to a sibling. The full body is buffered here (nothing committed to the
	// client yet), so failover is always clean.
	// Guarded: only OpenAI-format chat, only when the operator flag is on, and never
	// for a legitimate content-filter refusal (which is a valid non-empty verdict).
	if info.RelayFormat == types.RelayFormatOpenAI && !contentFiltered &&
		operation_setting.GetMonitorSetting().DisableOnEmptyResponse &&
		!openAIResponseHasOutput(&simpleResponse) {
		return nil, types.NewOpenAIError(
			fmt.Errorf("upstream returned an empty response (no choices/content)"),
			types.ErrorCodeChannelEmptyResponse, http.StatusTooManyRequests,
			emptyResponseDisableOption(info)...)
	}

	forceFormat := false
	if info.ChannelSetting.ForceFormat {
		forceFormat = true
	}

	usageModified := false
	// Each side is repaired independently: an upstream can report a real prompt
	// count alongside a stub completion count (some report every completion as
	// 0 while all the output rides in reasoning), and nesting the completion
	// repair under the prompt check billed those responses as free.
	promptStubbed := service.IsStubTokenCount(simpleResponse.Usage.PromptTokens)
	completionStubbed := service.IsStubTokenCount(simpleResponse.Usage.CompletionTokens)
	if promptStubbed || completionStubbed {
		promptTokens := simpleResponse.Usage.PromptTokens
		if promptStubbed {
			promptTokens = info.GetEstimatePromptTokens()
		}
		completionTokens := simpleResponse.Usage.CompletionTokens
		if completionStubbed {
			completionTokens = 0
			for _, choice := range simpleResponse.Choices {
				ctkm := service.CountTextToken(choice.Message.StringContent()+choice.Message.GetReasoningContent(), info.UpstreamModelName)
				completionTokens += ctkm
			}
			common.SetContextKey(c, constant.ContextKeyLocalCountTokens, true)
		}
		simpleResponse.Usage = dto.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
		usageModified = true
	}

	applyUsagePostProcessing(info, &simpleResponse.Usage, responseBody)

	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		if usageModified {
			var bodyMap map[string]interface{}
			err = common.Unmarshal(responseBody, &bodyMap)
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			bodyMap["usage"] = simpleResponse.Usage
			responseBody, _ = common.Marshal(bodyMap)
		}
		if forceFormat {
			responseBody, err = common.Marshal(simpleResponse)
			if err != nil {
				return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		} else {
			break
		}
	case types.RelayFormatClaude:
		convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatClaude, &simpleResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		claudeRespStr, err := common.Marshal(convertResult.Value)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = claudeRespStr
	case types.RelayFormatGemini:
		convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatGemini, &simpleResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		geminiRespStr, err := common.Marshal(convertResult.Value)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = geminiRespStr
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)

	return &simpleResponse.Usage, nil
}
