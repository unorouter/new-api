package gemini

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runGeminiChat(t *testing.T, payload dto.GeminiChatResponse) (*httptest.ResponseRecorder, *types.NewAPIError) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gemini-3.8-flash",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.8-flash"},
	}
	ms := operation_setting.GetMonitorSetting()
	oldFlag := ms.DisableOnEmptyResponse
	ms.DisableOnEmptyResponse = true
	t.Cleanup(func() { ms.DisableOnEmptyResponse = oldFlag })
	body, err := common.Marshal(payload)
	require.NoError(t, err)
	_, apiErr := GeminiChatHandler(c, info, &http.Response{Body: io.NopCloser(bytes.NewReader(body))})
	return recorder, apiErr
}

// A Gemini candidate whose only parts are thoughts (thinking ate max_tokens,
// finishReason MAX_TOKENS) converts to an OpenAI message with empty content and
// used to reach the client as a billable 200. It must fail over like any other
// empty reply, without counting toward disabling the lane.
func TestGeminiChatHandlerThoughtOnlyCandidateIsEmptyReply(t *testing.T) {
	recorder, apiErr := runGeminiChat(t, dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			FinishReason: strPtr("MAX_TOKENS"),
			Content:      dto.GeminiChatContent{Role: "model", Parts: []dto.GeminiPart{{Text: "let me think", Thought: true}}},
		}},
		UsageMetadata: dto.GeminiUsageMetadata{PromptTokenCount: 8, ThoughtsTokenCount: 56, TotalTokenCount: 64},
	})
	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeChannelEmptyResponse, apiErr.GetErrorCode())
	require.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	require.True(t, types.IsSkipDisableError(apiErr))
	require.Empty(t, recorder.Body.String())
}

func TestGeminiChatHandlerTextCandidatePasses(t *testing.T) {
	recorder, apiErr := runGeminiChat(t, dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			FinishReason: strPtr("STOP"),
			Content:      dto.GeminiChatContent{Role: "model", Parts: []dto.GeminiPart{{Text: "ready"}}},
		}},
		UsageMetadata: dto.GeminiUsageMetadata{PromptTokenCount: 8, CandidatesTokenCount: 1, TotalTokenCount: 9},
	})
	require.Nil(t, apiErr)
	require.Contains(t, recorder.Body.String(), "ready")
}

// Image generation answers with inline data and no text: real output, not empty.
func TestGeminiChatHandlerImageCandidatePasses(t *testing.T) {
	_, apiErr := runGeminiChat(t, dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{
			FinishReason: strPtr("STOP"),
			Content: dto.GeminiChatContent{Role: "model", Parts: []dto.GeminiPart{{
				InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="},
			}}},
		}},
	})
	require.Nil(t, apiErr)
}

func strPtr(s string) *string { return &s }
