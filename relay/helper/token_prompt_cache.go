package helper

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

var ephemeralCacheControl = json.RawMessage(`{"type":"ephemeral"}`)

// ApplyTokenPromptCache applies the key's prompt cache mode. "on" marks the
// system prompt and the last user message with cache_control when the client
// marked nothing, which providers with explicit caching (Anthropic, Alibaba,
// the z.ai coding endpoint) need and the rest ignore. "off" puts a line that
// was never sent before at the start, so no provider prefix cache can match.
func ApplyTokenPromptCache(c *gin.Context, request dto.Request) {
	mode := common.GetContextKeyString(c, constant.ContextKeyTokenPromptCache)
	if mode == "" {
		return
	}
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if len(req.Messages) == 0 {
			return
		}
		if mode == "off" {
			req.Messages[0].SetContent(prefixPromptText(req.Messages[0].Content, cacheBustLine()))
			return
		}
		for i := range req.Messages {
			if hasCacheMarker(req.Messages[i].Content) {
				return
			}
		}
		for i := range req.Messages {
			if req.Messages[i].Role == "system" || req.Messages[i].Role == "developer" {
				req.Messages[i].SetContent(markLastPromptText(req.Messages[i].Content))
				break
			}
		}
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				req.Messages[i].SetContent(markLastPromptText(req.Messages[i].Content))
				break
			}
		}
	case *dto.ClaudeRequest:
		if mode == "off" {
			if req.System != nil {
				req.System = prefixPromptText(req.System, cacheBustLine())
			} else if len(req.Messages) > 0 {
				req.Messages[0].SetContent(prefixPromptText(req.Messages[0].Content, cacheBustLine()))
			}
			return
		}
		if len(req.CacheControl) > 0 || hasCacheMarker(req.System) {
			return
		}
		for i := range req.Messages {
			if hasCacheMarker(req.Messages[i].Content) {
				return
			}
		}
		if req.System != nil {
			req.System = markLastPromptText(req.System)
		}
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				req.Messages[i].SetContent(markLastPromptText(req.Messages[i].Content))
				break
			}
		}
	}
}

// Random rather than counted: a counter restarts with each chat and repeats
// across users sharing one upstream key.
func cacheBustLine() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "[" + hex.EncodeToString(b) + "]\nIgnore the line above, it is a request id.\n\n"
}

func hasCacheMarker(content any) bool {
	switch parts := content.(type) {
	case []any:
		for _, part := range parts {
			if m, ok := part.(map[string]any); ok && m["cache_control"] != nil {
				return true
			}
		}
	case []dto.MediaContent:
		for _, part := range parts {
			if len(part.CacheControl) > 0 {
				return true
			}
		}
	case []dto.ClaudeMediaMessage:
		for _, part := range parts {
			if len(part.CacheControl) > 0 {
				return true
			}
		}
	}
	return false
}

// markLastPromptText sets cache_control on the last text part, turning plain
// string content into a single text part first. Other content is returned as is.
func markLastPromptText(content any) any {
	switch parts := content.(type) {
	case string:
		if parts == "" {
			return content
		}
		return []any{map[string]any{"type": "text", "text": parts, "cache_control": map[string]any{"type": "ephemeral"}}}
	case []any:
		for i := len(parts) - 1; i >= 0; i-- {
			if m, ok := parts[i].(map[string]any); ok && m["type"] == "text" {
				m["cache_control"] = map[string]any{"type": "ephemeral"}
				break
			}
		}
	case []dto.MediaContent:
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i].Type == dto.ContentTypeText {
				parts[i].CacheControl = ephemeralCacheControl
				break
			}
		}
	case []dto.ClaudeMediaMessage:
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i].Type == "text" {
				parts[i].CacheControl = ephemeralCacheControl
				break
			}
		}
	}
	return content
}

// prefixPromptText puts line in front of the first text part, or adds a text
// part when there is none.
func prefixPromptText(content any, line string) any {
	switch parts := content.(type) {
	case string:
		return line + parts
	case []any:
		if len(parts) > 0 {
			if m, ok := parts[0].(map[string]any); ok && m["type"] == "text" {
				if text, ok := m["text"].(string); ok {
					m["text"] = line + text
					return content
				}
			}
		}
		return append([]any{map[string]any{"type": "text", "text": line}}, parts...)
	case []dto.MediaContent:
		if len(parts) > 0 && parts[0].Type == dto.ContentTypeText {
			parts[0].Text = line + parts[0].Text
			return content
		}
		return append([]dto.MediaContent{{Type: dto.ContentTypeText, Text: line}}, parts...)
	case []dto.ClaudeMediaMessage:
		if len(parts) > 0 && parts[0].Type == "text" && parts[0].Text != nil {
			text := line + *parts[0].Text
			parts[0].Text = &text
			return content
		}
		return append([]dto.ClaudeMediaMessage{{Type: "text", Text: &line}}, parts...)
	}
	return content
}
