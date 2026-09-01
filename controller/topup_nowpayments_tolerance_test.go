package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"

	"github.com/stretchr/testify/assert"
)

// Crypto transfers rarely land on the exact invoice amount: gas, slippage and
// rounding leave a few cents either way. NowPayments only promotes such a
// deposit to "finished" when the merchant dashboard is configured to, and while
// it was not, a 0.25% shortfall left the order pending forever. The money
// arrived, the balance never did. Seven verified-paid orders were stuck this way,
// one of them $100.
func TestWithinUnderpaymentTolerance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pay      float64
		actual   float64
		expected bool
	}{
		// Verbatim from the stuck production order (ref_f519f8f9..., $20 invoice).
		{name: "tiny shortfall credits", pay: 20.20, actual: 20.183446, expected: true},
		{name: "overpayment credits", pay: 20.00, actual: 20.50, expected: true},
		{name: "exact amount credits", pay: 20.00, actual: 20.00, expected: true},
		{name: "shortfall at the 10 percent edge credits", pay: 100.00, actual: 90.00, expected: true},
		{name: "shortfall beyond tolerance stays pending", pay: 100.00, actual: 89.99, expected: false},
		{name: "half payment stays pending", pay: 20.00, actual: 10.00, expected: false},
		// Nothing arrived, or no invoice to compare against: never credit.
		{name: "zero paid stays pending", pay: 20.00, actual: 0, expected: false},
		{name: "zero invoice stays pending", pay: 0, actual: 20.00, expected: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := withinUnderpaymentTolerance(&dto.NowPaymentsWebhookEvent{
				PayAmount:    tc.pay,
				ActuallyPaid: tc.actual,
			})
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestWithinUnderpaymentToleranceHandlesNilEvent(t *testing.T) {
	assert.False(t, withinUnderpaymentTolerance(nil))
}
