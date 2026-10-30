package model

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProviderAlias is the public code name of a provider, its country plus a
// sequence per country (CN-07). Provider is internal and never leaves the
// gateway. Rows are never deleted, so an alias is never reused.
type ProviderAlias struct {
	Id        int    `json:"-" gorm:"primaryKey"`
	Provider  string `json:"-" gorm:"type:varchar(255);uniqueIndex:uk_provider_alias_provider;not null"`
	Alias     string `json:"alias" gorm:"type:varchar(16);uniqueIndex:uk_provider_alias_alias;not null"`
	Country   string `json:"country" gorm:"type:varchar(8);index:idx_provider_alias_country;not null;default:''"`
	Seq       int    `json:"-" gorm:"not null;default:0"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
}

// ProviderConsent documents a consent to one provider under Art. 4 No. 11 GDPR.
// Text is the dialog exactly as shown, because the consent is only provable
// together with what the person agreed to. Exactly one of UserId and GuestId is
// set; a guest is a browser identified by the chat BFF's guest cookie.
type ProviderConsent struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	UserId    int    `json:"user_id" gorm:"index:idx_provider_consent_user;not null;default:0"`
	GuestId   string `json:"-" gorm:"type:varchar(64);index:idx_provider_consent_guest;not null;default:''"`
	Alias     string `json:"alias" gorm:"type:varchar(16);not null"`
	Country   string `json:"country" gorm:"type:varchar(8);not null;default:''"`
	Locale    string `json:"locale" gorm:"type:varchar(16);not null;default:''"`
	Text      string `json:"-" gorm:"type:text;not null"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
	RevokedAt int64  `json:"revoked_at" gorm:"bigint;not null;default:0"`
}

// unknownAliasCountry prefixes the alias of a provider whose seat is not recorded.
const unknownAliasCountry = "XX"

type providerAliasIndex struct {
	byProvider map[string]ProviderAlias
	byAlias    map[string]ProviderAlias
}

var (
	providerAliasCache    atomic.Pointer[providerAliasIndex]
	providerAliasLoadedAt atomic.Int64
	providerAliasMu       sync.Mutex
)

func loadProviderAliases() (*providerAliasIndex, error) {
	var rows []ProviderAlias
	if err := DB.Find(&rows).Error; err != nil {
		return nil, err
	}
	index := &providerAliasIndex{byProvider: make(map[string]ProviderAlias, len(rows)), byAlias: make(map[string]ProviderAlias, len(rows))}
	for _, row := range rows {
		index.byProvider[row.Provider] = row
		index.byAlias[row.Alias] = row
	}
	providerAliasCache.Store(index)
	providerAliasLoadedAt.Store(time.Now().Unix())
	return index, nil
}

// providerAliases is the cached index; a miss reloads it at most every 10s,
// since another replica may have assigned the alias.
func providerAliases(missing ...string) *providerAliasIndex {
	index := providerAliasCache.Load()
	stale := index == nil
	if !stale {
		for _, p := range missing {
			if _, ok := index.byProvider[p]; !ok {
				stale = time.Now().Unix()-providerAliasLoadedAt.Load() >= 10
				break
			}
		}
	}
	if !stale {
		return index
	}
	loaded, err := loadProviderAliases()
	if err != nil {
		common.SysError("failed to load provider aliases: " + err.Error())
		if index == nil {
			return &providerAliasIndex{byProvider: map[string]ProviderAlias{}, byAlias: map[string]ProviderAlias{}}
		}
		return index
	}
	return loaded
}

