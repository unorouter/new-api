package types

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// An upstream 429 is normally capacity and must stay transient, or the busiest
// channels get banned for being busy. But a spent plan/daily allowance also
// arrives as a 429 and cannot clear on its own, so it must reclassify to a
// channel fault: that both fails the request over to a sibling and parks the
// exhausted channel instead of burning a hop on every later request.
func TestWithOpenAIErrorQuota429Reclassify(t *testing.T) {
	prev := ChannelFaultKeywordsProvider
	ChannelFaultKeywordsProvider = func() []string {
		return []string{
			"token plan limit exhausted",
			"tokens per day limit exceeded",
			"you exceeded your current quota",
			"insufficient credits",
		}
	}
	t.Cleanup(func() { ChannelFaultKeywordsProvider = prev })

	cases := []struct {
		name        string
		message     string
		wantChannel bool
	}{
		// Verified live bodies from exhausted upstreams.
		{"sensenova_plan_exhausted", "token plan limit exhausted", true},
		{"tokens_per_day", "Tokens per day limit exceeded - too many tokens today", true},
		{"google_daily_quota", "You exceeded your current quota, please check your plan and billing details.", true},
		{"drained_wallet", "Insufficient credits. Please top up your balance and try again.", true},
		// NVIDIA returns a bare 429 with no body at all; there is nothing to
		// classify on, and treating it as a channel fault would ban a channel that
		// is merely busy.
		{"bare_capacity_429", "", false},
		{"generic_rate_limit", "Rate limit reached for requests. Please try again later.", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := WithOpenAIError(OpenAIError{Message: c.message}, http.StatusTooManyRequests)
			assert.Equal(t, c.wantChannel, IsChannelError(err),
				"message %q", c.message)
		})
	}
}
