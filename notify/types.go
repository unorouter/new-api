package notify

import (
	"regexp"
	"strings"
)

const (
	EventModelOnline      = "model_online"
	EventModelOffline     = "model_offline"
	EventModelPriceChange = "model_price_change"
	EventModelAdded       = "model_added"
	EventModelRemoved     = "model_removed"
	EventModelBulkChange  = "model_bulk_change"
)

const (
	TopicAllModels  = "models"
	TopicFreeModels = "free-models"
	TopicPrices     = "prices"
	topicModelPfx   = "model:"
)

const ProtocolVersion = 1

// Event is the wire envelope published on Redis and delivered to WS clients
// and web push subscribers. Data is raw facts only; clients render prose.
type Event struct {
	Id     string    `json:"id"`
	Type   string    `json:"type"`
	Ts     int64     `json:"ts"`
	Topics []string  `json:"topics"`
	Data   EventData `json:"data"`
}

type EventData struct {
	Model             string   `json:"model"`
	Free              bool     `json:"free"`
	Online            *bool    `json:"online,omitempty"`
	CheapestRatio     *float64 `json:"cheapest_ratio,omitempty"`
	PrevCheapestRatio *float64 `json:"prev_cheapest_ratio,omitempty"`
	CheapestGroup     string   `json:"cheapest_group,omitempty"`
	// Set only on EventModelBulkChange: the collapsed event type, how many
	// models it covers, and a short sample for the client to name a few.
	BulkEvent string   `json:"bulk_event,omitempty"`
	BulkCount int      `json:"bulk_count,omitempty"`
	BulkFree  int      `json:"bulk_free,omitempty"`
	Models    []string `json:"models,omitempty"`
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9:._/\-*]{1,160}$`)

// ValidTopic accepts concrete topics and subscription patterns with at most
// one '*'. Patterns need at least 2 literal chars so a bare firehose like
// "*" or ":*" cannot be subscribed by accident.
func ValidTopic(topic string) bool {
	if !topicRe.MatchString(topic) {
		return false
	}
	if strings.Count(topic, "*") > 1 {
		return false
	}
	return len(strings.ReplaceAll(topic, "*", "")) >= 2
}

// TopicMatches reports whether subscription pattern sub matches the concrete
// topic. A single '*' matches any run of characters; no '*' means equality.
func TopicMatches(sub string, topic string) bool {
	i := strings.IndexByte(sub, '*')
	if i < 0 {
		return sub == topic
	}
	prefix, suffix := sub[:i], sub[i+1:]
	return len(topic) >= len(prefix)+len(suffix) &&
		strings.HasPrefix(topic, prefix) &&
		strings.HasSuffix(topic, suffix)
}

// SanitizeTopics filters invalid entries and caps the list at max.
func SanitizeTopics(topics []string, max int) []string {
	out := make([]string, 0, len(topics))
	seen := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		if !ValidTopic(t) {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) >= max {
			break
		}
	}
	return out
}

func ModelTopic(model string) string {
	return topicModelPfx + model
}

func IsFreeModel(model string) bool {
	return strings.HasSuffix(strings.ToLower(model), ":free")
}

// BulkTopicsFor is the topic set for a collapsed digest. A digest covers many
// models, so it carries no per-model topic: it lands on the catalog-wide feeds
// only. A watcher of one specific model still gets that model's own event,
// because the collapse only ever replaces events beyond the burst threshold.
func BulkTopicsFor(eventType string, anyFree bool) []string {
	topics := []string{TopicAllModels}
	if anyFree {
		topics = append(topics, TopicFreeModels)
	}
	if eventType == EventModelPriceChange {
		topics = append(topics, TopicPrices)
	}
	return topics
}

// TopicsFor assigns the server-side topic set for an event.
func TopicsFor(model string, eventType string) []string {
	topics := []string{ModelTopic(model), TopicAllModels}
	if IsFreeModel(model) {
		topics = append(topics, TopicFreeModels)
	}
	if eventType == EventModelPriceChange {
		topics = append(topics, TopicPrices)
	}
	return topics
}