// AssignProviderAliases gives every consent-requiring provider without an alias
// the next free one of its country. Safe on several replicas: a lost race on
// the unique alias is retried with the next sequence.
func AssignProviderAliases(providers map[string]string) error {
	pendingIn := func(index *providerAliasIndex) []string {
		var pending []string
		for p := range providers {
			if _, ok := index.byProvider[p]; !ok {
				pending = append(pending, p)
			}
		}
		return pending
	}
	pending := pendingIn(providerAliases())
	if len(pending) > 0 {
		index, err := loadProviderAliases()
		if err != nil {
			return err
		}
		pending = pendingIn(index)
	}
	if len(pending) == 0 {
		return nil
	}
	providerAliasMu.Lock()
	defer providerAliasMu.Unlock()
	slices.Sort(pending)
	for _, p := range pending {
		country := strings.ToUpper(strings.TrimSpace(providers[p]))
		if len(country) != 2 {
			country = unknownAliasCountry
		}
		for attempt := 0; ; attempt++ {
			var existing int64
			if err := DB.Model(&ProviderAlias{}).Where("provider = ?", p).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				break
			}
			var maxSeq int
			if err := DB.Model(&ProviderAlias{}).Where("country = ?", country).Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
				return err
			}
			row := ProviderAlias{Provider: p, Alias: fmt.Sprintf("%s-%02d", country, maxSeq+1), Country: country, Seq: maxSeq + 1, CreatedAt: common.GetTimestamp()}
			res := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 || attempt >= 5 {
				break
			}
		}
	}
	_, err := loadProviderAliases()
	return err
}

// ConsentProvider is one provider that some model needs a consent for.
// Provider is internal and must never reach an API response.
type ConsentProvider struct {
	Provider string
	Alias    string
	Country  string
	Unsafe   bool
	// Unsafe only because inputs pass on to unvetted recipients (transfer
	// none) while the seat itself is in the EEA or an adequacy country.
	OnwardUnvetted  bool
	TrainsOrRetains bool
	// trains, retains or unknown, the riskiest over its channels; none when
	// no channel may use inputs for its own purposes.
	Practice string
	Models   []string
	// Lane tags of the provider's channels, as they appear in group names.
	Tags []string
	// Label is the visible name built from Tags, the alias when there are none.
	Label              string
	LaneCount          int
	ActiveLaneCount    int
	ChannelCount       int
	ActiveChannelCount int
	// Channels and enabled channels of this provider per model it lists.
	ModelChannels       map[string]int
	ActiveModelChannels map[string]int
	Active              bool
}

type consentProviderCatalog struct {
	builtAt   time.Time
	providers []ConsentProvider
	// Lanes and facts of every provider with a channel, consent or not.
	all map[string]*ConsentProvider
}

var (
	consentProviderCache atomic.Pointer[consentProviderCatalog]
	consentProviderMu    sync.Mutex
)

const consentProviderTTL = 60 * time.Second

