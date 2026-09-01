package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A log served to the user who made the request must not carry the channel
// name. That name identifies the upstream provider behind a model, which is
// ours and not the caller's to see. An upstream sync once replaced the blanking
// line with a call that RESOLVES the name instead, inverting the rule while
// still compiling and passing every other test, and user-facing log views began
// exposing provider identities.
func TestFormatUserLogsStripsChannelName(t *testing.T) {
	logs := []*Log{
		{Id: 1, ChannelId: 7322, ChannelName: "di1-mimo-v2.5", Other: `{"admin_info":{"x":1},"audit_info":{"y":2},"frt":123}`},
		{Id: 2, ChannelId: 8169, ChannelName: "open1-gmicloud-fp8-minimax-m3"},
	}

	formatUserLogs(logs, 0)

	for _, l := range logs {
		assert.Empty(t, l.ChannelName, "user-facing logs must not disclose the upstream channel name")
	}

	// The admin-only blobs go with it; the ordinary timing field stays.
	assert.NotContains(t, logs[0].Other, "admin_info")
	assert.NotContains(t, logs[0].Other, "audit_info")
	assert.Contains(t, logs[0].Other, "frt")
}
