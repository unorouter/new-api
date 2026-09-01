package model

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/notify"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
)

type Pricing struct {
	ModelName              string                               `json:"model_name"`
	Description            string                               `json:"description,omitempty"`
	Icon                   string                               `json:"icon,omitempty"`
	Tags                   string                               `json:"tags,omitempty"`
	Metadata               string                               `json:"metadata"`
	CreatedTime            int64                                `json:"created_time,omitempty"`
	VendorID               int                                  `json:"vendor_id,omitempty"`
	QuotaType              int                                  `json:"quota_type"`
	ModelRatio             float64                              `json:"model_ratio"`
	ModelPrice             float64                              `json:"model_price"`
	OwnerBy                string                               `json:"owner_by"`
	CompletionRatio        float64                              `json:"completion_ratio"`
	CacheRatio             *float64                             `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64                             `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64                             `json:"image_ratio,omitempty"`
	AudioRatio             *float64                             `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64                             `json:"audio_completion_ratio,omitempty"`
	EnableGroup            []string                             `json:"enable_groups"`
	SupportedEndpointTypes []constant.EndpointType              `json:"supported_endpoint_types"`
	GridPricing            ratio_setting.GridPricingInfo        `json:"grid_pricing,omitempty"`
	BillingMode            string                               `json:"billing_mode,omitempty"`
	BillingExpr            string                               `json:"billing_expr,omitempty"`
	BillingUsageSchema     map[string]jsplugin.UsageFieldSchema `json:"billing_usage_schema,omitempty"`
	BillingUsageExamples   []jsplugin.UsageExample              `json:"billing_usage_examples,omitempty"`
	PricingVersion         string                               `json:"pricing_version,omitempty"`
	Online                 bool                                 `json:"online"`
}

type PricingVendor struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

var (
	pricingMap           []Pricing
	vendorsList          []PricingVendor
	supportedEndpointMap map[string]common.EndpointInfo
	lastGetPricingTime   time.Time
	updatePricingLock    sync.Mutex

	// Offline-inclusive variant: separately cached so the hot-path pricingMap is
	// never poisoned with offline (zero-enabled-channel) models.
	pricingMapWithOffline         []Pricing
	lastGetPricingWithOfflineTime time.Time
	updatePricingWithOfflineLock  sync.Mutex

	// 缓存映射：模型名 -> 启用分组 / 计费类型
	modelEnableGroups     = make(map[string][]string)
	modelQuotaTypeMap     = make(map[string]int)
	modelEnableGroupsLock = sync.RWMutex{}
)

var (
	modelSupportEndpointTypes = make(map[string][]constant.EndpointType)
	modelSupportEndpointsLock = sync.RWMutex{}
)

func GetPricing() []Pricing {
	if time.Since(lastGetPricingTime) > time.Minute*1 || len(pricingMap) == 0 {
		updatePricingLock.Lock()
		defer updatePricingLock.Unlock()
		// Double check after acquiring the lock
		if time.Since(lastGetPricingTime) > time.Minute*1 || len(pricingMap) == 0 {
			modelSupportEndpointsLock.Lock()
			defer modelSupportEndpointsLock.Unlock()
			updatePricing()
		}
	}
	return pricingMap
}

// GetPricingWithOffline returns pricing including offline models (online=false).
func GetPricingWithOffline() []Pricing {
	if time.Since(lastGetPricingWithOfflineTime) > time.Minute*1 || len(pricingMapWithOffline) == 0 {
		updatePricingWithOfflineLock.Lock()
		defer updatePricingWithOfflineLock.Unlock()
		if time.Since(lastGetPricingWithOfflineTime) > time.Minute*1 || len(pricingMapWithOffline) == 0 {
			updatePricingWithOffline()
		}
	}
	return pricingMapWithOffline
}

func InvalidatePricingCache() {
	updatePricingLock.Lock()
	defer updatePricingLock.Unlock()

	pricingMap = nil
	vendorsList = nil
	lastGetPricingTime = time.Time{}

	updatePricingWithOfflineLock.Lock()
	defer updatePricingWithOfflineLock.Unlock()
	pricingMapWithOffline = nil
	lastGetPricingWithOfflineTime = time.Time{}

	notify.MarkDirty("pricing")
}

// GetVendors 返回当前定价接口使用到的供应商信息
func GetVendors() []PricingVendor {
	if time.Since(lastGetPricingTime) > time.Minute*1 || len(pricingMap) == 0 {
		// 保证先刷新一次
		GetPricing()
	}
	return vendorsList
}

func GetModelSupportEndpointTypes(model string) []constant.EndpointType {
	if model == "" {
		return make([]constant.EndpointType, 0)
	}
	modelSupportEndpointsLock.RLock()
	defer modelSupportEndpointsLock.RUnlock()
	if endpoints, ok := modelSupportEndpointTypes[model]; ok {
		return endpoints
	}
	return make([]constant.EndpointType, 0)
}

// ModelLimits carries the context window + output cap parsed from a model's
// metadata JSON, for OpenAI-compatible clients that read them off /v1/models.
type ModelLimits struct {
	ContextLength   int
	MaxOutputTokens int
}

var (
	modelLimitsMap  map[string]ModelLimits
	modelLimitsLock sync.RWMutex
)

// buildModelLimitsMap parses contextWindow / maxOutputTokens out of the cached
// pricing metadata into a name-keyed map. Called from updatePricing under the
// pricing lock, so the map is rebuilt on the same 1-min cadence as pricingMap.
func buildModelLimitsMap(pricings []Pricing) {
	next := make(map[string]ModelLimits, len(pricings))
	for _, p := range pricings {
		if p.Metadata == "" {
			continue
		}
		var meta struct {
			ContextWindow   int `json:"contextWindow"`
			MaxOutputTokens int `json:"maxOutputTokens"`
		}
		if err := common.UnmarshalJsonStr(p.Metadata, &meta); err != nil {
			continue
		}
		if meta.ContextWindow == 0 && meta.MaxOutputTokens == 0 {
			continue
		}
		next[p.ModelName] = ModelLimits{ContextLength: meta.ContextWindow, MaxOutputTokens: meta.MaxOutputTokens}
	}
	modelLimitsLock.Lock()
	modelLimitsMap = next
	modelLimitsLock.Unlock()
}

// GetCachedModelLimits is GetModelLimits without the cache warm, for callers
// already holding a warmed cache (the relay clamps every request before it
// needs this) or running where no database exists. Returns zero values rather
// than opening a query.
func GetCachedModelLimits(modelName string) ModelLimits {
	if modelName == "" {
		return ModelLimits{}
	}
	modelLimitsLock.RLock()
	defer modelLimitsLock.RUnlock()
	return modelLimitsMap[modelName]
}

// GetModelLimits returns the cached context window + output cap for a model.
// Zero values when absent (caller omits them from the response).
func GetModelLimits(modelName string) ModelLimits {
	if modelName == "" {
		return ModelLimits{}
	}
	GetPricing() // ensure the cache (and the limits map) is warm
	modelLimitsLock.RLock()
	defer modelLimitsLock.RUnlock()
	return modelLimitsMap[modelName]
}

func getPricingEndpointTypesForAbility(ability AbilityWithChannel, advancedCustomConfigs map[int]*dto.AdvancedCustomConfig) []constant.EndpointType {
	if ability.ChannelType != constant.ChannelTypeAdvancedCustom {
		return common.GetEndpointTypesByChannelType(ability.ChannelType, ability.Model)
	}
	if config := advancedCustomConfigs[ability.ChannelId]; config != nil {
		return config.SupportedEndpointTypesForModel(ability.Model)
	}
	return common.GetEndpointTypesByChannelType(ability.ChannelType, ability.Model)
}

// loadPricingAdvancedCustomConfigs runs inside updatePricing while
// updatePricingLock is held, and nests channelSyncLock.RLock. This defines the
// global lock order updatePricingLock -> channelSyncLock: any code path holding
// channelSyncLock must release it before touching the pricing cache (see
// InitChannelCache / CacheUpdateChannel), otherwise it deadlocks.
// The returned configs are pointers shared with the channel cache; they are
// replaced wholesale on update and never mutated in place, so reading them after
// RUnlock is safe.
func loadPricingAdvancedCustomConfigs(enableAbilities []AbilityWithChannel) map[int]*dto.AdvancedCustomConfig {
	channelIDs := make([]int, 0)
	seen := make(map[int]struct{})
	for _, ability := range enableAbilities {
		if ability.ChannelType != constant.ChannelTypeAdvancedCustom {
			continue
		}
		if _, exists := seen[ability.ChannelId]; exists {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIDs = append(channelIDs, ability.ChannelId)
	}
	if len(channelIDs) == 0 {
		return nil
	}

	configs := make(map[int]*dto.AdvancedCustomConfig, len(channelIDs))
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		for _, channelID := range channelIDs {
			if config := channel2advancedCustomConfig[channelID]; config != nil {
				configs[channelID] = config
			}
		}
		return configs
	}

	for _, channelID := range channelIDs {
		channel, err := CacheGetChannel(channelID)
		if err != nil {
			common.SysLog(fmt.Sprintf("load advanced custom channel settings error: channel_id=%d, error=%v", channelID, err))
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			continue
		}
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			configs[channelID] = config
		}
	}
	return configs
}

func appendPricingEndpoint(endpoints []string, endpoint string) []string {
	if endpoint == "" || common.StringsContains(endpoints, endpoint) {
		return endpoints
	}
	return append(endpoints, endpoint)
}

func updatePricing() {
	enableAbilities, err := GetAllEnableAbilityWithChannels()
	if err != nil {
		common.SysLog(fmt.Sprintf("GetAllEnableAbilityWithChannels error: %v", err))
		return
	}
	hasEnabled := make(map[string]bool, len(enableAbilities))
	for _, ability := range enableAbilities {
		hasEnabled[ability.Model] = true
	}
	pricingMap = buildPricing(enableAbilities, hasEnabled, true)
	buildModelLimitsMap(pricingMap)
	lastGetPricingTime = time.Now()
}

// updatePricingWithOffline rebuilds the offline-inclusive cache. It reads ALL
// abilities (enabled+disabled) so models with zero live channels still surface,
// flagged online=false. It does NOT publish the shared endpoint/vendor globals.
func updatePricingWithOffline() {
	allAbilities, err := GetAllAbilityWithChannels()
	if err != nil {
		common.SysLog(fmt.Sprintf("GetAllEnableAbilityWithChannels error: %v", err))
		return
	}
	hasEnabled := make(map[string]bool)
	for _, ability := range allAbilities {
		if ability.Enabled {
			hasEnabled[ability.Model] = true
		}
	}
	pricingMapWithOffline = buildPricing(allAbilities, hasEnabled, false)
	lastGetPricingWithOfflineTime = time.Now()
}

// buildPricing builds the pricing slice from the given ability set. hasEnabled
// marks which models have at least one live channel (drives the Online flag).
// When publishGlobals is true it also refreshes the shared vendor/endpoint/
// group caches; the offline variant passes false so it never clobbers them.
func buildPricing(enableAbilities []AbilityWithChannel, hasEnabled map[string]bool, publishGlobals bool) []Pricing {
	// 预加载模型元数据与供应商一次，避免循环查询
	var allMeta []Model
	_ = DB.Find(&allMeta).Error
	metaMap := make(map[string]*Model)
	prefixList := make([]*Model, 0)
	suffixList := make([]*Model, 0)
	containsList := make([]*Model, 0)
	for i := range allMeta {
		m := &allMeta[i]
		if m.NameRule == NameRuleExact {
			metaMap[m.ModelName] = m
		} else {
			switch m.NameRule {
			case NameRulePrefix:
				prefixList = append(prefixList, m)
			case NameRuleSuffix:
				suffixList = append(suffixList, m)
			case NameRuleContains:
				containsList = append(containsList, m)
			}
		}
	}

	// 将非精确规则模型匹配到 metaMap
	for _, m := range prefixList {
		for _, pricingModel := range enableAbilities {
			if strings.HasPrefix(pricingModel.Model, m.ModelName) {
				if _, exists := metaMap[pricingModel.Model]; !exists {
					metaMap[pricingModel.Model] = m
				}
			}
		}
	}
	for _, m := range suffixList {
		for _, pricingModel := range enableAbilities {
			if strings.HasSuffix(pricingModel.Model, m.ModelName) {
				if _, exists := metaMap[pricingModel.Model]; !exists {
					metaMap[pricingModel.Model] = m
				}
			}
		}
	}
	for _, m := range containsList {
		for _, pricingModel := range enableAbilities {
			if strings.Contains(pricingModel.Model, m.ModelName) {
				if _, exists := metaMap[pricingModel.Model]; !exists {
					metaMap[pricingModel.Model] = m
				}
			}
		}
	}

	// 预加载供应商
	var vendors []Vendor
	_ = DB.Find(&vendors).Error
	vendorMap := make(map[int]*Vendor)
	for i := range vendors {
		vendorMap[vendors[i].Id] = &vendors[i]
	}

	// 初始化默认供应商映射
	initDefaultVendorMapping(metaMap, vendorMap, enableAbilities)

	// 构建对前端友好的供应商列表
	if publishGlobals {
		vendorsList = make([]PricingVendor, 0, len(vendorMap))
		for _, v := range vendorMap {
			vendorsList = append(vendorsList, PricingVendor{
				ID:          v.Id,
				Name:        v.Name,
				Description: v.Description,
				Icon:        v.Icon,
			})
		}
	}

	modelGroupsMap := make(map[string]*types.Set[string])

	// Only ROUTABLE groups are advertised: the offline-inclusive build passes
	// disabled abilities too (so dead models still surface), but their groups
	// must not appear in enable_groups - the frontend renders group pricing
	// and picks the cheapest advertised group from them, and a disabled
	// channel's group is a price nobody can route to.
	for _, ability := range enableAbilities {
		if !ability.Enabled {
			continue
		}
		groups, ok := modelGroupsMap[ability.Model]
		if !ok {
			groups = types.NewSet[string]()
			modelGroupsMap[ability.Model] = groups
		}
		groups.Add(ability.Group)
	}

	//这里使用切片而不是Set，因为一个模型可能支持多个端点类型，并且第一个端点是优先使用端点
	modelSupportEndpointsStr := make(map[string][]string)
	advancedCustomConfigs := loadPricingAdvancedCustomConfigs(enableAbilities)

	// 先根据已有能力填充原生端点
	for _, ability := range enableAbilities {
		endpoints := modelSupportEndpointsStr[ability.Model]
		channelTypes := getPricingEndpointTypesForAbility(ability, advancedCustomConfigs)
		for _, channelType := range channelTypes {
			if !common.StringsContains(endpoints, string(channelType)) {
				endpoints = append(endpoints, string(channelType))
			}
		}
		modelSupportEndpointsStr[ability.Model] = endpoints
	}

	// 再补充模型自定义端点：若配置有效则追加到已有推断，不再裁剪渠道真实能力
	for modelName, meta := range metaMap {
		if strings.TrimSpace(meta.Endpoints) == "" {
			continue
		}
		var raw map[string]interface{}
		if err := common.Unmarshal([]byte(meta.Endpoints), &raw); err == nil {
			endpoints := modelSupportEndpointsStr[modelName]
			for k, v := range raw {
				switch v.(type) {
				case string, map[string]interface{}:
					endpoints = appendPricingEndpoint(endpoints, k)
				}
			}
			if len(endpoints) > 0 {
				modelSupportEndpointsStr[modelName] = endpoints
			}
		}
	}

	endpointTypesByModel := make(map[string][]constant.EndpointType)
	for model, endpoints := range modelSupportEndpointsStr {
		supportedEndpoints := make([]constant.EndpointType, 0)
		for _, endpointStr := range endpoints {
			endpointType := constant.EndpointType(endpointStr)
			supportedEndpoints = append(supportedEndpoints, endpointType)
		}
		endpointTypesByModel[model] = supportedEndpoints
	}

	if publishGlobals {
		modelSupportEndpointTypes = endpointTypesByModel

		// 构建全局 supportedEndpointMap（默认 + 自定义覆盖）
		supportedEndpointMap = make(map[string]common.EndpointInfo)
		// 1. 默认端点
		for _, endpoints := range endpointTypesByModel {
			for _, et := range endpoints {
				if info, ok := common.GetDefaultEndpointInfo(et); ok {
					if _, exists := supportedEndpointMap[string(et)]; !exists {
						supportedEndpointMap[string(et)] = info
					}
				}
			}
		}
		// 2. 自定义端点（models 表）覆盖默认
		for _, meta := range metaMap {
			if strings.TrimSpace(meta.Endpoints) == "" {
				continue
			}
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(meta.Endpoints), &raw); err == nil {
				for k, v := range raw {
					switch val := v.(type) {
					case string:
						supportedEndpointMap[k] = common.EndpointInfo{Path: val, Method: "POST"}
					case map[string]interface{}:
						ep := common.EndpointInfo{Method: "POST"}
						if p, ok := val["path"].(string); ok {
							ep.Path = p
						}
						if m, ok := val["method"].(string); ok {
							ep.Method = strings.ToUpper(m)
						}
						supportedEndpointMap[k] = ep
					default:
						// ignore unsupported types
					}
				}
			}
		}
	}

	result := make([]Pricing, 0)
	pluginGeneration := jsplugin.DefaultRegistry.Generation()
	for model, groups := range modelGroupsMap {
		if strings.HasSuffix(model, "[1m]") {
			continue
		}
		pricing := Pricing{
			ModelName:              model,
			EnableGroup:            groups.Items(),
			SupportedEndpointTypes: endpointTypesByModel[model],
			Online:                 hasEnabled[model],
		}

		// 补充模型元数据（描述、标签、供应商、状态）
		if meta, ok := metaMap[model]; ok {
			// 若模型被禁用(status!=1)，则直接跳过，不返回给前端
			if meta.Status != 1 {
				continue
			}
			pricing.Description = meta.Description
			pricing.Icon = meta.Icon
			pricing.Tags = meta.Tags
			pricing.Metadata = meta.Metadata
			pricing.CreatedTime = meta.CreatedTime
			pricing.VendorID = meta.VendorID
		}
		applyPricingRatios(model, &pricing)
		if pricing.BillingMode == "" {
			if target, resolved := ResolveTaskModelAlias(pluginGeneration, model); resolved && target.Declared != "" {
				if tailMode := billing_setting.GetBillingMode(target.Declared); tailMode == "tiered_expr" {
					if expr, ok := billing_setting.GetBillingExpr(target.Declared); ok && strings.TrimSpace(expr) != "" {
						pricing.BillingMode = tailMode
						pricing.BillingExpr = expr
					}
				}
			}
		}
		plugin, ok := pluginGeneration.GetByModel(model)
		if !ok {
			if target, resolved := ResolveTaskModelAlias(pluginGeneration, model); resolved {
				plugin, ok = pluginGeneration.Get(target.PluginKey)
			}
		}
		if ok && plugin != nil && len(plugin.Meta.UsageSchema) > 0 {
			pricing.BillingUsageSchema = make(map[string]jsplugin.UsageFieldSchema, len(plugin.Meta.UsageSchema))
			for key, field := range plugin.Meta.UsageSchema {
				field.Enum = append([]string(nil), field.Enum...)
				field.Description = maps.Clone(field.Description)
				pricing.BillingUsageSchema[key] = field
			}
			if len(plugin.Meta.UsageExamples) > 0 {
				pricing.BillingUsageExamples = make([]jsplugin.UsageExample, len(plugin.Meta.UsageExamples))
				for index, example := range plugin.Meta.UsageExamples {
					facts := make(map[string]any, len(example.Facts))
					for key, value := range example.Facts {
						facts[key] = value
					}
					pricing.BillingUsageExamples[index] = jsplugin.UsageExample{
						Label: example.Label,
						Facts: facts,
					}
				}
			}
		}
		result = append(result, pricing)
	}

	// 防止大更新后数据不通用
	if len(result) > 0 {
		result[0].PricingVersion = "5a90f2b86c08bd983a9a2e6d66c255f4eaef9c4bc934386d2b6ae84ef0ff1f1f"
	}

	// 刷新缓存映射，供高并发快速查询
	if publishGlobals {
		modelEnableGroupsLock.Lock()
		modelEnableGroups = make(map[string][]string)
		modelQuotaTypeMap = make(map[string]int)
		for _, p := range result {
			modelEnableGroups[p.ModelName] = p.EnableGroup
			modelQuotaTypeMap[p.ModelName] = p.QuotaType
		}
		modelEnableGroupsLock.Unlock()
	}

	return result
}

// applyPricingRatios fills the price/ratio fields on a Pricing from the ratio +
// billing settings for the given model. Shared by buildPricing (per-model loop)
// and GetPricingByModelName (single fully-dark model).
func applyPricingRatios(model string, pricing *Pricing) {
	modelPrice, findPrice := ratio_setting.GetModelPrice(model, false)
	if findPrice {
		pricing.ModelPrice = modelPrice
		pricing.QuotaType = 1
	} else {
		modelRatio, _, _ := ratio_setting.GetModelRatio(model)
		pricing.ModelRatio = modelRatio
		pricing.CompletionRatio = ratio_setting.GetCompletionRatio(model)
		pricing.QuotaType = 0
	}
	// Override with custom quota type if set (e.g. 3=flat custom, 4=grid pricing)
	if override, ok := ratio_setting.GetModelQuotaTypeOverride(model); ok {
		pricing.QuotaType = override
	}
	// Attach grid pricing if available (works with any quota type)
	if gridInfo := ratio_setting.GetGridPricingInfo(model); gridInfo != nil {
		pricing.GridPricing = gridInfo
	}
	if cacheRatio, ok := ratio_setting.GetCacheRatio(model); ok {
		pricing.CacheRatio = &cacheRatio
	}
	if createCacheRatio, ok := ratio_setting.GetCreateCacheRatio(model); ok {
		pricing.CreateCacheRatio = &createCacheRatio
	}
	if imageRatio, ok := ratio_setting.GetImageRatio(model); ok {
		pricing.ImageRatio = &imageRatio
	}
	if ratio_setting.ContainsAudioRatio(model) {
		audioRatio := ratio_setting.GetAudioRatio(model)
		pricing.AudioRatio = &audioRatio
	}
	if ratio_setting.ContainsAudioCompletionRatio(model) {
		audioCompletionRatio := ratio_setting.GetAudioCompletionRatio(model)
		pricing.AudioCompletionRatio = &audioCompletionRatio
	}
	if billingMode := billing_setting.GetBillingMode(model); billingMode == "tiered_expr" {
		if expr, ok := billing_setting.GetBillingExpr(model); ok && strings.TrimSpace(expr) != "" {
			pricing.BillingMode = billingMode
			pricing.BillingExpr = expr
		}
	}
}

// GetPricingByModelName returns a single model's pricing BY NAME, always, even
// when every channel is disabled or deleted (zero abilities). The detail page
// must render a known model regardless of routability. Resolution order:
//  1. the offline-inclusive cache (carries enable_groups + online when any
//     ability still exists), then
//  2. a fresh entry built from the models metadata table + ratio settings for a
//     fully-dark model (no ability at all): enable_groups empty, online false.
//
// Returns (Pricing{}, false) only when the name is unknown to BOTH the pricing
// cache and the models table. Unlike GetPricing it applies NO usable-group
// filter - by-name lookup is never gated on routability.
func GetPricingByModelName(modelName string) (Pricing, bool) {
	if modelName == "" {
		return Pricing{}, false
	}
	for _, p := range GetPricingWithOffline() {
		if p.ModelName == modelName {
			return p, true
		}
	}

	// Fully dark: no ability row feeds the pricing cache. Build from metadata.
	var meta Model
	if err := DB.Where("model_name = ?", modelName).First(&meta).Error; err != nil {
		return Pricing{}, false
	}
	if meta.Status != 1 {
		return Pricing{}, false
	}
	pricing := Pricing{
		ModelName:              modelName,
		Description:            meta.Description,
		Icon:                   meta.Icon,
		Tags:                   meta.Tags,
		Metadata:               meta.Metadata,
		CreatedTime:            meta.CreatedTime,
		VendorID:               meta.VendorID,
		EnableGroup:            []string{},
		SupportedEndpointTypes: GetModelSupportEndpointTypes(modelName),
		Online:                 false,
	}
	applyPricingRatios(modelName, &pricing)
	return pricing, true
}

// GetSupportedEndpointMap 返回全局端点到路径的映射
func GetSupportedEndpointMap() map[string]common.EndpointInfo {
	return supportedEndpointMap
}
