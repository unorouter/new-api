package runware

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

// GetRequestURL is the same single endpoint for every task type: Runware discriminates on
// the body's taskType, not on the path.
func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s/v1", strings.TrimSuffix(info.ChannelBaseUrl, "/")), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	task := ImageInferenceTask{
		TaskType: taskTypeImageInference,
		// Runware requires a hyphenated UUIDv4; common.GetUUID strips the hyphens and is
		// rejected with invalidTaskUUID.
		TaskUUID:       uuid.NewString(),
		Model:          info.UpstreamModelName,
		PositivePrompt: request.Prompt,
		IncludeCost:    true,
	}

	if request.N != nil && *request.N > 0 {
		task.NumberResults = int(*request.N)
	}

	// Runware requires explicit dimensions and has no notion of a "size" string, so an
	// absent or unparseable size falls back to the SDXL-native square rather than being
	// forwarded as zero.
	width, height := parseSize(request.Size)
	task.Width, task.Height = width, height

	// The playground sends diffusion params the OpenAI image schema has no field for, so
	// they arrive in Extra. Anything unrecognised is dropped rather than forwarded, since
	// Runware rejects unknown keys for the whole task.
	applyExtras(&task, request.Extra)

	// A passthrough model carries the checkpoint per request, so an arbitrary Civitai
	// checkpoint is reachable without a config entry per model. The value replaces the
	// model name sent upstream, so it is validated as an AIR rather than forwarded raw.
	if air, ok := stringFrom(request.Extra, "air", "civitai_air"); ok && isAIR(air) {
		task.Model = air
	}

	// Runware bills by pixels and steps, but new-api charges one flat price per call for a
	// fixed-price model. Without a ceiling the caller picks our cost: 2048x2048 at 100 steps
	// costs 10x a 1024 default, and Flux at that size costs 35x, well past any sane retail.
	// Clamp both axes so an unbounded request cannot be billed below cost.
	clampCost(&task)

	// b64_json is the only OpenAI response_format that maps onto bytes; everything else
	// (including the default) takes the URL, which avoids paying to move the image twice.
	if strings.EqualFold(request.ResponseFormat, "b64_json") {
		task.OutputType = outputTypeBase64Data
	} else {
		task.OutputType = outputTypeURL
	}

	// The body is an array even for a single task.
	return []ImageInferenceTask{task}, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	if info.RelayMode != constant.RelayModeImagesGenerations && info.RelayMode != constant.RelayModeImagesEdits {
		return nil, types.NewError(errors.New("runware channel only supports image generation"), types.ErrorCodeInvalidRequest)
	}
	return imageHandler(c, resp, info)
}

func imageHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)

	var runwareResp Response
	if err := common.Unmarshal(body, &runwareResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	// A Runware error can arrive with HTTP 200, so the body is authoritative. Errors and
	// data can coexist on a partially-successful batch; only a completely empty result set
	// is a failure.
	if len(runwareResp.Data) == 0 {
		return nil, types.NewErrorWithStatusCode(
			errors.New(firstErrorMessage(runwareResp.Errors)),
			types.ErrorCodeBadResponse, upstreamStatus(resp.StatusCode))
	}

	imageResp := dto.ImageResponse{
		Created: common.GetTimestamp(),
		Data:    make([]dto.ImageData, 0, len(runwareResp.Data)),
	}
	for _, item := range runwareResp.Data {
		imageResp.Data = append(imageResp.Data, dto.ImageData{
			Url:     item.ImageURL,
			B64Json: item.ImageBase64Data,
			// Each image in a batch gets its own seed, so this is per-item rather than
			// per-response: without it a generation made without an explicit seed can
			// never be reproduced.
			Seed: item.Seed,
		})
	}

	jsonResponse, err := common.Marshal(imageResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	// Write errors are not surfaced: the header is already committed, so returning an
	// error here would have the caller write a second, conflicting body.
	_, _ = c.Writer.Write(jsonResponse)

	// Runware bills by GPU time, so the same prompt at the same size can cost different
	// amounts and no flat per-call price fits. It reports the real cost per image (we ask
	// via includeCost), so settlement bills the sum of what this request actually cost
	// times the model's markup. Summed, not averaged: a batch pays for every image.
	for _, item := range runwareResp.Data {
		info.UpstreamCostUSD += item.Cost
	}

	// Runware reports no token usage, and PromptTokens here means text tokens: ImageHelper
	// counts the prompt itself when this is left empty, and charges the fixed per-call price
	// from the request's N. Returning an image count instead would be billed as prompt text.
	return &dto.Usage{}, nil
}

func upstreamStatus(status int) int {
	if status >= http.StatusBadRequest {
		return status
	}
	return http.StatusBadGateway
}

func firstErrorMessage(errs []ResponseError) string {
	for _, e := range errs {
		if e.Message != "" {
			if e.Parameter != "" {
				return fmt.Sprintf("%s (parameter: %s)", e.Message, e.Parameter)
			}
			return e.Message
		}
	}
	return "runware returned no images and no error detail"
}

// parseSize turns an OpenAI "WIDTHxHEIGHT" size into explicit dimensions. Runware has no
// size string and requires both, so anything unparseable falls back to 1024x1024.
func parseSize(size string) (int, int) {
	const fallback = 1024
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return fallback, fallback
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return fallback, fallback
	}
	return width, height
}

// clampCost bounds the two things Runware prices on. Dimensions are scaled down together so
// the requested aspect ratio survives, rather than being squashed to a square.
func clampCost(task *ImageInferenceTask) {
	if task.Steps != nil && *task.Steps > maxSteps {
		capped := maxSteps
		task.Steps = &capped
	}
	if task.Width <= 0 || task.Height <= 0 {
		return
	}
	if task.Width*task.Height > maxPixels {
		scale := math.Sqrt(float64(maxPixels) / float64(task.Width*task.Height))
		task.Width = roundTo64(float64(task.Width) * scale)
		task.Height = roundTo64(float64(task.Height) * scale)
	}
	// Runware refuses any side that is not a multiple of 64 (invalidWidth/invalidHeight), so
	// this runs even for a request already inside the budget: a common size like 1920x1080
	// fits the pixel cap and would otherwise be forwarded verbatim and rejected outright.
	task.Width = roundTo64(float64(task.Width))
	task.Height = roundTo64(float64(task.Height))
	task.Width = clampSide(task.Width)
	task.Height = clampSide(task.Height)
}

func clampSide(v int) int {
	if v > maxSide {
		return maxSide
	}
	if v < minSide {
		return minSide
	}
	return v
}

// Runware only accepts dimensions that are multiples of 64.
func roundTo64(v float64) int {
	n := int(math.Round(v/64)) * 64
	if n < 64 {
		return 64
	}
	return n
}

func (a *Adaptor) GetModelList() []string { return nil }

func (a *Adaptor) GetChannelName() string { return "runware" }

// Runware is image-only. The remaining Adaptor methods exist to satisfy the interface and
// must reject rather than silently forwarding a request the upstream cannot serve.
func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errUnsupported("chat completions")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errUnsupported("rerank")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errUnsupported("embeddings")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errUnsupported("audio")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, errUnsupported("responses")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, errUnsupported("claude messages")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errUnsupported("gemini")
}

func errUnsupported(what string) error {
	return fmt.Errorf("runware channel does not support %s", what)
}
