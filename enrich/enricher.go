package enrich

import (
	"fmt"

	"release-engineer-helper/v0.1/analyze"
	"release-engineer-helper/v0.1/collect"
)

// RunForRepo executes the Enrich phase for a specific repo.
func RunForRepo(cache *collect.Cache, owner string, cr *collect.CollectResult, ar *analyze.AnalyzeResult, repo string) *EnrichResult {
	result := &EnrichResult{
		StableSince: make(map[string]collect.StableSinceInfo),
	}

	if len(ar.Behavior.StableFailing) == 0 {
		return result
	}

	fmt.Printf("  [enrich] Looking up history for %d stable-failing tests in MongoDB...\n", len(ar.Behavior.StableFailing))

	testNames := make([]string, 0, len(ar.Behavior.StableFailing))
	for name := range ar.Behavior.StableFailing {
		testNames = append(testNames, name)
	}

	result.StableSince = cache.FindEarliestRunWithTests(
		owner,
		repo,
		testNames,
		cr.AllBranchRunIDs,
	)

	fmt.Printf("  [enrich] Found history for %d/%d stable-failing tests\n",
		len(result.StableSince), len(ar.Behavior.StableFailing))

	return result
}
