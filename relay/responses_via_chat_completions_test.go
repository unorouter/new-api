package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	openaichannel "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldResponsesUseChatCompletions(t *testing.T) {
	responsesCap := func(v bool) *dto.ChannelCapabilities {
		return &dto.ChannelCapabilities{Responses: &v}
	}

	tests := []struct {
		name        string
		model       string
		caps        *dto.ChannelCapabilities
		channelType int
		apiType     int
		want        bool
	}{
		{name: "image model converts", model: "gpt-image-1", channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "chat-only channel converts", model: "glm-5.2-thinking", caps: responsesCap(false), channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "channel marked native passes through", model: "gpt-5.6-sol", caps: responsesCap(true), channelType: constant.ChannelTypeOpenAI, want: false},
		// An unmarked OpenAI-shaped channel is a chat-only relay far more often than
		// not, and guessing native is what left users collecting 404s.
		{name: "unmarked openai channel converts", model: "glm-5.2-thinking", channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "unmarked capabilities object converts", model: "glm-5.2-thinking", caps: &dto.ChannelCapabilities{}, channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "openrouter converts", model: "grok-4.6", channelType: constant.ChannelTypeOpenRouter, apiType: constant.APITypeOpenRouter, want: true},
		// Types whose own endpoint declaration includes Responses never convert, so a
		// Codex channel needs no marking.
		{name: "codex type passes through unmarked", model: "gpt-5.6-sol", channelType: constant.ChannelTypeCodex, apiType: constant.APITypeCodex, want: false},
		// Adaptors that emit Claude or Gemini on the wire own Responses themselves;
		// a detour would parse their reply as OpenAI chat. Not even an explicit
		// chat-only mark may force it.
		{name: "anthropic adaptor never detours even when marked chat-only", model: "claude-sonnet-5", caps: responsesCap(false), channelType: constant.ChannelTypeAnthropic, apiType: constant.APITypeAnthropic, want: false},
		{name: "gemini adaptor never detours", model: "gemini-3.7-flash", channelType: constant.ChannelTypeGemini, apiType: constant.APITypeGemini, want: false},
		{name: "bedrock adaptor never detours", model: "claude-sonnet-5", channelType: constant.ChannelTypeAws, apiType: constant.APITypeAws, want: false},
		{name: "vertex adaptor never detours", model: "gemini-3.7-flash", channelType: constant.ChannelTypeVertexAi, apiType: constant.APITypeVertexAi, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: tt.model,
					ChannelType:       tt.channelType,
					ApiType:           tt.apiType,
					ChannelSetting:    dto.ChannelSettings{Capabilities: tt.caps},
				},
			}
			assert.Equal(t, tt.want, shouldResponsesUseChatCompletions(info))
		})
	}
}

func TestShouldResponsesUseChatCompletionsFallsBackToOriginModel(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-1",
		ChannelMeta:     &relaycommon.ChannelMeta{},
	}
	assert.True(t, shouldResponsesUseChatCompletions(info))
}

const responsesDetourChatSSE = `data: {"id":"chatcmpl_up","object":"chat.completion.chunk","created":1710000000,"model":"grok-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}

data: {"id":"chatcmpl_up","object":"chat.completion.chunk","created":1710000000,"model":"grok-test","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}

data: {"id":"chatcmpl_up","object":"chat.completion.chunk","created":1710000000,"model":"grok-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_up","object":"chat.completion.chunk","created":1710000000,"model":"grok-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_up","object":"chat.completion.chunk","created":1710000000,"model":"grok-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl_up","object":"chat.completion.chunk","created":1710000000,"model":"grok-test","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}

data: [DONE]

`

const responsesDetourChatJSON = `{"id":"chatcmpl_up","object":"chat.completion","created":1710000000,"model":"grok-test","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`

type responsesDetourUpstream struct {
	path string
	body map[string]any
}

// runResponsesDetour drives responsesViaChatCompletions against a fake chat
// upstream that answers with contentType/body, and returns what the client
// received plus what the upstream was sent.
func runResponsesDetour(t *testing.T, clientStream bool, contentType string, upstreamBody string) (*httptest.ResponseRecorder, *relaycommon.RelayInfo, *dto.Usage, responsesDetourUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// StreamScannerHandler builds a ticker from this; zero panics.
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	captured := make(chan responsesDetourUpstream, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, common.Unmarshal(raw, &body))
		captured <- responsesDetourUpstream{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	t.Cleanup(server.Close)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(common.RequestIdKey, "resp-test")

	info := &relaycommon.RelayInfo{
		RelayMode:         relayconstant.RelayModeResponses,
		RelayFormat:       relaytypes.RelayFormatOpenAIResponses,
		OriginModelName:   "grok-test",
		IsStream:          clientStream,
		ClientWantsStream: clientStream,
		DisablePing:       true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ApiType:           constant.APITypeOpenAI,
			ChannelBaseUrl:    server.URL,
			ApiKey:            "test-key",
			UpstreamModelName: "grok-test",
		},
	}
	adaptor := &openaichannel.Adaptor{}
	adaptor.Init(info)
	request := &dto.OpenAIResponsesRequest{
		Model:  "grok-test",
		Input:  json.RawMessage(`"hello"`),
		Stream: lo.ToPtr(clientStream),
	}

	usage, apiErr := responsesViaChatCompletions(c, info, adaptor, request)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	return recorder, info, usage, <-captured
}

