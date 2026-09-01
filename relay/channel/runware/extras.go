package runware

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// Bounds one reference file; Runware's own inputs cap (8-10 per model) bounds the count.
const maxReferenceBytes = 20 << 20

// multipartReferenceImages reads the uploaded reference files of an /images/edits
// request as data URIs. Both the "image[]" array convention and the bare OpenAI
// "image" key are accepted. A JSON request has no multipart form and returns nil.
func multipartReferenceImages(c *gin.Context) ([]string, error) {
	if c == nil || c.Request == nil || c.Request.MultipartForm == nil {
		return nil, nil
	}
	form := c.Request.MultipartForm
	files := append(form.File["image[]"], form.File["image"]...)
	refs := make([]string, 0, len(files))
	for _, fh := range files {
		if fh.Size > maxReferenceBytes {
			return nil, fmt.Errorf("reference image %q exceeds %dMB", fh.Filename, maxReferenceBytes>>20)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open reference image: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxReferenceBytes+1))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("read reference image: %w", err)
		}
		if int64(len(data)) > maxReferenceBytes {
			return nil, fmt.Errorf("reference image %q exceeds %dMB", fh.Filename, maxReferenceBytes>>20)
		}
		mime := fh.Header.Get("Content-Type")
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		refs = append(refs, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return refs, nil
}

// applyExtras maps diffusion parameters that the OpenAI image schema has no field for, and
// which therefore arrive in ImageRequest.Extra, onto the Runware task.
//
// Only known keys are copied. Runware rejects an entire task on an unrecognised key, so
// forwarding Extra wholesale would turn any stray client field into a hard failure. A value
// of the wrong JSON type is skipped rather than defaulted, so a malformed param cannot
// silently change what the user asked for.
func applyExtras(task *ImageInferenceTask, extra map[string]json.RawMessage) {
	if len(extra) == 0 {
		return
	}

	if v, ok := intFrom(extra, "steps"); ok {
		task.Steps = &v
	}
	if v, ok := intFrom(extra, "clip_skip", "clipSkip"); ok {
		task.ClipSkip = &v
	}
	if v, ok := floatFrom(extra, "cfg_scale", "CFGScale", "cfg", "guidance"); ok {
		task.CFGScale = &v
	}
	if v, ok := floatFrom(extra, "strength", "denoise"); ok {
		task.Strength = &v
	}
	if v, ok := int64From(extra, "seed"); ok {
		task.Seed = &v
	}
	if v, ok := stringFrom(extra, "scheduler", "sampler"); ok {
		task.Scheduler = v
	}
	if v, ok := stringFrom(extra, "negative_prompt", "negativePrompt"); ok {
		task.NegativePrompt = v
	}
	// Runware takes a URL, a data URI or bare base64 here, so browser-held bytes reach
	// img2img and inpaint without any object storage on our side.
	if v, ok := stringFrom(extra, "init_image_url", "seedImage", "image"); ok {
		task.SeedImage = v
	}
	if v, ok := stringFrom(extra, "mask_url", "maskImage", "mask"); ok {
		task.MaskImage = v
	}
	if v, ok := stringFrom(extra, "output_format", "outputFormat"); ok {
		task.OutputFormat = normalizeOutputFormat(v)
	}

	if entries, ok := resourceEntries(extra, "loras", "lora"); ok {
		task.Lora = entries
	}
	if entries, ok := resourceEntries(extra, "embeddings"); ok {
		task.Embeddings = make([]EmbeddingEntry, 0, len(entries))
		for _, e := range entries {
			task.Embeddings = append(task.Embeddings, EmbeddingEntry{Model: e.Model, Weight: e.Weight})
		}
	}
}

// resourceEntries reads a LoRA/embedding chain. Both the playground's {name,weight} shape
// and Runware's native {model,weight} are accepted so a caller can pass either. An entry
// without an identifier is dropped; a missing weight defaults to 1, matching Runware.
func resourceEntries(extra map[string]json.RawMessage, keys ...string) ([]LoraEntry, bool) {
	raw, ok := firstPresent(extra, keys...)
	if !ok {
		return nil, false
	}
	var items []struct {
		Model  string   `json:"model"`
		Name   string   `json:"name"`
		Air    string   `json:"air"`
		Weight *float64 `json:"weight"`
	}
	if err := common.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	entries := make([]LoraEntry, 0, len(items))
	for _, item := range items {
		model := firstNonEmpty(item.Model, item.Air, item.Name)
		if model == "" {
			continue
		}
		weight := 1.0
		if item.Weight != nil {
			weight = *item.Weight
		}
		entries = append(entries, LoraEntry{Model: model, Weight: weight})
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

// isAIR reports whether s is a Runware AIR identifier: `<publisher>:<modelId>@<versionId>`,
// e.g. civitai:288584@324619. A passthrough model takes this from the client, so it is the
// one place an arbitrary caller string becomes the upstream model name. Validating the shape
// keeps a crafted value from reaching Runware as something other than a model reference.
func isAIR(s string) bool {
	colon := strings.IndexByte(s, ':')
	at := strings.IndexByte(s, '@')
	if colon <= 0 || at <= colon+1 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n/\\?&#") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == ':' || c == '@' || c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	// Everything after the '@' is a version id, and everything between ':' and '@' a model id.
	return isDigits(s[colon+1:at]) && isDigits(s[at+1:])
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// normalizeOutputFormat maps a lowercase client format onto the uppercase enum Runware
// expects, leaving anything unrecognised for the upstream to reject with its own message.
func normalizeOutputFormat(v string) string {
	switch {
	case equalFold(v, "png"):
		return "PNG"
	case equalFold(v, "jpg"), equalFold(v, "jpeg"):
		return "JPG"
	case equalFold(v, "webp"):
		return "WEBP"
	default:
		return v
	}
}

func firstPresent(extra map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, k := range keys {
		if raw, ok := extra[k]; ok && len(raw) > 0 && string(raw) != "null" {
			return raw, true
		}
	}
	return nil, false
}

func intFrom(extra map[string]json.RawMessage, keys ...string) (int, bool) {
	raw, ok := firstPresent(extra, keys...)
	if !ok {
		return 0, false
	}
	var v int
	if err := common.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func int64From(extra map[string]json.RawMessage, keys ...string) (int64, bool) {
	raw, ok := firstPresent(extra, keys...)
	if !ok {
		return 0, false
	}
	var v int64
	if err := common.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func floatFrom(extra map[string]json.RawMessage, keys ...string) (float64, bool) {
	raw, ok := firstPresent(extra, keys...)
	if !ok {
		return 0, false
	}
	var v float64
	if err := common.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func stringFrom(extra map[string]json.RawMessage, keys ...string) (string, bool) {
	raw, ok := firstPresent(extra, keys...)
	if !ok {
		return "", false
	}
	var v string
	if err := common.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	if v == "" {
		return "", false
	}
	return v, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
