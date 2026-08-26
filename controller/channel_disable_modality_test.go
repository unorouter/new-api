package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gateway auto-disables a channel when ShouldDisableChannel flags its error.
// PROD-ONLY (fork) invariant under test: a channel serving a NON-RECOVERABLE
// modality (audio / video / native-task image such as qwen-image, wan2.x, hailuo,
// happyhorse) must NOT be auto-disabled by a user-caused request error, because the
// scheduled channel-test cron has no probe path for those models and a false-disable
// is permanent. A GENUINE channel fault (dead key, drained quota, model-unavailable,
// 5xx) must STILL disable such a channel. Text / embedding / OpenAI-shaped image are
// cron-recoverable, so they are never spared here (self-heal in ~1 min).
func TestModalityAwareChannelDisable(t *testing.T) {
	prevEnabled := common.AutomaticDisableChannelEnabled
	prevRanges := operation_setting.AutomaticDisableStatusCodeRanges
	prevProvider := types.ChannelFaultKeywordsProvider
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = prevEnabled
		operation_setting.AutomaticDisableStatusCodeRanges = prevRanges
		types.ChannelFaultKeywordsProvider = prevProvider
	})

	common.AutomaticDisableChannelEnabled = true
	require.NoError(t, operation_setting.AutomaticDisableStatusCodesFromString("400-404,406,410,429,500,502-504,521,524,530"))
	types.ChannelFaultKeywordsProvider = func() []string {
		return []string{"api key not valid", "用户额度不足"}
	}

	openAI := func(msg string, code types.ErrorCode, status int) *types.NewAPIError {
		return types.NewOpenAIError(errors.New(msg), code, status)
	}
	// WithOpenAIError runs the fork reclassification path (credential -> channel:invalid_key,
	// model-unavailable -> channel:model_mapped_error), matching real upstream error shaping.
	reclassified := func(msg string, status int) *types.NewAPIError {
		return types.WithOpenAIError(types.OpenAIError{Message: msg}, status)
	}

	cases := []struct {
		name        string
		model       string
		err         *types.NewAPIError
		wantDisable bool
	}{
		{"media_400_bad_image", "qwen-image", openAI("The image data you provided does not represent a valid image", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest), false},
		{"native_video_r2v_400_url", "wan2.6-r2v", openAI("url error, please check url", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest), false},
		{"happyhorse_404_wrong_endpoint", "happyhorse-1.1-t2v", openAI("not found", types.ErrorCodeBadResponseStatusCode, http.StatusNotFound), false},
		{"hailuo_400", "minimax-hailuo-02", openAI("content parameter's length invalid", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest), false},
		{"audio_whisper_404", "whisper-1", openAI("The 'whisper-large-v3:free' model was not found", types.ErrorCodeBadResponseStatusCode, http.StatusNotFound), false},
		{"tts_400_missing_param", "tts-1", openAI("The voice property is required", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest), false},

		{"media_credential_fault_still_disables", "qwen-image", reclassified("API key not valid. Please pass a valid API key.", http.StatusBadRequest), true},
		{"media_model_unavailable_still_disables", "wan2.5-i2v", reclassified("Model 'wan2.5-i2v' not found. Available models: ...", http.StatusBadRequest), true},
		{"media_5xx_still_disables", "qwen-image", openAI("internal server error", types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError), true},

		// Recoverable modalities: our fork code must NEVER spare them; disable follows
		// ShouldDisableChannel alone. 400 is deterministic-upstream so it does not disable
		// (unchanged upstream behavior), but that is NOT due to our skip guard.
		{"text_400_unchanged", "gpt-4o", openAI("bad request", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest), false},
		{"imagegen_400_unchanged", "dall-e-3", openAI("bad request", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest), false},
		{"embedding_500_still_disables", "text-embedding-3-large", openAI("internal server error", types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			netDisable := service.ShouldDisableChannel(tc.err) && !shouldSkipDisableForModality(tc.model, tc.err)
			assert.Equal(t, tc.wantDisable, netDisable, "net auto-ban decision")
		})
	}
}

// isCronRecoverableModel must equal the set pickAutoTestModel can probe (text OR
// embedding OR OpenAI-shaped image). Anything else (audio/video/native-image) is a
// permanent black hole on disable and must be reported non-recoverable.
func TestIsCronRecoverableModelContract(t *testing.T) {
	recoverable := []string{
		"gpt-4o", "claude-opus-4-8", "dall-e-3", "sdxl", "flux.1-schnell",
		"gpt-image-1", "text-embedding-3-large", "bge-large-en-v1.5", "voyage-3",
	}
	unrecoverable := []string{
		"qwen-image", "z-image-turbo", "wan2.6-r2v", "wan2.7-t2v", "happyhorse-1.1-r2v",
		"minimax-hailuo-02", "wan2.2-animate-mix", "wan2.1-vace-plus",
		"whisper-1", "tts-1", "sora-2", "wan2.5-i2v",
	}
	for _, m := range recoverable {
		assert.Truef(t, isCronRecoverableModel(m), "%s should be cron-recoverable", m)
	}
	for _, m := range unrecoverable {
		assert.Falsef(t, isCronRecoverableModel(m), "%s should be non-recoverable", m)
	}

	// Our fork code never spares a recoverable modality, regardless of error shape.
	req := types.NewOpenAIError(errors.New("bad request"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
	for _, m := range recoverable {
		assert.Falsef(t, shouldSkipDisableForModality(m, req), "%s must never be spared by fork guard", m)
	}
}