// ConsentProviders lists every provider a model in the catalog needs a consent
// for, over enabled and disabled lanes, with aliases assigned. Shared and cached.
func ConsentProviders() []ConsentProvider {
	if cached := consentProviderCache.Load(); cached != nil && time.Since(cached.builtAt) < consentProviderTTL {
		return cached.providers
	}
	consentProviderMu.Lock()
	defer consentProviderMu.Unlock()
	if cached := consentProviderCache.Load(); cached != nil && time.Since(cached.builtAt) < consentProviderTTL {
		return cached.providers
	}
	state := groupDataPolicyState()
	pricing := GetPricingWithOffline()
	names := make([]string, 0, len(pricing))
	for _, p := range pricing {
		names = append(names, p.ModelName)
	}
	// Disabled lanes count too, as in RecordedConsentScope: a consent given now
	// must still cover them when the retest brings them back.
	listed := ListedModelGroups(names)
	all := consentProviderLanes()
	byProvider := map[string]*ConsentProvider{}
	for _, p := range pricing {
		groups := slices.Clone(p.EnableGroup)
		for _, g := range listed[p.ModelName] {
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
		scope := consentScope(state, p.ModelName, groups)
		if !scope.Required {
			continue
		}
		for _, provider := range scope.Providers {
			entry := byProvider[provider]
			if entry == nil {
				entry = &ConsentProvider{Provider: provider, Tags: []string{}}
				if lanes := all[provider]; lanes != nil {
					copied := *lanes
					entry = &copied
				}
				byProvider[provider] = entry
			}
			entry.Models = append(entry.Models, p.ModelName)
		}
	}
	countries := make(map[string]string, len(byProvider))
	for p, entry := range byProvider {
		countries[p] = entry.Country
	}
	if err := AssignProviderAliases(countries); err != nil {
		common.SysError("failed to assign provider aliases: " + err.Error())
	}
	index := providerAliases()
	out := make([]ConsentProvider, 0, len(byProvider))
	for p, entry := range byProvider {
		alias, ok := index.byProvider[p]
		if !ok {
			continue
		}
		entry.Alias = alias.Alias
		entry.Label = ProviderLabel(entry.Tags, entry.Alias)
		if entry.Country == "" && alias.Country != unknownAliasCountry {
			entry.Country = alias.Country
		}
		slices.Sort(entry.Models)
		out = append(out, *entry)
	}
	// Two providers whose tags reduce to the same label are told apart by country.
	labelCount := map[string]int{}
	for _, p := range out {
		labelCount[p.Label]++
	}
	for i := range out {
		if labelCount[out[i].Label] > 1 && out[i].Country != "" {
			out[i].Label += " (" + out[i].Country + ")"
		}
	}
	slices.SortFunc(out, func(a, b ConsentProvider) int { return strings.Compare(a.Alias, b.Alias) })
	consentProviderCache.Store(&consentProviderCatalog{builtAt: time.Now(), providers: out, all: all})
	return out
}

// consentProviderLanes collects every provider with a channel: its facts from
// the channel policies, and its channels and lanes (channel tags), overall,
// per model and enabled.
func consentProviderLanes() map[string]*ConsentProvider {
	var channels []*Channel
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		for _, channel := range channelsIDM {
			channels = append(channels, channel)
		}
		channelSyncLock.RUnlock()
	} else if err := DB.Select("id", "tag", "status", "models", "setting").Find(&channels).Error; err != nil {
		common.SysError("failed to load channels for consent providers: " + err.Error())
		return map[string]*ConsentProvider{}
	}
	all := map[string]*ConsentProvider{}
	lanes := map[string]map[string]bool{}
	for _, channel := range channels {
		var setting kitdto.ChannelSettings
		if channel.Setting != nil && *channel.Setting != "" {
			_ = common.UnmarshalJsonStr(*channel.Setting, &setting)
		}
		policy := setting.DataPolicy
		provider := ""
		if policy != nil {
			provider = policy.Provider
		}
		if provider == "" {
			provider = "channel:" + strconv.Itoa(channel.Id)
		}
		entry := all[provider]
		if entry == nil {
			entry = &ConsentProvider{Provider: provider, ModelChannels: map[string]int{}, ActiveModelChannels: map[string]int{}}
			all[provider] = entry
		}
		if practice := policy.Practice(); entry.Practice == "" || promptsRisk[practice] > promptsRisk[entry.Practice] {
			entry.Practice = practice
		}
		entry.TrainsOrRetains = entry.TrainsOrRetains || policy.UsesInputsForOwnPurposes()
		if policy != nil {
			unsafe := policy.UnsafeThirdCountry()
			entry.Unsafe = entry.Unsafe || unsafe
			entry.OnwardUnvetted = entry.OnwardUnvetted || (unsafe && (kitdto.IsEEACountry(policy.SeatCountry) || kitdto.IsEUAdequacyCountry(policy.SeatCountry)))
			if entry.Country == "" {
				entry.Country = policy.SeatCountry
			}
		}
		enabled := channel.Status == common.ChannelStatusEnabled
		entry.ChannelCount++
		for _, m := range channel.GetModels() {
			m = strings.TrimSpace(m)
			entry.ModelChannels[m]++
			if enabled {
				entry.ActiveModelChannels[m]++
			}
		}
		lane := "#" + strconv.Itoa(channel.Id)
		if channel.Tag != nil && strings.TrimSpace(*channel.Tag) != "" {
			lane = strings.TrimSpace(*channel.Tag)
		}
		if lanes[provider] == nil {
			lanes[provider] = map[string]bool{}
		}
		lanes[provider][lane] = lanes[provider][lane] || enabled
		if enabled {
			entry.Active = true
			entry.ActiveChannelCount++
		}
	}
	for provider, entry := range all {
		entry.Tags = []string{}
		for lane, active := range lanes[provider] {
			entry.LaneCount++
			if active {
				entry.ActiveLaneCount++
			}
			if !strings.HasPrefix(lane, "#") {
				entry.Tags = append(entry.Tags, lane)
			}
		}
		slices.Sort(entry.Tags)
	}
	return all
}

