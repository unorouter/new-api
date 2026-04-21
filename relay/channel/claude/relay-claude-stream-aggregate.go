package claude

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// blockAggregator tracks one content block as it streams in. Supports text
// and thinking blocks. tool_use / server_tool_use still abort — those need
// argument-fragment accumulation that isn't implemented yet.
type blockAggregator struct {
	index     int
	blockType string // "text" or "thinking"
	text      strings.Builder
	thinking  strings.Builder
	signature string
}

// ClaudeStreamToJsonHandler consumes an Anthropic Messages SSE stream from
// upstream and rebuilds a single non-stream ClaudeResponse to send to the
// client. Activated when the client sent stream=false but the relay forced
// stream=true upstream to avoid reseller header-wait timeouts.
func ClaudeStreamToJsonHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.WithClaudeError(types.ClaudeError{Type: "invalid_response", Message: "empty upstream response"}, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}

	var (
		responseId    string
		modelName     = info.UpstreamModelName
		role          = "assistant"
		stopReason    string
		finalUsage    *dto.ClaudeUsage
		startUsage    *dto.ClaudeUsage
		blocksByIndex = map[int]*blockAggregator{}
		orderedIdx    []int
		unsupported   error
	)

	streamErr := helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if data == "" {
			return
		}
		var event dto.ClaudeResponse
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			// ignore unparseable ping / comment lines
			return
		}
		if claudeErr := event.GetClaudeError(); claudeErr != nil && claudeErr.Type != "" {
			sr.Error(fmt.Errorf("upstream error: %s", claudeErr.Message))
			return
		}

		// Feed the shared usage tracker so billing numbers line up with the
		// non-aggregated stream path.
		FormatClaudeResponseInfo(&event, nil, claudeInfo)

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				if event.Message.Id != "" {
					responseId = event.Message.Id
				}
				if event.Message.Model != "" {
					modelName = event.Message.Model
					info.UpstreamModelName = modelName
				}
				if event.Message.Role != "" {
					role = event.Message.Role
				}
				if event.Message.Usage != nil {
					u := *event.Message.Usage
					startUsage = &u
				}
			}
		case "content_block_start":
			idx := event.GetIndex()
			agg, ok := blocksByIndex[idx]
			if !ok {
				agg = &blockAggregator{index: idx}
				blocksByIndex[idx] = agg
				orderedIdx = append(orderedIdx, idx)
			}
			if event.ContentBlock != nil {
				agg.blockType = event.ContentBlock.Type
				switch event.ContentBlock.Type {
				case "text":
					if event.ContentBlock.Text != nil {
						agg.text.WriteString(*event.ContentBlock.Text)
					}
				case "thinking":
					if event.ContentBlock.Thinking != nil {
						agg.thinking.WriteString(*event.ContentBlock.Thinking)
					}
				default:
					unsupported = fmt.Errorf("force_upstream_stream: content block type %q not supported", event.ContentBlock.Type)
					sr.Error(unsupported)
					return
				}
			} else {
				agg.blockType = "text"
			}
		case "content_block_delta":
			idx := event.GetIndex()
			agg, ok := blocksByIndex[idx]
			if !ok {
				agg = &blockAggregator{index: idx, blockType: "text"}
				blocksByIndex[idx] = agg
				orderedIdx = append(orderedIdx, idx)
			}
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					if event.Delta.Text != nil {
						agg.text.WriteString(*event.Delta.Text)
					}
				case "thinking_delta":
					if event.Delta.Thinking != nil {
						agg.thinking.WriteString(*event.Delta.Thinking)
					}
					if agg.blockType == "" {
						agg.blockType = "thinking"
					}
				case "signature_delta":
					// Cryptographic signature bound to the thinking block.
					// Upstream sends it as a single final delta on the thinking block.
					if event.Delta.Signature != "" {
						agg.signature = event.Delta.Signature
					}
				case "input_json_delta":
					unsupported = fmt.Errorf("force_upstream_stream: delta type %q not supported (tool calls not implemented)", event.Delta.Type)
					sr.Error(unsupported)
					return
				default:
					// Some upstreams omit Type on text deltas; if Text is set, treat as text.
					if event.Delta.Text != nil {
						agg.text.WriteString(*event.Delta.Text)
					}
				}
			}
		case "content_block_stop":
			// nothing to do — block is closed, content already accumulated
		case "message_delta":
			if event.Delta != nil {
				if event.Delta.StopReason != nil {
					stopReason = *event.Delta.StopReason
				}
			}
			if event.Usage != nil {
				u := *event.Usage
				finalUsage = &u
			}
		case "message_stop":
			// end of stream
		}
	})

	if streamErr != nil {
		return nil, streamErr
	}
	if unsupported != nil {
		return nil, types.NewError(unsupported, types.ErrorCodeBadResponse)
	}

	if responseId == "" {
		responseId = "msg_" + common.GetRandomString(24)
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}

	// Build the final ClaudeResponse body. Merge usage: start event carries
	// input_tokens + cache fields, message_delta carries output_tokens.
	merged := mergeClaudeUsages(startUsage, finalUsage)

	response := dto.ClaudeResponse{
		Id:           responseId,
		Type:         "message",
		Role:         role,
		Model:        modelName,
		StopReason:   stopReason,
		Usage:        merged,
	}
	for _, idx := range orderedIdx {
		agg := blocksByIndex[idx]
		switch agg.blockType {
		case "thinking":
			block := dto.ClaudeMediaMessage{Type: "thinking"}
			thinkingText := agg.thinking.String()
			block.Thinking = &thinkingText
			if agg.signature != "" {
				block.Signature = agg.signature
			}
			response.Content = append(response.Content, block)
		default:
			// text or unset — treat as text
			block := dto.ClaudeMediaMessage{Type: "text"}
			block.SetText(agg.text.String())
			response.Content = append(response.Content, block)
		}
	}

	// Ensure usage is populated for billing even if upstream was stingy.
	if claudeInfo.Usage.PromptTokens == 0 && claudeInfo.Usage.CompletionTokens == 0 {
		fallback := service.ResponseText2Usage(c, claudeInfo.ResponseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		claudeInfo.Usage.PromptTokens = fallback.PromptTokens
		claudeInfo.Usage.CompletionTokens = fallback.CompletionTokens
		claudeInfo.Usage.TotalTokens = fallback.TotalTokens
	}
	claudeInfo.Usage.UsageSemantic = "anthropic"

	body, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	if _, werr := c.Writer.Write(body); werr != nil {
		logger.LogError(c, "force_upstream_stream: write response failed: "+werr.Error())
	}

	return claudeInfo.Usage, nil
}

