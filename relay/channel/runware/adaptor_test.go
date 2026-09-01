package runware

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func extras(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

// Runware requires explicit width/height and has no size string, so every OpenAI size must
// resolve to real dimensions. A malformed size must fall back rather than sending zeros,
// which the upstream would reject for the whole task.
func TestParseSize(t *testing.T) {
	cases := []struct {
		name          string
		size          string
		width, height int
	}{
		{"square", "1024x1024", 1024, 1024},
		{"portrait", "832x1216", 832, 1216},
		{"uppercase separator", "1024X768", 1024, 768},
		{"padded", " 512 x 768 ", 512, 768},
		{"empty falls back", "", 1024, 1024},
		{"auto falls back", "auto", 1024, 1024},
		{"garbage falls back", "wide", 1024, 1024},
		{"zero falls back", "0x0", 1024, 1024},
		{"negative falls back", "-64x512", 1024, 1024},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, h := parseSize(c.size)
			assert.Equal(t, c.width, w)
			assert.Equal(t, c.height, h)
		})
	}
}

// Omitted params must stay absent from the marshalled task. Sending steps=0 or CFGScale=0
// is not the same as omitting them: Runware would take the zero literally instead of
// applying the model's own default.
func TestOmittedParamsAreNotSerialized(t *testing.T) {
	task := ImageInferenceTask{
		TaskType:       taskTypeImageInference,
		TaskUUID:       "uuid",
		Model:          "civitai:257749@290640",
		PositivePrompt: "a cat",
		Width:          1024,
		Height:         1024,
	}
	body, err := common.Marshal([]ImageInferenceTask{task})
	require.NoError(t, err)

	for _, absent := range []string{"steps", "CFGScale", "seed", "clipSkip", "strength", "scheduler", "negativePrompt", "seedImage", "maskImage"} {
		assert.NotContains(t, string(body), `"`+absent+`"`, "%s must be omitted when unset", absent)
	}
	assert.Contains(t, string(body), `"positivePrompt":"a cat"`)
}

