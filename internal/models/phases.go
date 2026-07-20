package models

// Contract types passed between pipeline phases
// (collect → analyze → enrich → render).

// CollectResult is the output of the Collect phase.
type CollectResult struct {
	Summary         map[string]StringSet    // compositeKey → set of failed test names
	Meta            map[string]RunMeta      // compositeKey → run metadata
	AllTestDetails  map[string][]TestDetail // testName → detail items
	AllTestKeys     map[string]StringSet    // compositeKey → set of ALL test base keys (passed+failed)
	MasterFailed    StringSet               // tests failing in master
	AllBranchRunIDs []int                   // ALL completed run IDs for the branch
	OrderedKeys     []string                // composite keys in chronological order (oldest first)
}

// StableSinceInfo holds the earliest run info for a stable-failing test.
type StableSinceInfo struct {
	RunID     int    `json:"run_id"`
	CreatedAt string `json:"created_at"`
}

// AnalyzeResult is the output of the Analyze phase.
type AnalyzeResult struct {
	Behavior BehaviorAnalysis
	RunDiffs []RunDiff
	Stats    Stats
}

// BehaviorAnalysis contains classified test behaviors.
type BehaviorAnalysis struct {
	StableFailing map[string]*TestBehavior
	FixedTests    map[string]*TestBehavior
	FlakyTests    map[string]*TestBehavior
}

// TestBehavior describes the behavior pattern of a single test.
type TestBehavior struct {
	Type           string          `json:"type"` // stable_failing, fixed, flaky
	TestName       string          `json:"test_name"`
	TotalRuns      int             `json:"total_runs"`
	PresentCount   int             `json:"present_count"` // runs where test actually existed
	FailCount      int             `json:"fail_count"`
	FirstFailRun   *int            `json:"first_fail_run"` // 1-based, nil if never failed
	LastFailRun    *int            `json:"last_fail_run"`  // 1-based, nil if never failed
	FailedRuns     []FailedRunInfo `json:"failed_runs"`
	Pattern        string          `json:"pattern"` // 🔴=fail, 🟢=pass, ⚪=not present
	Details        []TestDetail    `json:"details"`
	NextPRLink     string          `json:"next_pr_link,omitempty"`
	NextCommitInfo *CommitInfo     `json:"next_commit_info,omitempty"`
}

// FailedRunInfo contains info about a single failed run for a test.
type FailedRunInfo struct {
	SHA          string  `json:"sha"`
	CompositeKey string  `json:"composite_key"`
	Meta         RunMeta `json:"meta"`
	RunNumber    int     `json:"run_number"` // 1-based
}

// CommitInfo for the commit that fixed a test.
type CommitInfo struct {
	SHA   string `json:"sha"`
	Title string `json:"title"`
	TS    string `json:"ts"`
	Link  string `json:"link"`
}

// RunDiff represents the difference in failed tests between two consecutive runs.
type RunDiff struct {
	SHA          string    `json:"sha"`
	CompositeKey string    `json:"composite_key"`
	Meta         RunMeta   `json:"meta"`
	Order        []string  `json:"order"`
	PrevOrder    []string  `json:"prev_order"`
	Added        StringSet `json:"added"`
	Removed      StringSet `json:"removed"`
	OnlyHere     StringSet `json:"only_here"`
	Current      StringSet `json:"current"`
}

// Stats contains aggregate statistics from the analysis.
type Stats struct {
	TotalRuns         int `json:"total_runs"`
	UniqueFailedTests int `json:"unique_failed_tests"`
	MasterFailedTests int `json:"master_failed_tests"`
	NewFailures       int `json:"new_failures"`
}

// EnrichResult is the output of the Enrich phase.
type EnrichResult struct {
	StableSince map[string]StableSinceInfo // testName → earliest run info
}

// RepoResult groups all phase results for a single repo/branch.
type RepoResult struct {
	Repo    string
	Branch  string
	Collect *CollectResult
	Analyze *AnalyzeResult
	Enrich  *EnrichResult
}
