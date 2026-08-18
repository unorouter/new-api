package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The slugs below are what the model pages are linked by, so this asserts
// equivalence with the client's vendorSlug (lowercase, spaces to hyphens, drop
// everything else, collapse and trim hyphens). A vendor whose name is entirely
// non-ASCII slugs to "", which must never match a filter.
func TestVendorSlug(t *testing.T) {
	cases := []struct{ name, want string }{
		{"OpenAI", "openai"},
		{"01.AI", "01ai"},
		{"SL-AI", "sl-ai"},
		{"AI21 Labs", "ai21-labs"},
		{"Azure Cognitive Services", "azure-cognitive-services"},
		{"Zhipu AI Coding Plan", "zhipu-ai-coding-plan"},
		{"  Nous Research  ", "nous-research"},
		{"智谱", ""},
		{"讯飞", ""},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, vendorSlug(c.name), "vendorSlug(%q)", c.name)
	}
}

func TestVendorMatches(t *testing.T) {
	assert.True(t, vendorMatches("01.AI", "01.AI"), "exact name")
	assert.True(t, vendorMatches("01.AI", "01ai"), "slug")
	assert.True(t, vendorMatches("AI21 Labs", "ai21-labs"), "slug with space")
	assert.True(t, vendorMatches("OpenAI", "OPENAI"), "slug match is case insensitive")

	assert.False(t, vendorMatches("01.AI", "01-ai"), "a different slug")
	assert.False(t, vendorMatches("OpenAI", "openai-x"), "prefix is not a match")
	// An unslugabble vendor must not be selected by an empty filter, else it
	// would answer every request whose vendor param was dropped.
	assert.False(t, vendorMatches("智谱", ""), "empty filter never matches")
}
