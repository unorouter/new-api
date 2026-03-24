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

// GetGridPrice looks up the price for a specific resolution from the grid pricing data.
// Returns the price and true if found, or 0 and false if not found.
func GetGridPrice(modelName string, resolution string) (float64, bool) {
	info := GetGridPricingInfo(modelName)
	if info == nil || resolution == "" {
		return 0, false
	}
	for _, row := range info {
		res, ok := row["Resolution"]
		if !ok {
			continue
		}
		resStr, ok := res.(string)
		if !ok {
			continue
		}
		if resStr == resolution {
			if price, ok := row["Pricing"]; ok {
				switch v := price.(type) {
				case float64:
					return v, true
				case int:
					return float64(v), true
				}
			}
		}
	}
	return 0, false
}

func UpdateModelGridPricingByJSONString(jsonStr string) error {
	return types.LoadFromJsonStringWithCallback(modelGridPricingMap, jsonStr, InvalidateExposedDataCache)
}
