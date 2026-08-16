package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
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
		gotType, gotChat := catalogModality(model.Pricing{}, catalogMetadata{OutputModalities: c.modalites})
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
	gotType, gotChat := catalogModality(m, catalogMetadata{OutputModalities: []string{"text"}})
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
