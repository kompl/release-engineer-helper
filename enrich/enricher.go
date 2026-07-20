package enrich

import (
	"fmt"

	"release-engineer-helper/internal/models"
)

// StableSinceFinder locates the earliest run in which each test appears.
// *collect.Cache satisfies it; tests substitute a fake.
type StableSinceFinder interface {
	FindEarliestRunWithTests(owner, repo string, testNames []string, candidateRunIDs []int) map[string]models.StableSinceInfo
}

// RunForRepo executes the Enrich phase for a specific repo.
func RunForRepo(finder StableSinceFinder, owner string, cr *models.CollectResult, ar *models.AnalyzeResult, repo string) *models.EnrichResult {
	result := &models.EnrichResult{
		StableSince: make(map[string]models.StableSinceInfo),
	}

	if len(ar.Behavior.StableFailing) == 0 {
		return result
	}

	fmt.Printf("  [enrich] Looking up history for %d stable-failing tests in MongoDB...\n", len(ar.Behavior.StableFailing))

	testNames := make([]string, 0, len(ar.Behavior.StableFailing))
	for name := range ar.Behavior.StableFailing {
		testNames = append(testNames, name)
	}

	result.StableSince = finder.FindEarliestRunWithTests(
		owner,
		repo,
		testNames,
		cr.AllBranchRunIDs,
	)

	fmt.Printf("  [enrich] Found history for %d/%d stable-failing tests\n",
		len(result.StableSince), len(ar.Behavior.StableFailing))

	return result
}
