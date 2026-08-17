package dto

import (
	"github.com/QuantumNous/new-api/constant"
)

// PricingModel mirrors model.Pricing for OpenAPI schema generation.
// Defined here to avoid an import cycle (dto → model → relay/common → dto).
type PricingModel struct {
	ModelName              string                  `json:"model_name"`
	Description            string                  `json:"description,omitempty"`
	Icon                   string                  `json:"icon,omitempty"`
	Tags                   string                  `json:"tags,omitempty"`
	Metadata               string                  `json:"metadata"`
	CreatedTime            int64                   `json:"created_time,omitempty"`
	VendorID               int                     `json:"vendor_id,omitempty"`
	QuotaType              int                     `json:"quota_type"`
	ModelRatio             float64                 `json:"model_ratio"`
	ModelPrice             float64                 `json:"model_price"`
	OwnerBy                string                  `json:"owner_by"`
	CompletionRatio        float64                 `json:"completion_ratio"`
	CacheRatio             *float64                `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64                `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64                `json:"image_ratio,omitempty"`
	AudioRatio             *float64                `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64                `json:"audio_completion_ratio,omitempty"`
	EnableGroup            []string                `json:"enable_groups" validate:"required"`
	SupportedEndpointTypes []constant.EndpointType `json:"supported_endpoint_types"`
	GridPricing            interface{}             `json:"grid_pricing,omitempty"`
	BillingMode            string                  `json:"billing_mode,omitempty"`
	BillingExpr            string                  `json:"billing_expr,omitempty"`
	PricingVersion         string                  `json:"pricing_version,omitempty"`
	Online                 bool                    `json:"online"`
	// Derived here so callers that only gate on "is this free" (the guest
	// free-model checks on the chat/image paths) read a flag instead of
	// re-deriving it from ratios, model_price and the group map.
	IsFree bool `json:"is_free"`
}

// PricingCatalogModel is the row a model PICKER needs: enough to list, group,
// search and badge a model, and nothing else. Deliberately carries no
// enable_groups, ratios or metadata blob - a caller that needs those for one
// model fetches /pricing/model, which is ~3KB, rather than making every caller
// pay for all of them.
type PricingCatalogModel struct {
	ModelName string `json:"model_name"`
	Vendor    string `json:"vendor"`
	// Grouping key. Names collide across sources, so "same vendor" is an id
	// comparison, not a string one.
	VendorID int      `json:"vendor_id"`
	Icon     string   `json:"icon,omitempty"`
	Type     string   `json:"type"`
	Tags     []string `json:"tags" validate:"required"`
	// Epoch ms, 0 when the model has no release date. Sorted on by callers.
	ReleaseTs int64 `json:"release_ts"`
	IsFree    bool  `json:"is_free"`
	// False when every channel serving the model is disabled. Callers picking a
	// default model must not land a user on one nothing can route.
	Online bool `json:"online"`

	// Display prices in USD per million tokens, already multiplied by the
	// cheapest servable group ratio. Derived here because the inputs (ratios,
	// the group map, the sticker price) are gateway concepts; a caller that
	// re-derives them has to ship the whole group map to do it.
	InputPrice  float64 `json:"input_price"`
	OutputPrice float64 `json:"output_price"`
	// Per-call price for quota_type 1/3/4, where the model bills a flat rate
	// rather than per token.
	FixedPrice   float64 `json:"fixed_price"`
	IsFixedPrice bool    `json:"is_fixed_price"`
	// Undiscounted prices, set only when the model is actually discounted (a
	// group ratio below 1) and the operator enables original-price display.
	// Null means no strikethrough, NOT zero.
	OriginalInputPrice  *float64 `json:"original_input_price,omitempty"`
	OriginalOutputPrice *float64 `json:"original_output_price,omitempty"`
	OriginalFixedPrice  *float64 `json:"original_fixed_price,omitempty"`

	Description string `json:"description,omitempty"`
	// The sync's hint blob, parsed. Vanilla /pricing still publishes it as a
	// JSON string; only this route hands callers a typed object.
	Metadata ModelMetadata `json:"metadata"`
	// Whether the model can serve a chat completion. Upstream types every
	// embedding model as text, so a type check alone leaks models that 400 on
	// /chat/completions.
	Chat bool `json:"chat"`
	// Which upstream protocols route this model. The send paths pick an endpoint
	// from it, so it is a routing fact rather than a display one. 5KB across the
	// whole catalog (1.23 entries per model), so it rides every response.
	SupportedEndpointTypes []constant.EndpointType `json:"supported_endpoint_types" validate:"required"`
}

// PricingCatalogDetail is one model's full record: the catalog row plus the
// fields that only matter once a specific model is chosen. Embeds the row so a
// caller reads the same field names as the list it came from, rather than a
// second shape for the same model.
type PricingCatalogDetail struct {
	PricingCatalogModel
	// The groups this model is served by, and the ratios for those groups only.
	EnableGroups []string           `json:"enable_groups" validate:"required"`
	GroupRatio   map[string]float64 `json:"group_ratio" validate:"required"`
	// The auto-routing chain restricted to this model's groups, cheapest first.
	AutoChain []string `json:"auto_chain" validate:"required"`
	// Raw ratios behind the display prices, for the pricing breakdown.
	ModelRatio       float64  `json:"model_ratio"`
	CompletionRatio  float64  `json:"completion_ratio"`
	CacheRatio       *float64 `json:"cache_ratio,omitempty"`
	CreateCacheRatio *float64 `json:"create_cache_ratio,omitempty"`
	// Per-tier pricing rows, when the model bills on a grid rather than a flat
	// rate. Columns vary per model, so a row is an open map, but the array shape
	// is fixed: typed here rather than as interface{} so generated clients get a
	// list instead of unknown. GridMinRatio is the cheapest group ratio the tiers
	// are multiplied by.
	GridPricing  []map[string]interface{} `json:"grid_pricing,omitempty"`
	GridMinRatio float64                  `json:"grid_min_ratio"`
	// True when the model bills through a tiered billing expression, which has no
	// single per-token price to display.
	IsTiered    bool   `json:"is_tiered"`
	CreatedTime int64  `json:"created_time,omitempty"`
	BillingExpr string `json:"billing_expr,omitempty"`
	// Full text, not the list's truncated blurb.
	Description string `json:"description,omitempty"`
}

// PricingCatalogData is pre-sorted: free models first, then by name. Callers
// render it as-is; re-sorting client-side is what let three copies of this
// ordering drift apart.
//
// Deliberately NOT a {success, data} envelope. Clients unwrap that shape to
// `data` and drop every sibling with it, so an envelope here would cost the
// vendors and the default model. HTTP status carries success; the field names
// say what they hold.
type PricingCatalogData struct {
	Models  []PricingCatalogModel `json:"models" validate:"required"`
	Vendors []PricingVendor       `json:"vendors" validate:"required"`
	// The chat default before a user picks anything: the newest free chat model
	// that is actually routable, so a fresh visitor lands on the current
	// flagship rather than whatever sorts first. Empty when none qualifies.
	FirstFreeModel string `json:"first_free_model,omitempty"`
	// Totals over the same group-filtered list the rows come from. Derived here
	// so a caller that wants four numbers does not download 341 rows to count
	// them, and so the counts cannot disagree with the list.
	Counts PricingCatalogCounts `json:"counts"`
	// Endpoint path/method per endpoint type, for the detail panel that prints a
	// model's callable routes. 11 keys, so it rides the list rather than needing
	// a request of its own.
	SupportedEndpoint map[string]EndpointInfo `json:"supported_endpoint,omitempty"`
}

type PricingCatalogCounts struct {
	Models  int `json:"models"`
	Free    int `json:"free"`
	Paid    int `json:"paid"`
	Vendors int `json:"vendors"`
}

// PricingVendorModel is the row for surfaces that only resolve a model NAME to
// its vendor and badge: log tables, the token group-mapping picker, the status
// page and the ticker. A third of the catalog row's size, because none of them
// price anything.
type PricingVendorModel struct {
	ModelName string `json:"model_name"`
	Vendor    string `json:"vendor"`
	Chat      bool   `json:"chat"`
	IsFree    bool   `json:"is_free"`
	// The primary tag, already defaulted: callers group by it, and an empty
	// string is not a group.
	Tag       string `json:"tag"`
	ReleaseTs int64  `json:"release_ts"`
}

// PricingVendorsData carries the vendor NAMES that actually serve a model, not
// every configured vendor. 114 vendors exist but only 45 have a routable model,
// so this cannot be derived from the vendor table.
type PricingVendorsData struct {
	VendorNames  []string             `json:"vendor_names" validate:"required"`
	ModelVendors []PricingVendorModel `json:"model_vendors" validate:"required"`
}

// PricingModelGroupsData is the group panel for ONE model. Every field is
// already scoped to that model: the full auto-group list is 56KB and the full
// ratio map has 1800+ keys, and a caller rendering one model's groups needs
// neither.
type PricingModelGroupsData struct {
	EnableGroups []string `json:"enable_groups" validate:"required"`
	// Ratios for this model's groups only.
	GroupRatio map[string]float64 `json:"group_ratio" validate:"required"`
	// The auto-routing chain restricted to groups this model is served by,
	// cheapest first. Intersecting the global chain client-side meant shipping
	// all of it to keep a handful of entries.
	AutoChain []string `json:"auto_chain" validate:"required"`
}

// PricingVendor mirrors model.PricingVendor for OpenAPI schema generation.
type PricingVendor struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

// EndpointInfo mirrors common.EndpointInfo for OpenAPI schema generation.
type EndpointInfo struct {
	Path   string `json:"path"`
	Method string `json:"method"`
}

type PricingData struct {
	Success           bool                    `json:"success"`
	Data              []PricingModel          `json:"data" validate:"required"`
	Vendors           []PricingVendor         `json:"vendors" validate:"required"`
	GroupRatio        map[string]float64      `json:"group_ratio"`
	UsableGroup       map[string]string       `json:"usable_group"`
	SupportedEndpoint map[string]EndpointInfo `json:"supported_endpoint"`
	AutoGroups        []string                `json:"auto_groups" validate:"required"`
	ShowOriginalPrice bool                    `json:"show_original_price"`
}
