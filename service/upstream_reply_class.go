package service

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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
	{markers: []string{"flagged as having abnormal activity"}},
	{markers: append([]string{"如果您有服务器运维、云资源管理"}, operation_setting.CannedReplyMarkers...)},
	{markers: []string{"please retry later, or reduce the request parameters/content"}},
	{markers: []string{"this version of antigravity is no longer supported"}},
	{markers: []string{"account restrictions for prohibited use policy"}},
	// Gemini web's transient refusal, resold behind a marketplace supplier.
	{markers: []string{"i encountered an error doing what you asked"}},
	// A supplier's own "abnormal network requests, access temporarily restricted"
	// notice, served at 200 to 40 users on one lane in two hours (2026-11-25).
	{markers: []string{"已被临时限制访问", "当前网络的请求存在异常"}},
	// A relay whose own supplier key ran dry: "Your quota is exhausted. API key
	// status: exceeded ... USDT balance: -0.000959", served as the answer for 22h.
	{markers: []string{"api key status: exceeded", "your quota is exhausted"}},
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
