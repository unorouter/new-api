package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// Some channels serve image models through the async task-plugin system rather
// than a synchronous adaptor (AI Horde is the only one today). Their models are
// published in the catalog as ordinary image models, so an OpenAI client and the
// web chat both post them to /v1/images/generations, where ChannelType2APIType
// deliberately yields -1 and GetAdaptor returns nil. Before this bridge that was
// a bare "invalid api type: -1" 500 for every such request.
//
// The bridge submits through the same path the native task endpoints use, then
// waits on the task row until it reaches a terminal state, and answers in the
// OpenAI image shape. Submission, retry, billing and persistence all stay in
// executeTaskSubmission, so a request is priced and logged exactly once.

// imageTaskWaitSeconds bounds the wait. 0 (the default) means no bound: the loop
// then ends only on a terminal task state or client disconnect. AI Horde measures
// p50 1.2s / p95 1.3s in production, so the cap exists as an escape hatch rather
// than as normal operation.
var imageTaskWaitSeconds = common.GetEnvOrDefault("AIHORDE_IMAGE_WAIT_SECONDS", 0)

// imageTaskPollInterval is the gap between task-row reads.
var imageTaskPollInterval = time.Duration(common.GetEnvOrDefault("AIHORDE_IMAGE_POLL_MS", 400)) * time.Millisecond

// imageTaskLoadTimeout bounds a single database read so a stalled query cannot
// wedge the loop.
const imageTaskLoadTimeout = 5 * time.Second

// imageTaskPluginKey is the task plugin that serves this channel type. It matches
// relay.taskPluginKeys, which maps ChannelTypeAIHorde to the same key.
const imageTaskPluginKey = "aihorde"

// IsImageTaskChannel reports whether an image request for this channel type must
// be served through the task-plugin system instead of a synchronous adaptor.
func IsImageTaskChannel(channelType int) bool {
	return channelType == constant.ChannelTypeAIHorde
}

