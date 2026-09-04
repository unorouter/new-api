package service

import (
	"math/rand"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func GetUserUsableGroups(userGroup string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" {
		specialSettings, b := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get(userGroup)
		if b {
			// 处理特殊可用分组
			for specialGroup, desc := range specialSettings {
				if strings.HasPrefix(specialGroup, "-:") {
					// 移除分组
					groupToRemove := strings.TrimPrefix(specialGroup, "-:")
					delete(groupsCopy, groupToRemove)
				} else if strings.HasPrefix(specialGroup, "+:") {
					// 添加分组
					groupToAdd := strings.TrimPrefix(specialGroup, "+:")
					groupsCopy[groupToAdd] = desc
				} else {
					// 直接添加分组
					groupsCopy[specialGroup] = desc
				}
			}
		}
		// 如果userGroup不在UserUsableGroups中，返回UserUsableGroups + userGroup
		if _, ok := groupsCopy[userGroup]; !ok {
			groupsCopy[userGroup] = "User group"
		}
	}
	return groupsCopy
}

// Membership without materializing the map: GetUserUsableGroups clones the whole
// usable-group map (thousands of entries), which is 62%-of-CPU expensive on the
// per-request path. Semantics mirror the clone-based version: the user's own group
// is always usable (the clone re-adds it after specials run), special "+:"/plain
// entries grant, special "-:" revokes a base-map entry.
func GroupInUserUsableGroups(userGroup, groupName string) bool {
	if userGroup != "" && groupName == userGroup {
		return true
	}
	if userGroup != "" {
		if specialSettings, ok := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get(userGroup); ok {
			if _, ok := specialSettings[groupName]; ok {
				return true
			}
			if _, ok := specialSettings["+:"+groupName]; ok {
				return true
			}
			if _, ok := specialSettings["-:"+groupName]; ok {
				return false
			}
		}
	}
	return setting.UserUsableGroupsContains(groupName)
}

func IsUserSelectableGroup(userGroup, groupName string) bool {
	if groupName == "" || groupName == "auto" {
		return false
	}
	return GroupInUserUsableGroups(userGroup, groupName) && ratio_setting.ContainsGroupRatio(groupName)
}

// GetUserAutoGroup 根据用户分组获取自动分组设置
func GetUserAutoGroup(userGroup string) []string {
	autoGroups := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range setting.GetAutoGroups() {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		autoGroups = append(autoGroups, group)
	}
	return autoGroups
}

// FilterUserTokenAutoGroups applies current permissions before the current
// per-token limit. It intentionally does not fall back to the global Auto list.
func FilterUserTokenAutoGroups(userGroup string, groups []string) []string {
	maxCount := setting.GetMaxTokenAutoGroups()
	filtered := make([]string, 0, min(len(groups), maxCount))
	seen := make(map[string]struct{})
	for _, group := range groups {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		filtered = append(filtered, group)
		if len(filtered) == maxCount {
			break
		}
	}
	return filtered
}

// shuffleFreeAutoGroups randomizes the order of the zero-ratio (free) groups and
// leaves every paid group where it is. The Auto list is a fixed order, so without
// this the first few groups serving a model take all the traffic and get throttled
// while identical later ones stay idle, unreachable behind the total attempt cap.
// Paid groups keep their order because that order encodes price preference.
//
// The order is drawn once per request and reused: ContextKeyAutoGroupIndex is a
// position into the returned slice and retries re-resolve the list, so reshuffling
// per call would make a retry read its index against a different order, re-trying
// groups it already tried and skipping others entirely.
func shuffleFreeAutoGroups(c *gin.Context, userGroup string, groups []string) []string {
	freeIndexes := make([]int, 0, len(groups))
	for i, group := range groups {
		if GetUserGroupRatio(userGroup, group) == 0 {
			freeIndexes = append(freeIndexes, i)
		}
	}
	if len(freeIndexes) < 2 {
		return groups
	}
	var order []int
	if c != nil {
		if cached, ok := common.GetContextKey(c, constant.ContextKeyResolvedAutoGroups); ok {
			if previous, ok := cached.([]int); ok && len(previous) == len(freeIndexes) {
				order = previous
			}
		}
	}
	if order == nil {
		order = make([]int, len(freeIndexes))
		copy(order, freeIndexes)
		rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		if c != nil {
			common.SetContextKey(c, constant.ContextKeyResolvedAutoGroups, order)
		}
	}
	shuffled := make([]string, len(groups))
	copy(shuffled, groups)
	for i, target := range order {
		shuffled[freeIndexes[i]] = groups[target]
	}
	return shuffled
}

// GetRequestAutoGroups resolves the ordered Auto groups for the current token.
// The absence of the context value means that the token inherits the complete
// global Auto list; a present (even empty) value is an explicit token snapshot.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	if c == nil {
		return GetUserAutoGroup(userGroup)
	}
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return shuffleFreeAutoGroups(c, userGroup, GetUserAutoGroup(userGroup))
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	// Shuffle before the per-token cap, or the cap would keep the same head of the
	// fixed list every time and the shuffle would only reorder those few.
	return FilterUserTokenAutoGroups(userGroup, shuffleFreeAutoGroups(c, userGroup, groups))
}

