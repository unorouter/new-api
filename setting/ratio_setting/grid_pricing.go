package ratio_setting

import "github.com/QuantumNous/new-api/types"

// GridPricingRow is a single row in a pricing grid.
// All keys except "Pricing" become display columns. "Pricing" is the price value.
// Example: {"Audio": "Audio", "Resolution": "720P", "Duration": "5s", "Pricing": 1.5}
type GridPricingRow = map[string]interface{}

// GridPricingInfo is an array of pricing rows with arbitrary columns.
type GridPricingInfo = []GridPricingRow

var modelGridPricingMap = types.NewRWMap[string, GridPricingInfo]()

func GetGridPricingInfo(modelName string) GridPricingInfo {
	info, ok := modelGridPricingMap.Get(modelName)
	if !ok {
		return nil
	}
	return info
}

func ModelGridPricing2JSONString() string {
	return modelGridPricingMap.MarshalJSONString()
}

func UpdateModelGridPricingByJSONString(jsonStr string) error {
	return types.LoadFromJsonStringWithCallback(modelGridPricingMap, jsonStr, InvalidateExposedDataCache)
}
