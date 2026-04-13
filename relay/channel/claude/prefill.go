package claude

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/dto"
)

// prefillContinuationPrompt formats the user-role continuation prompt used to
// preserve assistant-prefill semantics on providers that reject trailing
// assistant messages. The wording mirrors Anthropic's own migration guidance
// for moving prefill behavior into the user turn on Claude 4.5/4.6 models:
// https://platform.claude.com/docs/en/about-claude/models/migration-guide
func prefillContinuationPrompt(prefill string) string {
	return fmt.Sprintf(
		"Your previous response was interrupted and ended with: %q. Continue from where you left off, beginning your reply with exactly that text and then continuing naturally without repeating what came before.",
		prefill,
	)
}

// HandleUnsupportedAssistantPrefill rewrites a Claude messages slice so that
// the conversation ends with a user message, preserving the semantics of any
// trailing assistant-prefill message by appending a user-role continuation
// prompt.
//
// Background: Bedrock and Vertex variants of Claude (e.g. claude-opus-4-6)
// reject requests whose last message has role=assistant with
// "This model does not support assistant message prefill. The conversation
// must end with a user message." Stripping the trailing assistant turn alone
// would lose the prefill hint. Anthropic's official migration guide recommends
// moving the continuation into the user turn; we follow that pattern.
//
// The trailing assistant message is dropped and a synthetic user message is
// appended referencing the prefill text. If the prefill is empty, the
// assistant turn is simply dropped. The inputs are returned unchanged if the
// slice does not end with an assistant turn.
//
// The second return value is the system field, returned unchanged so callers
// with the same call signature remain ergonomic.
func HandleUnsupportedAssistantPrefill(messages []dto.ClaudeMessage, system any) ([]dto.ClaudeMessage, any) {
	if len(messages) == 0 {
		return messages, system
	}
	last := messages[len(messages)-1]
	if last.Role != "assistant" {
		return messages, system
	}

	prefill := strings.TrimSpace(last.GetStringContent())
	trimmed := messages[:len(messages)-1]

	if prefill == "" {
		// Nothing worth preserving; just drop the empty assistant turn.
		return trimmed, system
	}

	continuation := dto.ClaudeMessage{
		Role:    "user",
		Content: prefillContinuationPrompt(prefill),
	}
	return append(trimmed, continuation), system
}

// HandleUnsupportedAssistantPrefillOpenAI is the OpenAI-format counterpart of
// HandleUnsupportedAssistantPrefill. OpenAI-style requests carry messages in
// a single flat slice; the prefill fallback is appended as a new user message.
//
// Returns the (possibly updated) messages slice. Inputs are returned unchanged
// if the slice is empty or does not end with an assistant message.
func HandleUnsupportedAssistantPrefillOpenAI(messages []dto.Message) []dto.Message {
	if len(messages) == 0 {
		return messages
	}
	last := messages[len(messages)-1]
	if last.Role != "assistant" {
		return messages
	}

	prefill := strings.TrimSpace(last.StringContent())
	trimmed := messages[:len(messages)-1]

	if prefill == "" {
		return trimmed
	}

	continuation := dto.Message{
		Role:    "user",
		Content: prefillContinuationPrompt(prefill),
	}
	return append(trimmed, continuation)
}
