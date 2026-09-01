package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// OpenAI-compatible proxies (NVIDIA, most resellers) accept reasoning_effort
// low/medium/high only. A client that disables reasoning through the
// OpenRouter-style `reasoning` object must not have that rendered as "none"
// on such a channel; only real OpenAI understands "none".
func TestDisabledReasoningIsNotSentAsNoneToCompatibleChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		channelType int
		wantEffort  string
	}{
		{name: "openai-compatible proxy", channelType: constant.ChannelTypeCustom, wantEffort: ""},
		{name: "real openai", channelType: constant.ChannelTypeOpenAI, wantEffort: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				OriginModelName: "llama-3.2-11b-vision:free",
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: tc.channelType, UpstreamModelName: "llama-3.2-11b-vision:free"},
			}
			request := &dto.GeneralOpenAIRequest{
				Model:     "llama-3.2-11b-vision:free",
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
				Reasoning: json.RawMessage(`{"effort":"none"}`),
			}
			out, err := (&Adaptor{}).ConvertOpenAIRequest(c, info, request)
			require.NoError(t, err)
			converted, ok := out.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			require.Equal(t, tc.wantEffort, converted.ReasoningEffort)
		})
	}
}
