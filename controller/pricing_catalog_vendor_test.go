package controller

import (
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
)

// The vendor page is a release timeline, so it sorts newest first with a name
// tiebreak. Most models share a release date, so without the tiebreak the
// majority sits in slice order.
func TestVendorSortNewestFirstNameTiebreak(t *testing.T) {
	rows := []dto.PricingCatalogModel{
		{ModelName: "b", ReleaseTs: 100},
		{ModelName: "a", ReleaseTs: 100},
		{ModelName: "c", ReleaseTs: 300},
	}
	collator := newCatalogCollator()
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ReleaseTs != rows[j].ReleaseTs {
			return rows[i].ReleaseTs > rows[j].ReleaseTs
		}
		return collator.CompareString(rows[i].ModelName, rows[j].ModelName) < 0
	})
	assert.Equal(t, []string{"c", "a", "b"},
		[]string{rows[0].ModelName, rows[1].ModelName, rows[2].ModelName})
}
