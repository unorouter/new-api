package model

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

type groupDataPolicySnapshot struct {
	generation uint64
	builtAt    time.Time
	byGroup    map[string]dto.GroupDataPolicy
	recipients map[string]groupRecipients
}

// groupRecipients is who receives inputs through one group. Internal only:
// provider tags name our upstreams, which is a business secret.
type groupRecipients struct {
	// Channels whose provider trains on, retains or may use prompts (practice unknown).
	ownPurpose []dataRecipient
	// Channels whose provider neither trains on nor retains prompts.
	other []dataRecipient
	// True when some channel neither trains on nor retains prompts.
	withoutOwnPurpose bool
	// Seat countries of every channel in the group.
	countries []string
}

type dataRecipient struct {
	provider string
	country  string
	unsafe   bool
}

var (
	groupDataPolicyGeneration atomic.Uint64
	groupDataPolicyCache      atomic.Pointer[groupDataPolicySnapshot]
	groupDataPolicyDBCache    atomic.Pointer[groupDataPolicySnapshot]
)

const groupDataPolicyDBReuse = 5 * time.Second

func invalidateGroupDataPolicies() {
	groupDataPolicyGeneration.Add(1)
}

// GroupDataPolicies returns the data-protection profile of every group in
// which at least one channel records a data_policy. The map is shared and must
// not be modified.
func GroupDataPolicies() map[string]dto.GroupDataPolicy {
	return groupDataPolicyState().byGroup
}

func groupDataPolicyState() *groupDataPolicySnapshot {
	if !common.MemoryCacheEnabled {
		// Briefly reused: a catalog listing asks once per model row.
		if cached := groupDataPolicyDBCache.Load(); cached != nil && time.Since(cached.builtAt) < groupDataPolicyDBReuse {
			return cached
		}
		var channels []*Channel
		err := DB.Select("id", commonGroupCol, "setting").Find(&channels).Error
		if err != nil {
			common.SysLog("failed to load channel data policies: " + err.Error())
			return &groupDataPolicySnapshot{byGroup: map[string]dto.GroupDataPolicy{}, recipients: map[string]groupRecipients{}}
		}
		snapshot := mergeGroupDataPolicies(channels)
		snapshot.builtAt = time.Now()
		groupDataPolicyDBCache.Store(snapshot)
		return snapshot
	}
	// Read the generation first so an invalidation that lands mid-build leaves
	// the stored snapshot stale instead of current.
	generation := groupDataPolicyGeneration.Load()
	if cached := groupDataPolicyCache.Load(); cached != nil && cached.generation == generation {
		return cached
	}
	channelSyncLock.RLock()
	channels := make([]*Channel, 0, len(channelsIDM))
	for _, channel := range channelsIDM {
		channels = append(channels, &Channel{Id: channel.Id, Group: channel.Group, Setting: channel.Setting})
	}
	channelSyncLock.RUnlock()
	snapshot := mergeGroupDataPolicies(channels)
	snapshot.generation = generation
	groupDataPolicyCache.Store(snapshot)
	return snapshot
}

