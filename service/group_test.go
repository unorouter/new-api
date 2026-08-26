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
	mapping := ParseTokenGroupMapping(
		`{"deepseek-v3.2":["yun-deepseek-v3.2","pol-deepseek-v3.2"],"gpt-5.5":["cent-gpt-5.5"],"empty":[]}`)
	require.NotNil(t, mapping)

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
			assert.Equal(t, c.want, ResolveTokenGroupForModel(mapping, c.model, "auto"))
		})
	}

	assert.Nil(t, ParseTokenGroupMapping(""))
	assert.Nil(t, ParseTokenGroupMapping("{}"))
	assert.Nil(t, ParseTokenGroupMapping("not json"))
	assert.Equal(t, "auto", ResolveTokenGroupForModel(nil, "any", "auto"))
}
