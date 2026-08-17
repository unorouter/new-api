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
