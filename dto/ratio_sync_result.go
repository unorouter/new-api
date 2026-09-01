package dto

import relaydto "github.com/QuantumNous/new-api/relaykit/dto"

// FetchUpstreamRatiosResult holds the response data for FetchUpstreamRatios.
type FetchUpstreamRatiosResult struct {
	Differences map[string]map[string]relaydto.DifferenceItem `json:"differences"`
	TestResults []relaydto.TestResult                         `json:"test_results"`
}