// ServeImageAsTask answers an /v1/images/generations request from a task-plugin
// channel. It returns nil once a response has been written.
func ServeImageAsTask(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	// These models are published as image models but reach the gateway two ways:
	// /v1/images/generations from OpenAI clients, and /v1/chat/completions from the
	// web chat, which renders the result as markdown. Both must work.
	var prompt, requestedModel, responseFormat string
	var imageCount *uint
	var size string
	_, isChat := info.Request.(*dto.GeneralOpenAIRequest)
	switch request := info.Request.(type) {
	case *dto.ImageRequest:
		prompt, requestedModel, size, responseFormat, imageCount = request.Prompt, request.Model, request.Size, request.ResponseFormat, request.N
	case *dto.GeneralOpenAIRequest:
		requestedModel = request.Model
		for i := len(request.Messages) - 1; i >= 0; i-- {
			if request.Messages[i].Role == "user" {
				prompt = strings.TrimSpace(request.Messages[i].StringContent())
				break
			}
		}
	default:
		return types.NewErrorWithStatusCode(
			fmt.Errorf("unsupported request type %T for a task-plugin image channel", info.Request),
			types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if prompt == "" {
		return types.NewErrorWithStatusCode(errors.New("prompt is required"),
			types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	taskInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		return types.NewError(err, types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithSkipRetry())
	}
	// The image request IS the plugin's request body: buildSubmitBody reads
	// prompt and size straight off it, and metadata carries the knobs an OpenAI
	// caller cannot express otherwise. Setting task_request also tells the
	// adaptor to skip ValidateBasicTaskRequest, which demands video fields.
	// `n` arrives already bounded by dto.MaxImageN from the shared image validator.
	requestBody := map[string]any{
		"model":  requestedModel,
		"prompt": prompt,
	}
	if size != "" {
		requestBody["size"] = size
	}
	if imageCount != nil && *imageCount > 0 {
		requestBody["metadata"] = map[string]any{"n": int(*imageCount)}
	}
	c.Set("task_request", requestBody)
	c.Set("task_action", "generate")
	taskInfo.Action = "generate"
	// Name the plugin explicitly. Without this GetTaskPlatform falls back to the
	// channel type, and the task row is stored under platform "62" while the
	// background poller only ever advances rows under the plugin key, so the task
	// would stay QUEUED forever.
	plugin, resolved := pluginruntime.DefaultRegistry.Generation().Get(imageTaskPluginKey)
	if !resolved || plugin == nil {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("task plugin %q is unavailable", imageTaskPluginKey),
			types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	c.Set("task_plugin_key", plugin.Meta.Key)
	c.Set("platform", plugin.Meta.Key)
	// Pin the plugin object as the task routes do. Resolving by channel type alone
	// keeps the numeric platform, and RelayTaskSubmit then stores the task under
	// "62" instead of the plugin key.
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{
		Generation: pluginruntime.DefaultRegistry.Generation(),
		Plugin:     plugin,
	})

	outcome, taskErr := executeTaskSubmission(c, taskInfo)
	if taskErr != nil {
		return imageTaskSubmitError(taskErr)
	}

	return waitImageTask(c, outcome, requestedModel, responseFormat, isChat)
}

// waitImageTask polls the task row until it is terminal, then writes the OpenAI
// image response. Without a configured wait bound the only exits are a terminal
// status and the client going away.
func waitImageTask(c *gin.Context, outcome *taskSubmissionOutcome, requestedModel, responseFormat string, asChat bool) *types.NewAPIError {
	if outcome == nil || outcome.Task == nil {
		return types.NewError(errors.New("task submission returned no task"), types.ErrorCodeDoRequestFailed)
	}
	taskID := outcome.Task.TaskID
	platform := outcome.Task.Platform
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)

	waitContext := c.Request.Context()
	if imageTaskWaitSeconds > 0 {
		var cancel context.CancelFunc
		waitContext, cancel = context.WithTimeout(waitContext, time.Duration(imageTaskWaitSeconds)*time.Second)
		defer cancel()
	}

	// The submitted task may already be terminal: some upstreams answer the
	// submit call with a finished job.
	for {
		loadContext, cancelLoad := context.WithTimeout(waitContext, imageTaskLoadTimeout)
		task, exists, loadErr := model.GetTaskForProtocolObservation(loadContext, userID, platform, taskID)
		overloaded := errors.Is(loadContext.Err(), context.DeadlineExceeded) && waitContext.Err() == nil
		cancelLoad()

		switch {
		case overloaded:
			logger.LogWarn(c, fmt.Sprintf("image task observation overloaded; task=%s", taskID))
		case loadErr != nil || !exists || task == nil:
			if waitContext.Err() != nil {
				return imageTaskWaitEnded(c, taskID, waitContext)
			}
			if loadErr != nil && !errors.Is(loadErr, context.Canceled) {
				logger.LogError(c, fmt.Sprintf("image task observation failed; task=%s", taskID))
			}
			return types.NewErrorWithStatusCode(
				fmt.Errorf("image task %s is no longer available", taskID),
				types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
		case task.Status == model.TaskStatusSuccess:
			return writeImageTaskSuccess(c, task, requestedModel, responseFormat, asChat)
		case task.Status == model.TaskStatusFailure:
			// Upstream faults stay retryable so the relay loop can try another
			// channel serving the same model.
			reason := strings.TrimSpace(task.FailReason)
			if reason == "" {
				reason = "image generation failed upstream"
			}
			return types.NewErrorWithStatusCode(errors.New(reason), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
		}

		select {
		case <-waitContext.Done():
			return imageTaskWaitEnded(c, taskID, waitContext)
		case <-time.After(imageTaskPollInterval):
		}
	}
}

// imageTaskWaitEnded separates a client that hung up (nothing to report) from a
// configured wait bound that elapsed while the task kept running.
func imageTaskWaitEnded(c *gin.Context, taskID string, waitContext context.Context) *types.NewAPIError {
	if c.Request.Context().Err() != nil {
		logger.LogDebug(c, fmt.Sprintf("image task client disconnected; task=%s", taskID))
		return nil
	}
	if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
		// The task is durable and keeps running; name it so the caller can fetch it.
		return types.NewErrorWithStatusCode(
			fmt.Errorf("image is still generating; retrieve it with task %s", taskID),
			types.ErrorCodeDoRequestFailed, http.StatusGatewayTimeout, types.ErrOptionWithSkipRetry())
	}
	return nil
}

func writeImageTaskSuccess(c *gin.Context, task *model.Task, requestedModel, responseFormat string, asChat bool) *types.NewAPIError {
	urls := task.GetResultURLs()
	if len(urls) == 0 {
		return types.NewErrorWithStatusCode(
			errors.New("image task finished without an image"),
			types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	if asChat {
		// The web chat renders markdown, so the image arrives as an image tag in
		// the assistant message rather than as an images-API payload.
		var content strings.Builder
		for _, url := range urls {
			if content.Len() > 0 {
				content.WriteString("\n")
			}
			content.WriteString("![image](")
			content.WriteString(url)
			content.WriteString(")")
		}
		c.JSON(http.StatusOK, dto.OpenAITextResponse{
			Id:      "chatcmpl-" + task.TaskID,
			Model:   requestedModel,
			Object:  "chat.completion",
			Created: common.GetTimestamp(),
			Choices: []dto.OpenAITextResponseChoice{{
				Index:        0,
				Message:      dto.Message{Role: "assistant", Content: content.String()},
				FinishReason: "stop",
			}},
		})
		return nil
	}
	wantBase64 := strings.EqualFold(responseFormat, "b64_json")
	data := make([]dto.ImageData, 0, len(urls))
	for _, url := range urls {
		// parseTaskResult returns either an https URL or an inline data: URI.
		if payload, ok := strings.CutPrefix(url, "data:"); ok {
			if _, encoded, found := strings.Cut(payload, "base64,"); found {
				data = append(data, dto.ImageData{B64Json: encoded})
				continue
			}
		}
		if wantBase64 {
			// Only an inline result can satisfy b64_json without refetching the
			// image; hand back the URL rather than failing the request.
			logger.LogDebug(c, "image task returned a url while b64_json was requested")
		}
		data = append(data, dto.ImageData{Url: url})
	}
	c.JSON(http.StatusOK, dto.ImageResponse{Created: common.GetTimestamp(), Data: data})
	return nil
}

func imageTaskSubmitError(taskErr *taskdto.TaskError) *types.NewAPIError {
	status := taskErr.StatusCode
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	message := strings.TrimSpace(taskErr.Message)
	if message == "" {
		message = "image task submission failed"
	}
	options := []types.NewAPIErrorOptions{}
	// A local rejection (bad request, unusable model) fails identically on every
	// channel; only upstream faults are worth another channel.
	if taskErr.LocalError || status == http.StatusBadRequest {
		options = append(options, types.ErrOptionWithSkipRetry())
	}
	return types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeDoRequestFailed, status, options...)
}
