package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DeloPay rejects an empty-string customer field with IR_06 "Invalid request
// body" and creates no payment, so dropping `omitempty` from any of these would
// break checkout for every account without an email (OAuth signups) or without
// a resolvable name.
func TestDeloPayCreatePaymentRequestOmitsBlankCustomerFields(t *testing.T) {
	cases := []struct {
		name     string
		request  DeloPayCreatePaymentRequest
		absent   []string
		expected map[string]string
	}{
		{
			name: "no email",
			request: DeloPayCreatePaymentRequest{
				CustomerId: "uno_12794",
				Name:       "bigmass.co.uk",
			},
			absent:   []string{"email"},
			expected: map[string]string{"customer_id": "uno_12794", "name": "bigmass.co.uk"},
		},
		{
			name:     "no customer at all",
			request:  DeloPayCreatePaymentRequest{},
			absent:   []string{"customer_id", "email", "name"},
			expected: map[string]string{},
		},
		{
			name: "full customer",
			request: DeloPayCreatePaymentRequest{
				CustomerId: "uno_1",
				Email:      "buyer@example.com",
				Name:       "Buyer",
			},
			expected: map[string]string{
				"customer_id": "uno_1",
				"email":       "buyer@example.com",
				"name":        "Buyer",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.request)
			require.NoError(t, err)

			var body map[string]any
			require.NoError(t, json.Unmarshal(raw, &body))

			for _, key := range tc.absent {
				assert.NotContains(t, body, key, "blank %s must be omitted, not sent as \"\"", key)
			}
			for key, want := range tc.expected {
				assert.Equal(t, want, body[key])
			}
		})
	}
}
