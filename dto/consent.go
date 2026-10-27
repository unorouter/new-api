package dto

// OwnPurposeObjectionData is the caller's Art. 21 GDPR objection to providers
// that use inputs for their own purposes.
type OwnPurposeObjectionData struct {
	Objection bool `json:"objection"`
}

// OwnPurposeObjectionRequest sets or clears the objection.
type OwnPurposeObjectionRequest struct {
	Objection *bool `json:"objection"`
}

type ProviderConsentStatus string

const (
	ProviderConsentGiven   ProviderConsentStatus = "given"
	ProviderConsentMissing ProviderConsentStatus = "missing"
)

// ProviderConsentItem is one provider that needs a consent, by its public alias.
type ProviderConsentItem struct {
	Alias string `json:"alias"`
	// Visible name from the lane tags; alias stays the key.
	Label   string `json:"label"`
	Country string `json:"country"`
	Unsafe  bool   `json:"unsafe"`
	// Unsafe only because inputs pass on to unvetted recipients; the seat country is safe.
	OnwardUnvetted bool `json:"onward_unvetted"`
	// trains, retains or unknown (practice not known, counted as own use).
	Practice        string   `json:"practice"`
	TrainsOrRetains bool     `json:"trains_or_retains"`
	ModelCount      int      `json:"model_count"`
	Models          []string `json:"models"`
	// Lane tags as they appear in public group names.
	Tags               []string                   `json:"tags"`
	LaneCount          int                        `json:"lane_count"`
	ActiveLaneCount    int                        `json:"active_lane_count"`
	ActiveModelCount   int                        `json:"active_model_count"`
	ChannelCount       int                        `json:"channel_count"`
	ActiveChannelCount int                        `json:"active_channel_count"`
	ModelDetails       []ProviderConsentModelItem `json:"model_details"`
	// Some channel of this provider is enabled.
	Active  bool                  `json:"active"`
	Status  ProviderConsentStatus `json:"status" validate:"oneof=given missing"`
	GivenAt int64                 `json:"given_at,omitempty"`
}

type ProviderConsentModelItem struct {
	Name           string `json:"name"`
	Active         bool   `json:"active"`
	Channels       int    `json:"channels"`
	ActiveChannels int    `json:"active_channels"`
}

// ProviderConsentListData lists providers; with ?model only that model's, plus
// whether the model is covered.
type ProviderConsentListData struct {
	Model string `json:"model,omitempty"`
	// Whether the providers now serving the model are all covered.
	Covered   *bool                 `json:"covered,omitempty"`
	Providers []ProviderConsentItem `json:"providers"`
	// With ?model: the model's providers that need no consent, and why.
	OtherProviders []ProviderOtherItem `json:"other_providers,omitempty"`
}

// ProviderOtherItem is a provider of the model that needs no consent. Reason is
// safe_country (own use or unknown practice from a safe country) or
// no_own_purpose (prompts none).
type ProviderOtherItem struct {
	Label          string `json:"label"`
	Country        string `json:"country"`
	Unsafe         bool   `json:"unsafe"`
	OnwardUnvetted bool   `json:"onward_unvetted"`
	Active         bool   `json:"active"`
	Practice       string `json:"practice"`
	Reason         string `json:"reason"`
}

// ProviderConsentGrantRequest names either aliases or a model, whose missing
// providers are all granted. Text is the dialog exactly as shown and must name
// every alias granted.
type ProviderConsentGrantRequest struct {
	Aliases []string `json:"aliases" validate:"max=500"`
	Model   string   `json:"model" validate:"max=255"`
	Locale  string   `json:"locale" validate:"max=16"`
	Text    string   `json:"text" validate:"required,max=20000"`
}

type ProviderConsentGrantData struct {
	Granted []string `json:"granted"`
}

type ProviderConsentRevokeRequest struct {
	Alias string `json:"alias" validate:"required,max=16"`
}