// OtherProvider is a provider of a model that needs no consent, with why.
type OtherProvider struct {
	Label          string
	Country        string
	Unsafe         bool
	OnwardUnvetted bool
	Active         bool
	Practice       string
	// safe_country: may use inputs for its own purposes (practice unknown
	// included) from a safe country; no_own_purpose: records prompts none.
	Reason string
}

// ModelOtherProviders lists the providers of the model's recorded groups that
// are not in consentProviders, each once, by label.
func ModelOtherProviders(modelName string, consentProviders []string) []OtherProvider {
	ConsentProviders()
	catalog := consentProviderCache.Load()
	resolved, groups := recordedConsentGroups(modelName)
	state := groupDataPolicyState()
	reasons := map[string]string{}
	var order []string
	note := func(recipients []dataRecipient, reason string) {
		for _, r := range recipients {
			if slices.Contains(consentProviders, r.provider) {
				continue
			}
			if _, seen := reasons[r.provider]; !seen {
				order = append(order, r.provider)
				reasons[r.provider] = reason
			}
		}
	}
	for _, group := range expandGroups(state, groups) {
		recipients := state.recipients[group]
		note(recipients.ownPurpose, "safe_country")
		note(recipients.other, "no_own_purpose")
	}
	out := make([]OtherProvider, 0, len(order))
	for _, provider := range order {
		item := OtherProvider{Reason: reasons[provider]}
		if catalog != nil {
			if lanes := catalog.all[provider]; lanes != nil {
				item.Label = ProviderLabel(lanes.Tags, "")
				item.Country = lanes.Country
				item.Unsafe = lanes.Unsafe
				item.OnwardUnvetted = lanes.OnwardUnvetted
				item.Practice = lanes.Practice
				item.Active = lanes.ActiveModelChannels[resolved] > 0
			}
		}
		out = append(out, item)
	}
	slices.SortFunc(out, func(a, b OtherProvider) int { return strings.Compare(a.Label, b.Label) })
	return out
}

// ProviderLabel names a provider by its lane tag prefixes without their
// numbers: ab1..ab8 is "ab", cd1 with ef1..ef3 is "cd, ef". A
// prefix shorter than two characters keeps the whole tag, so a7 stays "a7".
func ProviderLabel(tags []string, alias string) string {
	if len(tags) == 0 {
		return alias
	}
	var names []string
	for _, tag := range tags {
		name := strings.TrimRight(tag, "0123456789")
		if len(name) < 2 {
			name = tag
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), ", ")
}

// ProviderAliasesFor maps internal providers to their aliases; a provider
// without one yet is left out.
func ProviderAliasesFor(providers []string) map[string]ProviderAlias {
	index := providerAliases(providers...)
	out := make(map[string]ProviderAlias, len(providers))
	for _, p := range providers {
		if alias, ok := index.byProvider[p]; ok {
			out[p] = alias
		}
	}
	return out
}

// ProviderAliasByName resolves a public alias.
func ProviderAliasByName(alias string) (ProviderAlias, bool) {
	index := providerAliases()
	row, ok := index.byAlias[alias]
	if !ok && time.Now().Unix()-providerAliasLoadedAt.Load() >= 10 {
		if loaded, err := loadProviderAliases(); err == nil {
			row, ok = loaded.byAlias[alias]
		}
	}
	return row, ok
}

