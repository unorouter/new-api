package relay

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
)

func TestShouldResponsesUseChatCompletions(t *testing.T) {
	responsesCap := func(v bool) *dto.ChannelCapabilities {
		return &dto.ChannelCapabilities{Responses: &v}
	}

	tests := []struct {
		name  string
		model string
		caps  *dto.ChannelCapabilities
		want  bool
	}{
		{name: "image model converts", model: "gpt-image-1", want: true},
		{name: "chat-only channel converts", model: "glm-5.2-thinking", caps: responsesCap(false), want: true},
		{name: "native responses channel passes through", model: "gpt-5.6-sol", caps: responsesCap(true), want: false},
		{name: "untested responses capability passes through", model: "glm-5.2-thinking", caps: &dto.ChannelCapabilities{}, want: false},
		{name: "no capabilities passes through", model: "glm-5.2-thinking", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: tt.model,
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
