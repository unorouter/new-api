package controller

import (
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Matches the JS localeCompare the clients used before this list was sorted
// server-side. Byte order is NOT equivalent: it puts "glm-4.1v-..." before
// "glm-4:free" because ':' > '.'. Built per request, never shared: Collator
// carries mutable iterator state and races under concurrent use.
func newCatalogCollator() *collate.Collator {
	return collate.New(language.Und)
}

// catalogTags splits the varchar(255) CSV the sync writes. Trimmed and
// empties dropped so a trailing comma does not yield a blank tag.
func catalogTags(raw string) []string {
	out := make([]string, 0, 4)
	for _, tag := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(tag); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// The blob is written by the sync and opaque to the gateway, so a model with
// unparseable metadata still belongs in the catalog: it just carries no hints.
func parseCatalogMetadata(raw string) dto.ModelMetadata {
	var md dto.ModelMetadata
	if raw == "" {
		return md
	}
	_ = common.UnmarshalJsonStr(raw, &md)
	return md
}

var nonChatEndpoints = map[string]bool{
	"embedding": true, "rerank": true, "moderation": true,
}

// Cards clamp the blurb to two lines, so the rest of a 1000-character
// description is bytes nobody reads. The detail routes serve the full text.
const catalogDescriptionChars = 200

func truncateDescription(s string) string {
	if len(s) <= catalogDescriptionChars {
		return s
	}
	cut := s[:catalogDescriptionChars]
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return cut + "..."
}

// A list caller filters and sorts; it does not send a request. Parameter lists
// and provider defaults only matter once a specific model is chosen, and those
// callers fetch that model on its own.
func listMetadata(md dto.ModelMetadata) dto.ModelMetadata {
	md.SupportedParameters = nil
	md.DefaultParameters = nil
	return md
}

// A model bills per call (a flat ModelPrice) rather than per token for these
// quota types.
func isFixedPriceQuota(quotaType int) bool {
	return quotaType == 1 || quotaType == 3 || quotaType == 4
}

// The cheapest ratio among the groups this model is actually served by. Display
// prices quote the best rate a caller could get, so the discount shown matches
// what they would pay on the cheapest lane.
func minGroupRatio(enableGroups []string, groupRatio map[string]float64) float64 {
	min := math.Inf(1)
	for _, g := range enableGroups {
		if r, ok := groupRatio[g]; ok && r < min {
			min = r
		}
	}
	if math.IsInf(min, 1) {
		return 1
	}
	return min
}

// quotaToUSD converts a stored ratio to dollars per million tokens.
const quotaToUSD = 2

type catalogPrices struct {
	input, output, fixed             float64
	origInput, origOutput, origFixed *float64
}

// Prices are the sticker value times the cheapest group ratio. Original prices
// are populated only when a discount actually applies, so a null means "no
// strikethrough" rather than "free".
func catalogPricing(m model.Pricing, groupRatio map[string]float64, showOriginal bool) catalogPrices {
	var p catalogPrices
	ratio := minGroupRatio(m.EnableGroup, groupRatio)
	discounted := showOriginal && ratio < 1

	if isFixedPriceQuota(m.QuotaType) {
		p.fixed = m.ModelPrice * ratio
		if discounted && m.ModelPrice > 0 {
			sticker := m.ModelPrice
			p.origFixed = &sticker
		}
	} else {
		p.input = m.ModelRatio * quotaToUSD * ratio
		p.output = p.input * m.CompletionRatio
		if discounted {
			in := m.ModelRatio * quotaToUSD
			out := in * m.CompletionRatio
			p.origInput = &in
			p.origOutput = &out
		}
	}

	// Grid pricing overrides the flat rate: the cheapest tier is what a caller
	// sees quoted.
	if len(m.GridPricing) > 0 {
		minTier := math.Inf(1)
		for _, row := range m.GridPricing {
			price, ok := row["Pricing"].(float64)
			if ok && price > 0 && price < minTier {
				minTier = price
			}
		}
		if !math.IsInf(minTier, 1) {
			p.fixed = minTier * ratio
			p.origFixed = nil
			if discounted {
				tier := minTier
				p.origFixed = &tier
			}
		}
	}
	return p
}

// What a model emits is a stated fact, so type comes from outputModalities
// rather than a name or tag heuristic. The endpoint check stays as the
// authority on chat-eligibility: a model routed to /embeddings cannot serve a
// chat completion no matter what modality it claims.
func catalogModality(m model.Pricing, md dto.ModelMetadata) (modelType string, chat bool) {
	for _, want := range []string{"image", "video", "audio", "embedding"} {
		for _, got := range md.OutputModalities {
			if got == want {
				return want, false
			}
		}
	}
	for _, ep := range m.SupportedEndpointTypes {
		if nonChatEndpoints[string(ep)] {
			return "text", false
		}
	}
	return "text", len(md.OutputModalities) > 0
}

// GetPricingCatalog returns the model-picker list: enough to list, group, search
// and badge every model, without the group maps and ratio fields that make
// /pricing an order of magnitude larger. Pre-sorted (free first, then by name)
// so callers do not each re-derive the same ordering.
// Every catalog surface must see the SAME models: what a caller may route is a
// property of their group, so the filter belongs with the fetch rather than at
// each call site. Group ratios come back too, since prices need them.
func visiblePricing(c fuego.ContextNoBody) ([]model.Pricing, map[string]float64) {
	groupRatio := ratio_setting.GetGroupRatioCopy()
	var group string
	if userId, exists := dto.GinCtx(c).Get("id"); exists {
		if user, err := model.GetUserCache(userId.(int)); err == nil {
			group = user.Group
			for g := range groupRatio {
				if ratio, ok := ratio_setting.GetGroupGroupRatio(group, g); ok {
					groupRatio[g] = ratio
				}
			}
		}
	}
	usableGroup := service.GetUserUsableGroups(group)
	return filterPricingByUsableGroups(model.GetPricing(), usableGroup), groupRatio
}

// A model's vendor name, defaulted: a row with no resolvable vendor still has to
// group somewhere.
func catalogVendorName(vendorByID map[int]model.PricingVendor, vendorID int) string {
	if name := vendorByID[vendorID].Name; name != "" {
		return name
	}
	return "Unknown"
}

func vendorsByID() map[int]model.PricingVendor {
	out := make(map[int]model.PricingVendor)
	for _, v := range model.GetVendors() {
		out[v.ID] = v
	}
	return out
}

func GetPricingCatalog(c fuego.ContextNoBody) (dto.PricingCatalogData, error) {
	pricing, groupRatio := visiblePricing(c)
	vendorByID := vendorsByID()

	// The picker needs a name, a badge and a price; the browse page also filters
	// on metadata and renders a blurb. Off by default so the chat path is not
	// paying for the browse page's fields.
	full := dto.GinCtx(c).Query("full") == "true"
	// A vendor page shows one vendor's models, so it filters here rather than
	// downloading all 341 rows to keep 12. Implies `full`: the cards render a
	// blurb and capability chips.
	vendorFilter := dto.GinCtx(c).Query("vendor")
	if vendorFilter != "" {
		full = true
	}

	showOriginal := operation_setting.ShowOriginalPriceEnabled
	out := make([]dto.PricingCatalogModel, 0, len(pricing))
	for _, m := range pricing {
		md := parseCatalogMetadata(m.Metadata)
		modelType, chat := catalogModality(m, md)
		vendorName := catalogVendorName(vendorByID, m.VendorID)
		if vendorFilter != "" && vendorName != vendorFilter {
			continue
		}
		price := catalogPricing(m, groupRatio, showOriginal)
		row := dto.PricingCatalogModel{
			ModelName:           m.ModelName,
			Vendor:              vendorName,
			VendorID:            m.VendorID,
			Icon:                vendorByID[m.VendorID].Icon,
			Type:                modelType,
			Tags:                catalogTags(m.Tags),
			ReleaseTs:           md.ReleaseTs,
			IsFree:              modelIsFree(m, groupRatio),
			Online:              m.Online,
			Chat:                chat,
			InputPrice:          price.input,
			OutputPrice:         price.output,
			FixedPrice:          price.fixed,
			IsFixedPrice:        isFixedPriceQuota(m.QuotaType),
			OriginalInputPrice:  price.origInput,
			OriginalOutputPrice: price.origOutput,
			OriginalFixedPrice:  price.origFixed,
		}
		if full {
			row.Description = truncateDescription(m.Description)
			row.Metadata = listMetadata(md)
		}
		out = append(out, row)
	}

	collator := newCatalogCollator()
	sort.SliceStable(out, func(i, j int) bool {
		// One vendor's page is a release timeline, so it reads newest first. The
		// name tiebreak is load-bearing either way: most models share a release
		// date with another, and date alone leaves those in slice order.
		if vendorFilter != "" {
			if out[i].ReleaseTs != out[j].ReleaseTs {
				return out[i].ReleaseTs > out[j].ReleaseTs
			}
			return collator.CompareString(out[i].ModelName, out[j].ModelName) < 0
		}
		if out[i].IsFree != out[j].IsFree {
			return out[i].IsFree
		}
		return collator.CompareString(out[i].ModelName, out[j].ModelName) < 0
	})

	data := dto.PricingCatalogData{
		Models:         out,
		Vendors:        toPricingVendors(model.GetVendors()),
		FirstFreeModel: newestFreeChatModel(out),
		Counts:         catalogCounts(out),
	}
	// Only the browse/detail surface prints endpoint routes; the picker would be
	// paying 716 bytes for something it never reads.
	if full {
		data.SupportedEndpoint = toEndpointInfoMap(model.GetSupportedEndpointMap())
	}
	return data, nil
}

// GetPricingVendors resolves a model name to its vendor and badge for the log
// table, the token group-mapping picker, the status page and the ticker. None of
// them price anything, so this carries no prices, metadata or description: a
// third of the catalog's size.
func GetPricingVendors(c fuego.ContextNoBody) (dto.PricingVendorsData, error) {
	pricing, groupRatio := visiblePricing(c)
	vendorByID := vendorsByID()

	out := make([]dto.PricingVendorModel, 0, len(pricing))
	seen := make(map[string]struct{}, len(pricing))
	names := make([]string, 0, len(vendorByID))
	for _, m := range pricing {
		md := parseCatalogMetadata(m.Metadata)
		_, chat := catalogModality(m, md)
		vendorName := catalogVendorName(vendorByID, m.VendorID)
		if _, ok := seen[vendorName]; !ok {
			seen[vendorName] = struct{}{}
			names = append(names, vendorName)
		}
		tag := "Other"
		if tags := catalogTags(m.Tags); len(tags) > 0 {
			tag = tags[0]
		}
		out = append(out, dto.PricingVendorModel{
			ModelName: m.ModelName,
			Vendor:    vendorName,
			Chat:      chat,
			IsFree:    modelIsFree(m, groupRatio),
			Tag:       tag,
			ReleaseTs: md.ReleaseTs,
		})
	}

	collator := newCatalogCollator()
	sort.SliceStable(names, func(i, j int) bool {
		return collator.CompareString(names[i], names[j]) < 0
	})

	return dto.PricingVendorsData{VendorNames: names, ModelVendors: out}, nil
}

// GetPricingModelGroups returns the group panel for ONE model. Everything is
// scoped to that model here rather than in the client, which previously had to
// download the 56KB global auto-group list and the 1800-key ratio map to render
// a handful of chips.
func GetPricingModelGroups(c fuego.ContextNoBody) (dto.PricingModelGroupsData, error) {
	modelName := dto.GinCtx(c).Query("model")
	pricing, ok := model.GetPricingByModelName(modelName)
	if !ok {
		return dto.PricingModelGroupsData{}, fuego.NotFoundError{Title: "model not found"}
	}

	all := ratio_setting.GetGroupRatioCopy()
	groupRatio := make(map[string]float64, len(pricing.EnableGroup))
	if common.StringsContains(pricing.EnableGroup, "all") {
		groupRatio = all
	} else {
		for _, g := range pricing.EnableGroup {
			if f, ok := all[g]; ok {
				groupRatio[g] = f
			}
		}
	}

	var userGroup string
	if userId, exists := dto.GinCtx(c).Get("id"); exists {
		if user, err := model.GetUserCache(userId.(int)); err == nil {
			userGroup = user.Group
			for g := range groupRatio {
				if ratio, ok := ratio_setting.GetGroupGroupRatio(userGroup, g); ok {
					groupRatio[g] = ratio
				}
			}
		}
	}

	enabled := make(map[string]struct{}, len(pricing.EnableGroup))
	for _, g := range pricing.EnableGroup {
		enabled[g] = struct{}{}
	}
	servesAll := common.StringsContains(pricing.EnableGroup, "all")
	chain := make([]string, 0, 4)
	for _, g := range service.GetUserAutoGroup(userGroup) {
		if _, ok := enabled[g]; ok || servesAll {
			chain = append(chain, g)
		}
	}
	// Cheapest first: the chain is what a caller falls through, so the order is
	// the routing order, not the config order.
	sort.SliceStable(chain, func(i, j int) bool {
		ri, iok := groupRatio[chain[i]]
		rj, jok := groupRatio[chain[j]]
		if !iok {
			return false
		}
		if !jok {
			return true
		}
		return ri < rj
	})

	return dto.PricingModelGroupsData{
		EnableGroups: pricing.EnableGroup,
		GroupRatio:   groupRatio,
		AutoChain:    chain,
	}, nil
}

// Vendors counts only vendors that actually serve a model, not every configured
// one: it is quoted as "N+ providers", and an empty vendor is not a provider.
func catalogCounts(models []dto.PricingCatalogModel) dto.PricingCatalogCounts {
	counts := dto.PricingCatalogCounts{Models: len(models)}
	vendors := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m.IsFree {
			counts.Free++
		}
		vendors[m.Vendor] = struct{}{}
	}
	counts.Paid = counts.Models - counts.Free
	counts.Vendors = len(vendors)
	return counts
}

// Newest first, name as tiebreak: most models share a release date with another,
// so date alone leaves the winner up to slice order. Falls back to any free
// model when no free chat model is routable.
func newestFreeChatModel(models []dto.PricingCatalogModel) string {
	newest := func(match func(dto.PricingCatalogModel) bool) string {
		best := ""
		var bestTs int64
		for _, m := range models {
			if !match(m) {
				continue
			}
			if best == "" || m.ReleaseTs > bestTs ||
				(m.ReleaseTs == bestTs && m.ModelName < best) {
				best, bestTs = m.ModelName, m.ReleaseTs
			}
		}
		return best
	}
	if pick := newest(func(m dto.PricingCatalogModel) bool {
		return m.IsFree && m.Chat && m.Online
	}); pick != "" {
		return pick
	}
	return newest(func(m dto.PricingCatalogModel) bool { return m.IsFree })
}
