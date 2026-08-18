package cloudflare

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	//TODO implement me
	panic("implement me")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// Base URL already includes the account path (".../accounts/{id}/ai"). When the
	// legacy host-only form (no "/ai" suffix) is configured, reconstruct it from the
	// account id carried in ApiVersion for backward compatibility.
	base := strings.TrimRight(info.ChannelBaseUrl, "/")
	if !strings.HasSuffix(base, "/ai") {
		base = fmt.Sprintf("%s/client/v4/accounts/%s/ai", base, info.ApiVersion)
	}
	switch info.RelayMode {
	case constant.RelayModeChatCompletions:
		return base + "/v1/chat/completions", nil
	case constant.RelayModeEmbeddings:
		return base + "/v1/embeddings", nil
	case constant.RelayModeResponses:
		return base + "/v1/responses", nil
	default:
		return fmt.Sprintf("%s/run/%s", base, info.UpstreamModelName), nil
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", fmt.Sprintf("Bearer %s", info.ApiKey))
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch info.RelayMode {
	case constant.RelayModeCompletions:
		return convertCf2CompletionsRequest(*request), nil
	default:
		return request, nil
	}
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	// TTS (text-to-speech): Workers AI takes a native {prompt, lang} JSON body,
	// not the OpenAI /audio/speech shape. melotts etc. return base64 WAV.
	if info.RelayMode == constant.RelayModeAudioSpeech {
		lang := "en"
		if l := request.Language; len(l) > 0 {
			var parsed string
			if err := common.Unmarshal(l, &parsed); err == nil && parsed != "" {
				lang = parsed
			}
		}
		body, err := common.Marshal(CfTTSRequest{Prompt: request.Input, Lang: lang})
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(body), nil
	}
	// STT (transcription/translation): forward the uploaded audio file as-is.
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		return nil, errors.New("file is required")
	}
	defer file.Close()
	requestBody := &bytes.Buffer{}
	if _, err := io.Copy(requestBody, file); err != nil {
		return nil, err
	}
	return requestBody, nil
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	cfReq := CfImageRequest{Prompt: request.Prompt}
	if w, h, ok := parseImageSize(request.Size); ok {
		cfReq.Width = w
		cfReq.Height = h
	}
	return cfReq, nil
}

// parseImageSize splits an OpenAI "WxH" size string into width/height.
func parseImageSize(size string) (int, int, bool) {
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case constant.RelayModeEmbeddings:
		fallthrough
	case constant.RelayModeChatCompletions:
		if info.IsStream {
			err, usage = cfStreamHandler(c, info, resp)
		} else {
			err, usage = cfHandler(c, info, resp)
		}
	case constant.RelayModeResponses:
		if info.IsStream {
			usage, err = openai.OaiResponsesStreamHandler(c, info, resp)
		} else {
			usage, err = openai.OaiResponsesHandler(c, info, resp)
		}
	case constant.RelayModeAudioSpeech:
		err, usage = cfTTSHandler(c, info, resp)
	case constant.RelayModeAudioTranslation:
		fallthrough
	case constant.RelayModeAudioTranscription:
		err, usage = cfSTTHandler(c, info, resp)
	case constant.RelayModeImagesGenerations:
		err, usage = cfImageHandler(c, info, resp)
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
