package common

import (
	"strings"
	"unicode"
)

// ResponseModel records upstream declarations before response conversion. It
// never changes pricing or downstream output; only a Downgrade pulls the lane.
// Only the three names are stored; whether they disagree is computed on demand
// so every consumer applies the current comparison rule to old rows as well.
type ResponseModel struct {
	RequestedModel string `json:"requested_model"`
	UpstreamModel  string `json:"upstream_model"`
	ReturnedModel  string `json:"returned_model"`
}

// matches reports whether an upstream declaration is compatible with the
// requested or upstream model: equal ignoring case, a dated or variant name
// that extends it, or the same name behind a provider path such as
// "deepseek/deepseek-v4.1-flash".
func (r *ResponseModel) matches(model string) bool {
	returned := strings.ToLower(model)
	for _, expected := range []string{r.RequestedModel, r.UpstreamModel} {
		expected = strings.ToLower(expected)
		if expected != "" && (strings.HasPrefix(returned, expected) || strings.HasSuffix(returned, expected)) {
			return true
		}
	}
	return false
}

// Mismatch reports whether the retained upstream declaration disagrees with
// both the requested and upstream models.
func (r *ResponseModel) Mismatch() bool {
	return r != nil && r.ReturnedModel != "" && !r.matches(r.ReturnedModel)
}

// Downgrade reports a mismatch where the upstream answered with a lighter tier
// than the one requested: resold Gemini groups answered Carbon and Argon as
// 3.1-flash-lite, free lanes answered other models as nemotron nano. A router
// picks its model per request by design, so it never counts.
func (r *ResponseModel) Downgrade() bool {
	if !r.Mismatch() {
		return false
	}
	expected := strings.ToLower(r.RequestedModel + " " + r.UpstreamModel)
	if strings.Contains(expected, "router") {
		return false
	}
	return modelTier(r.ReturnedModel) > modelTier(expected)
}

// modelTier ranks the size words in a model id: 0 for a full model, 1 for
// flash or haiku, 2 for lite, mini or nano.
func modelTier(model string) int {
	tier := 0
	notWordRune := func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }
	for word := range strings.FieldsFuncSeq(strings.ToLower(model), notWordRune) {
		switch word {
		case "flash", "haiku":
			tier = max(tier, 1)
		case "lite", "mini", "nano":
			tier = 2
		}
	}
	return tier
}

// ObserveResponseModel retains the first differing model for inspection, with
// mismatches taking priority over provider-path, prefix, or case-only
// differences. A later matching or empty event cannot erase it. Only observe
// upstream declarations, never models synthesized by a response converter.
func (info *RelayInfo) ObserveResponseModel(model string) {
	if info == nil || strings.TrimSpace(model) == "" {
		return
	}
	if info.ResponseModel == nil {
		info.ResponseModel = &ResponseModel{
			RequestedModel: info.OriginModelName,
			UpstreamModel:  info.GetUpstreamModelName(),
		}
	}
	observation := info.ResponseModel
	if observation.Mismatch() {
		return
	}
	if observation.matches(model) && observation.ReturnedModel != "" &&
		observation.ReturnedModel != observation.RequestedModel && observation.ReturnedModel != observation.UpstreamModel {
		return
	}
	observation.ReturnedModel = model
}
