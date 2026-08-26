package common

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A paid top-up must convert to quota rather than being rejected as an
// overflow. The bound was MaxInt32, which at QuotaPerUnit=500000 refused every
// purchase above $4294: RechargeCreem returned an error, the webhook answered
// 500, and the buyer was charged without receiving credit.
func TestQuotaFromDecimalStrictAcceptsLargeTopUps(t *testing.T) {
	const quotaPerUnit = 500_000

	cases := []struct {
		name string
		usd  int64
	}{
		{"just below the old int32 ceiling", 4_000},
		{"at the old int32 ceiling", 4_294},
		{"just past the old int32 ceiling", 4_295},
		{"the order that was refused in production", 5_000},
		{"a large enterprise top-up", 1_000_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quota, err := QuotaFromDecimalStrict(
				decimal.NewFromInt(tc.usd).Mul(decimal.NewFromInt(quotaPerUnit)),
			)
			require.NoError(t, err, "a paid top-up must never fail quota conversion")
			assert.Equal(t, int(tc.usd*quotaPerUnit), quota)
		})
	}
}

// Creem stores Amount already in quota units, so it converts without the
// QuotaPerUnit multiplication every other gateway applies.
func TestQuotaFromDecimalStrictAcceptsCreemQuotaUnits(t *testing.T) {
	quota, err := QuotaFromDecimalStrict(decimal.NewFromInt(2_500_000_000))
	require.NoError(t, err)
	assert.Equal(t, 2_500_000_000, quota)
}

// The bound still has to catch a genuine overflow, so a runaway product cannot
// wrap negative and turn a charge into a credit.
func TestQuotaFromDecimalStrictStillRejectsOverflow(t *testing.T) {
	quota, err := QuotaFromDecimalStrict(decimal.NewFromFloat(1e30))
	require.Error(t, err)
	assert.Zero(t, quota)

	var clamp *QuotaClamp
	require.ErrorAs(t, err, &clamp)
	assert.Equal(t, QuotaClampOverflow, clamp.Kind)
	assert.Equal(t, MaxQuota, clamp.Clamped)
}

// MaxQuota must stay within the range float64 represents exactly, because the
// conversions round-trip through float64 before reaching an int.
func TestMaxQuotaIsExactlyRepresentableAsFloat64(t *testing.T) {
	assert.Equal(t, int64(MaxQuota), int64(float64(MaxQuota)),
		"MaxQuota must survive the float64 conversion used by saturateQuota")
	assert.Equal(t, int64(MinQuota), int64(float64(MinQuota)))
}
