package openai

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// choiceAggregator tracks streamed deltas for a single choice index so the final
// non-stream response can be reconstructed. Supports text, reasoning, and tool
// call deltas.
type choiceAggregator struct {
	index            int
	role             string
	content          strings.Builder
	reasoningContent strings.Builder
	finishReason     string
	// toolCalls maps tool-call index -> accumulated call. Order preserved in
	// toolCallOrder so output matches the sequence upstream streamed.
	toolCalls      map[int]*toolCallAggregator
	toolCallOrder  []int
}

// toolCallAggregator accumulates streamed fragments of a single tool call.
// Upstream typically sends id+name+type in the first delta and argument
// fragments across subsequent deltas, all keyed by the same Index.
type toolCallAggregator struct {
	id        string
	callType  string
	name      string
	arguments strings.Builder
}

// OaiStreamToJsonHandler consumes an SSE response from upstream but DOES NOT
// write SSE to the client. Instead it rebuilds a single OpenAITextResponse
// (or the Claude/Gemini equivalent via the existing format-conversion path in
// OpenaiHandler) and writes it as a single JSON body.
//
// This is used when the client originally sent stream=false but the relay
// forced stream=true upstream to avoid reseller header-wait timeouts on long
// responses. See relay/compatible_handler.go for the eligibility gate.
func OaiStreamToJsonHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(errors.New("invalid response or response body"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	var (
		responseId        string
		object            string
		created           int64
		model             = info.UpstreamModelName
		systemFingerprint *string
		usage             *dto.Usage
		choicesByIndex    = map[int]*choiceAggregator{}
		orderedIndexes    []int
	)

	streamErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if data == "" {
			return
		}
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			// ignore unparseable keep-alive or comment frames
			return
		}
		if responseId == "" && chunk.Id != "" {
			responseId = chunk.Id
		}
		if object == "" && chunk.Object != "" {
			object = chunk.Object
		}
		if created == 0 && chunk.Created != 0 {
			created = chunk.Created
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.SystemFingerprint != nil && systemFingerprint == nil {
			systemFingerprint = chunk.SystemFingerprint
		}
		if chunk.Usage != nil && service.ValidUsage(chunk.Usage) {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			agg, ok := choicesByIndex[choice.Index]
			if !ok {
				agg = &choiceAggregator{index: choice.Index}
				choicesByIndex[choice.Index] = agg
				orderedIndexes = append(orderedIndexes, choice.Index)
			}
			if choice.Delta.Role != "" {
				agg.role = choice.Delta.Role
			}
			if s := choice.Delta.GetContentString(); s != "" {
				agg.content.WriteString(s)
			}
			if s := choice.Delta.GetReasoningContent(); s != "" {
				agg.reasoningContent.WriteString(s)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				agg.finishReason = *choice.FinishReason
			}
			// Tool call delta: keyed by tool_call.Index. First chunk usually carries
			// id+type+name, later chunks append arguments fragments.
			for _, tc := range choice.Delta.ToolCalls {
				tcIdx := 0
				if tc.Index != nil {
					tcIdx = *tc.Index
				}
				if agg.toolCalls == nil {
					agg.toolCalls = map[int]*toolCallAggregator{}
				}
				ta, ok := agg.toolCalls[tcIdx]
				if !ok {
					ta = &toolCallAggregator{}
					agg.toolCalls[tcIdx] = ta
					agg.toolCallOrder = append(agg.toolCallOrder, tcIdx)
				}
				if tc.ID != "" {
					ta.id = tc.ID
				}
				if tStr, ok := tc.Type.(string); ok && tStr != "" {
					ta.callType = tStr
				}
				if tc.Function.Name != "" {
					ta.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					ta.arguments.WriteString(tc.Function.Arguments)
				}
			}
		}
	})

	if streamErr != nil {
		return nil, streamErr
	}

	if responseId == "" {
		responseId = "chatcmpl-" + common.GetRandomString(16)
	}
	if object == "" {
		object = "chat.completion"
	}
	if created == 0 {
		created = time.Now().Unix()
	}

	if usage == nil {
		// Fall back to estimating from produced text. Prompt tokens come from the
		// estimate captured earlier in the pipeline.
		var allText strings.Builder
		for _, idx := range orderedIndexes {
			agg := choicesByIndex[idx]
			allText.WriteString(agg.content.String())
			allText.WriteString(agg.reasoningContent.String())
		}
		usage = service.ResponseText2Usage(c, allText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}

	response := dto.OpenAITextResponse{
		Id:      responseId,
		Model:   model,
		Object:  object,
		Created: created,
		Usage:   *usage,
	}

	for _, idx := range orderedIndexes {
		agg := choicesByIndex[idx]
		msg := dto.Message{Role: agg.role}
		if msg.Role == "" {
			msg.Role = "assistant"
		}
		msg.SetStringContent(agg.content.String())
		if agg.reasoningContent.Len() > 0 {
			msg.ReasoningContent = agg.reasoningContent.String()
		}
		if len(agg.toolCallOrder) > 0 {
			toolCalls := make([]dto.ToolCallRequest, 0, len(agg.toolCallOrder))
			for _, tcIdx := range agg.toolCallOrder {
				ta := agg.toolCalls[tcIdx]
				callType := ta.callType
				if callType == "" {
					callType = "function"
				}
				toolCalls = append(toolCalls, dto.ToolCallRequest{
					ID:   ta.id,
					Type: callType,
					Function: dto.FunctionRequest{
						Name:      ta.name,
						Arguments: ta.arguments.String(),
					},
				})
			}
			msg.SetToolCalls(toolCalls)
		}
		finish := agg.finishReason
		if finish == "" {
			if len(agg.toolCallOrder) > 0 {
				finish = "tool_calls"
			} else {
				finish = constant.FinishReasonStop
			}
		}
		response.Choices = append(response.Choices, dto.OpenAITextResponseChoice{
			Index:        agg.index,
			Message:      msg,
			FinishReason: finish,
		})
	}

	applyUsagePostProcessing(info, &response.Usage, nil)

	// Convert to the client's expected format before writing. For non-OpenAI
	// relay formats the existing service converters build the body.
	var body []byte
	var err error
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		claudeResp := service.ResponseOpenAI2Claude(&response, info)
		body, err = common.Marshal(claudeResp)
	case types.RelayFormatGemini:
		geminiResp := service.ResponseOpenAI2Gemini(&response, info)
		body, err = common.Marshal(geminiResp)
	default:
		body, err = common.Marshal(response)
	}
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	if _, werr := c.Writer.Write(body); werr != nil {
		logger.LogError(c, "force_upstream_stream: write response failed: "+werr.Error())
	}

	return &response.Usage, nil
}
