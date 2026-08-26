package aihorde

// AI Horde (aihorde.net) native async image API. Unlike ComfyUI (which injects
// params into a node graph), Horde takes a flat params object, so there is no
// workflow/strategy layer - one submit shape, one poll shape.

// ModelDefaults carries per-published-model default generation params, forwarded
// from the sync tool via the channel's workflow_templates JSON. A submit merges
// these under any client-supplied overrides.
type ModelDefaults struct {
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	Steps       int     `json:"steps,omitempty"`
	CfgScale    float64 `json:"cfg_scale,omitempty"`
	SamplerName string  `json:"sampler_name,omitempty"`
	Karras      *bool   `json:"karras,omitempty"`
	ClipSkip    int     `json:"clip_skip,omitempty"`
	// HordeModel is the exact model name Horde expects (e.g. "Deliberate"),
	// when it differs from the published/exposed id.
	HordeModel string `json:"horde_model,omitempty"`
}

// ChannelConfig is the shape of the channel's workflow_templates JSON for an
// AI Horde channel: { "models": { "<published-id>": {defaults...} } }.
type ChannelConfig struct {
	Models map[string]ModelDefaults `json:"models"`
}

// hordeParams is the `params` sub-object of the async submit body
// (ModelGenerationInputStable). Pointers/omitempty so unset fields fall back to
// Horde/worker defaults instead of forcing zeros.
type hordeParams struct {
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	Steps       int     `json:"steps,omitempty"`
	CfgScale    float64 `json:"cfg_scale,omitempty"`
	SamplerName string  `json:"sampler_name,omitempty"`
	Karras      *bool   `json:"karras,omitempty"`
	ClipSkip    int     `json:"clip_skip,omitempty"`
	N           int     `json:"n,omitempty"`
	Seed        string  `json:"seed,omitempty"`
}

// hordeSubmit is the POST /api/v2/generate/async body (GenerationInputStable).
// The uncensored flags (nsfw, censor_nsfw:false, replacement_filter:false) are
// what make Horde serve NSFW/RP finetunes without prompt sanitizing.
type hordeSubmit struct {
	Prompt            string      `json:"prompt"`
	Params            hordeParams `json:"params"`
	Models            []string    `json:"models"`
	NSFW              bool        `json:"nsfw"`
	CensorNSFW        bool        `json:"censor_nsfw"`
	ReplacementFilter bool        `json:"replacement_filter"`
	R2                bool        `json:"r2"`
	SlowWorkers       bool        `json:"slow_workers"`
}

// hordeSubmitResp is the async submit response: { "id": "...", "kudos": N }.
type hordeSubmitResp struct {
	ID      string  `json:"id"`
	Kudos   float64 `json:"kudos"`
	Message string  `json:"message"`
	Errors  any     `json:"errors"`
}

// hordeStatusResp is GET /api/v2/generate/status/{id} (RequestStatusStable),
// which also carries the RequestStatusCheck progress fields.
type hordeStatusResp struct {
	Done          bool              `json:"done"`
	Faulted       bool              `json:"faulted"`
	IsPossible    bool              `json:"is_possible"`
	Finished      int               `json:"finished"`
	Processing    int               `json:"processing"`
	Waiting       int               `json:"waiting"`
	WaitTime      int               `json:"wait_time"`
	QueuePosition int               `json:"queue_position"`
	Generations   []hordeGeneration `json:"generations"`
	Message       string            `json:"message"`
}

type hordeGeneration struct {
	Img      string `json:"img"`
	Seed     string `json:"seed"`
	ID       string `json:"id"`
	Censored bool   `json:"censored"`
}
