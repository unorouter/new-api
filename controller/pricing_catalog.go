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

type catalogMetadata struct {
	OutputModalities []string `json:"outputModalities"`
	ReleaseTs        int64    `json:"releaseTs"`
}

func parseCatalogMetadata(raw string) catalogMetadata {
	var md catalogMetadata
	if raw == "" {
		return md
	}
	_ = common.UnmarshalJsonStr(raw, &md)
	return md
}

var nonChatEndpoints = map[string]bool{
	"embedding": true, "rerank": true, "moderation": true,
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
func catalogModality(m model.Pricing, md catalogMetadata) (modelType string, chat bool) {
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
func GetPricingCatalog(c fuego.ContextNoBody) (dto.PricingCatalogData, error) {
	pricing := model.GetPricing()

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
	pricing = filterPricingByUsableGroups(pricing, usableGroup)

	vendorByID := make(map[int]string)
	for _, v := range model.GetVendors() {
		vendorByID[v.ID] = v.Name
	}

	showOriginal := operation_setting.ShowOriginalPriceEnabled
	out := make([]dto.PricingCatalogModel, 0, len(pricing))
	for _, m := range pricing {
		md := parseCatalogMetadata(m.Metadata)
		modelType, chat := catalogModality(m, md)
		vendor := vendorByID[m.VendorID]
		if vendor == "" {
			vendor = "Unknown"
		}
		price := catalogPricing(m, groupRatio, showOriginal)
		out = append(out, dto.PricingCatalogModel{
			ModelName:           m.ModelName,
			Vendor:              vendor,
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
		})
	}

	collator := newCatalogCollator()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsFree != out[j].IsFree {
			return out[i].IsFree
		}
		return collator.CompareString(out[i].ModelName, out[j].ModelName) < 0
	})

	return dto.PricingCatalogData{
		Success: true,
		Data:    out,
		Vendors: toPricingVendors(model.GetVendors()),
	}, nil
}
