package ali

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The DashScope TTS envelope carries the generated audio at output.audio.url.
func TestAliTTSResponseParse(t *testing.T) {
	body := `{"output":{"audio":{"url":"http://dashscope.example/x.wav","expires_at":1783294911,"id":"audio_1"},"finish_reason":"stop"},"usage":{"characters":11}}`
	var r AliResponse
	require.NoError(t, common.Unmarshal([]byte(body), &r))
	require.NotNil(t, r.Output.Audio)
	assert.Equal(t, "http://dashscope.example/x.wav", r.Output.Audio.URL)
	assert.Equal(t, "", r.Code) // no error
}

// The TTS request uses the mapped upstream model + a voice default.
func TestOaiAudio2AliTTSRequest(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "qwen3-tts-flash"},
	}

	// voice supplied
	got := oaiAudio2AliTTSRequest(info, dto.AudioRequest{Model: "qwen3-tts-flash:free", Input: "hi", Voice: "Ethan"})
	assert.Equal(t, "qwen3-tts-flash", got.Model) // mapped, not the :free alias
	assert.Equal(t, "hi", got.Input.Text)
	assert.Equal(t, "Ethan", got.Input.Voice)
	require.NotNil(t, got.Parameters)
	assert.Equal(t, "Ethan", got.Parameters.Voice)

	// voice omitted -> default
	got2 := oaiAudio2AliTTSRequest(info, dto.AudioRequest{Model: "qwen3-tts-flash:free", Input: "hi"})
	assert.Equal(t, defaultAliVoice, got2.Input.Voice)
}
