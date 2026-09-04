package service

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTokenAutoGroups(t *testing.T) {
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"default":"d","vip":"v","discount":"c","auto":"a"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(
		`{"default":1,"vip":2,"discount":0.5}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))

	cases := []struct {
		name       string
		tokenGroup string
		want       []string
	}{
		{"auto passthrough uses configured auto groups", "auto", []string{"default", "vip"}},
		{"composite sorts cheapest ratio first", "vip,discount,default", []string{"discount", "default", "vip"}},
		{"composite drops unusable and deprecated groups", "vip,ghost,discount", []string{"discount", "vip"}},
		{"composite trims whitespace", " vip , discount ", []string{"discount", "vip"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, GetTokenAutoGroups(nil, "default", c.tokenGroup))
		})
	}
}

func TestResolveTokenGroupForModel(t *testing.T) {
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"default":"d","cheap":"c","mid":"m","pricey":"p","auto":"a"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(
		`{"default":1,"cheap":0.02,"mid":0.5,"pricey":3.9}`))

	mapping := ParseTokenGroupMapping(
		`{"deepseek-v3.2":{"groups":["yun-deepseek-v3.2","pol-deepseek-v3.2"]},` +
			`"gpt-5.5":{"groups":["cent-gpt-5.5"]},"empty":{"groups":[]}}`)
	require.NotNil(t, mapping)

	noCandidates := func() []string { return nil }

	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"mapped model composes scoped group", "deepseek-v3.2", "yun-deepseek-v3.2,pol-deepseek-v3.2"},
		{"single mapped group stays plain", "gpt-5.5", "cent-gpt-5.5"},
		{"unmapped model keeps base group", "claude-fable-5", "auto"},
		{"empty mapping entry keeps base group", "empty", "auto"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, ResolveTokenGroupForModel(mapping, "default", c.model, "auto", noCandidates))
		})
	}

	assert.Nil(t, ParseTokenGroupMapping(""))
	assert.Nil(t, ParseTokenGroupMapping("{}"))
	assert.Nil(t, ParseTokenGroupMapping("not json"))
	assert.Nil(t, ParseTokenGroupMapping(`{"m":["legacy-array-no-longer-parses"]}`))
	assert.Equal(t, "auto", ResolveTokenGroupForModel(nil, "default", "any", "auto", noCandidates))
}

func TestResolveTokenGroupForModelPriceBand(t *testing.T) {
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(
		`{"default":"d","cheap":"c","mid":"m","pricey":"p","auto":"a"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(
		`{"default":1,"cheap":0.02,"mid":0.5,"pricey":3.9}`))

	candidates := func() []string { return []string{"cheap", "mid", "pricey"} }
	ptr := func(f float64) *float64 { return &f }

	cases := []struct {
		name    string
		mapping string
		want    string
	}{
		{
			name:    "band selects only lanes inside it",
			mapping: `{"m":{"groups":[],"min":0,"max":0.6}}`,
			want:    "cheap,mid",
		},
		{
			name:    "band unions with an out-of-band pin",
			mapping: `{"m":{"groups":["pricey"],"min":0,"max":0.1}}`,
			want:    "pricey,cheap",
		},
		{
			name:    "pin already inside the band is not duplicated",
			mapping: `{"m":{"groups":["cheap"],"min":0,"max":0.6}}`,
			want:    "cheap,mid",
		},
		{
			name:    "auto parks the band and keeps the base group",
			mapping: `{"m":{"groups":["cheap"],"min":0,"max":0.6,"auto":true}}`,
			want:    "auto",
		},
		{
			name:    "band matching nothing falls back to the base group",
			mapping: `{"m":{"groups":[],"min":9,"max":10}}`,
			want:    "auto",
		},
		{
			name:    "min only keeps the expensive tail",
			mapping: `{"m":{"groups":[],"min":0.4}}`,
			want:    "mid,pricey",
		},
		{
			name:    "max only keeps the cheap head",
			mapping: `{"m":{"groups":[],"max":0.4}}`,
			want:    "cheap",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mapping := ParseTokenGroupMapping(c.mapping)
			require.NotNil(t, mapping)
			assert.Equal(t, c.want, ResolveTokenGroupForModel(mapping, "default", "m", "auto", candidates))
		})
	}

	t.Run("unbanded entry never consults candidates", func(t *testing.T) {
		mapping := ParseTokenGroupMapping(`{"m":{"groups":["cheap"]}}`)
		require.NotNil(t, mapping)
		called := false
		assert.Equal(t, "cheap", ResolveTokenGroupForModel(mapping, "default", "m", "auto", func() []string {
			called = true
			return []string{"mid"}
		}))
		assert.False(t, called, "a pin-only entry must not trigger the abilities scan")
	})

	t.Run("zero max is a real bound and not treated as unset", func(t *testing.T) {
		entry := TokenPinEntry{Max: ptr(0)}
		assert.True(t, entry.HasBand())
	})
}