// mergeClaudeUsages combines the usage reported at message_start (input/cache)
// with the one at message_delta (output/final) into a single object matching
// what a non-streaming response carries.
func mergeClaudeUsages(start, final *dto.ClaudeUsage) *dto.ClaudeUsage {
	if start == nil && final == nil {
		return nil
	}
	out := &dto.ClaudeUsage{}
	if start != nil {
		*out = *start
	}
	if final != nil {
		if final.InputTokens > 0 {
			out.InputTokens = final.InputTokens
		}
		if final.CacheReadInputTokens > 0 {
			out.CacheReadInputTokens = final.CacheReadInputTokens
		}
		if final.CacheCreationInputTokens > 0 {
			out.CacheCreationInputTokens = final.CacheCreationInputTokens
		}
		if final.OutputTokens > 0 {
			out.OutputTokens = final.OutputTokens
		}
		if final.CacheCreation != nil {
			out.CacheCreation = final.CacheCreation
		}
		if final.ClaudeCacheCreation5mTokens > 0 {
			out.ClaudeCacheCreation5mTokens = final.ClaudeCacheCreation5mTokens
		}
		if final.ClaudeCacheCreation1hTokens > 0 {
			out.ClaudeCacheCreation1hTokens = final.ClaudeCacheCreation1hTokens
		}
		if final.ServerToolUse != nil {
			out.ServerToolUse = final.ServerToolUse
		}
	}
	return out
}

