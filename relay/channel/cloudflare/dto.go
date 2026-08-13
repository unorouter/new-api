package cloudflare

import "github.com/QuantumNous/new-api/relaykit/dto"

type CfRequest struct {
	Messages    []dto.Message `json:"messages,omitempty"`
	Lora        string        `json:"lora,omitempty"`
	MaxTokens   uint          `json:"max_tokens,omitempty"`
	Prompt      string        `json:"prompt,omitempty"`
	Raw         bool          `json:"raw,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
}

type CfAudioResponse struct {
	Result CfSTTResult `json:"result"`
}

type CfSTTResult struct {
	Text string `json:"text"`
}

// CfTTSRequest is the Workers AI text-to-speech body (e.g. @cf/myshell-ai/melotts):
// prompt is the text, lang the BCP-47-ish language code.
type CfTTSRequest struct {
	Prompt string `json:"prompt"`
	Lang   string `json:"lang,omitempty"`
}

// CfTTSResponse holds the base64-encoded audio (WAV) returned by melotts.
type CfTTSResponse struct {
	Result CfTTSResult `json:"result"`
}

type CfTTSResult struct {
	Audio string `json:"audio"`
}

// CfImageRequest is the Workers AI text-to-image body. flux/SD models take a bare
// prompt; width/height/steps/seed are optional and only sent when set.
type CfImageRequest struct {
	Prompt   string `json:"prompt"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	NumSteps int    `json:"num_steps,omitempty"`
	Seed     int    `json:"seed,omitempty"`
}

// CfImageResponse is the JSON shape some models return (flux): base64 image in
// result.image. Classic SD models instead stream raw image bytes (handled by
// content-type in cfImageHandler).
type CfImageResponse struct {
	Result  CfImageResult `json:"result"`
	Success bool          `json:"success"`
}

type CfImageResult struct {
	Image string `json:"image"`
}
