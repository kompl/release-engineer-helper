package collect

import (
	"log"
	"strconv"
	"strings"
)

// delegatedCIRepos maps repos whose CI is delegated to a sibling repo.
// homs's own ci.yml is a thin proxy: it dispatches ci.yml in homs-ci
// (run-name "CI: <branch> <dispatching run id>") and waits for the result,
// so tests run and artifacts live on homs-ci runs. The dispatching run id
// is absent for scheduled/manual dispatches made directly in homs-ci.
var delegatedCIRepos = map[string]string{
	"homs": "homs-ci",
}

// runSource locates CI runs and their test data for a logical repo.
// It hides CI delegation from the rest of the collector: returned runs
// always carry the logical repo's branch and commit SHA, while run IDs,
// conclusions and links refer to the repo where the tests actually ran.
type runSource struct {
	gh     *GitHubClient
	repo   string // logical repo — cache identity, commit lookups
	ciRepo string // repo hosting runs and artifacts (== repo unless delegated)
}

func newRunSource(gh *GitHubClient, repo string) *runSource {
	ciRepo := repo
	if delegated, ok := delegatedCIRepos[repo]; ok {
		ciRepo = delegated
	}
	return &runSource{gh: gh, repo: repo, ciRepo: ciRepo}
}

// dataRepo is the repo to download artifacts and logs from.
func (s *runSource) dataRepo() string { return s.ciRepo }

func (s *runSource) delegated() bool { return s.ciRepo != s.repo }

// fetchRunsPage returns completed (success/failure) runs of the logical
// branch from one API page, normalized so HeadBranch names the logical
// repo's branch. hasMore reports whether further pages may exist; a page
// can yield zero matching runs while more pages remain.
func (s *runSource) fetchRunsPage(branch string, page int) (runs []ghWorkflowRun, hasMore bool, err error) {
	apiBranch := branch
	if s.delegated() {
		// Delegated runs are workflow_dispatch on the CI repo's default
		// branch; the logical branch is only in the run name.
		apiBranch = ""
	}

	raw, err := s.gh.FetchRunsPage(s.ciRepo, apiBranch, page)
	if err != nil {
		return nil, false, err
	}
	hasMore = len(raw) > 0

	for _, run := range raw {
		if run.Status != "completed" || (run.Conclusion != "success" && run.Conclusion != "failure") {
			continue
		}
		if s.delegated() {
			titleBranch, dispatchRunID, ok := parseDispatchTitle(run.DisplayTitle)
			if !ok || titleBranch != branch {
				continue
			}
			run.HeadBranch = branch
			run.HeadSHA = "" // the CI repo's own SHA; resolveHead fills the logical one
			run.dispatchRunID = dispatchRunID
		}
		runs = append(runs, run)
	}
	return runs, hasMore, nil
}

// latestCompletedRun returns the most recent completed run for a branch.
func (s *runSource) latestCompletedRun(branch string) *ghWorkflowRun {
	for page := 1; page <= maxPages; page++ {
		runs, hasMore, err := s.fetchRunsPage(branch, page)
		if err != nil {
			log.Printf("[collect] Error getting latest run for %s/%s: %v", s.repo, branch, err)
			return nil
		}
		if len(runs) > 0 {
			return &runs[0]
		}
		if !hasMore {
			return nil
		}
	}
	return nil
}

// resolveHead fills HeadSHA with the logical repo's commit for delegated
// runs: via the dispatching run when its id is in the run name, otherwise
// (scheduled/manual dispatch) via the branch head at the run's start time.
func (s *runSource) resolveHead(run *ghWorkflowRun) {
	if !s.delegated() || run.HeadSHA != "" {
		return
	}

	if run.dispatchRunID != 0 {
		dispatch, err := s.gh.GetRun(s.repo, run.dispatchRunID)
		if err == nil && dispatch.HeadSHA != "" {
			run.HeadSHA = dispatch.HeadSHA
			return
		}
		log.Printf("[collect] Error resolving dispatch run %d for %s run %d: %v",
			run.dispatchRunID, s.ciRepo, run.ID, err)
	}

	ts := run.RunStartedAt
	if ts == "" {
		ts = run.CreatedAt
	}
	sha, err := s.gh.GetBranchHeadSHA(s.repo, run.HeadBranch, ts)
	if err != nil {
		log.Printf("[collect] Error resolving head SHA for %s/%s run %d: %v",
			s.repo, run.HeadBranch, run.ID, err)
		return
	}
	run.HeadSHA = sha
}

// parseDispatchTitle extracts the logical branch and the dispatching run id
// from a delegated run name like "CI: v2.8 29190545294"; the run id part is
// optional ("CI: v2.8 ").
func parseDispatchTitle(title string) (branch string, dispatchRunID int, ok bool) {
	rest, found := strings.CutPrefix(title, "CI: ")
	if !found {
		return "", 0, false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, false
	}
	if len(fields) > 1 {
		dispatchRunID, _ = strconv.Atoi(fields[1])
	}
	return fields[0], dispatchRunID, true
}
