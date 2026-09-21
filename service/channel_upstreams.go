package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// HasEnabledSiblingUpstream reports whether another enabled channel on a
// DIFFERENT upstream host still serves the model. Disabling the last one gains
// nothing: the model errors either way, and an enabled lane at least serves
// again the moment its upstream recovers. Four lanes on one host are one lane.
func HasEnabledSiblingUpstream(modelName string, channelId int, baseUrl string) bool {
	if modelName == "" {
		return true
	}
	urls, err := model.EnabledBaseUrlsForModelExcept(modelName, channelId)
	if err != nil {
		common.SysError("last upstream check failed: " + err.Error())
		return true
	}
	own := upstreamHost(baseUrl)
	for _, u := range urls {
		if upstreamHost(u) != own {
			return true
		}
	}
	return false
}
