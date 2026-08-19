package controller

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
	"github.com/samber/lo"
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

// Keys are EndpointType VALUES, so they must match the constants exactly:
// "rerank"/"moderation" were never endpoint types and matched nothing, which
// let a moderation classifier through as chat-eligible. "embedding" (singular)
// is not a constant either but is what the sync writes, so it stays.
var nonChatEndpoints = map[string]bool{
	string(constant.EndpointTypeEmbeddings):  true,
	string(constant.EndpointTypeJinaRerank):  true,
	string(constant.EndpointTypeModerations): true,
	"embedding":                              true,
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
	// 80KB across the catalog, mostly 75-entry sampler lists, and only an image
	// FORM reads it. Restored below when the caller asked for image models.
	md.ImageParams = nil
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

// The URL-safe form of a vendor name, matching the slug the model pages are
// linked by ("Z.AI" -> "zai"). Lossy, so it identifies a vendor but never
// reconstructs the name.
func vendorSlug(name string) string {
	var b strings.Builder
	lastHyphen := true // leading hyphens are trimmed, so start as if one was written
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case unicode.IsSpace(r), r == '-':
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func servesAnyEndpoint(types []constant.EndpointType, want []string) bool {
	for _, t := range types {
		for _, w := range want {
			if string(t) == w {
				return true
			}
		}
	}
	return false
}

// Vendors are filtered by exact name or by slug, so a caller holding only a URL
// segment does not have to fetch every vendor name to translate it back first.
func vendorMatches(vendorName, filter string) bool {
	if filter == "" {
		return false
	}
	if vendorName == filter {
		return true
	}
	// A name with no slug-safe characters (a CJK-only vendor) slugs to "", which
	// must not match anything rather than matching every unslugabble vendor.
	slug := vendorSlug(vendorName)
	return slug != "" && slug == strings.ToLower(filter)
}

func vendorsByID() map[int]model.PricingVendor {
	out := make(map[int]model.PricingVendor)
	for _, v := range model.GetVendors() {
		out[v.ID] = v
	}
	return out
}

// One mapping for the list row, shared by the list and the per-model detail, so
// a caller reads the same field names whichever it fetched.
// Reliability rides the catalog because it is a per-model FACT the gateway
// already computes for the status and perf pages. Callers that want to sort or
// badge on it would otherwise fetch two more whole-catalog payloads and join
// them by name client-side.
type reliability struct {
	uptime  map[string]float64
	success map[string]float64
}

// Both inputs are aggregate queries, and the catalog is a hot path, so the
// result is cached rather than recomputed per request. The window matches what
// the status and perf pages already report (24h).
var (
	reliabilityMu    sync.Mutex
	reliabilityAt    time.Time
	reliabilityValue reliability
)

const reliabilityTTL = 5 * time.Minute

func catalogReliability() reliability {
	reliabilityMu.Lock()
	if !reliabilityAt.IsZero() && time.Since(reliabilityAt) < reliabilityTTL {
		v := reliabilityValue
		reliabilityMu.Unlock()
		return v
	}
	reliabilityMu.Unlock()

	out := reliability{
		uptime:  map[string]float64{},
		success: map[string]float64{},
	}
	// A reliability lookup must never fail the catalog: the prices are the
	// payload, these two are decoration, so an error leaves them absent (null)
	// rather than 500ing the model list.
	if comps, err := model.GetAllPublicModelStatusComponents(); err == nil {
		names := make([]string, 0, len(comps))
		for _, comp := range comps {
			names = append(names, comp.ModelName)
		}
		if u24, _, err := cachedUptimes(names); err == nil {
			out.uptime = u24
		}
	}
	activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	if summary, err := perfmetrics.QuerySummaryAll(24, activeGroups); err == nil {
		for _, row := range summary.Models {
			out.success[row.ModelName] = row.SuccessRate
		}
	}

	reliabilityMu.Lock()
	reliabilityAt = time.Now()
	reliabilityValue = out
	reliabilityMu.Unlock()
	return out
}

func catalogRow(
	m model.Pricing,
	md dto.ModelMetadata,
	vendorName string,
	icon string,
	modelType string,
	chat bool,
	groupRatio map[string]float64,
	showOriginal bool,
	rel reliability,
) dto.PricingCatalogModel {
	price := catalogPricing(m, groupRatio, showOriginal)
	uptime, hasUptime := rel.uptime[m.ModelName]
	success, hasSuccess := rel.success[m.ModelName]
	return dto.PricingCatalogModel{
		ModelName:              m.ModelName,
		Vendor:                 vendorName,
		VendorID:               m.VendorID,
		Icon:                   icon,
		Type:                   modelType,
		Tags:                   catalogTags(m.Tags),
		ReleaseTs:              md.ReleaseTs,
		IsFree:                 modelIsFree(m, groupRatio),
		Online:                 m.Online,
		Chat:                   chat,
		InputPrice:             price.input,
		OutputPrice:            price.output,
		FixedPrice:             price.fixed,
		IsFixedPrice:           isFixedPriceQuota(m.QuotaType),
		OriginalInputPrice:     price.origInput,
		OriginalOutputPrice:    price.origOutput,
		OriginalFixedPrice:     price.origFixed,
		SupportedEndpointTypes: m.SupportedEndpointTypes,
		Uptime24h:              optionalFloat(uptime, hasUptime),
		SuccessRate:            optionalFloat(success, hasSuccess),
	}
}

func optionalFloat(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// Image endpoints a synchronous generation can route to, in the order a model is
// tried. aihorde is deliberately absent: it is an async task adaptor, so a row
// serving only it can be listed but never submitted from a form.
var syncImageEndpoints = []constant.EndpointType{
	constant.EndpointTypeImageGeneration,
	constant.EndpointTypeOpenAI,
	constant.EndpointTypeGemini,
}

func syncImageEndpoint(types []constant.EndpointType) string {
	for _, want := range syncImageEndpoints {
		for _, got := range types {
			if got == want {
				return string(want)
			}
		}
	}
	return ""
}

// imageParamsFor completes the sync's schema-derived flags with what only the
// gateway knows (which endpoint routes the model) and the defaults a form starts
// from, so a client reads fields instead of re-deriving them per model.
func imageParamsFor(m model.Pricing, md dto.ModelMetadata) *dto.ImageParams {
	endpoint := syncImageEndpoint(m.SupportedEndpointTypes)
	if endpoint == "" {
		return nil
	}
	p := dto.ImageParams{}
	if md.ImageParams != nil {
		p = *md.ImageParams
	}
	p.Endpoint = endpoint
	p.SupportsSize = endpoint == string(constant.EndpointTypeImageGeneration)
	p.DefaultWidth = 1024
	p.DefaultHeight = 1024
	p.DefaultSampler = "Default"

	p.DefaultSteps = 20
	if md.ImageParams != nil && md.ImageParams.Steps != nil && md.ImageParams.Steps.Default != nil {
		p.DefaultSteps = int(*md.ImageParams.Steps.Default)
	}
	if md.ImageParams != nil && md.ImageParams.Cfg != nil {
		p.DefaultCfg = md.ImageParams.Cfg.Default
	}

	// An unresolved schema says nothing about references, so a generation endpoint
	// still allows the one image the relay accepts. A RESOLVED zero is authoritative:
	// an SDXL checkpoint takes none, and offering an uploader would only fail.
	if md.ImageParams == nil {
		p.MaxReferenceImages = md.MaxImageInputs
		if p.MaxReferenceImages == 0 && p.SupportsSize {
			p.MaxReferenceImages = 1
		}
	}
	p.SupportsReferences = p.MaxReferenceImages >= 1
	return &p
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
	// Comma-separated endpoint types, for a picker that can only submit to some of
	// them: the image UI routes through image-generation/openai/gemini, so an
	// aihorde-only row is a model it can list but never call.
	endpointFilter := splitCSV(dto.GinCtx(c).Query("endpoint"))
	// Modality, for a picker that only submits one kind of generation.
	typeFilter := dto.GinCtx(c).Query("type")
	// aihorde is an async TASK adaptor (submit then poll), not a sync generation
	// endpoint, so an aihorde-only row is one an image form can list but never
	// submit to. Which endpoints those are is a gateway fact, so asking for image
	// models implies it rather than making every caller restate the list.
	if typeFilter == "image" && len(endpointFilter) == 0 {
		endpointFilter = []string{
			string(constant.EndpointTypeImageGeneration),
			string(constant.EndpointTypeOpenAI),
			string(constant.EndpointTypeGemini),
		}
	}

	showOriginal := operation_setting.ShowOriginalPriceEnabled
	rel := catalogReliability()
	out := make([]dto.PricingCatalogModel, 0, len(pricing))
	for _, m := range pricing {
		md := parseCatalogMetadata(m.Metadata)
		modelType, chat := catalogModality(m, md)
		vendorName := catalogVendorName(vendorByID, m.VendorID)
		if vendorFilter != "" && !vendorMatches(vendorName, vendorFilter) {
			continue
		}
		if len(endpointFilter) > 0 && !servesAnyEndpoint(m.SupportedEndpointTypes, endpointFilter) {
			continue
		}
		if typeFilter != "" && modelType != typeFilter {
			continue
		}
		row := catalogRow(m, md, vendorName, vendorByID[m.VendorID].Icon, modelType, chat, groupRatio, showOriginal, rel)
		if full {
			row.Description = truncateDescription(m.Description)
			row.Metadata = listMetadata(md)
			// A caller that asked for image models is rendering a generation form,
			// which is the only surface these flags drive.
			if typeFilter == "image" {
				row.Metadata.ImageParams = imageParamsFor(m, md)
			}
		}
		out = append(out, row)
	}

	collator := newCatalogCollator()
	sort.SliceStable(out, func(i, j int) bool {
		// One vendor's page is a release timeline, so it reads newest first. A
		// picker scoped to an endpoint reads the same way. The name tiebreak is
		// load-bearing either way: most models share a release date with another,
		// and date alone leaves those in slice order.
		if vendorFilter != "" || len(endpointFilter) > 0 || typeFilter != "" {
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
// modelGroups resolves one model's servable groups, the ratios for just those
// groups, and the auto chain restricted to them. Shared by the groups route and
// the per-model detail so the two cannot disagree.
func modelGroups(c fuego.ContextNoBody, pricing model.Pricing) ([]string, map[string]float64, []string) {
	all := ratio_setting.GetGroupRatioCopy()
	groupRatio := make(map[string]float64, len(pricing.EnableGroup))
	servesAll := common.StringsContains(pricing.EnableGroup, "all")
	if servesAll {
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
	return pricing.EnableGroup, groupRatio, chain
}

// GetPricingModelGroups returns the group panel for ONE model. Everything is
// scoped to that model here rather than in the client, which previously had to
// download the 56KB global auto-group list and the 1800-key ratio map to render
// a handful of chips.
func GetPricingModelGroups(c fuego.ContextNoBody) (dto.PricingModelGroupsData, error) {
	// An unknown model is served by no groups, which these empty collections say
	// exactly. A 404 would carry the same information as an error the caller has
	// to catch and then translate back into this same empty shape.
	empty := dto.PricingModelGroupsData{
		EnableGroups: []string{},
		GroupRatio:   map[string]float64{},
		AutoChain:    []string{},
	}
	pricing, ok := model.GetPricingByModelName(dto.GinCtx(c).Query("model"))
	if !ok {
		return empty, nil
	}
	groups, groupRatio, chain := modelGroups(c, pricing)
	return dto.PricingModelGroupsData{
		EnableGroups: groups,
		GroupRatio:   groupRatio,
		AutoChain:    chain,
	}, nil
}

// GetPricingCatalogModel returns ONE model as a catalog row plus the fields that
// only matter once a model is chosen. Unlike the list this is reachable by name
// even when every channel is offline, so a detail page still renders for a model
// nothing can currently route.
func GetPricingCatalogModel(c fuego.ContextNoBody) (dto.PricingCatalogDetail, error) {
	pricing, ok := model.GetPricingByModelName(dto.GinCtx(c).Query("model"))
	if !ok {
		return dto.PricingCatalogDetail{}, fuego.NotFoundError{Title: "model not found"}
	}

	groups, groupRatio, chain := modelGroups(c, pricing)
	md := parseCatalogMetadata(pricing.Metadata)
	modelType, chat := catalogModality(pricing, md)
	vendorByID := vendorsByID()
	row := catalogRow(
		pricing, md,
		catalogVendorName(vendorByID, pricing.VendorID),
		vendorByID[pricing.VendorID].Icon,
		modelType, chat, groupRatio,
		operation_setting.ShowOriginalPriceEnabled,
		catalogReliability(),
	)

	// The list strips parameter lists and provider defaults and truncates the
	// blurb; a caller that asked for ONE model wants all of it.
	row.Metadata = md
	// The send paths resolve a model through THIS route and read the endpoint off
	// it, so the gateway-derived half has to be here too, not only on the list.
	row.Metadata.ImageParams = imageParamsFor(pricing, md)

	return dto.PricingCatalogDetail{
		PricingCatalogModel: row,
		EnableGroups:        groups,
		GroupRatio:          groupRatio,
		AutoChain:           chain,
		ModelRatio:          pricing.ModelRatio,
		CompletionRatio:     pricing.CompletionRatio,
		CacheRatio:          pricing.CacheRatio,
		CreateCacheRatio:    pricing.CreateCacheRatio,
		GridPricing:         pricing.GridPricing,
		GridMinRatio:        minGroupRatio(pricing.EnableGroup, groupRatio),
		IsTiered:            pricing.BillingMode == "tiered_expr" && pricing.BillingExpr != "",
		CreatedTime:         pricing.CreatedTime,
		BillingExpr:         pricing.BillingExpr,
		Description:         pricing.Description,
	}, nil
}

// Vendors counts only vendors that actually serve a model, not every configured
// one: it is quoted as "N+ providers", and an empty vendor is not a provider.
// GetPricingCounts is the homepage stat row: four numbers. Counted straight off
// the filtered pricing list, so it never builds the 341 rows a caller would
// otherwise download 137KB of to sum. Same group filter as the catalog, so the
// totals always describe the same set of models the list would return.
func GetPricingCounts(c fuego.ContextNoBody) (dto.PricingCatalogCounts, error) {
	pricing, groupRatio := visiblePricing(c)
	vendorByID := vendorsByID()

	counts := dto.PricingCatalogCounts{Models: len(pricing)}
	vendors := make(map[string]struct{}, len(pricing))
	for _, m := range pricing {
		if modelIsFree(m, groupRatio) {
			counts.Free++
		}
		vendors[catalogVendorName(vendorByID, m.VendorID)] = struct{}{}
	}
	counts.Paid = counts.Models - counts.Free
	counts.Vendors = len(vendors)
	return counts, nil
}

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
