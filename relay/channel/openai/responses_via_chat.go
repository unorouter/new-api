package openai

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func OaiChatToResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	if responseID := helper.GetResponseID(c); responseID != "" {
		chatResp.Id = responseID
	}
	convertResult, err := service.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, &chatResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	responsesResp, ok := convertResult.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, types.NewOpenAIError(fmt.Errorf("expected OpenAI responses response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	usage := convertResult.Usage
	if usage == nil || usage.TotalTokens == 0 {
		text := service.ExtractOutputTextFromResponses(responsesResp)
		usage = service.ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
		responsesResp.Usage = relayconvert.UsageFromChatUsage(usage)
	}

	responseBody, err := common.Marshal(responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

// chatToResponsesEmitter drives one Chat -> Responses stream state and writes
// its events to the client as SSE. Shared by the live-stream handler and the
// replay handler, which differ only in where the chat chunks come from.
type chatToResponsesEmitter struct {
	c         *gin.Context
	info      *relaycommon.RelayInfo
	state     *relayconvert.ResponseStreamState
	streamErr *types.NewAPIError
}

func newChatToResponsesEmitter(c *gin.Context, info *relaycommon.RelayInfo) (*chatToResponsesEmitter, *types.NewAPIError) {
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:                 helper.GetResponseID(c),
		Model:              info.UpstreamModelName,
		EmitSequenceNumber: true,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	return &chatToResponsesEmitter{c: c, info: info, state: state}, nil
}

func (e *chatToResponsesEmitter) send(event relayconvert.ChatToResponsesStreamEvent) bool {
	data, err := common.Marshal(event.Payload)
	if err != nil {
		e.streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		return false
	}
	if err := helper.ResponseChunkData(e.c, dto.ResponsesStreamResponse{Type: event.Type}, string(data)); err != nil {
		e.streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
		return false
	}
	return true
}

func (e *chatToResponsesEmitter) sendAll(results []relayconvert.ResponseResult) bool {
	for _, result := range results {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			e.streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
			return false
		}
		if !e.send(event) {
			return false
		}
	}
	return true
}

// fail emits response.failed for err when the state can express it and reports
// whether it did, so the caller can fall back to a plain error otherwise.
func (e *chatToResponsesEmitter) fail(err error) bool {
	failureResults, handled := e.state.FailResponsesStream("server_error", err.Error(), "")
	if !handled {
		return false
	}
	e.sendAll(failureResults)
	return true
}

// chunk converts one upstream chat chunk and emits the resulting events.
// Returns false once the stream is in error; streamErr holds the cause.
func (e *chatToResponsesEmitter) chunk(data string, statusCode int) bool {
	var errorResp dto.OpenAITextResponse
	if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
		if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
			if !e.fail(fmt.Errorf("%s", oaiError.Message)) {
				e.streamErr = types.WithOpenAIError(*oaiError, statusCode)
			}
			return false
		}
	}

	var chunk dto.ChatCompletionsStreamResponse
	if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
		logger.LogError(e.c, "failed to unmarshal chat stream response: "+err.Error())
		if !e.fail(err) {
			e.streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		return false
	}
	return e.convert(&chunk)
}

