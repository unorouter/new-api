package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedPaidAmountTopUp(t *testing.T, tradeNo string) {
	t.Helper()
	require.NoError(t, DB.Create(&TopUp{
		UserId:     4242,
		Amount:     10,
		Money:      10,
		TradeNo:    tradeNo,
		Status:     common.TopUpStatusPending,
		CreateTime: common.GetTimestamp(),
	}).Error)
}

func paidAmountOf(t *testing.T, tradeNo string) float64 {
	t.Helper()
	var row TopUp
	require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&row).Error)
	return row.PaidAmount
}

// Nothing else in top_ups separates a paid-but-uncredited order from a checkout
// the buyer abandoned: status is 'pending' for both, complete_time is only
// written on success, and provider_payment_id is recorded on every webhook
// including "waiting". Alerting on those counted 27 orders / $142 owed when the
// providers said nothing had been paid at all.
func TestSetTopUpPaidAmount(t *testing.T) {
	truncateTables(t)

	t.Run("records what the provider received", func(t *testing.T) {
		seedPaidAmountTopUp(t, "ref_paid_basic")
		require.NoError(t, SetTopUpPaidAmount("ref_paid_basic", 19.95))
		assert.InDelta(t, 19.95, paidAmountOf(t, "ref_paid_basic"), 0.0001)
	})

	t.Run("an unpaid order stays at zero", func(t *testing.T) {
		seedPaidAmountTopUp(t, "ref_paid_none")
		require.NoError(t, SetTopUpPaidAmount("ref_paid_none", 0))
		assert.Zero(t, paidAmountOf(t, "ref_paid_none"), "an abandoned checkout must never look paid")
	})

	t.Run("a later smaller figure does not erase a larger one", func(t *testing.T) {
		seedPaidAmountTopUp(t, "ref_paid_monotonic")
		require.NoError(t, SetTopUpPaidAmount("ref_paid_monotonic", 20))
		require.NoError(t, SetTopUpPaidAmount("ref_paid_monotonic", 5))
		assert.InDelta(t, 20.0, paidAmountOf(t, "ref_paid_monotonic"), 0.0001,
			"webhooks can arrive out of order; the highest received figure wins")
	})

	t.Run("a topped-up figure replaces a smaller one", func(t *testing.T) {
		seedPaidAmountTopUp(t, "ref_paid_increase")
		require.NoError(t, SetTopUpPaidAmount("ref_paid_increase", 5))
		require.NoError(t, SetTopUpPaidAmount("ref_paid_increase", 20))
		assert.InDelta(t, 20.0, paidAmountOf(t, "ref_paid_increase"), 0.0001)
	})
}
