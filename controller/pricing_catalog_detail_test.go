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
