package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The buyer covers the card processing fee, so the charge sits above the
// credit: a 1.00 top-up bills 1.50 and still grants 1.00. Credit is granted
// from TopUp.Money, which never sees this value, so a regression that fed the
// surcharge back into Money would hand out the fee as free quota.
func TestApplyCreemFeeSurcharge(t *testing.T) {
	fixed, percent, threshold := setting.CreemFeeFixed, setting.CreemFeePercent, setting.CreemFeeThreshold
	t.Cleanup(func() {
		setting.CreemFeeFixed, setting.CreemFeePercent = fixed, percent
		setting.CreemFeeThreshold = threshold
	})

	cases := []struct {
		name      string
		fixed     float64
		percent   float64
		threshold float64
		money     float64
		want      float64
	}{
		{name: "fixed fee on the minimum topup", fixed: 0.5, threshold: 2, money: 1, want: 1.5},
		{name: "fee still applies at the threshold", fixed: 0.5, threshold: 2, money: 2, want: 2.5},
		{name: "no fee above the threshold", fixed: 0.5, threshold: 2, money: 2.01, want: 2.01},
		{name: "no fee on a large topup", fixed: 0.5, threshold: 2, money: 20, want: 20},
		{name: "zero threshold charges every amount", fixed: 0.5, money: 20, want: 20.5},
		{name: "percent and fixed combined", fixed: 0.49, percent: 0.039, money: 10, want: 10.88},
		{name: "rounds up to the cent, never under", fixed: 0.49, percent: 0.039, threshold: 2, money: 1, want: 1.53},
		{name: "no fee configured bills the topup", money: 5, want: 5},
		{name: "negative settings never bill under the credit", fixed: -5, money: 5, want: 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting.CreemFeeFixed = tc.fixed
			setting.CreemFeePercent = tc.percent
			setting.CreemFeeThreshold = tc.threshold

			billed := applyCreemFeeSurcharge(tc.money)

			assert.InDelta(t, tc.want, billed, 0.0001)
			assert.GreaterOrEqual(t, billed, tc.money, "billing below the credited amount is a loss")
		})
	}
}

// Creem was the only gateway ignoring payment_setting.amount_discount, so a
// configured tier silently charged full price.
func TestCreemAmountDiscount(t *testing.T) {
	s := operation_setting.GetPaymentSetting()
	require.NotNil(t, s)
	original := s.AmountDiscount
	t.Cleanup(func() { s.AmountDiscount = original })

	s.AmountDiscount = map[int]float64{100: 0.9, 500: 0.8, 20: 0}

	assert.InDelta(t, 0.9, creemAmountDiscount(100), 0.0001, "configured tier applies")
	assert.InDelta(t, 0.8, creemAmountDiscount(500), 0.0001)
	assert.InDelta(t, 1, creemAmountDiscount(7), 0.0001, "unconfigured amount pays full price")
	assert.InDelta(t, 1, creemAmountDiscount(20), 0.0001, "a zero discount is ignored, not free")
}