// GetGroupsEnabledModels 按 groups 顺序获取各分组启用的模型并去重
func GetGroupsEnabledModels(groups []string) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}

// GetUserGroupRatio 获取用户使用某个分组的倍率
// userGroup 用户分组
// group 需要获取倍率的分组
func GetUserGroupRatio(userGroup, group string) float64 {
	ratio, ok := ratio_setting.GetGroupGroupRatio(userGroup, group)
	if ok {
		return ratio
	}
	return ratio_setting.GetGroupRatio(group)
}

// IsCompositeTokenGroup reports whether a token's group value pins multiple
// groups (comma-separated), which behaves like a scoped auto group.
func IsCompositeTokenGroup(tokenGroup string) bool {
	return strings.Contains(tokenGroup, ",")
}

// ParseTokenGroups splits a composite token group into its trimmed non-empty
// elements, preserving declared order.
func ParseTokenGroups(tokenGroup string) []string {
	parts := strings.Split(tokenGroup, ",")
	groups := make([]string, 0, len(parts))
	for _, g := range parts {
		g = strings.TrimSpace(g)
		if g != "" {
			groups = append(groups, g)
		}
	}
	return groups
}

// TokenPinEntry is a token's per-model routing override: explicitly pinned
// groups, an optional price band over the group ratio, or both. Min/Max are
// pointers so "unset" stays distinct from a real 0 (free groups bill at 0).
type TokenPinEntry struct {
	Groups []string `json:"groups"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	Auto   bool     `json:"auto,omitempty"`
}

// HasBand reports whether the entry constrains price at all.
func (e TokenPinEntry) HasBand() bool {
	return e.Min != nil || e.Max != nil
}

// ParseTokenGroupMapping parses a token's per-model group mapping JSON
// ({"model":{"groups":["group",...],"min":0.02,"max":0.05}}). Returns nil on
// empty or invalid input; callers rely on nil meaning "invalid" (validation)
// and on an absent key meaning "unmapped" (routing).
func ParseTokenGroupMapping(mappingJSON string) map[string]TokenPinEntry {
	mappingJSON = strings.TrimSpace(mappingJSON)
	if mappingJSON == "" || mappingJSON == "{}" {
		return nil
	}
	var mapping map[string]TokenPinEntry
	if err := common.UnmarshalJsonStr(mappingJSON, &mapping); err != nil {
		return nil
	}
	if len(mapping) == 0 {
		return nil
	}
	return mapping
}

// ResolveTokenGroupForModel returns the effective token group for a request:
// the union of the entry's pinned groups and, when a price band is set, every
// candidate group whose ratio falls inside it. The result composes into the
// same comma-separated scoped-auto group the channel-select engine already
// handles, so every downstream consumer is unchanged.
//
// candidates supplies the groups that actually serve this model; it is only
// consulted when a band is set, so the unbanded (pin-only) path stays free of
// any lookup. An entry on auto returns the base group with its configuration
// left stored but inert.
func ResolveTokenGroupForModel(mapping map[string]TokenPinEntry, userGroup, model, baseGroup string, candidates func() []string) string {
	if len(mapping) == 0 {
		return baseGroup
	}
	entry, ok := mapping[model]
	if !ok || entry.Auto {
		return baseGroup
	}
	cleaned := make([]string, 0, len(entry.Groups))
	seen := make(map[string]struct{}, len(entry.Groups))
	for _, g := range entry.Groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		cleaned = append(cleaned, g)
	}
	if entry.HasBand() && candidates != nil {
		for _, g := range candidates() {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if _, dup := seen[g]; dup {
				continue
			}
			ratio := GetUserGroupRatio(userGroup, g)
			if entry.Min != nil && ratio < *entry.Min {
				continue
			}
			if entry.Max != nil && ratio > *entry.Max {
				continue
			}
			seen[g] = struct{}{}
			cleaned = append(cleaned, g)
		}
	}
	if len(cleaned) == 0 {
		return baseGroup
	}
	return strings.Join(cleaned, ",")
}

// GetTokenAutoGroups returns the ordered group list the channel-select engine
// iterates for a token: the configured auto groups for "auto", or - for a
// composite pinned group like "vip,discount" - the pinned groups the user may
// actually use, cheapest ratio first (pinning exists so users avoid billing at
// a pricier group, so the cheapest usable pinned group must be tried first).
func GetTokenAutoGroups(c *gin.Context, userGroup, tokenGroup string) []string {
	if !IsCompositeTokenGroup(tokenGroup) {
		return GetRequestAutoGroups(c, userGroup)
	}
	usable := GetUserUsableGroups(userGroup)
	groups := make([]string, 0)
	for _, g := range ParseTokenGroups(tokenGroup) {
		if _, ok := usable[g]; !ok {
			continue
		}
		if !ratio_setting.ContainsGroupRatio(g) {
			continue
		}
		groups = append(groups, g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return GetUserGroupRatio(userGroup, groups[i]) < GetUserGroupRatio(userGroup, groups[j])
	})
	return groups
}
