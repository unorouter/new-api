package common

import (
	"strings"

	"github.com/QuantumNous/new-api/dto"
)

// IsDeepSeekV4ThinkingModel reports whether the upstream model name
// corresponds to a DeepSeek V4 family member that runs in thinking mode
// by default. The current set is deepseek-v4-pro / deepseek-v4-flash and
// any future deepseek-v4* variants. A model that opts out of thinking
// upstream would not match this prefix.
func IsDeepSeekV4ThinkingModel(modelName string) bool {
	return strings.HasPrefix(modelName, "deepseek-v4")
}

// EnsureDeepSeekReasoningContentClaude backfills `reasoning_content` on
// every assistant message in a Claude-shape request so that DeepSeek V4
// upstreams accept the turn. Per the official DeepSeek thinking-mode
// spec (https://api-docs.deepseek.com/guides/thinking_mode):
//
//   - For assistant turns that performed tool calls, reasoning_content
//     "must be fully passed back to the API in all subsequent requests";
//     missing it produces "The reasoning_content in the thinking mode
//     must be passed back to the API." 400.
//   - For assistant turns without tool calls, the field is ignored and
//     can be anything (empty string is the community-recommended default).
//
// We backfill on every assistant turn rather than only tool-call ones
// because (a) it's a no-op for non-tool-call turns, (b) cheap reseller
// shims (aigcbest, openclaude, etc.) sometimes apply the rule
// unconditionally even though the official API doesn't, (c) the model
// itself "occasionally returns reasoning_content even when thinking is
// disabled" (per OpenClaude bug 74374), so any assistant turn could
// have or need the field.
//
// For each assistant turn this:
//   - lifts any {type:"thinking"} content blocks into the top-level
//     reasoning_content field (dropping them from content) so the
//     upstream receives the field where it expects it;
//   - backfills empty string when no thinking content was present, per
//     the OpenClaude/Gitlawb community recommendation (issue 878).
func EnsureDeepSeekReasoningContentClaude(request *dto.ClaudeRequest) {
	if request == nil || !IsDeepSeekV4ThinkingModel(request.Model) {
		return
	}
	for i := range request.Messages {
		msg := &request.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		blocks, err := msg.ParseContent()
		if err == nil && len(blocks) > 0 {
			kept := make([]dto.ClaudeMediaMessage, 0, len(blocks))
			for _, b := range blocks {
				if b.Type == "thinking" {
					if b.Thinking != nil {
						msg.ReasoningContent += *b.Thinking
					}
					continue
				}
				kept = append(kept, b)
			}
			if len(kept) != len(blocks) {
				msg.Content = kept
			}
		}
		// Note: ReasoningContent stays empty if no thinking block was
		// present. The Anthropic message DTO will omit the field on
		// marshal due to its omitempty tag, which is acceptable for
		// non-tool-call turns. Tool-call turns will already have a
		// thinking block lifted in above and so won't hit this branch.
	}
}

// BackfillDeepSeekReasoningContentOpenAI ensures the reasoning_content
// field is present on assistant messages in the OpenAI-shape request
// the Claude->OpenAI converter produces. Per DeepSeek's spec
// (https://api-docs.deepseek.com/guides/thinking_mode), assistant turns
// that performed tool calls require reasoning_content; turns without
// tool calls don't. We force a non-empty placeholder regardless of
// tool-call status because reseller shims (aigcbest, openclaude) apply
// the rule more strictly than the upstream API, and the empty-string
// approach has been observed to fail on some of those — a single space
// placeholder is the most conservative compromise.
func BackfillDeepSeekReasoningContentOpenAI(msg *dto.Message, requestModel string) {
	if msg == nil || !IsDeepSeekV4ThinkingModel(requestModel) {
		return
	}
	if msg.Role != "assistant" {
		return
	}
	if msg.ReasoningContent == "" {
		msg.ReasoningContent = " "
	}
}

// ApplyDeepSeekV4OpenAIRequestRules normalizes an OpenAI-shape outbound
// request for DeepSeek V4 models. It downcasts image_url blocks to a
// text placeholder (V4 is text-only), maps the xhigh reasoning_effort
// alias to max, and floors max_tokens at the upstream-required minimum
// for Think Max mode.
//
// We deliberately do NOT inject `extra_body.thinking: {type: "enabled"}`
// or clear temperature/top_p. The DeepSeek API enables thinking by
// default on V4 (per https://api-docs.deepseek.com/guides/thinking_mode)
// and silently ignores temperature/top_p in thinking mode, so neither
// override is required. Empirically, force-injecting thinking on every
// V4 request did not affect outcomes on aigcbest's reseller; the
// reasoning_content backfill on assistant turns is what actually solved
// the original 400.
//
// References:
//   - https://api-docs.deepseek.com/guides/thinking_mode
//   - https://recipes.vllm.ai/deepseek-ai/DeepSeek-V4-Pro
//   - https://github.com/RooCodeInc/Roo-Code/pull/12204
func ApplyDeepSeekV4OpenAIRequestRules(request *dto.GeneralOpenAIRequest) error {
	if request == nil || !IsDeepSeekV4ThinkingModel(request.Model) {
		return nil
	}
	stripImagesForTextOnlyModel(request)
	if request.ReasoningEffort == "xhigh" {
		request.ReasoningEffort = "max"
	}
	floorDeepSeekV4MaxTokens(request)
	return nil
}

// deepSeekV4MaxModelLen is the minimum max_tokens DeepSeek V4 needs in
// Think Max mode to avoid mid-reasoning truncation. Per the official
// vLLM recipe (https://recipes.vllm.ai/deepseek-ai/DeepSeek-V4-Pro):
// "Think Max — maximum reasoning effort; requires --max-model-len >=
// 393216 (384K tokens) to avoid truncation."
//
// Without this, the model can exhaust its token budget inside the
// reasoning block and emit only reasoning tokens with empty content
// (e.g. completion_tokens=14, reasoning_tokens=14, finish_reason=stop),
// which surfaces to users as "the model returned nothing".
//
// DeepSeek auto-promotes effort to max for "complex agent requests
// (Claude Code, OpenCode)", so callers that pass a smaller max_tokens
// because they're targeting High mode will silently hit truncation
// when the upstream upgrades them to Max. Floor unconditionally on V4
// to cover both cases.
const deepSeekV4MaxModelLen uint = 393216

func floorDeepSeekV4MaxTokens(request *dto.GeneralOpenAIRequest) {
	floor := deepSeekV4MaxModelLen
	if request.MaxTokens == nil {
		request.MaxTokens = &floor
		return
	}
	if *request.MaxTokens < floor {
		request.MaxTokens = &floor
	}
}

// stripImagesForTextOnlyModel walks each message's content array and
// replaces every image_url block with a single placeholder text block
// telling the model an image was attached but cannot be processed.
// This is visible to the model (so it can mention the limitation in
// its reply) and to the user (because the model's reply will reflect
// it). Plain string content is left alone since it can't carry images.
func stripImagesForTextOnlyModel(request *dto.GeneralOpenAIRequest) {
	const placeholder = "[image attached: this model does not support image inputs, content omitted]"
	for i := range request.Messages {
		msg := &request.Messages[i]
		blocks := msg.ParseContent()
		if len(blocks) == 0 {
			continue
		}
		changed := false
		for j := range blocks {
			if blocks[j].Type == "image_url" {
				blocks[j] = dto.MediaContent{Type: "text", Text: placeholder}
				changed = true
			}
		}
		if changed {
			msg.SetMediaContent(blocks)
		}
	}
}

