package dto

import relaydto "github.com/QuantumNous/new-api/relaykit/dto"

// FetchUpstreamRatiosRequest is the body of POST /api/ratio_sync/fetch;
// IncludeUnchanged also returns the prices of models every upstream agrees on.
type FetchUpstreamRatiosRequest struct {
	relaydto.UpstreamRequest
	IncludeUnchanged bool `json:"include_unchanged"`
}

// FetchUpstreamRatiosResult holds the response data for FetchUpstreamRatios.
type FetchUpstreamRatiosResult struct {
	Differences map[string]map[string]relaydto.DifferenceItem `json:"differences"`
	Prices      any                                           `json:"prices"`
	TestResults []relaydto.TestResult                         `json:"test_results"`
}
