package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The status page cache is keyed by (bucket, hours) on a PUBLIC route, so an
// anonymous caller walking hours=1..720 mints a distinct entry per request. Each
// payload is ~1.25MB against a 2Gi pod limit, so entries that are merely ignored
// once expired -- rather than deleted -- accumulate into an OOM.
func TestStatusPageCacheDropsExpiredEntries(t *testing.T) {
	statusPageCacheMu.Lock()
	statusPageCache = map[string]statusPageCacheEntry{}
	statusPageCacheMu.Unlock()

	for i := range 50 {
		statusPageCacheMu.Lock()
		statusPageCache[fmt.Sprintf("compact|60|%d", i)] = statusPageCacheEntry{
			payload:   "stale",
			expiresAt: time.Now().Add(-time.Minute),
		}
		statusPageCacheMu.Unlock()
	}

	statusPageCacheSet("compact|60|fresh", "current")

	statusPageCacheMu.Lock()
	size := len(statusPageCache)
	statusPageCacheMu.Unlock()
	assert.Equal(t, 1, size, "a write must sweep expired entries, not accumulate them")

	got, ok := statusPageCacheGet("compact|60|fresh")
	require.True(t, ok, "the entry just written must still be served")
	assert.Equal(t, "current", got)
}

func TestStatusPageCacheGetRemovesTheExpiredEntryItRefuses(t *testing.T) {
	statusPageCacheMu.Lock()
	statusPageCache = map[string]statusPageCacheEntry{
		"compact|60|1": {payload: "stale", expiresAt: time.Now().Add(-time.Second)},
	}
	statusPageCacheMu.Unlock()

	_, ok := statusPageCacheGet("compact|60|1")
	assert.False(t, ok, "an expired entry must not be served")

	statusPageCacheMu.Lock()
	size := len(statusPageCache)
	statusPageCacheMu.Unlock()
	assert.Zero(t, size, "refusing an expired entry must also drop it")
}
