package notify

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopicsFor(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		eventType string
		want      []string
	}{
		{
			name:      "paid model availability",
			model:     "deepseek-v3.2",
			eventType: EventModelOnline,
			want:      []string{"model:deepseek-v3.2", TopicAllModels},
		},
		{
			name:      "free model gets free topic",
			model:     "glm-5.2:free",
			eventType: EventModelOffline,
			want:      []string{"model:glm-5.2:free", TopicAllModels, TopicFreeModels},
		},
		{
			name:      "price change gets prices topic",
			model:     "claude-sonnet-5",
			eventType: EventModelPriceChange,
			want:      []string{"model:claude-sonnet-5", TopicAllModels, TopicPrices},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TopicsFor(tt.model, tt.eventType))
		})
	}
}

func TestSanitizeTopics(t *testing.T) {
	got := SanitizeTopics([]string{
		"model:deepseek-v3.2",
		"model:deepseek-v3.2", // duplicate
		"free-models",
		"bad topic with spaces",
		"",
		"model:GLM-5.2:free",
	}, 3)
	assert.Equal(t, []string{"model:deepseek-v3.2", "free-models", "model:GLM-5.2:free"}, got)

	assert.Empty(t, SanitizeTopics([]string{"###", "a b"}, 10))
}

func TestValidTopicWildcards(t *testing.T) {
	assert.True(t, ValidTopic("model:glm-*"))
	assert.True(t, ValidTopic("model:glm-*:free"))
	assert.True(t, ValidTopic("model:*:free"))
	assert.False(t, ValidTopic("model:*-*:free"), "more than one wildcard")
	assert.False(t, ValidTopic("*"), "bare firehose")
	assert.False(t, ValidTopic("a*"), "under 2 literal chars")
	assert.True(t, ValidTopic("ab*"))
}

func TestTopicMatches(t *testing.T) {
	tests := []struct {
		sub   string
		topic string
		want  bool
	}{
		{"model:glm-5.2", "model:glm-5.2", true},
		{"model:glm-5.2", "model:glm-5.2:free", false},
		{"model:glm-*", "model:glm-5.2", true},
		{"model:glm-*", "model:glm-5.2:free", true},
		{"model:glm-*", "model:kimi-k2.5", false},
		{"model:*:free", "model:glm-5.2:free", true},
		{"model:*:free", "model:glm-5.2", false},
		{"model:glm-*:free", "model:glm-5.2:free", true},
		{"model:glm-*:free", "model:glm-5.2", false},
		{"model:glm-*:free", "model:glm-:free", true /* empty star run */},
		{"model:ab*ab", "model:ab", false /* overlap guard */},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, TopicMatches(tt.sub, tt.topic), "%s vs %s", tt.sub, tt.topic)
	}
}
