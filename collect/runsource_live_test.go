package collect

import (
	"os"
	"testing"
)

// TestLiveHomsRunSource hits the real GitHub API; runs only with GITHUB_TOKEN set.
func TestLiveHomsRunSource(t *testing.T) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Skip("GITHUB_TOKEN not set")
	}

	gh := NewGitHubClient(token, "hydra-billing", "ci.yml")
	src := newRunSource(gh, "homs")

	for _, branch := range []string{"v2.8", "master"} {
		runs, hasMore, err := src.fetchRunsPage(branch, 1)
		if err != nil {
			t.Fatalf("fetchRunsPage(%s): %v", branch, err)
		}
		t.Logf("%s: %d completed runs on page 1, hasMore=%v", branch, len(runs), hasMore)
		if len(runs) == 0 {
			t.Errorf("expected at least one completed run for %s", branch)
			continue
		}

		run := runs[0]
		if run.HeadBranch != branch {
			t.Errorf("run %d: HeadBranch=%q, want %q", run.ID, run.HeadBranch, branch)
		}
		if run.HeadSHA != "" {
			t.Errorf("run %d: HeadSHA must be empty before resolveHead, got %q", run.ID, run.HeadSHA)
		}

		src.resolveHead(&run)
		if run.HeadSHA == "" {
			t.Errorf("run %d (dispatchRunID=%d): resolveHead left HeadSHA empty", run.ID, run.dispatchRunID)
			continue
		}
		title := gh.GetCommitTitle("homs", run.HeadSHA)
		t.Logf("%s: run %d (dispatch %d) → sha %.7s %q link %s",
			branch, run.ID, run.dispatchRunID, run.HeadSHA, title, run.HTMLURL)
	}

	// Both resolution paths must be covered: via dispatch run and via branch head
	if run := src.latestCompletedRun("master"); run == nil {
		t.Error("latestCompletedRun(master) returned nil")
	}
}
