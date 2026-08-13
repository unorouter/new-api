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
	EnableGroup            []string                `json:"enable_groups"`
	SupportedEndpointTypes []constant.EndpointType `json:"supported_endpoint_types"`
	GridPricing            interface{}             `json:"grid_pricing,omitempty"`
	BillingMode            string                  `json:"billing_mode,omitempty"`
	BillingExpr            string                  `json:"billing_expr,omitempty"`
	PricingVersion         string                  `json:"pricing_version,omitempty"`
	Online                 bool                    `json:"online"`
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
	Data              []PricingModel          `json:"data"`
	Vendors           []PricingVendor         `json:"vendors"`
	GroupRatio        map[string]float64      `json:"group_ratio"`
	UsableGroup       map[string]string       `json:"usable_group"`
	SupportedEndpoint map[string]EndpointInfo `json:"supported_endpoint"`
	AutoGroups        []string                `json:"auto_groups"`
	ShowOriginalPrice bool                    `json:"show_original_price"`
}
