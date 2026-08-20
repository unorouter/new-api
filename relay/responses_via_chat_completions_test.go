package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
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
		want        bool
	}{
		{name: "image model converts", model: "gpt-image-1", channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "chat-only channel converts", model: "glm-5.2-thinking", caps: responsesCap(false), channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "channel marked native passes through", model: "gpt-5.6-sol", caps: responsesCap(true), channelType: constant.ChannelTypeOpenAI, want: false},
		// An unmarked OpenAI-shaped channel is a chat-only relay far more often than
		// not, and guessing native is what left users collecting 404s.
		{name: "unmarked openai channel converts", model: "glm-5.2-thinking", channelType: constant.ChannelTypeOpenAI, want: true},
		{name: "unmarked capabilities object converts", model: "glm-5.2-thinking", caps: &dto.ChannelCapabilities{}, channelType: constant.ChannelTypeOpenAI, want: true},
		// Types whose own endpoint declaration includes Responses never convert, so a
		// Codex channel needs no marking.
		{name: "codex type passes through unmarked", model: "gpt-5.6-sol", channelType: constant.ChannelTypeCodex, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: tt.model,
					ChannelType:       tt.channelType,
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
