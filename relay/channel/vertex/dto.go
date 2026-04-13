package vertex

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/claude"
)

type VertexAIClaudeRequest struct {
	AnthropicVersion string              `json:"anthropic_version"`
	Messages         []dto.ClaudeMessage `json:"messages"`
	System           any                 `json:"system,omitempty"`
	MaxTokens        *uint               `json:"max_tokens,omitempty"`
	StopSequences    []string            `json:"stop_sequences,omitempty"`
	Stream           *bool               `json:"stream,omitempty"`
	Temperature      *float64            `json:"temperature,omitempty"`
	TopP             *float64            `json:"top_p,omitempty"`
	TopK             *int                `json:"top_k,omitempty"`
	Tools            any                 `json:"tools,omitempty"`
	ToolChoice       any                 `json:"tool_choice,omitempty"`
	Thinking         *dto.Thinking       `json:"thinking,omitempty"`
	OutputConfig     json.RawMessage     `json:"output_config,omitempty"`
	//Metadata         json.RawMessage     `json:"metadata,omitempty"`
}

func copyRequest(req *dto.ClaudeRequest, version string) *VertexAIClaudeRequest {
	// Vertex Claude variants (e.g. claude-opus-4-6) reject assistant prefill:
	// "This model does not support assistant message prefill." Fold the prefill
	// into the system prompt so the hint is preserved while the conversation
	// ends with a user message.
	messages, system := claude.HandleUnsupportedAssistantPrefill(req.Messages, req.System)
	return &VertexAIClaudeRequest{
		AnthropicVersion: version,
		System:           system,
		Messages:         messages,
		MaxTokens:        req.MaxTokens,
		Stream:           req.Stream,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		TopK:             req.TopK,
		StopSequences:    req.StopSequences,
		Tools:            req.Tools,
		ToolChoice:       req.ToolChoice,
		Thinking:         req.Thinking,
		OutputConfig:     req.OutputConfig,
	}
}