func responsesOutputTexts(t *testing.T, resp dto.OpenAIResponsesResponse) (texts []string, calls []dto.ResponsesOutput) {
	t.Helper()
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				texts = append(texts, part.Text)
			}
		case "function_call":
			calls = append(calls, item)
		}
	}
	return texts, calls
}

// The bug this guards: a client asking for a Responses stream used to get one
// buffered JSON blob under a text/event-stream header, which CLIs cannot parse.
func TestResponsesViaChatCompletionsStreamsResponsesSSE(t *testing.T) {
	recorder, info, usage, upstream := runResponsesDetour(t, true, "text/event-stream", responsesDetourChatSSE)

	assert.Equal(t, "/v1/chat/completions", upstream.path)
	assert.Equal(t, true, upstream.body["stream"])

	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/event-stream"))
	body := recorder.Body.String()
	events := []string{
		"event: response.created",
		"event: response.output_text.delta",
		`"delta":"Hel"`,
		"event: response.function_call_arguments.delta",
		`{\"q\":\"x\"}`,
		"event: response.completed",
	}
	pos := 0
	for _, want := range events {
		idx := strings.Index(body[pos:], want)
		require.GreaterOrEqual(t, idx, 0, "missing %q after offset %d in:\n%s", want, pos, body)
		pos += idx
	}
	assert.Contains(t, body, `"input_tokens":7`)
	assert.Contains(t, body, `"output_tokens":5`)
	assert.NotContains(t, body, `"id":"grok-test"`)
	assert.Equal(t, 12, usage.TotalTokens)

	assert.Equal(t, relayconstant.RelayModeResponses, info.RelayMode)
	assert.Empty(t, info.RequestURLPath)
}

// The old aggregator kept only delta.content, so a tool call streamed by the
// upstream vanished from the JSON the client got.
func TestResponsesViaChatCompletionsBuffersUpstreamSSEForNonStreamClient(t *testing.T) {
	recorder, info, usage, upstream := runResponsesDetour(t, false, "text/event-stream", responsesDetourChatSSE)

	assert.NotEqual(t, true, upstream.body["stream"])
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	body := recorder.Body.String()
	assert.NotContains(t, body, "data:")
	assert.NotContains(t, body, "event:")

	var resp dto.OpenAIResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	assert.Equal(t, `"completed"`, string(resp.Status))
	texts, calls := responsesOutputTexts(t, resp)
	assert.Equal(t, []string{"Hello"}, texts)
	require.Len(t, calls, 1)
	assert.Equal(t, "lookup", calls[0].Name)
	// Arguments arrive either as a JSON string or as raw JSON; either way the
	// call the upstream streamed must survive the aggregation intact.
	arguments := string(calls[0].Arguments)
	var quoted string
	if err := json.Unmarshal(calls[0].Arguments, &quoted); err == nil {
		arguments = quoted
	}
	assert.JSONEq(t, `{"q":"x"}`, arguments)
	assert.NotEqual(t, "grok-test", resp.ID)
	assert.Contains(t, resp.ID, "resp-test")
	assert.Equal(t, 12, usage.TotalTokens)
	assert.False(t, info.IsStream)
}

func TestResponsesViaChatCompletionsConvertsJSONForNonStreamClient(t *testing.T) {
	recorder, _, usage, _ := runResponsesDetour(t, false, "application/json", responsesDetourChatJSON)

	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json"))
	var resp dto.OpenAIResponsesResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
	assert.Equal(t, `"completed"`, string(resp.Status))
	texts, _ := responsesOutputTexts(t, resp)
	assert.Equal(t, []string{"Hello"}, texts)
	assert.Equal(t, 12, usage.TotalTokens)
}

// An upstream that ignores stream:true must not turn the client's stream into
// a JSON body: the completion is replayed as the SSE sequence that was asked for.
func TestResponsesViaChatCompletionsReplaysJSONAsSSEForStreamClient(t *testing.T) {
	recorder, _, usage, upstream := runResponsesDetour(t, true, "application/json", responsesDetourChatJSON)

	assert.Equal(t, true, upstream.body["stream"])
	assert.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/event-stream"))
	body := recorder.Body.String()
	assert.Contains(t, body, "event: response.created")
	assert.Equal(t, 1, strings.Count(body, "event: response.output_text.delta"))
	assert.Contains(t, body, `"delta":"Hello"`)
	assert.Contains(t, body, "event: response.completed")
	assert.Contains(t, body, `"input_tokens":7`)
	assert.Equal(t, 12, usage.TotalTokens)
}