// ActiveProviderConsents lists the subject's unrevoked provider consents.
func ActiveProviderConsents(userId int, guestId string) ([]ProviderConsent, error) {
	where, arg := consentWhere(userId, guestId)
	var consents []ProviderConsent
	err := DB.Where(where, arg).Order("created_at desc").Find(&consents).Error
	return consents, err
}

// GrantProviderConsents records one consent per alias in one transaction, each
// with the shown text; an earlier active consent for the same alias is revoked.
func GrantProviderConsents(userId int, guestId string, aliases []ProviderAlias, locale, text string) error {
	if (userId == 0) == (guestId == "") {
		return errors.New("provider consent needs exactly one of user and guest")
	}
	if len(aliases) == 0 {
		return errors.New("no provider to consent to")
	}
	now := common.GetTimestamp()
	where, arg := consentWhere(userId, guestId)
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, alias := range aliases {
			if err := tx.Model(&ProviderConsent{}).Where(where, arg).Where("alias = ?", alias.Alias).Update("revoked_at", now).Error; err != nil {
				return err
			}
			if err := tx.Create(&ProviderConsent{UserId: userId, GuestId: guestId, Alias: alias.Alias, Country: alias.Country, Locale: locale, Text: text, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	consentCache.Delete(consentSubject(userId, guestId))
	return err
}

// RevokeProviderConsent ends the consent for one alias; the record stays as proof.
func RevokeProviderConsent(userId int, guestId string, alias string) error {
	where, arg := consentWhere(userId, guestId)
	err := DB.Model(&ProviderConsent{}).Where(where, arg).Where("alias = ?", alias).Update("revoked_at", common.GetTimestamp()).Error
	consentCache.Delete(consentSubject(userId, guestId))
	return err
}

// PruneProviderConsents applies the proof period of the archived model consents.
func PruneProviderConsents(now time.Time) (int64, error) {
	cutoff := now.AddDate(-consentProofYears, 0, 0)
	res := DB.Where(
		"(revoked_at > 0 AND revoked_at < ?)"+
			" OR (revoked_at = 0 AND guest_id <> '' AND created_at < ?)"+
			" OR (revoked_at = 0 AND user_id > 0 AND user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?))"+
			" OR (revoked_at = 0 AND user_id > 0 AND created_at < ? AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = provider_consents.user_id))",
		cutoff.Unix(), cutoff.Add(-guestIdLifetime).Unix(), cutoff, cutoff.Unix(),
	).Delete(&ProviderConsent{})
	return res.RowsAffected, res.Error
}

// MissingConsentProvider is a provider the subject still needs to consent to,
// by its public names.
type MissingConsentProvider struct {
	Alias   string
	Label   string
	Country string
}

// MissingConsentProviders lists the providers of a model that no active
// provider consent covers, in the given order. A provider without an alias is
// left out: it cannot be consented to until the sync assigns one.
func MissingConsentProviders(userId int, guestId string, providers []string) ([]MissingConsentProvider, error) {
	entry, err := consentEntryFor(userId, guestId, providers)
	if err != nil {
		return nil, err
	}
	aliases := ProviderAliasesFor(providers)
	labels := map[string]string{}
	for _, p := range ConsentProviders() {
		labels[p.Alias] = p.Label
	}
	var out []MissingConsentProvider
	for _, p := range providers {
		alias, ok := aliases[p]
		if !ok || entry.aliases[alias.Alias] {
			continue
		}
		label := labels[alias.Alias]
		if label == "" {
			label = alias.Alias
		}
		country := alias.Country
		if country == unknownAliasCountry {
			country = ""
		}
		out = append(out, MissingConsentProvider{Alias: alias.Alias, Label: label, Country: country})
	}
	return out, nil
}
