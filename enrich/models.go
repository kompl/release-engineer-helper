package enrich

import "release-engineer-helper/v0.1/collect"

// EnrichResult is the output of the Enrich phase.
type EnrichResult struct {
	StableSince map[string]collect.StableSinceInfo // testName → earliest run info
}
