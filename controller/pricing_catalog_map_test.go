package controller

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogTags(t *testing.T) {
	assert.Equal(t, []string{}, catalogTags(""))
	assert.Equal(t, []string{"text", "Reasoning", "200K"}, catalogTags("text,Reasoning,200K"))
	assert.Equal(t, []string{"image", "Vision"}, catalogTags(" image , Vision "))
	assert.Equal(t, []string{"video"}, catalogTags("video,"))
}

func TestCatalogModality(t *testing.T) {
	cases := []struct {
		name      string
		modalites []string
		wantType  string
		wantChat  bool
	}{
		{"text only serves chat", []string{"text"}, "text", true},
		{"embedding is not chat", []string{"embedding"}, "embedding", false},
		{"image is not chat", []string{"image"}, "image", false},
		{"audio is not chat", []string{"audio"}, "audio", false},
		{"video is not chat", []string{"video"}, "video", false},
		// An image generator that also returns text is still an image model:
		// putting it in the chat picker sends chat traffic to /images.
		{"image plus text is image", []string{"image", "text"}, "image", false},
		// No stated modality means nothing can be claimed, so not chat-eligible.
		{"absent modality is not chat", nil, "text", false},
	}
	for _, c := range cases {
		gotType, gotChat := catalogModality(model.Pricing{}, dto.ModelMetadata{OutputModalities: c.modalites})
		assert.Equal(t, c.wantType, gotType, c.name)
		assert.Equal(t, c.wantChat, gotChat, c.name)
	}
}

func TestParseCatalogMetadata(t *testing.T) {
	md := parseCatalogMetadata(`{"outputModalities":["text"],"releaseTs":1764010580000}`)
	assert.Equal(t, []string{"text"}, md.OutputModalities)
	assert.Equal(t, int64(1764010580000), md.ReleaseTs)

	// Malformed or absent metadata must not fail the whole catalog response.
	assert.Empty(t, parseCatalogMetadata("").OutputModalities)
	assert.Empty(t, parseCatalogMetadata("not json").OutputModalities)
}

// An embedding routed to /embeddings must never be chat-eligible, even while a
// source still publishes outputModalities ["text"] for it.
func TestCatalogModalityEndpointOverridesTextClaim(t *testing.T) {
	m := model.Pricing{SupportedEndpointTypes: []constant.EndpointType{"openai", "embedding"}}
	gotType, gotChat := catalogModality(m, dto.ModelMetadata{OutputModalities: []string{"text"}})
	assert.Equal(t, "text", gotType)
	assert.False(t, gotChat)
}

func TestCatalogPricing(t *testing.T) {
	ratios := map[string]float64{"cheap": 0.5, "full": 1}

	t.Run("ratio priced applies the cheapest group", func(t *testing.T) {
		m := model.Pricing{ModelRatio: 2.5, CompletionRatio: 5, EnableGroup: []string{"cheap", "full"}}
		p := catalogPricing(m, ratios, true)
		assert.InDelta(t, 2.5, p.input, 1e-9) // 2.5 * 2 * 0.5
		assert.InDelta(t, 12.5, p.output, 1e-9)
		require.NotNil(t, p.origInput)
		assert.InDelta(t, 5, *p.origInput, 1e-9) // undiscounted: 2.5 * 2
		assert.InDelta(t, 25, *p.origOutput, 1e-9)
	})

	t.Run("no discount when nothing is below full rate", func(t *testing.T) {
		m := model.Pricing{ModelRatio: 2.5, CompletionRatio: 5, EnableGroup: []string{"full"}}
		p := catalogPricing(m, ratios, true)
		assert.InDelta(t, 5, p.input, 1e-9)
		assert.Nil(t, p.origInput, "a full-rate model must not render a strikethrough")
	})

	t.Run("operator can disable original prices", func(t *testing.T) {
		m := model.Pricing{ModelRatio: 2.5, CompletionRatio: 5, EnableGroup: []string{"cheap"}}
		p := catalogPricing(m, ratios, false)
		assert.InDelta(t, 2.5, p.input, 1e-9)
		assert.Nil(t, p.origInput)
	})

	t.Run("fixed price bills per call, not per token", func(t *testing.T) {
		m := model.Pricing{QuotaType: 1, ModelPrice: 0.04, EnableGroup: []string{"cheap"}}
		p := catalogPricing(m, ratios, true)
		assert.InDelta(t, 0.02, p.fixed, 1e-9)
		assert.Zero(t, p.input, "fixed-price models quote no per-token rate")
		require.NotNil(t, p.origFixed)
		assert.InDelta(t, 0.04, *p.origFixed, 1e-9)
	})

	t.Run("grid pricing quotes the cheapest tier", func(t *testing.T) {
		m := model.Pricing{
			QuotaType:   1,
			ModelPrice:  9,
			EnableGroup: []string{"cheap"},
			GridPricing: []map[string]interface{}{
				{"Pricing": 1.5}, {"Pricing": 0.5}, {"Pricing": 0.0},
			},
		}
		p := catalogPricing(m, ratios, true)
		assert.InDelta(t, 0.25, p.fixed, 1e-9, "cheapest NON-ZERO tier x group ratio")
		require.NotNil(t, p.origFixed)
		assert.InDelta(t, 0.5, *p.origFixed, 1e-9)
	})

	t.Run("unknown groups fall back to full rate", func(t *testing.T) {
		m := model.Pricing{ModelRatio: 1, CompletionRatio: 1, EnableGroup: []string{"missing"}}
		p := catalogPricing(m, ratios, true)
		assert.InDelta(t, 2, p.input, 1e-9)
		assert.Nil(t, p.origInput)
	})
}

