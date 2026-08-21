package model

import (
	"testing"

	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
)

// Contract: the stored discount is bounded at read time too, so a value written
// before a policy change cannot outlive it, and 100 stays expressible because
// the enforcement path treats it as a full bypass.
func TestClampFreeRateLimitWindowPctBounds(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"negative is no discount", -5, 0},
		{"zero is no discount", 0, 0},
		{"the server tag value passes through", 25, 25},
		{"a full bypass is expressible", 100, 100},
		{"above the ceiling clamps rather than inverting", 400, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, types.ClampFreeRateLimitWindowPct(tc.in))
		})
	}
}
