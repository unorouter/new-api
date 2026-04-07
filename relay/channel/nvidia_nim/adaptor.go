package nvidia_nim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil {
		return "", errors.New("nvidia_nim: relay info is nil")
	}
	baseURL := info.ChannelBaseUrl
	if baseURL == "" {
		baseURL = constant.ChannelBaseURLs[constant.ChannelTypeNvidiaNIM]
	}
	baseURL = strings.TrimRight(baseURL, "/")

	model := info.UpstreamModelName
	if model == "" {
		return "", errors.New("nvidia_nim: model name is required")
	}

	return fmt.Sprintf("%s/v1/genai/%s", baseURL, model), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	if info == nil {
		return errors.New("nvidia_nim: relay info is nil")
	}
	if info.ApiKey == "" {
		return errors.New("nvidia_nim: API key is required")
	}
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	req.Set("Content-Type", "application/json")
	req.Set("Accept", "application/json")
	return nil
}

// nimImageRequest is the upstream request format for NVIDIA NIM image generation.
type nimImageRequest struct {
	Prompt   string  `json:"prompt"`
	CfgScale float64 `json:"cfg_scale,omitempty"`
	Steps    int     `json:"steps,omitempty"`
	Seed     *int    `json:"seed,omitempty"`
}

// nimImageResponse is the upstream response format from NVIDIA NIM image generation.
type nimImageResponse struct {
	Image        string `json:"image"`
	FinishReason string `json:"finish_reason"`
	Seed         int    `json:"seed"`
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("nvidia_nim: prompt is required")
	}

	nimReq := nimImageRequest{
		Prompt:   request.Prompt,
		CfgScale: 5,
		Steps:    20,
	}

	// Pass through extra fields if provided (cfg_scale, steps, seed, etc.)
	if len(request.ExtraFields) > 0 {
		var extra map[string]json.RawMessage
		if err := common.Unmarshal(request.ExtraFields, &extra); err == nil {
			if v, ok := extra["cfg_scale"]; ok {
				var f float64
				if json.Unmarshal(v, &f) == nil {
					nimReq.CfgScale = f
				}
			}
			if v, ok := extra["steps"]; ok {
				var s int
				if json.Unmarshal(v, &s) == nil {
					nimReq.Steps = s
				}
			}
			if v, ok := extra["seed"]; ok {
				var s int
				if json.Unmarshal(v, &s) == nil {
					nimReq.Seed = &s
				}
			}
		}
	}

	for key, raw := range request.Extra {
		switch strings.ToLower(key) {
		case "cfg_scale":
			var f float64
			if common.Unmarshal(raw, &f) == nil {
				nimReq.CfgScale = f
			}
		case "steps":
			var s int
			if common.Unmarshal(raw, &s) == nil {
				nimReq.Steps = s
			}
		case "seed":
			var s int
			if common.Unmarshal(raw, &s) == nil {
				nimReq.Seed = &s
			}
		}
	}

	return nimReq, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	if resp == nil {
		return nil, types.NewError(errors.New("nvidia_nim: empty response"), types.ErrorCodeBadResponse)
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadResponseBodyFailed)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, types.NewError(
			fmt.Errorf("nvidia_nim: upstream returned HTTP %d: %s", resp.StatusCode, string(responseBody)),
			types.ErrorCodeBadResponse,
		)
	}

	var nimResp nimImageResponse
	if err := common.Unmarshal(responseBody, &nimResp); err != nil {
		return nil, types.NewError(
			fmt.Errorf("nvidia_nim: failed to decode response: %w", err),
			types.ErrorCodeBadResponseBody,
		)
	}

	if nimResp.Image == "" {
		return nil, types.NewError(
			errors.New("nvidia_nim: no image data in response"),
			types.ErrorCodeBadResponseBody,
		)
	}

	imageResponse := dto.ImageResponse{
		Created: common.GetTimestamp(),
		Data: []dto.ImageData{
			{B64Json: nimResp.Image},
		},
	}

	responseBytes, err := common.Marshal(imageResponse)
	if err != nil {
		return nil, types.NewError(
			fmt.Errorf("nvidia_nim: failed to encode response: %w", err),
			types.ErrorCodeBadResponseBody,
		)
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(responseBytes)

	usage := &dto.Usage{}
	return usage, nil
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

// Unsupported methods (image-only adaptor)

func (a *Adaptor) ConvertOpenAIRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("nvidia_nim: text generation is not supported, use OpenAI channel type instead")
}

func (a *Adaptor) ConvertRerankRequest(*gin.Context, int, dto.RerankRequest) (any, error) {
	return nil, errors.New("nvidia_nim: rerank is not supported")
}

func (a *Adaptor) ConvertEmbeddingRequest(*gin.Context, *relaycommon.RelayInfo, dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("nvidia_nim: embeddings not supported")
}

func (a *Adaptor) ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("nvidia_nim: audio not supported")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(*gin.Context, *relaycommon.RelayInfo, dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("nvidia_nim: responses API not supported")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("nvidia_nim: Claude format not supported")
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("nvidia_nim: Gemini format not supported")
}
