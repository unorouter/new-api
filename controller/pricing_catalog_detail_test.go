package controller

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The embedded row also declares `description`, so the outer field must be the
// one that serializes: the detail carries full text, the list a 200-char blurb.
func TestDetailDescriptionWins(t *testing.T) {
	d := dto.PricingCatalogDetail{
		PricingCatalogModel: dto.PricingCatalogModel{ModelName: "m", Description: "TRUNCATED"},
		Description:         "FULL TEXT",
	}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	var back map[string]any
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, "FULL TEXT", back["description"])
	assert.Equal(t, "m", back["model_name"], "embedded row fields stay flat")
}

// The list deliberately strips metadata unless full=true. A single-model fetch
// must NOT inherit that: the chat send path clamps output tokens on
// contextWindow/maxOutputTokens, and an empty blob silently removed the clamp.
func TestListMetadataKeepsSizingFields(t *testing.T) {
	md := dto.ModelMetadata{
		ReleaseTs:           7,
		ContextWindow:       1000000,
		MaxOutputTokens:     64000,
		SupportedParameters: []string{"temperature"},
	}
	lean := listMetadata(md)
	assert.Equal(t, 1000000, lean.ContextWindow, "list keeps sizing fields")
	assert.Equal(t, 64000, lean.MaxOutputTokens)
	assert.Nil(t, lean.SupportedParameters, "list drops parameter lists")
	assert.Equal(t, []string{"temperature"}, md.SupportedParameters,
		"listMetadata must not mutate the caller's copy: the detail route reuses it")
}

// /pricing/counts counts off model.Pricing directly while /pricing/catalog
// counts the rows it built. Both must agree, or the homepage stat row contradicts
// the model list it links to.
func TestStandaloneCountsMatchRowCounts(t *testing.T) {
	rows := []dto.PricingCatalogModel{
		{ModelName: "a", Vendor: "OpenAI", IsFree: true},
		{ModelName: "b", Vendor: "OpenAI", IsFree: false},
		{ModelName: "c", Vendor: "Unknown", IsFree: true},
		{ModelName: "d", Vendor: "Anthropic", IsFree: false},
	}
	fromRows := catalogCounts(rows)

	// Mirrors GetPricingCounts, which cannot run without gateway state.
	direct := dto.PricingCatalogCounts{Models: len(rows)}
	vendors := map[string]struct{}{}
	for _, m := range rows {
		if m.IsFree {
			direct.Free++
		}
		vendors[m.Vendor] = struct{}{}
	}
	direct.Paid = direct.Models - direct.Free
	direct.Vendors = len(vendors)

	assert.Equal(t, fromRows, direct)
	assert.Equal(t, 3, direct.Vendors, "counts vendors serving a model, not configured vendors")
}
