package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
)

func TestCatalogTags(t *testing.T) {
	assert.Equal(t, []string{}, catalogTags(""))
	assert.Equal(t, []string{"text", "Reasoning", "200K"}, catalogTags("text,Reasoning,200K"))
	assert.Equal(t, []string{"image", "Vision"}, catalogTags(" image , Vision "))
	assert.Equal(t, []string{"video"}, catalogTags("video,"))
}

func TestCatalogModality(t *testing.T) {
	cases := []struct {
		name      string
		modalites []string
		wantType  string
		wantChat  bool
	}{
		{"text only serves chat", []string{"text"}, "text", true},
		{"embedding is not chat", []string{"embedding"}, "embedding", false},
		{"image is not chat", []string{"image"}, "image", false},
		{"audio is not chat", []string{"audio"}, "audio", false},
		{"video is not chat", []string{"video"}, "video", false},
		// An image generator that also returns text is still an image model:
		// putting it in the chat picker sends chat traffic to /images.
		{"image plus text is image", []string{"image", "text"}, "image", false},
		// No stated modality means nothing can be claimed, so not chat-eligible.
		{"absent modality is not chat", nil, "text", false},
	}
	for _, c := range cases {
		gotType, gotChat := catalogModality(model.Pricing{}, catalogMetadata{OutputModalities: c.modalites})
		assert.Equal(t, c.wantType, gotType, c.name)
		assert.Equal(t, c.wantChat, gotChat, c.name)
	}
}

func TestParseCatalogMetadata(t *testing.T) {
	md := parseCatalogMetadata(`{"outputModalities":["text"],"releaseTs":1764010580000}`)
	assert.Equal(t, []string{"text"}, md.OutputModalities)
	assert.Equal(t, int64(1764010580000), md.ReleaseTs)

	// Malformed or absent metadata must not fail the whole catalog response.
	assert.Empty(t, parseCatalogMetadata("").OutputModalities)
	assert.Empty(t, parseCatalogMetadata("not json").OutputModalities)
}

// An embedding routed to /embeddings must never be chat-eligible, even while a
// source still publishes outputModalities ["text"] for it.
func TestCatalogModalityEndpointOverridesTextClaim(t *testing.T) {
	m := model.Pricing{SupportedEndpointTypes: []constant.EndpointType{"openai", "embedding"}}
	gotType, gotChat := catalogModality(m, catalogMetadata{OutputModalities: []string{"text"}})
	assert.Equal(t, "text", gotType)
	assert.False(t, gotChat)
}
