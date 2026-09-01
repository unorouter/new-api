package openai

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
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
		name         string
		channelType  int
		reasoning    string
		clientEffort string
		rawBody      string
		wantEffort   string
	}{
		{name: "compat: disabled via reasoning object", channelType: constant.ChannelTypeCustom, reasoning: `{"effort":"none"}`, wantEffort: ""},
		{name: "compat: effort via reasoning object is not synthesized", channelType: constant.ChannelTypeCustom, reasoning: `{"effort":"low"}`, wantEffort: ""},
		{name: "compat: explicit reasoning_effort passes through", channelType: constant.ChannelTypeCustom, clientEffort: "low", wantEffort: "low"},
		{name: "openai: disabled renders none", channelType: constant.ChannelTypeOpenAI, reasoning: `{"effort":"none"}`, wantEffort: "none"},
		{name: "openai: effort via reasoning object is rendered", channelType: constant.ChannelTypeOpenAI, reasoning: `{"effort":"low"}`, wantEffort: "low"},
		// An earlier relay step already wrote "none" into the struct; the raw body
		// never carried reasoning_effort, so a compat channel must not receive it.
		{name: "compat: struct value written upstream of the adaptor is not client-sent", channelType: constant.ChannelTypeCustom, reasoning: `{"enabled":false}`, clientEffort: "none", rawBody: `{"model":"llama-3.2-11b-vision:free","messages":[{"role":"user","content":"hi"}],"reasoning":{"enabled":false}}`, wantEffort: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			var body io.Reader
			if tc.rawBody != "" {
				body = strings.NewReader(tc.rawBody)
			}
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", body)
			info := &relaycommon.RelayInfo{
				OriginModelName: "llama-3.2-11b-vision:free",
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: tc.channelType, UpstreamModelName: "llama-3.2-11b-vision:free"},
			}
			request := &dto.GeneralOpenAIRequest{
				Model:           "llama-3.2-11b-vision:free",
				Messages:        []dto.Message{{Role: "user", Content: "hi"}},
				ReasoningEffort: tc.clientEffort,
			}
			if tc.reasoning != "" {
				request.Reasoning = json.RawMessage(tc.reasoning)
			}
			out, err := (&Adaptor{}).ConvertOpenAIRequest(c, info, request)
			require.NoError(t, err)
			converted, ok := out.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			require.Equal(t, tc.wantEffort, converted.ReasoningEffort)
		})
	}
}
