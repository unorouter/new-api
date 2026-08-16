package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
)

func TestCatalogCountsMatchesRows(t *testing.T) {
	rows := []dto.PricingCatalogModel{
		{ModelName: "a", Vendor: "OpenAI", IsFree: true},
		{ModelName: "b", Vendor: "OpenAI", IsFree: false},
		{ModelName: "c", Vendor: "Unknown", IsFree: true},
	}
	got := catalogCounts(rows)
	assert.Equal(t, 3, got.Models)
	assert.Equal(t, 2, got.Free)
	assert.Equal(t, 1, got.Paid)
	assert.Equal(t, 2, got.Vendors, "counts distinct vendors that serve a model")
}

func TestCatalogCountsEmpty(t *testing.T) {
	got := catalogCounts(nil)
	assert.Equal(t, dto.PricingCatalogCounts{}, got)
}
