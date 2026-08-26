package i18n

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A locale with its own bundle must still serve its OWN translation; the
// English fallback applies only to keys that locale does not define.
func TestTranslateLangPrefersLocaleOverFallback(t *testing.T) {
	Init()

	zh := TranslateLang("zh-CN", "common.invalid_params")
	en := TranslateLang("en", "common.invalid_params")

	assert.NotEqual(t, "common.invalid_params", zh, "key must resolve")
	assert.NotEqual(t, en, zh, "zh-CN must not be served the English string")
}

// English-only keys must not leak the raw key id to non-English users.
func TestTranslateLangFallsBackForEnglishOnlyKeys(t *testing.T) {
	Init()

	for _, lang := range []string{"zh-CN", "zh-TW", "fr", "ja"} {
		got := TranslateLang(lang, "notify.model_online.title", map[string]any{"Model": "glm-5.2"})
		assert.Equal(t, "glm-5.2 is back online", got, "lang=%s", lang)
	}
}

// A genuinely unknown key still returns the key, so a typo stays diagnosable.
func TestTranslateLangUnknownKeyReturnsKey(t *testing.T) {
	Init()

	assert.Equal(t, "notify.nope.title", TranslateLang("zh-CN", "notify.nope.title"))
}