// An explicit zero must survive, since 0 is a legitimate value for seed and clipSkip. This
// is the reason those fields are pointers rather than plain ints with omitempty.
func TestExplicitZeroIsPreserved(t *testing.T) {
	task := ImageInferenceTask{TaskType: taskTypeImageInference, TaskUUID: "u", Model: "m", PositivePrompt: "p"}
	applyExtras(&task, extras(t, `{"seed":0,"clip_skip":0}`))

	require.NotNil(t, task.Seed)
	require.NotNil(t, task.ClipSkip)
	assert.Equal(t, int64(0), *task.Seed)
	assert.Equal(t, 0, *task.ClipSkip)

	body, err := common.Marshal(task)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"seed":0`)
	assert.Contains(t, string(body), `"clipSkip":0`)
}

// The playground sends diffusion params under names the OpenAI image schema has no field
// for. Each must reach its Runware equivalent, including the aliases the form actually uses
// (cfg for CFGScale, denoise for strength, sampler for scheduler).
func TestApplyExtrasMapsPlaygroundParams(t *testing.T) {
	task := ImageInferenceTask{TaskType: taskTypeImageInference, TaskUUID: "u", Model: "m", PositivePrompt: "p"}
	applyExtras(&task, extras(t, `{
		"steps": 28,
		"cfg": 6.5,
		"sampler": "DPM++ 2M Karras",
		"seed": 12345,
		"clip_skip": 2,
		"denoise": 0.65,
		"negative_prompt": "blurry",
		"output_format": "png"
	}`))

	require.NotNil(t, task.Steps)
	require.NotNil(t, task.CFGScale)
	require.NotNil(t, task.Seed)
	require.NotNil(t, task.ClipSkip)
	require.NotNil(t, task.Strength)

	assert.Equal(t, 28, *task.Steps)
	assert.InDelta(t, 6.5, *task.CFGScale, 1e-9)
	assert.Equal(t, int64(12345), *task.Seed)
	assert.Equal(t, 2, *task.ClipSkip)
	assert.InDelta(t, 0.65, *task.Strength, 1e-9)
	assert.Equal(t, "DPM++ 2M Karras", task.Scheduler)
	assert.Equal(t, "blurry", task.NegativePrompt)
	assert.Equal(t, "PNG", task.OutputFormat, "lowercase client format must become the uppercase enum")
}

// A value of the wrong JSON type must be skipped, not coerced. Coercing would silently
// change what the user asked for; skipping lets the model default apply.
func TestApplyExtrasSkipsWrongTypes(t *testing.T) {
	task := ImageInferenceTask{TaskType: taskTypeImageInference, TaskUUID: "u", Model: "m", PositivePrompt: "p"}
	applyExtras(&task, extras(t, `{"steps":"twenty","cfg":"high","seed":null,"scheduler":123}`))

	assert.Nil(t, task.Steps)
	assert.Nil(t, task.CFGScale)
	assert.Nil(t, task.Seed)
	assert.Empty(t, task.Scheduler)
}

// LoRA chains arrive in the playground's {name,weight} shape but Runware expects
// {model,weight} addressed by AIR. Both must work, a missing weight must default to 1, and
// an entry with no identifier must be dropped rather than sent as an empty model.
func TestApplyExtrasLoraChain(t *testing.T) {
	t.Run("playground name shape", func(t *testing.T) {
		task := ImageInferenceTask{}
		applyExtras(&task, extras(t, `{"loras":[
			{"name":"civitai:1234@5678","weight":0.8},
			{"name":"civitai:1111@2222"}
		]}`))
		require.Len(t, task.Lora, 2)
		assert.Equal(t, "civitai:1234@5678", task.Lora[0].Model)
		assert.InDelta(t, 0.8, task.Lora[0].Weight, 1e-9)
		assert.InDelta(t, 1.0, task.Lora[1].Weight, 1e-9, "missing weight defaults to 1")
	})

	t.Run("native model shape", func(t *testing.T) {
		task := ImageInferenceTask{}
		applyExtras(&task, extras(t, `{"lora":[{"model":"civitai:9@9","weight":0.5}]}`))
		require.Len(t, task.Lora, 1)
		assert.Equal(t, "civitai:9@9", task.Lora[0].Model)
	})

	t.Run("entries without an identifier are dropped", func(t *testing.T) {
		task := ImageInferenceTask{}
		applyExtras(&task, extras(t, `{"loras":[{"weight":0.7},{"name":"civitai:1@1","weight":0.3}]}`))
		require.Len(t, task.Lora, 1)
		assert.Equal(t, "civitai:1@1", task.Lora[0].Model)
	})
}

// Reference and mask images may be data URIs, since the playground holds bytes client-side
// and Runware accepts inline data. They must pass through untouched.
func TestApplyExtrasPassesDataUriImages(t *testing.T) {
	const dataURI = "data:image/png;base64,iVBORw0KGgo="
	task := ImageInferenceTask{}
	applyExtras(&task, extras(t, `{"init_image_url":"`+dataURI+`","mask_url":"`+dataURI+`"}`))

	assert.Equal(t, dataURI, task.SeedImage)
	assert.Equal(t, dataURI, task.MaskImage)
}

// Unknown keys must never be forwarded. Runware rejects an entire task on an unrecognised
// field, so a stray client param would otherwise turn every request into a hard failure.
func TestApplyExtrasIgnoresUnknownKeys(t *testing.T) {
	task := ImageInferenceTask{TaskType: taskTypeImageInference, TaskUUID: "u", Model: "m", PositivePrompt: "p"}
	applyExtras(&task, extras(t, `{"adetailer":{"yoloModel":"face"},"layer_diffusion":{"weight":1},"ensd":31337}`))

	body, err := common.Marshal(task)
	require.NoError(t, err)
	for _, unknown := range []string{"adetailer", "layer_diffusion", "ensd", "yoloModel"} {
		assert.NotContains(t, string(body), unknown)
	}
}

// convertOne runs the real ConvertImageRequest and returns the single task it builds, so
// these assertions cover the shipped conversion rather than a copy of its logic.
func convertOne(t *testing.T, req dto.ImageRequest, model string) ImageInferenceTask {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	a := &Adaptor{}
	out, err := a.ConvertImageRequest(c, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model},
	}, req)
	require.NoError(t, err)

	tasks, ok := out.([]ImageInferenceTask)
	require.True(t, ok, "request body must be an array of tasks")
	require.Len(t, tasks, 1)
	return tasks[0]
}

// The whole request must be an ARRAY of tasks addressed by AIR, with the model carried
// through verbatim - an arbitrary Civitai checkpoint reaches Runware with no per-model
// configuration on our side.
func TestConvertImageRequestShape(t *testing.T) {
	n := uint(3)
	task := convertOne(t, dto.ImageRequest{
		Prompt: "a cat",
		Size:   "832x1216",
		N:      &n,
	}, "civitai:257749@290640")

	assert.Equal(t, taskTypeImageInference, task.TaskType)
	assert.NotEmpty(t, task.TaskUUID, "each task needs its own id")
	assert.Equal(t, "civitai:257749@290640", task.Model)
	assert.Equal(t, "a cat", task.PositivePrompt)
	assert.Equal(t, 832, task.Width)
	assert.Equal(t, 1216, task.Height)
	assert.Equal(t, 3, task.NumberResults)
	assert.True(t, task.IncludeCost, "cost is needed to reconcile billing")
}

// Runware validates taskUUID as a hyphenated UUIDv4 and rejects the whole request with
// invalidTaskUUID otherwise. common.GetUUID strips the hyphens, so using it here failed
// every live generation while every unit test still passed.
func TestTaskUUIDIsHyphenatedUUIDv4(t *testing.T) {
	task := convertOne(t, dto.ImageRequest{Prompt: "p"}, "civitai:288584@324619")

	parsed, err := uuid.Parse(task.TaskUUID)
	require.NoError(t, err, "taskUUID must parse as a UUID")
	assert.Equal(t, uuid.Version(4), parsed.Version())
	assert.Equal(t, task.TaskUUID, parsed.String(), "taskUUID must keep its hyphens")
	assert.Len(t, task.TaskUUID, 36)
}

// response_format is the only OpenAI knob that selects bytes over a URL. Defaulting to URL
// avoids moving image bytes through the gateway when the caller did not ask for them.
func TestConvertImageRequestOutputType(t *testing.T) {
	cases := map[string]string{
		"b64_json": outputTypeBase64Data,
		"B64_JSON": outputTypeBase64Data,
		"url":      outputTypeURL,
		"":         outputTypeURL,
	}
	for format, want := range cases {
		t.Run("format="+format, func(t *testing.T) {
			task := convertOne(t, dto.ImageRequest{Prompt: "p", ResponseFormat: format}, "m")
			assert.Equal(t, want, task.OutputType)
		})
	}
}

// new-api charges one flat price per call, but Runware bills per pixel and per step, so an
// unbounded request would let the caller pick our cost: measured upstream, 2048x2048 at 100
// steps is $0.0141 against $0.0013 for a 1024 default, and Flux at 2048x50 is $0.0461. Both
// axes must be clamped, and the aspect ratio must survive the clamp.
func TestClampCost(t *testing.T) {
	t.Run("oversized square is scaled to the pixel cap", func(t *testing.T) {
		task := ImageInferenceTask{Width: 4096, Height: 4096}
		clampCost(&task)
		assert.LessOrEqual(t, task.Width*task.Height, maxPixels)
		assert.Equal(t, task.Width, task.Height)
	})

	t.Run("a hires-fix pass is not clamped", func(t *testing.T) {
		// 1024 base re-diffused at 2048 is the whole point of the raised ceiling.
		task := ImageInferenceTask{Width: 2048, Height: 2048}
		clampCost(&task)
		assert.Equal(t, 2048, task.Width)
		assert.Equal(t, 2048, task.Height)
	})

	t.Run("aspect ratio survives", func(t *testing.T) {
		task := ImageInferenceTask{Width: 2048, Height: 1024}
		clampCost(&task)
		assert.LessOrEqual(t, task.Width*task.Height, maxPixels)
		assert.InDelta(t, 2.0, float64(task.Width)/float64(task.Height), 0.1)
	})

	t.Run("dimensions stay multiples of 64", func(t *testing.T) {
		task := ImageInferenceTask{Width: 1920, Height: 1080}
		clampCost(&task)
		assert.Zero(t, task.Width%64)
		assert.Zero(t, task.Height%64)
		assert.LessOrEqual(t, task.Width*task.Height, maxPixels)
	})

	// Runware rejects any side that is not a multiple of 64, so normalisation has to run
	// even when the request already fits the pixel budget. 1920x1080 is the common case:
	// it is inside the budget and was previously forwarded verbatim and refused.
	t.Run("in-budget sides are still snapped to a multiple of 64", func(t *testing.T) {
		task := ImageInferenceTask{Width: 1920, Height: 1080}
		clampCost(&task)
		assert.Zero(t, task.Width%64)
		assert.Zero(t, task.Height%64)
		assert.Equal(t, 1920, task.Width)
		assert.Equal(t, 1088, task.Height)
	})

	// Each side is bounded independently, so a shape that fits the pixel budget can still
	// exceed the per-side maximum.
	t.Run("a long thin request is bounded per side", func(t *testing.T) {
		task := ImageInferenceTask{Width: 4096, Height: 256}
		clampCost(&task)
		assert.LessOrEqual(t, task.Width, maxSide)
		assert.GreaterOrEqual(t, task.Height, minSide)
	})

	t.Run("within budget is untouched", func(t *testing.T) {
		task := ImageInferenceTask{Width: 832, Height: 1216}
		clampCost(&task)
		assert.Equal(t, 832, task.Width)
		assert.Equal(t, 1216, task.Height)
	})

	t.Run("steps are capped", func(t *testing.T) {
		steps := 150
		task := ImageInferenceTask{Width: 1024, Height: 1024, Steps: &steps}
		clampCost(&task)
		require.NotNil(t, task.Steps)
		assert.Equal(t, maxSteps, *task.Steps)
	})

	t.Run("steps under the cap are untouched", func(t *testing.T) {
		steps := 28
		task := ImageInferenceTask{Width: 1024, Height: 1024, Steps: &steps}
		clampCost(&task)
		require.NotNil(t, task.Steps)
		assert.Equal(t, 28, *task.Steps)
	})
}

// The clamp must run on the real conversion path, not just as a helper.
func TestConvertImageRequestClampsCost(t *testing.T) {
	task := convertOne(t, dto.ImageRequest{
		Prompt: "p",
		Size:   "2048x2048",
		Extra:  extras(t, `{"steps":100}`),
	}, "civitai:288584@324619")

	assert.LessOrEqual(t, task.Width*task.Height, maxPixels)
	require.NotNil(t, task.Steps)
	assert.Equal(t, maxSteps, *task.Steps)
}

// Runware reports failures in the body, and a batch can partially succeed. Only a fully
// empty data array is an error; the message must carry the upstream's own detail so the
// user learns which parameter was rejected.
func TestFirstErrorMessage(t *testing.T) {
	assert.Equal(t,
		"Invalid model identifier (parameter: model)",
		firstErrorMessage([]ResponseError{{Message: "Invalid model identifier", Parameter: "model"}}))

	assert.Equal(t, "quota exceeded",
		firstErrorMessage([]ResponseError{{}, {Message: "quota exceeded"}}))

	assert.NotEmpty(t, firstErrorMessage(nil), "an empty result with no detail still needs a message")
}

// A Runware error can arrive with HTTP 200, so a success status must not be forwarded as
// the error status - the caller would treat a failure as a success.
func TestUpstreamStatus(t *testing.T) {
	assert.Equal(t, 400, upstreamStatus(400))
	assert.Equal(t, 429, upstreamStatus(429))
	assert.Equal(t, 502, upstreamStatus(200), "a body-level error on HTTP 200 becomes 502")
}

// A passthrough model takes its checkpoint from the caller, so this is the one place an
// arbitrary client string becomes the upstream model name. Anything that is not an AIR must
// be refused rather than forwarded.
func TestIsAIR(t *testing.T) {
	valid := []string{
		"civitai:288584@324619",
		"runware:101@1",
		"purplesmart:257749@290640",
		"rundiffusion:130@100",
	}
	for _, s := range valid {
		assert.True(t, isAIR(s), "%q is a valid AIR", s)
	}

	invalid := []string{
		"",
		"civitai:288584",              // no version
		"civitai@288584:324619",       // separators swapped
		":288584@324619",              // no publisher
		"civitai:@324619",             // no model id
		"civitai:288584@",             // no version id
		"civitai:abc@324619",          // non-numeric model id
		"civitai:288584@def",          // non-numeric version id
		"civitai:288584@324619 extra", // trailing junk
		"../../etc/passwd",
		"https://evil.example/x@1",
		"civitai:288584@324619/../x",
		"civitai:288584@324619?a=b",
		"civitai:288584@324619\nx:1@2",
	}
	for _, s := range invalid {
		assert.False(t, isAIR(s), "%q must be refused", s)
	}
}

// The passthrough override replaces the upstream model only when it is a real AIR; a junk
// value must leave the configured model in place rather than being sent as the model name.
func TestConvertImageRequestAirOverride(t *testing.T) {
	t.Run("valid air overrides the model", func(t *testing.T) {
		task := convertOne(t, dto.ImageRequest{
			Prompt: "p",
			Extra:  extras(t, `{"air":"civitai:999@111"}`),
		}, "custom-civitai")
		assert.Equal(t, "civitai:999@111", task.Model)
	})

	t.Run("malformed air is ignored", func(t *testing.T) {
		task := convertOne(t, dto.ImageRequest{
			Prompt: "p",
			Extra:  extras(t, `{"air":"../../etc/passwd"}`),
		}, "custom-civitai")
		assert.Equal(t, "custom-civitai", task.Model)
	})
}

// Runware picks a random seed when the request omits one and reports it per image. Dropping
// it makes a generation the user liked unreproducible, so it has to survive the response
// conversion, per image rather than per response.
func TestDoResponseCarriesPerImageSeed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	body := `{"data":[
		{"taskType":"imageInference","imageURL":"https://example.com/a.png","seed":11,"cost":0.001},
		{"taskType":"imageInference","imageURL":"https://example.com/b.png","seed":22,"cost":0.002}
	]}`
	resp := httptest.NewRecorder()
	resp.Code = 200
	_, _ = resp.WriteString(body)

	info := &relaycommon.RelayInfo{RelayMode: constant.RelayModeImagesGenerations}
	adaptor := &Adaptor{}
	usage, apiErr := adaptor.DoResponse(c, resp.Result(), info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	var out dto.ImageResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	require.Len(t, out.Data, 2)
	assert.Equal(t, int64(11), out.Data[0].Seed)
	assert.Equal(t, int64(22), out.Data[1].Seed)

	// Cost is summed across the batch, not averaged.
	assert.InDelta(t, 0.003, info.UpstreamCostUSD, 1e-9)
}
