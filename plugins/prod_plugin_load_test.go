package plugins

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The prod-only plugins replaced native Go task adaptors. These pin the wire
// behavior that AI Horde and xAI channels depend on, so a future upstream sync
// cannot silently change the submit shape or the status mapping.
func TestProdPluginsRegister(t *testing.T) {
	for _, key := range []string{"aihorde", "xai"} {
		_, ok := jsplugin.DefaultRegistry.Generation().Get(key)
		require.Truef(t, ok, "plugin %q did not register", key)
	}
}

func callPlugin(t *testing.T, key, hook string, args ...any) any {
	t.Helper()
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get(key)
	require.True(t, ok)
	value, err := plugin.Engine.Call(context.Background(), hook, args...)
	require.NoError(t, err)
	return value
}

func TestAIHordeSubmitUsesChannelModelMapping(t *testing.T) {
	ctx := map[string]any{
		"baseUrl":       "https://aihorde.net",
		"apiKey":        "test-key",
		"model":         "deliberate:free",
		"upstreamModel": "deliberate:free",
		"requestBody":   map[string]any{"prompt": "a cat", "metadata": map[string]any{"negative_prompt": "blurry"}},
		"channelConfig": `{"models":{"deliberate:free":{"horde_model":"Deliberate","width":512,"height":512,"steps":25,"cfg_scale":7}}}`,
	}
	result, ok := callPlugin(t, "aihorde", "buildSubmitRequest", ctx).(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "https://aihorde.net/api/v2/generate/async", result["url"])
	body, ok := result["body"].(map[string]any)
	require.True(t, ok)

	// The published id must be translated to the real Horde checkpoint name.
	models, ok := body["models"].([]any)
	require.True(t, ok)
	require.Len(t, models, 1)
	assert.Equal(t, "Deliberate", models[0])

	// Negative prompt is encoded inline, not as a separate field.
	assert.Equal(t, "a cat ### blurry", body["prompt"])

	// The uncensored flags are what make the NSFW finetunes usable.
	assert.Equal(t, true, body["nsfw"])
	assert.Equal(t, false, body["censor_nsfw"])
	assert.Equal(t, false, body["replacement_filter"])
}

func TestAIHordeParseTaskResultStatuses(t *testing.T) {
	ctx := map[string]any{"taskId": "t1", "upstreamTaskId": "t1"}

	faulted := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{"faulted": true, "message": "boom"}).(map[string]any)
	assert.Equal(t, "FAILURE", faulted["status"])
	assert.Equal(t, "boom", faulted["reason"])

	impossible := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{"is_possible": false}).(map[string]any)
	assert.Equal(t, "FAILURE", impossible["status"])

	done := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{
		"done":        true,
		"is_possible": true,
		"generations": []any{map[string]any{"img": "https://cdn.example/a.webp"}},
	}).(map[string]any)
	assert.Equal(t, "SUCCESS", done["status"])
	assert.Equal(t, "https://cdn.example/a.webp", done["url"])

	// r2:false workers return inline base64 rather than a link.
	inline := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{
		"done":        true,
		"is_possible": true,
		"generations": []any{map[string]any{"img": "QUJD"}},
	}).(map[string]any)
	assert.Equal(t, "data:image/webp;base64,QUJD", inline["url"])

	queued := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{"is_possible": true, "processing": 0}).(map[string]any)
	assert.Equal(t, "QUEUED", queued["status"])

	// Horde drops a job that no worker picked up; its error envelope carries no
	// status fields. That is terminal, not "still queued" (a live probe polled
	// one of these for 15 minutes before this case was added).
	dropped := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{
		"message": "Image Waiting Prompt (Status) with ID 'x' not found.", "rc": "RequestNotFound",
	}).(map[string]any)
	assert.Equal(t, "FAILURE", dropped["status"])
	assert.Contains(t, dropped["reason"], "not found")

	running := callPlugin(t, "aihorde", "parseTaskResult", ctx, map[string]any{"is_possible": true, "processing": 1}).(map[string]any)
	assert.Equal(t, "IN_PROGRESS", running["status"])
}

