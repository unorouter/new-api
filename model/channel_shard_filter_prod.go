package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Shard series whose lanes all sit behind one upstream content filter: every
// shard of a series fronts the same endpoint. A refusal from one is the refusal from all.
var sharedFilterShardSeries = []string{"cfp", "chat"}

func shardSeries(name string) string {
	head, _, _ := strings.Cut(name, "-")
	for _, series := range sharedFilterShardSeries {
		rest, ok := strings.CutPrefix(head, series)
		if ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return series
		}
	}
	return ""
}

// SharedFilterSiblingIDs returns every channel in the same shared-filter shard
// series as channelID, or nil when the channel is not part of one.
func SharedFilterSiblingIDs(channelID int) []int {
	if !common.MemoryCacheEnabled {
		return nil
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	ch, ok := channelsIDM[channelID]
	if !ok {
		return nil
	}
	series := shardSeries(ch.Name)
	if series == "" {
		return nil
	}
	var ids []int
	for id, other := range channelsIDM {
		if shardSeries(other.Name) == series {
			ids = append(ids, id)
		}
	}
	return ids
}
