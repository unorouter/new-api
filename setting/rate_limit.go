package setting

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var ModelRequestRateLimitEnabled = false
var ModelRequestRateLimitDurationMinutes = 1
var ModelRequestRateLimitCount = 0
var ModelRequestRateLimitSuccessCount = 1000
var ModelRequestRateLimitGroup = map[string][2]int{}
var ModelRequestRateLimitMutex sync.RWMutex

// Per-model limits keyed by exact model name (the sync pre-expands globs to exact
// `:free` names). Value is [total, success] or [total, success, windowMinutes];
// window absent/0 = the shared ModelRequestRateLimitDurationMinutes.
var ModelRequestRateLimitModels = map[string][]int{}
var ModelRequestRateLimitModelsMutex sync.RWMutex

func ModelRequestRateLimitGroup2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := json.Marshal(ModelRequestRateLimitGroup)
	if err != nil {
		common.SysLog("error marshalling model ratio: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelRequestRateLimitGroupByJSONString(jsonStr string) error {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	ModelRequestRateLimitGroup = make(map[string][2]int)
	return json.Unmarshal([]byte(jsonStr), &ModelRequestRateLimitGroup)
}

func GetGroupRateLimit(group string) (totalCount, successCount int, found bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	if ModelRequestRateLimitGroup == nil {
		return 0, 0, false
	}

	limits, found := ModelRequestRateLimitGroup[group]
	if !found {
		return 0, 0, false
	}
	return limits[0], limits[1], true
}

func CheckModelRequestRateLimitGroup(jsonStr string) error {
	checkModelRequestRateLimitGroup := make(map[string][2]int)
	err := json.Unmarshal([]byte(jsonStr), &checkModelRequestRateLimitGroup)
	if err != nil {
		return err
	}
	for group, limits := range checkModelRequestRateLimitGroup {
		if limits[0] < 0 || limits[1] < 1 {
			return fmt.Errorf("group %s has negative rate limit values: [%d, %d]", group, limits[0], limits[1])
		}
		if limits[0] > math.MaxInt32 || limits[1] > math.MaxInt32 {
			return fmt.Errorf("group %s [%d, %d] has max rate limits value 2147483647", group, limits[0], limits[1])
		}
	}

	return nil
}

func ModelRequestRateLimitModels2JSONString() string {
	ModelRequestRateLimitModelsMutex.RLock()
	defer ModelRequestRateLimitModelsMutex.RUnlock()

	jsonBytes, err := json.Marshal(ModelRequestRateLimitModels)
	if err != nil {
		common.SysLog("error marshalling model rate limit models: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelRequestRateLimitModelsByJSONString(jsonStr string) error {
	ModelRequestRateLimitModelsMutex.Lock()
	defer ModelRequestRateLimitModelsMutex.Unlock()

	parsed := make(map[string][]int)
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return err
	}
	for model, limits := range parsed {
		if len(limits) < 2 || len(limits) > 3 {
			return fmt.Errorf("model %s rate limit must be [total, success] or [total, success, windowMinutes]", model)
		}
	}
	ModelRequestRateLimitModels = parsed
	return nil
}

// GetModelRateLimit returns the per-model limits. windowMinutes is 0 when the
// entry carries no custom window (caller falls back to the global duration).
func GetModelRateLimit(model string) (totalCount, successCount, windowMinutes int, found bool) {
	ModelRequestRateLimitModelsMutex.RLock()
	defer ModelRequestRateLimitModelsMutex.RUnlock()

	if ModelRequestRateLimitModels == nil {
		return 0, 0, 0, false
	}
	limits, found := ModelRequestRateLimitModels[model]
	if !found || len(limits) < 2 {
		return 0, 0, 0, false
	}
	window := 0
	if len(limits) >= 3 {
		window = limits[2]
	}
	return limits[0], limits[1], window, true
}

func HasModelRateLimits() bool {
	ModelRequestRateLimitModelsMutex.RLock()
	defer ModelRequestRateLimitModelsMutex.RUnlock()
	return len(ModelRequestRateLimitModels) > 0
}

func CheckModelRequestRateLimitModels(jsonStr string) error {
	checkModels := make(map[string][]int)
	err := json.Unmarshal([]byte(jsonStr), &checkModels)
	if err != nil {
		return err
	}
	for model, limits := range checkModels {
		if len(limits) < 2 || len(limits) > 3 {
			return fmt.Errorf("model %s rate limit must be [total, success] or [total, success, windowMinutes]", model)
		}
		if limits[0] < 0 || limits[1] < 1 {
			return fmt.Errorf("model %s has negative rate limit values: [%d, %d]", model, limits[0], limits[1])
		}
		if limits[0] > math.MaxInt32 || limits[1] > math.MaxInt32 {
			return fmt.Errorf("model %s [%d, %d] has max rate limits value 2147483647", model, limits[0], limits[1])
		}
		if len(limits) == 3 && (limits[2] < 1 || limits[2] > 10080) {
			return fmt.Errorf("model %s windowMinutes must be 1-10080, got %d", model, limits[2])
		}
	}
	return nil
}