func (e *chatToResponsesEmitter) convert(chunk *dto.ChatCompletionsStreamResponse) bool {
	results, err := service.ConvertStreamResponseChunk(e.c, e.info, e.state, chunk)
	if err != nil {
		if !e.fail(err) {
			e.streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		return false
	}
	return e.sendAll(results)
}

// finish fills in usage when the upstream sent none, emits the terminal events
// and returns the usage to bill.
func (e *chatToResponsesEmitter) finish() (*dto.Usage, *types.NewAPIError) {
	if e.streamErr != nil {
		return nil, e.streamErr
	}
	usage := e.state.Usage()
	if usage == nil || usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(e.c, e.state.UsageText(), e.info.UpstreamModelName, e.info.GetEstimatePromptTokens())
		e.state.SetUsage(usage)
	}
	finalResults, err := service.FinalizeStreamResponse(e.c, e.info, e.state)
	if err != nil {
		if e.fail(err) {
			return usage, e.streamErr
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	if !e.sendAll(finalResults) {
		return nil, e.streamErr
	}
	return usage, nil
}

func OaiChatToResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	emitter, newApiErr := newChatToResponsesEmitter(c, info)
	if newApiErr != nil {
		return nil, newApiErr
	}

	scannerErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if emitter.streamErr != nil {
			sr.Stop(emitter.streamErr)
			return
		}
		if !emitter.chunk(data, resp.StatusCode) {
			sr.Stop(emitter.streamErr)
		}
	})
	if scannerErr != nil {
		return nil, scannerErr
	}
	return emitter.finish()
}

// OaiChatToResponsesReplayHandler serves a client that asked for a Responses
// stream from an upstream that ignored stream:true and answered with one chat
// completion. The completion is replayed as a single chunk through the same
// state machine, so the client still gets the SSE sequence it asked for.
func OaiChatToResponsesReplayHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	chunk := dto.ChatCompletionsStreamResponse{
		Id:      chatResp.Id,
		Object:  "chat.completion.chunk",
		Model:   chatResp.Model,
		Choices: make([]dto.ChatCompletionsStreamResponseChoice, 0, len(chatResp.Choices)),
		Usage:   &chatResp.Usage,
	}
	if created, ok := chatResp.Created.(float64); ok {
		chunk.Created = int64(created)
	}
	for _, choice := range chatResp.Choices {
		delta := dto.ChatCompletionsStreamResponseChoiceDelta{Role: choice.Role}
		if content := choice.StringContent(); content != "" {
			delta.Content = &content
		}
		if reasoning := choice.GetReasoningContent(); reasoning != "" {
			delta.ReasoningContent = &reasoning
		}
		for i, call := range choice.ParseToolCalls() {
			index := i
			delta.ToolCalls = append(delta.ToolCalls, dto.ToolCallResponse{
				Index: &index,
				ID:    call.ID,
				Type:  call.Type,
				Function: dto.FunctionResponse{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
		finishReason := choice.FinishReason
		chunk.Choices = append(chunk.Choices, dto.ChatCompletionsStreamResponseChoice{
			Index:        choice.Index,
			Delta:        delta,
			FinishReason: &finishReason,
		})
	}

	emitter, newApiErr := newChatToResponsesEmitter(c, info)
	if newApiErr != nil {
		return nil, newApiErr
	}
	helper.SetEventStreamHeaders(c)
	if !emitter.convert(&chunk) {
		return nil, emitter.streamErr
	}
	return emitter.finish()
}

// OaiChatToResponsesBufferedStreamHandler serves a client that asked for one
// JSON Response from an upstream that streamed chat SSE anyway. The chunks run
// through the same Chat -> Responses state as the live handler, nothing is
// emitted, and the terminal event's Response is written as application/json,
// so tool calls and reasoning survive the aggregation.
func OaiChatToResponsesBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:    helper.GetResponseID(c),
		Model: info.UpstreamModelName,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 6 || line[:5] != "data:" {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
				return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
			}
		}
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		if _, err := service.ConvertStreamResponseChunk(c, info, state, &chunk); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	usage := state.Usage()
	if usage == nil || usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		state.SetUsage(usage)
	}
	finalResults, err := service.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	var finalResponse *dto.OpenAIResponsesResponse
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if event.Payload.Response != nil {
			finalResponse = event.Payload.Response
		}
	}
	if finalResponse == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("chat stream produced no terminal response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	responseBody, err := common.Marshal(finalResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	// nil src: the upstream Content-Type is text/event-stream and must not be
	// copied onto a JSON body.
	c.Writer.Header().Set("Content-Type", "application/json")
	service.IOCopyBytesGracefully(c, nil, responseBody)
	return usage, nil
}
