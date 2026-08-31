package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every gateway that settles a paid order must hold the same three invariants:
// take a real row lock, convert money through the shared strict helper, and push
// the credit into the user quota cache. These are asserted against the source
// rather than through a database, because each one was broken by a merge that
// compiled and passed every behavioural test: two gateways arrived using the
// GORM v1 lock form that GORM v2 silently ignores, multiplying a float64 into
// the UPDATE instead of converting, and never syncing the cache.
func TestRechargeFunctionsShareSettlementInvariants(t *testing.T) {
	src, err := os.ReadFile("topup.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "topup.go", src, 0)
	require.NoError(t, err)

	settlers := map[string]bool{
		"RechargeEpay":         true,
		"Recharge":             true,
		"ManualCompleteTopUp":  true,
		"RechargeCreem":        true,
		"RechargeWaffo":        true,
		"RechargeWaffoPancake": true,
		"RechargeNowPayments":  true,
		"RechargeDeloPay":      true,
	}

	bodies := map[string]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !settlers[fn.Name.Name] {
			continue
		}
		bodies[fn.Name.Name] = string(src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset])
	}

	require.Len(t, bodies, len(settlers), "a settlement function was renamed or removed")

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			assert.NotContains(t, body, `Set("gorm:query_option"`,
				"GORM v2 ignores the v1 locking form, so this settles without a row lock and a retried webhook can credit twice")
			assert.Contains(t, body, "lockForUpdate(tx)",
				"settlement must lock the order row so concurrent deliveries serialise")
			assert.Contains(t, body, "QuotaFromDecimalStrict",
				"money must convert through the shared helper, which rejects non-positive and out-of-range quota")
			assert.Contains(t, body, "syncCreditUserQuotaCache",
				"a credit that skips the cache leaves Redis under-reporting the balance the user paid for")
			assert.Contains(t, body, "applyTopUpBonus",
				"a settler that skips the bonus silently underpays enterprise partners on money already received")
		})
	}
}