// Every key the sync writes must survive the round trip. A field missing from
// the struct is dropped silently: consumers see undefined rather than an error,
// so a filter quietly returns nothing instead of failing.
func TestModelMetadataCoversSyncedKeys(t *testing.T) {
	// The 37 keys observed across all live models.
	raw := `{"releaseTs":1764010580000,"releaseDate":"2025-11-24","contextWindow":200000,
	"maxInputTokens":200000,"maxOutputTokens":64000,"outputModalities":["text"],"inputModalities":["text","image"],"mode":"chat",
	"series":"claude","categories":["reasoning"],"tokenizer":"claude",
	"knowledgeCutoff":"2025-03","deprecationDate":"2027-01-01","expirationDate":"2027-06-01",
	"huggingFaceId":"org/model","quantization":"fp8","isModerated":true,
	"isReasoning":true,"supportsTools":true,"supportsParallelTools":true,
	"supportsVision":true,"supportsAudio":true,"supportsAudioOutput":true,
	"supportsVideo":true,"supportsPdf":true,"supportsCache":true,
	"supportsWebSearch":true,"supportsComputerUse":true,"supportsResponseFormat":true,
	"supportsAssistantPrefill":true,"supportsUrlContext":true,
	"supportsNativeStreaming":true,"supportsSystemMessages":true,
	"supportedParameters":["temperature"],"supportedParametersAll":["temperature","top_p"],
	"defaultParameters":{"temperature":1},"reasoningEfforts":["low","high"]}`

	md := parseCatalogMetadata(raw)

	assert.Equal(t, int64(1764010580000), md.ReleaseTs)
	assert.Equal(t, 200000, md.ContextWindow)
	assert.Equal(t, []string{"text"}, md.OutputModalities)
	assert.Equal(t, "chat", md.Mode)
	assert.Equal(t, "claude", md.Series)
	assert.Equal(t, []string{"reasoning"}, md.Categories)
	assert.True(t, md.IsReasoning)
	assert.True(t, md.SupportsTools)
	assert.True(t, md.SupportsSystemMessages)
	assert.Equal(t, []string{"temperature", "top_p"}, md.SupportedParametersAll)
	assert.Equal(t, []string{"low", "high"}, md.ReasoningEfforts)
	require.NotNil(t, md.DefaultParameters["temperature"])
	assert.Equal(t, float64(1), *md.DefaultParameters["temperature"])
}

func TestTruncateDescription(t *testing.T) {
	assert.Equal(t, "short", truncateDescription("short"))

	long := strings.Repeat("word ", 80) // 400 chars
	got := truncateDescription(long)
	assert.LessOrEqual(t, len(got), catalogDescriptionChars+3)
	assert.True(t, strings.HasSuffix(got, "..."))
	// Cuts on a space, so the blurb does not end mid-word.
	assert.False(t, strings.HasSuffix(strings.TrimSuffix(got, "..."), " "))

	// A single unbroken token has no space to cut on; it must still truncate.
	assert.True(t, strings.HasSuffix(truncateDescription(strings.Repeat("x", 500)), "..."))
}

func TestListMetadataDropsSendPathFields(t *testing.T) {
	full := dto.ModelMetadata{
		ContextWindow:          200000,
		SupportedParametersAll: []string{"temperature", "top_p"},
		SupportedParameters:    []string{"temperature"},
		DefaultParameters:      map[string]*float64{"temperature": nil},
	}
	lean := listMetadata(full)

	// Browse filters on these, so they must survive.
	assert.Equal(t, 200000, lean.ContextWindow)
	assert.Equal(t, []string{"temperature", "top_p"}, lean.SupportedParametersAll)
	// Only the send path and the detail view read these, and both fetch per model.
	assert.Nil(t, lean.SupportedParameters)
	assert.Nil(t, lean.DefaultParameters)
}

func TestNewestFreeChatModel(t *testing.T) {
	rows := []dto.PricingCatalogModel{
		{ModelName: "paid-newest", ReleaseTs: 300, Chat: true, Online: true},
		{ModelName: "free-old", IsFree: true, Chat: true, Online: true, ReleaseTs: 100},
		{ModelName: "free-newest", IsFree: true, Chat: true, Online: true, ReleaseTs: 200},
		// Same date as free-newest: the name breaks the tie, so the pick is stable.
		{ModelName: "free-aaa", IsFree: true, Chat: true, Online: true, ReleaseTs: 200},
	}
	assert.Equal(t, "free-aaa", newestFreeChatModel(rows))

	// A model nothing can route is a bad default even when it is the newest.
	offline := []dto.PricingCatalogModel{
		{ModelName: "free-offline", IsFree: true, Chat: true, Online: false, ReleaseTs: 900},
		{ModelName: "free-live", IsFree: true, Chat: true, Online: true, ReleaseTs: 100},
	}
	assert.Equal(t, "free-live", newestFreeChatModel(offline))

	// No free chat model: fall back to any free model rather than nothing.
	assert.Equal(t, "free-image", newestFreeChatModel([]dto.PricingCatalogModel{
		{ModelName: "free-image", IsFree: true, Chat: false, Online: true},
	}))

	assert.Empty(t, newestFreeChatModel([]dto.PricingCatalogModel{{ModelName: "paid"}}))
}
