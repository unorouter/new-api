package ali

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// defaultAliVoice is used when the OpenAI request omits a voice.
const defaultAliVoice = "Cherry"

// oaiAudio2AliTTSRequest converts an OpenAI /v1/audio/speech request into the
// DashScope multimodal-generation TTS body. Uses the model-mapped upstream name.
func oaiAudio2AliTTSRequest(info *relaycommon.RelayInfo, request dto.AudioRequest) *AliAudioRequest {
	model := info.UpstreamModelName
	if model == "" {
		model = request.Model
	}
	voice := request.Voice
	if voice == "" {
		voice = defaultAliVoice
	}
	return &AliAudioRequest{
		Model:      model,
		Input:      AliAudioInput{Text: request.Input, Voice: voice},
		Parameters: &AliAudioParameters{Voice: voice},
	}
}

// aliTTSHandler parses the DashScope TTS JSON envelope (output.audio.url), fetches
// the generated audio, and streams the raw bytes back as an OpenAI audio response.
func aliTTSHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.Usage) {
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.NewError(err, types.ErrorCodeReadResponseBodyFailed), nil
	}

	var aliResp AliResponse
	if err := common.Unmarshal(body, &aliResp); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody), nil
	}
	if aliResp.Code != "" {
		return types.WithOpenAIError(types.OpenAIError{
			Message: aliResp.Message,
			Type:    "upstream_error",
			Code:    aliResp.Code,
		}, resp.StatusCode), nil
	}
	_ = aliResp.RequestId
	if aliResp.Output.Audio == nil || aliResp.Output.Audio.URL == "" {
		return types.NewOpenAIError(
			fmt.Errorf("dashscope tts returned no audio url"),
			types.ErrorCodeChannelEmptyResponse, http.StatusBadGateway), nil
	}

	audioResp, err := service.DoDownloadRequest(aliResp.Output.Audio.URL)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed), nil
	}
	defer service.CloseResponseBodyGracefully(audioResp)

	audioBytes, err := io.ReadAll(audioResp.Body)
	if err != nil {
		return types.NewError(err, types.ErrorCodeReadResponseBodyFailed), nil
	}

	// DashScope TTS emits .wav; forward the upstream content-type when present.
	contentType := audioResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/wav"
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.WriteHeader(http.StatusOK)
	if _, err := c.Writer.Write(audioBytes); err != nil {
		logger.LogError(c, fmt.Sprintf("failed to write ali tts audio: %v", err))
	}

	// Billing: DashScope bills TTS by input characters (free tier makes exact
	// accounting moot). Use the estimated prompt tokens, or the upstream usage.
	usage := &dto.Usage{
		PromptTokens: info.GetEstimatePromptTokens(),
		TotalTokens:  info.GetEstimatePromptTokens(),
	}
	if aliResp.Usage.TotalTokens > 0 {
		usage.TotalTokens = aliResp.Usage.TotalTokens
	}
	return nil, usage
}
