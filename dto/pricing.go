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
	ModelName string   `json:"model_name"`
	Vendor    string   `json:"vendor"`
	Type      string   `json:"type"`
	Tags      []string `json:"tags" validate:"required"`
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
	// Whether the model can serve a chat completion. Upstream types every
	// embedding model as text, so a type check alone leaks models that 400 on
	// /chat/completions.
	Chat bool `json:"chat"`
}

// PricingCatalogData is pre-sorted: free models first, then by name. Callers
// render it as-is; re-sorting client-side is what let three copies of this
// ordering drift apart.
type PricingCatalogData struct {
	Success bool                  `json:"success"`
	Data    []PricingCatalogModel `json:"data" validate:"required"`
	Vendors []PricingVendor       `json:"vendors" validate:"required"`
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
