package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
)

// The guest free-model gate reads is_free on the chat, image and best-key
// paths, so a wrong answer either bills a guest or blocks a free model.
func TestModelIsFree(t *testing.T) {
	groupRatio := map[string]float64{"free": 0, "paid": 1, "half": 0.5}

	cases := []struct {
		name string
		in   model.Pricing
		want bool
	}{
		{"fixed price zero", model.Pricing{QuotaType: 1, ModelPrice: 0, EnableGroup: []string{"paid"}}, true},
		{"fixed price paid group", model.Pricing{QuotaType: 1, ModelPrice: 5, EnableGroup: []string{"paid"}}, false},
		{"fixed price zero-ratio group", model.Pricing{QuotaType: 1, ModelPrice: 5, EnableGroup: []string{"free"}}, true},
		{"ratio zero-ratio group", model.Pricing{QuotaType: 0, ModelRatio: 2, EnableGroup: []string{"free"}}, true},
		{"ratio paid group", model.Pricing{QuotaType: 0, ModelRatio: 2, EnableGroup: []string{"paid"}}, false},
		{"ratio model_ratio zero", model.Pricing{QuotaType: 0, ModelRatio: 0, EnableGroup: []string{"paid"}}, true},
		{"ratio with model_price set", model.Pricing{QuotaType: 0, ModelRatio: 0, ModelPrice: 3, EnableGroup: []string{"free"}}, false},
		{"no enabled groups", model.Pricing{QuotaType: 0, ModelRatio: 2, EnableGroup: []string{}}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, modelIsFree(c.in, groupRatio))
		})
	}
}
