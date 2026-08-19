package dto

// ModelMetadata is the per-model hint blob the sync writes into Model.Metadata
// as a JSON string. The column stays a string (opaque to the gateway, and the
// shape vanilla /pricing publishes), but the CURATED routes emit it as a real
// object so callers read typed fields instead of parsing the blob themselves.
//
// Every field is optional: a model carries only what its sources published.
// Pointers rather than zero values where the difference matters, so "not
// stated" stays distinguishable from "stated as 0/false".
type ModelMetadata struct {
	// Sort key, epoch ms. Derived by the sync from releaseDate; 0 when the
	// model has no known release date, so callers read a number either way.
	ReleaseTs   int64  `json:"releaseTs"`
	ReleaseDate string `json:"releaseDate,omitempty"`

	ContextWindow   int `json:"contextWindow,omitempty"`
	MaxInputTokens  int `json:"maxInputTokens,omitempty"`
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	// Cap on reference images per request. Absent on most models; consumers
	// fall back to their own limit rather than treating 0 as "none allowed".
	MaxImageInputs int `json:"maxImageInputs,omitempty"`

	// Which generation controls an image model accepts, resolved by the sync
	// from the provider's own schema. Absent means unresolved, which is distinct
	// from a spec stating every control is off.
	ImageParams *ImageParams `json:"imageParams,omitempty"`

	// What the model emits. Every synced model carries this, and it is the
	// authority for model type: a name or tag heuristic guesses at what this
	// states outright.
	OutputModalities []string `json:"outputModalities,omitempty"`
	InputModalities  []string `json:"inputModalities,omitempty"`
	// "chat" or "embedding". Distinguishes an embedding model that upstream
	// still types as text.
	Mode string `json:"mode,omitempty"`

	Series          string   `json:"series,omitempty"`
	Categories      []string `json:"categories,omitempty"`
	Tokenizer       string   `json:"tokenizer,omitempty"`
	KnowledgeCutoff string   `json:"knowledgeCutoff,omitempty"`
	DeprecationDate string   `json:"deprecationDate,omitempty"`
	ExpirationDate  string   `json:"expirationDate,omitempty"`
	HuggingFaceID   string   `json:"huggingFaceId,omitempty"`
	Quantization    string   `json:"quantization,omitempty"`
	IsModerated     bool     `json:"isModerated,omitempty"`

	IsReasoning                    bool `json:"isReasoning,omitempty"`
	SupportsTools                  bool `json:"supportsTools,omitempty"`
	SupportsParallelTools          bool `json:"supportsParallelTools,omitempty"`
	SupportsVision                 bool `json:"supportsVision,omitempty"`
	SupportsAudio                  bool `json:"supportsAudio,omitempty"`
	SupportsAudioOutput            bool `json:"supportsAudioOutput,omitempty"`
	SupportsVideo                  bool `json:"supportsVideo,omitempty"`
	SupportsPdf                    bool `json:"supportsPdf,omitempty"`
	SupportsCache                  bool `json:"supportsCache,omitempty"`
	SupportsWebSearch              bool `json:"supportsWebSearch,omitempty"`
	SupportsComputerUse            bool `json:"supportsComputerUse,omitempty"`
	SupportsResponseFormat         bool `json:"supportsResponseFormat,omitempty"`
	SupportsAssistantPrefill       bool `json:"supportsAssistantPrefill,omitempty"`
	SupportsCodeExecution          bool `json:"supportsCodeExecution,omitempty"`
	SupportsFileSearch             bool `json:"supportsFileSearch,omitempty"`
	SupportsServiceTier            bool `json:"supportsServiceTier,omitempty"`
	SupportsUrlContext             bool `json:"supportsUrlContext,omitempty"`
	SupportsNativeStreaming        bool `json:"supportsNativeStreaming,omitempty"`
	SupportsNativeStructuredOutput bool `json:"supportsNativeStructuredOutput,omitempty"`
	SupportsSystemMessages         bool `json:"supportsSystemMessages,omitempty"`

	// Sampling parameters the model accepts. SupportedParameters is what the
	// provider advertises; SupportedParametersAll adds ones we verified.
	SupportedParameters    []string `json:"supportedParameters,omitempty"`
	SupportedParametersAll []string `json:"supportedParametersAll,omitempty"`
	// Provider defaults, e.g. {"temperature": 1}. Null values are meaningful
	// (the provider states no default), hence the pointer.
	DefaultParameters map[string]*float64 `json:"defaultParameters,omitempty"`
	ReasoningEfforts  []string            `json:"reasoningEfforts,omitempty"`
}

// ImageParamRange is the slider bounds and default for one numeric generation
// parameter, as published by the provider.
type ImageParamRange struct {
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Default *float64 `json:"default,omitempty"`
}

// ImageParams says which controls an image model accepts. A parameter absent
// from the provider's schema is rejected upstream, so rendering a control for it
// could only ever produce a failed generation.
type ImageParams struct {
	SupportsNegativePrompt bool `json:"supportsNegativePrompt"`
	SupportsCfg            bool `json:"supportsCfg"`
	SupportsSteps          bool `json:"supportsSteps"`
	SupportsSampler        bool `json:"supportsSampler"`
	SupportsLoraChain      bool `json:"supportsLoraChain"`
	SupportsSeed           bool `json:"supportsSeed"`
	SupportsStrength       bool `json:"supportsStrength"`
	SupportsHiresFix       bool `json:"supportsHiresFix"`
	SupportsAdetailer      bool `json:"supportsAdetailer"`
	// Accepted "<sampler> <schedule>" strings; the provider folds both into one field.
	Samplers []string         `json:"samplers,omitempty"`
	Steps    *ImageParamRange `json:"steps,omitempty"`
	Cfg      *ImageParamRange `json:"cfg,omitempty"`
	// 0 means the model takes no reference images at all, which is distinct from
	// taking an init image for img2img.
	MaxReferenceImages int  `json:"maxReferenceImages"`
	SupportsSeedImage  bool `json:"supportsSeedImage"`
	SupportsMaskImage  bool `json:"supportsMaskImage"`
	// Provider-scoped enums, so the offered choices are the accepted ones.
	OutputFormatChoices []string `json:"outputFormatChoices,omitempty"`
	QualityChoices      []string `json:"qualityChoices,omitempty"`
	BackgroundChoices   []string `json:"backgroundChoices,omitempty"`

	// Derived by the gateway, which owns the endpoint types: the endpoint a
	// synchronous image request routes to. Empty when the model serves none of
	// them (an aihorde row is an async task), which is what makes it
	// unsubmittable from an image form.
	Endpoint     string `json:"endpoint,omitempty"`
	SupportsSize bool   `json:"supportsSize"`
	// Where a form starts. Steps/cfg come from the model's own schema when it
	// states them.
	DefaultWidth   int      `json:"defaultWidth"`
	DefaultHeight  int      `json:"defaultHeight"`
	DefaultSteps   int      `json:"defaultSteps"`
	DefaultCfg     *float64 `json:"defaultCfg,omitempty"`
	DefaultSampler string   `json:"defaultSampler"`
	// References the model accepts, already defaulted for rows whose schema is
	// unresolved but which route to a generation endpoint.
	SupportsReferences bool `json:"supportsReferences"`
}