func TestXaiSubmitNormalizesResolution(t *testing.T) {
	ctx := map[string]any{
		"baseUrl":       "https://api.example.com",
		"apiKey":        "test-key",
		"model":         "grok-video-3",
		"upstreamModel": "grok-video-3",
		"requestBody":   map[string]any{"prompt": "a dog", "quality": "480p"},
	}
	result := callPlugin(t, "xai", "buildSubmitRequest", ctx).(map[string]any)
	assert.Equal(t, "https://api.example.com/v1/video/create", result["url"])

	body := result["body"].(map[string]any)
	// 480p is unsupported upstream and must be upscaled, and the alias fields
	// must not survive: the upstream rejects them.
	assert.Equal(t, "720P", body["size"])
	assert.NotContains(t, body, "quality")
	assert.NotContains(t, body, "resolution")
}

func TestXaiParseTaskResultStatuses(t *testing.T) {
	ctx := map[string]any{"taskId": "t1", "upstreamTaskId": "t1"}

	completed := callPlugin(t, "xai", "parseTaskResult", ctx, map[string]any{
		"id": "t1", "status": "completed", "video_url": "https://cdn.example/v.mp4",
	}).(map[string]any)
	assert.Equal(t, "SUCCESS", completed["status"])
	assert.Equal(t, "https://cdn.example/v.mp4", completed["url"])

	failed := callPlugin(t, "xai", "parseTaskResult", ctx, map[string]any{
		"id": "t1", "status": "failed", "error": "nope",
	}).(map[string]any)
	assert.Equal(t, "FAILURE", failed["status"])
	assert.Equal(t, "nope", failed["reason"])

	processing := callPlugin(t, "xai", "parseTaskResult", ctx, map[string]any{
		"id": "t1", "status": "processing", "progress": float64(42),
	}).(map[string]any)
	assert.Equal(t, "IN_PROGRESS", processing["status"])
	assert.Equal(t, "42%", processing["progress"])
}

func callPluginPath(t *testing.T, key string, path []string, args ...any) any {
	t.Helper()
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get(key)
	require.True(t, ok)
	value, err := plugin.Engine.CallPath(context.Background(), "protocols", path, args...)
	require.NoError(t, err)
	return value
}

// The openai_responses entry point is how the dashboard and Responses-API
// clients reach these plugins; it must yield the same submit shape the native
// task path does.
func TestProdPluginsDecodeResponsesRequest(t *testing.T) {
	horde := callPluginPath(t, "aihorde", []string{"openai_responses", "decodeRequest"}, map[string]any{
		"model": "deliberate:free",
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "deliberate:free",
			"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "a cat"}}}},
		}},
	}).(map[string]any)
	assert.Equal(t, "submit", horde["kind"])
	assert.Equal(t, "deliberate:free", horde["model"])
	assert.Equal(t, "generate", horde["action"])
	assert.Equal(t, "a cat", horde["requestBody"].(map[string]any)["prompt"])

	xai := callPluginPath(t, "xai", []string{"openai_responses", "decodeRequest"}, map[string]any{
		"model": "grok-video-3",
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model":   "grok-video-3",
			"input":   "a dog",
			"image":   "https://img.example/ref.png",
			"quality": "480p",
		}},
	}).(map[string]any)
	assert.Equal(t, "submit", xai["kind"])
	assert.Equal(t, "image_to_video", xai["action"])
	body := xai["requestBody"].(map[string]any)
	assert.Equal(t, "a dog", body["prompt"])
	assert.Equal(t, "https://img.example/ref.png", body["image"])
	// resolution normalization happens at submit time, so the alias survives decode
	assert.Equal(t, "480p", body["quality"])
}
