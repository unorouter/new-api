package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"

	"github.com/stretchr/testify/assert"
)

// The buyer covers PayPal's processing fee, so the billed amount sits above the
// credited amount. Credit is granted from TopUp.Money, which never sees this
// value: a regression that fed the surcharge back into Money would hand out the
// fee as free quota.
func TestApplyDeloPayFeeSurcharge(t *testing.T) {
	fixed, percent, threshold := setting.DeloPayFeeFixed, setting.DeloPayFeePercent, setting.DeloPayFeeThreshold
	t.Cleanup(func() {
		setting.DeloPayFeeFixed, setting.DeloPayFeePercent = fixed, percent
		setting.DeloPayFeeThreshold = threshold
	})

	cases := []struct {
		name      string
		fixed     float64
		percent   float64
		threshold float64
		money     float64
		want      float64
	}{
		{name: "fixed fee on a one dollar topup", fixed: 0.5, threshold: 2, money: 1, want: 1.5},
		{name: "fee still applies at the threshold", fixed: 0.5, threshold: 2, money: 2, want: 2.5},
		{name: "no fee above the threshold", fixed: 0.5, threshold: 2, money: 2.01, want: 2.01},
		{name: "no fee on a large topup", fixed: 0.5, threshold: 2, money: 20, want: 20},
		{name: "zero threshold charges every amount", fixed: 0.5, money: 20, want: 20.5},
		{name: "percent and fixed combined", fixed: 0.49, percent: 0.039, money: 10, want: 10.88},
		{name: "rounds up to the cent, never under", fixed: 0.49, percent: 0.039, threshold: 2, money: 1, want: 1.53},
		{name: "no fee configured bills the topup", money: 5, want: 5},
		{name: "negative settings never bill under the credit", fixed: -5, money: 5, want: 5},
		{name: "zero stays zero", fixed: 0.5, money: 0, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting.DeloPayFeeFixed = tc.fixed
			setting.DeloPayFeePercent = tc.percent
			setting.DeloPayFeeThreshold = tc.threshold

			billed := applyDeloPayFeeSurcharge(tc.money)

			assert.InDelta(t, tc.want, billed, 0.0001)
			assert.GreaterOrEqual(t, billed, tc.money, "billing below the credited amount is a loss")
		})
	}
}
