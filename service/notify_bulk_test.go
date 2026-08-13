package service

import (
	"testing"

	"github.com/QuantumNous/new-api/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bulkData(models ...string) []notify.EventData {
	out := make([]notify.EventData, 0, len(models))
	for _, m := range models {
		out = append(out, notify.EventData{Model: m, Free: notify.IsFreeModel(m)})
	}
	return out
}

// A digest must name no single model (it covers many) and must carry the
// collapsed type plus an accurate count, or clients cannot render it.
func TestNotifyMakeBulkEventCarriesCountAndCollapsedType(t *testing.T) {
	list := bulkData("glm-5.2:free", "kimi-k2.6:free", "deepseek-v4-flash")

	evt := notifyMakeBulkEvent(notify.EventModelOnline, list)

	assert.Equal(t, notify.EventModelBulkChange, evt.Type)
	assert.Equal(t, notify.EventModelOnline, evt.Data.BulkEvent)
	assert.Equal(t, 3, evt.Data.BulkCount)
	assert.Equal(t, 2, evt.Data.BulkFree)
	assert.Empty(t, evt.Data.Model, "a digest covers many models, so it names none")
	assert.False(t, evt.Data.Free, "mixed free/paid set is not wholly free")
}

// The sample is capped so the payload stays inside the web push size limit, and
// sorted so the same set always renders identically.
func TestNotifyMakeBulkEventSamplesDeterministically(t *testing.T) {
	list := bulkData("m9", "m3", "m1", "m7", "m5", "m2", "m8", "m4")

	evt := notifyMakeBulkEvent(notify.EventModelAdded, list)

	assert.Equal(t, 8, evt.Data.BulkCount, "count reflects the whole set, not the sample")
	require.Len(t, evt.Data.Models, 5, "sample is capped")
	assert.Equal(t, []string{"m1", "m2", "m3", "m4", "m5"}, evt.Data.Models)
}

// An all-free set marks the digest free so it reaches the free-models topic.
func TestNotifyMakeBulkEventAllFree(t *testing.T) {
	evt := notifyMakeBulkEvent(notify.EventModelOnline, bulkData("a:free", "b:free"))

	assert.True(t, evt.Data.Free)
	assert.Equal(t, 2, evt.Data.BulkFree)
	assert.Contains(t, evt.Topics, notify.TopicFreeModels)
	assert.Contains(t, evt.Topics, notify.TopicAllModels)
}

// A digest must NOT carry a per-model topic: subscribers watching one specific
// model would otherwise receive digests about models they never asked for.
func TestBulkTopicsForOmitsPerModelTopic(t *testing.T) {
	topics := notify.BulkTopicsFor(notify.EventModelOnline, false)

	assert.Equal(t, []string{notify.TopicAllModels}, topics)

	priced := notify.BulkTopicsFor(notify.EventModelPriceChange, true)
	assert.Contains(t, priced, notify.TopicPrices)
	assert.Contains(t, priced, notify.TopicFreeModels)
	for _, tp := range priced {
		assert.NotContains(t, tp, "model:", "digest carries no per-model topic")
	}
}
