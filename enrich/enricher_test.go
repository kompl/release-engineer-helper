package enrich

import (
	"sort"
	"testing"

	"release-engineer-helper/internal/models"
)

// fakeFinder records the query and returns canned results.
type fakeFinder struct {
	gotOwner   string
	gotRepo    string
	gotTests   []string
	gotRunIDs  []int
	stableInfo map[string]models.StableSinceInfo
}

func (f *fakeFinder) FindEarliestRunWithTests(owner, repo string, testNames []string, candidateRunIDs []int) map[string]models.StableSinceInfo {
	f.gotOwner = owner
	f.gotRepo = repo
	f.gotTests = testNames
	f.gotRunIDs = candidateRunIDs
	return f.stableInfo
}

func TestRunForRepo(t *testing.T) {
	finder := &fakeFinder{
		stableInfo: map[string]models.StableSinceInfo{
			"SpecA::a": {RunID: 42, CreatedAt: "2026-06-15T00:00:00Z"},
		},
	}
	cr := &models.CollectResult{AllBranchRunIDs: []int{40, 41, 42}}
	ar := &models.AnalyzeResult{
		Behavior: models.BehaviorAnalysis{
			StableFailing: map[string]*models.TestBehavior{
				"SpecA::a": {},
				"SpecB::b": {},
			},
		},
	}

	er := RunForRepo(finder, "hydra-billing", cr, ar, "hoper")

	if finder.gotOwner != "hydra-billing" || finder.gotRepo != "hoper" {
		t.Errorf("finder queried with %s/%s", finder.gotOwner, finder.gotRepo)
	}
	sort.Strings(finder.gotTests)
	if len(finder.gotTests) != 2 || finder.gotTests[0] != "SpecA::a" || finder.gotTests[1] != "SpecB::b" {
		t.Errorf("finder queried with tests %v", finder.gotTests)
	}
	if len(finder.gotRunIDs) != 3 {
		t.Errorf("finder queried with run IDs %v, want the full branch history", finder.gotRunIDs)
	}
	if got := er.StableSince["SpecA::a"]; got.RunID != 42 {
		t.Errorf("StableSince[SpecA::a] = %+v", got)
	}
}

func TestRunForRepoNoStableFailing(t *testing.T) {
	finder := &fakeFinder{}
	er := RunForRepo(finder, "o", &models.CollectResult{}, &models.AnalyzeResult{}, "r")

	if finder.gotOwner != "" {
		t.Error("finder must not be queried when there are no stable-failing tests")
	}
	if er == nil || len(er.StableSince) != 0 {
		t.Errorf("result = %+v, want empty StableSince", er)
	}
}