// mergeGroupDataPolicies folds the channels of each group into the most
// conservative profile. A channel without a policy counts as unknown, and a
// group where no channel has one is left out of the public profiles.
func mergeGroupDataPolicies(channels []*Channel) *groupDataPolicySnapshot {
	merged := make(map[string]dto.GroupDataPolicy)
	recipients := make(map[string]groupRecipients)
	recorded := make(map[string]bool)
	for _, channel := range channels {
		// Not GetSetting: on a parse error it saves the channel, and the DB path
		// loads only three columns.
		var setting kitdto.ChannelSettings
		if channel.Setting != nil && *channel.Setting != "" {
			_ = common.UnmarshalJsonStr(*channel.Setting, &setting)
		}
		policy := setting.DataPolicy
		current := kitdto.ChannelDataPolicy{}
		if policy != nil {
			current = *policy
		}
		recipient := dataRecipient{provider: current.Provider, country: current.SeatCountry, unsafe: current.UnsafeThirdCountry()}
		onwardUnvetted := recipient.unsafe && (kitdto.IsEEACountry(current.SeatCountry) || kitdto.IsEUAdequacyCountry(current.SeatCountry))
		// Without a tag the channel still has to count, or adding it would not
		// change the provider set a consent covers.
		if recipient.provider == "" {
			recipient.provider = "channel:" + strconv.Itoa(channel.Id)
		}
		for group := range strings.SplitSeq(channel.Group, ",") {
			if group == "" {
				continue
			}
			if policy != nil {
				recorded[group] = true
			}
			r := recipients[group]
			if current.SeatCountry != "" && !slices.Contains(r.countries, current.SeatCountry) {
				r.countries = append(r.countries, current.SeatCountry)
			}
			switch {
			case policy.UsesInputsForOwnPurposes():
				r.ownPurpose = append(r.ownPurpose, recipient)
			default:
				r.other = append(r.other, recipient)
				r.withoutOwnPurpose = true
			}
			recipients[group] = r
			existing, seen := merged[group]
			if !seen {
				merged[group] = dto.GroupDataPolicy{
					SeatCountry:        current.SeatCountry,
					Prompts:            orUnknown(current.Prompts),
					Transfer:           orUnknown(current.Transfer),
					UnsafeThirdCountry: current.UnsafeThirdCountry(),
					OnwardUnvetted:     onwardUnvetted,
				}
				continue
			}
			if seatRisk(current.SeatCountry) > seatRisk(existing.SeatCountry) ||
				(seatRisk(current.SeatCountry) == seatRisk(existing.SeatCountry) && current.SeatCountry < existing.SeatCountry) {
				existing.SeatCountry = current.SeatCountry
			}
			if promptsRisk[orUnknown(current.Prompts)] > promptsRisk[existing.Prompts] {
				existing.Prompts = orUnknown(current.Prompts)
			}
			if transferRisk[orUnknown(current.Transfer)] > transferRisk[existing.Transfer] {
				existing.Transfer = orUnknown(current.Transfer)
			}
			existing.UnsafeThirdCountry = existing.UnsafeThirdCountry || current.UnsafeThirdCountry()
			existing.OnwardUnvetted = existing.OnwardUnvetted || onwardUnvetted
			merged[group] = existing
		}
	}
	for group := range merged {
		if !recorded[group] {
			delete(merged, group)
		}
	}
	return &groupDataPolicySnapshot{byGroup: merged, recipients: recipients}
}

// GroupPoliciesFor returns the profiles of the given groups that record one;
// "all" stands for every group.
func GroupPoliciesFor(groups []string) map[string]dto.GroupDataPolicy {
	policies := GroupDataPolicies()
	out := make(map[string]dto.GroupDataPolicy)
	if slices.Contains(groups, "all") {
		maps.Copy(out, policies)
		return out
	}
	for _, g := range groups {
		if p, ok := policies[g]; ok {
			out[g] = p
		}
	}
	return out
}

// OfflineModelGroups lists the groups a model would route through once any of
// its channels is back, with their profiles: an offline model still needs its
// seat countries for a consent given before it returns.
func OfflineModelGroups(modelName string) ([]string, map[string]dto.GroupDataPolicy) {
	groups, err := GroupsListingModel(modelName)
	if err != nil {
		common.SysError("failed to list groups of offline model " + modelName + ": " + err.Error())
		return []string{}, map[string]dto.GroupDataPolicy{}
	}
	return groups, GroupPoliciesFor(groups)
}

// ListedModelGroups maps each of the given models to the groups listing it,
// enabled or not, in one query per chunk.
func ListedModelGroups(models []string) map[string][]string {
	out := make(map[string][]string, len(models))
	for chunk := range slices.Chunk(models, 1000) {
		var rows []Ability
		if err := DB.Model(&Ability{}).Select("model", commonGroupCol).Where("model IN ?", chunk).Distinct().Find(&rows).Error; err != nil {
			common.SysError("failed to list groups of offline models: " + err.Error())
			return out
		}
		for _, row := range rows {
			out[row.Model] = append(out[row.Model], row.Group)
		}
	}
	return out
}

// OwnPurposeGroup is one group of a model whose providers train on or retain
// prompts. Providers is internal and must never reach an API response.
type OwnPurposeGroup struct {
	Group     string
	Providers []string
	Countries []string
	Unsafe    bool
}

// OwnPurposeGroups returns the model's groups whose providers use inputs for
// their own purposes: the enabled groups, or every group listing the model
// while it is offline.
func OwnPurposeGroups(modelName string) []OwnPurposeGroup {
	_, groups := consentModelGroups(modelName)
	return ownPurposeGroups(groupDataPolicyState(), groups)
}

