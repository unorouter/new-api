package service

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type replyRule struct {
	markers []string
}

// The counterpart of upstreamRules for answers that arrive as a clean 200: the
// upstream (or a product it was scraped from) wrote its own notice into the
// assistant message, so no status code or error body ever says the lane is
// broken and every such reply was counted as a success and billed.
//
// Every marker is lowercase; the reply is lowercased before matching. Matched on
// the text alone, like upstreamRules, and only on a short reply: a long answer
// that happens to quote one of these sentences is the model talking.
var replyRules = []replyRule{
	// The upstream account behind the lane is banned and says so as the answer
	// (axz1 qwen3.6-plus:free, 2026-10-01).
	{markers: []string{"flagged as having abnormal activity"}},
	// Tencent Cloud's OrcaTerm terminal assistant resold as a chat model: it
	// refuses anything off its own topic and names itself when asked
	// (a7 merchant 4224 deepseek-v4-pro-0813, 2026-10-01).
	{markers: []string{"orcaterm", "如果您有服务器运维、云资源管理"}},
	// LoreBary pauses external traffic at peak and answers with a bracketed notice.
	{markers: []string{"[lorebary:"}},
}

const cannedReplyMaxRunes = 600

// CannedReplyError is non-nil when a successful reply is a provider notice. A
// committed reply is already on the client's screen and cannot fail over.
func CannedReplyError(reply string, committed bool) *types.NewAPIError {
	text := strings.ToLower(strings.TrimSpace(reply))
	if text == "" || utf8.RuneCountInString(text) > cannedReplyMaxRunes {
		return nil
	}
	for i := range replyRules {
		for _, marker := range replyRules[i].markers {
			if !strings.Contains(text, marker) {
				continue
			}
			err := fmt.Errorf("a provider answered with its own notice instead of the model's reply (%q). It has left rotation and this request was not charged. Send it again", marker)
			if committed {
				return types.NewOpenAIError(err, types.ErrorCodeChannelCannedReply, http.StatusTooManyRequests, types.ErrOptionWithSkipRetry())
			}
			return types.NewOpenAIError(err, types.ErrorCodeChannelCannedReply, http.StatusTooManyRequests)
		}
	}
	return nil
}
