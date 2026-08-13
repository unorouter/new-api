package ratio_setting

import "github.com/QuantumNous/new-api/types"

// Markup applied to an upstream's self-reported per-request cost. Providers that bill by GPU
// time (Runware) cannot be priced by a flat per-call number without either losing money on
// expensive requests or overcharging cheap ones, so those models bill actual cost times this.
//
// A model with no entry falls back to the default, so adding a model needs no config change.
const defaultUpstreamCostMarkup = 20.0

var modelUpstreamCostMarkupMap = types.NewRWMap[string, float64]()

func GetUpstreamCostMarkup(modelName string) float64 {
	if markup, ok := modelUpstreamCostMarkupMap.Get(modelName); ok && markup > 0 {
		return markup
	}
	return defaultUpstreamCostMarkup
}

func UpstreamCostMarkup2JSONString() string {
	return modelUpstreamCostMarkupMap.MarshalJSONString()
}

func UpdateUpstreamCostMarkupByJSONString(jsonStr string) error {
	return types.LoadFromJsonStringWithCallback(modelUpstreamCostMarkupMap, jsonStr, InvalidateExposedDataCache)
}