func ownPurposeGroups(state *groupDataPolicySnapshot, groups []string) []OwnPurposeGroup {
	var out []OwnPurposeGroup
	for _, group := range expandGroups(state, groups) {
		recipients := state.recipients[group].ownPurpose
		if len(recipients) == 0 {
			continue
		}
		providers := map[string]bool{}
		countries := map[string]bool{}
		unsafe := false
		for _, r := range recipients {
			addRecipient(r, providers, countries)
			unsafe = unsafe || r.unsafe
		}
		out = append(out, OwnPurposeGroup{
			Group:     group,
			Providers: slices.Sorted(maps.Keys(providers)),
			Countries: slices.Sorted(maps.Keys(countries)),
			Unsafe:    unsafe,
		})
	}
	return out
}

func expandGroups(state *groupDataPolicySnapshot, groups []string) []string {
	if slices.Contains(groups, "all") {
		return slices.Sorted(maps.Keys(state.recipients))
	}
	return groups
}

func addRecipient(r dataRecipient, providers, countries map[string]bool) {
	providers[r.provider] = true
	if r.country != "" {
		countries[r.country] = true
	}
}

// ConsentScope says whether a model needs a recorded consent before inputs go
// to its providers, and which providers and countries that consent covers.
// Providers is internal and must never reach an API response.
type ConsentScope struct {
	// The model name consents are recorded under, after resolving the name the
	// distributor would route.
	Model           string
	TrainsOrRetains bool
	// An own-purpose provider, practice unknown included, sits in an unsafe
	// third country (Art. 49 (1)(a) GDPR).
	Required  bool
	Providers []string
	Countries []string
	// Seat countries of every provider of the model, own-purpose or not.
	SeatCountries []string
	// Some group can serve the model without an own-purpose provider, so an
	// objection under Art. 21 GDPR still leaves it usable.
	ServableWithoutOwnPurpose bool
}

// ConsentScopeForGroups computes the scope over exactly these groups.
func ConsentScopeForGroups(modelName string, groups []string) ConsentScope {
	return consentScope(groupDataPolicyState(), modelName, groups)
}

func consentScope(state *groupDataPolicySnapshot, modelName string, groups []string) ConsentScope {
	scope := ConsentScope{Model: modelName}
	providers := map[string]bool{}
	countries := map[string]bool{}
	for _, g := range ownPurposeGroups(state, groups) {
		scope.TrainsOrRetains = true
		scope.Required = scope.Required || g.Unsafe
		for _, p := range g.Providers {
			providers[p] = true
		}
		for _, c := range g.Countries {
			countries[c] = true
		}
	}
	seats := map[string]bool{}
	for _, group := range expandGroups(state, groups) {
		recipients := state.recipients[group]
		for _, c := range recipients.countries {
			seats[c] = true
		}
		scope.ServableWithoutOwnPurpose = scope.ServableWithoutOwnPurpose || recipients.withoutOwnPurpose
	}
	scope.Providers = slices.Sorted(maps.Keys(providers))
	scope.Countries = slices.Sorted(maps.Keys(countries))
	maps.Copy(seats, countries)
	scope.SeatCountries = slices.Sorted(maps.Keys(seats))
	return scope
}

// ConsentRequired reports whether inputs may reach the model only with a
// recorded consent.
func ConsentRequired(modelName string) bool {
	return ConsentScopeFor(modelName).Required
}

type consentScopeEntry struct {
	generation uint64
	expires    time.Time
	scope      ConsentScope
}

const (
	consentScopeCacheTTL = 30 * time.Second
	consentScopeCacheMax = 4096
)

var (
	consentScopeCache     sync.Map
	consentScopeCacheSize atomic.Int64
)

// ConsentScopeFor is the relay-path scope: over the groups currently routing
// the model, cached per requested name.
func ConsentScopeFor(modelName string) ConsentScope {
	generation := groupDataPolicyGeneration.Load()
	if cached, ok := consentScopeCache.Load(modelName); ok {
		if entry, ok := cached.(consentScopeEntry); ok && entry.generation == generation && time.Now().Before(entry.expires) {
			return entry.scope
		}
	}
	resolved, groups := consentModelGroups(modelName)
	scope := consentScope(groupDataPolicyState(), resolved, groups)
	// Request names are caller input, so the cache is bounded; unknown names are
	// cached too, so a stream of typos costs one lookup each per TTL.
	if consentScopeCacheSize.Load() >= consentScopeCacheMax {
		trimConsentScopeCache(generation)
	}
	if _, loaded := consentScopeCache.Swap(modelName, consentScopeEntry{generation: generation, expires: time.Now().Add(consentScopeCacheTTL), scope: scope}); !loaded {
		consentScopeCacheSize.Add(1)
	}
	return scope
}

