package openai

import (
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// PROD-ONLY (fork): sendStreamData must drop an upstream usage-only chunk
// ("choices":[]) for a client that did NOT explicitly request stream usage
// (yun gemini-2.5-pro emits it unconditionally, crashing fragile clients on
// chunk.choices[0].delta). A client that DID ask keeps receiving it, and a
// normal content chunk is always forwarded.
func TestSendStreamDataDropsUnrequestedEmptyChoicesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const contentChunk = `{"choices":[{"delta":{"content":"hi"},"index":0}]}`
	const emptyUsageChunk = `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`

	cases := []struct {
		name           string
		data           string
		clientReqUsage bool
		wantForwarded  bool
	}{
		{"content chunk always forwarded (no usage req)", contentChunk, false, true},
		{"content chunk always forwarded (usage req)", contentChunk, true, true},
		{"empty-choices usage dropped when client did not ask", emptyUsageChunk, false, false},
		{"empty-choices usage kept when client asked", emptyUsageChunk, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{ClientRequestedStreamUsage: tc.clientReqUsage}

			err := sendStreamData(c, info, tc.data, false, false)
			assert.NoError(t, err)

			forwarded := strings.Contains(rec.Body.String(), tc.data)
			assert.Equal(t, tc.wantForwarded, forwarded,
				"forwarded=%v body=%q", forwarded, rec.Body.String())
		})
	}
}
