package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A text-only upstream rejects the whole request when an image arrives, and iOS
// sends stickers and Memoji as PNG attachments, so a message the user read as
// plain text loses them the reply. The image is dropped and the text kept.
func TestStripImagesKeepsText(t *testing.T) {
	override := map[string]interface{}{
		"operations": []interface{}{
			map[string]interface{}{"mode": "strip_images"},
		},
	}
	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"user","content":[` +
		`{"type":"text","text":"look at this"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)

	out, err := ApplyParamOverride(body, override, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "image_url")
	assert.NotContains(t, string(out), "base64")
	assert.Contains(t, string(out), "look at this")
}

// A message that was only an image must not become an empty content array,
// which strict upstreams reject in place of the error it was meant to avoid.
func TestStripImagesLeavesPlaceholderWhenOnlyImage(t *testing.T) {
	override := map[string]interface{}{
		"operations": []interface{}{
			map[string]interface{}{"mode": "strip_images"},
		},
	}
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)

	out, err := ApplyParamOverride(body, override, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "image_url")
	assert.Contains(t, string(out), "[image omitted]")
}

// Plain-text requests are the common case and must come through byte-identical.
func TestStripImagesLeavesTextOnlyRequestsAlone(t *testing.T) {
	override := map[string]interface{}{
		"operations": []interface{}{
			map[string]interface{}{"mode": "strip_images"},
		},
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	out, err := ApplyParamOverride(body, override, nil)
	require.NoError(t, err)
	assert.JSONEq(t, string(body), string(out))
}