// trimConsentScopeCache drops stale entries first and, when every entry is
// fresh, half of them, so a burst of new names never empties the whole cache.
func trimConsentScopeCache(generation uint64) {
	now := time.Now()
	removed := int64(0)
	consentScopeCache.Range(func(key, value any) bool {
		if entry, ok := value.(consentScopeEntry); !ok || entry.generation != generation || now.After(entry.expires) {
			if _, loaded := consentScopeCache.LoadAndDelete(key); loaded {
				removed++
			}
		}
		return true
	})
	if removed == 0 {
		evict := true
		consentScopeCache.Range(func(key, _ any) bool {
			if evict {
				if _, loaded := consentScopeCache.LoadAndDelete(key); loaded {
					removed++
				}
			}
			evict = !evict
			return true
		})
	}
	consentScopeCacheSize.Add(-removed)
}

// RecordedConsentScope is the scope a new consent records: every group that
// lists the model, enabled or not, so a lane that is down for a moment and
// comes back does not invalidate the consent.
func RecordedConsentScope(modelName string) ConsentScope {
	resolved, groups := recordedConsentGroups(modelName)
	return consentScope(groupDataPolicyState(), resolved, groups)
}

// recordedConsentGroups is the resolved name and every group listing it,
// enabled or not.
func recordedConsentGroups(modelName string) (string, []string) {
	resolved, cached := consentModelGroups(modelName)
	// The enabled groups come from the shared pricing map.
	groups := slices.Clone(cached)
	listed, err := GroupsListingModel(resolved)
	if err != nil {
		common.SysError("failed to list groups of model " + resolved + ": " + err.Error())
	}
	for _, g := range listed {
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	return resolved, groups
}

// consentModelGroups resolves a requested name the way the distributor routes
// it and returns that model's enabled groups, or the groups listing it when it
// is offline or hidden from the catalog.
func consentModelGroups(modelName string) (string, []string) {
	if modelName == "" {
		return modelName, nil
	}
	if groups := GetModelEnableGroups(modelName); len(groups) > 0 {
		return modelName, groups
	}
	if normalized := ratio_setting.RoutingMatchModelName(modelName); normalized != modelName {
		if groups := GetModelEnableGroups(normalized); len(groups) > 0 {
			return normalized, groups
		}
	}
	// The in-memory channel list answers for unknown names without the abilities scan.
	if !common.MemoryCacheEnabled || ModelHasAnyChannel(modelName) {
		if groups, err := GroupsListingModel(modelName); err == nil && len(groups) > 0 {
			return modelName, groups
		}
	}
	resolved, ok := ResolveModelNameVariant(modelName)
	if !ok {
		return modelName, nil
	}
	if groups := GetModelEnableGroups(resolved); len(groups) > 0 {
		return resolved, groups
	}
	groups, _ := GroupsListingModel(resolved)
	return resolved, groups
}

var promptsRisk = map[string]int{
	kitdto.DataPolicyPromptsNone:    0,
	kitdto.DataPolicyUnknown:        1,
	kitdto.DataPolicyPromptsRetains: 2,
	kitdto.DataPolicyPromptsTrains:  3,
}

var transferRisk = map[string]int{
	kitdto.DataPolicyTransferAdequacy: 0,
	kitdto.DataPolicyTransferDPF:      1,
	kitdto.DataPolicyTransferSCC:      2,
	kitdto.DataPolicyUnknown:          3,
	kitdto.DataPolicyTransferNone:     4,
}

func seatRisk(country string) int {
	switch {
	case country == "":
		return 2
	case kitdto.IsEEACountry(country):
		return 0
	case kitdto.IsEUAdequacyCountry(country):
		return 1
	default:
		return 3
	}
}

func orUnknown(value string) string {
	if value == "" {
		return kitdto.DataPolicyUnknown
	}
	return value
}

// ChannelUsesInputsForOwnPurposes reports whether the channel's provider trains
// on or retains prompts or has no recorded practice.
func ChannelUsesInputsForOwnPurposes(ch *Channel) bool {
	if ch == nil || ch.Setting == nil || *ch.Setting == "" {
		return true
	}
	var setting kitdto.ChannelSettings
	if err := common.UnmarshalJsonStr(*ch.Setting, &setting); err != nil {
		return true
	}
	return setting.DataPolicy.UsesInputsForOwnPurposes()
}
