package analyze

import (
	"testing"

	"release-engineer-helper/internal/models"
)

// Shorthand for readable state sequences in tests.
const (
	F = TestFailed
	P = TestPassed
	N = TestNotPresent
)

func TestBaseTestKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Class::test | ORA-00600: internal error", "Class::test"},
		{"Class::test", "Class::test"},
		{"a | b | c", "a"},
		{"", ""},
	}
	for _, c := range cases {
		if got := baseTestKey(c.in); got != c.want {
			t.Errorf("baseTestKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComputePatternStats(t *testing.T) {
	cases := []struct {
		name   string
		states []TestState
		want   PatternStats
	}{
		{
			name:   "empty",
			states: nil,
			want:   PatternStats{FirstFailIdx: -1, LastFailIdx: -1, LastPresent: -1},
		},
		{
			name:   "all not present",
			states: []TestState{N, N},
			want:   PatternStats{FirstFailIdx: -1, LastFailIdx: -1, LastPresent: -1},
		},
		{
			name:   "single session",
			states: []TestState{P, F, F},
			want:   PatternStats{FailCount: 2, PresentCount: 3, SessionCount: 1, FirstFailIdx: 1, LastFailIdx: 2, LastPresent: 2},
		},
		{
			name:   "not-present is transparent inside a session",
			states: []TestState{F, N, F},
			want:   PatternStats{FailCount: 2, PresentCount: 2, SessionCount: 1, FirstFailIdx: 0, LastFailIdx: 2, LastPresent: 2},
		},
		{
			name:   "two sessions",
			states: []TestState{F, N, F, P, F},
			want:   PatternStats{FailCount: 3, PresentCount: 4, SessionCount: 2, FirstFailIdx: 0, LastFailIdx: 4, LastPresent: 4},
		},
	}
	for _, c := range cases {
		if got := ComputePatternStats(c.states); got != c.want {
			t.Errorf("%s: ComputePatternStats = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestClassifyStates(t *testing.T) {
	cases := []struct {
		name   string
		states []TestState
		want   string
	}{
		{"empty", nil, "absent"},
		{"never present", []TestState{N, N}, "absent"},
		{"missing from latest run", []TestState{P, F, N}, "undefined"},
		{"never failed", []TestState{P, P}, "never_failed"},
		{"stable failing", []TestState{P, F, F}, "stable_failing"},
		{"stable failing through gaps", []TestState{F, N, F}, "stable_failing"},
		{"fixed", []TestState{F, P, P}, "fixed"},
		{"fixed with trailing gap before pass", []TestState{F, N, P}, "fixed"},
		{"flaky two sessions", []TestState{F, P, F}, "flaky"},
		{"single failure is stable_failing when last", []TestState{P, P, F}, "stable_failing"},
	}
	for _, c := range cases {
		if got := ClassifyStates(c.states); got != c.want {
			t.Errorf("%s: ClassifyStates(%v) = %q, want %q", c.name, c.states, got, c.want)
		}
	}
}

func TestBuildPattern(t *testing.T) {
	if got := BuildPattern([]TestState{F, P, N}); got != "🔴🟢⚪" {
		t.Errorf("BuildPattern = %q, want 🔴🟢⚪", got)
	}
	if got := BuildPattern(nil); got != "" {
		t.Errorf("BuildPattern(nil) = %q, want empty", got)
	}
}

// Composite keys and metadata for a synthetic three-run history.
var (
	testKeys = []string{
		"aaaaaaaaaaaaaaaa_101",
		"bbbbbbbbbbbbbbbb_102",
		"cccccccccccccccc_103",
	}
	testMeta = map[string]models.RunMeta{
		testKeys[0]: {SHA: "aaaaaaaaaaaaaaaa", RunID: 101, Title: "commit 1", Timestamp: "2026-07-01T10:00:00Z", Link: "https://ci/101", CompositeKey: testKeys[0]},
		testKeys[1]: {SHA: "bbbbbbbbbbbbbbbb", RunID: 102, Title: "commit 2", Timestamp: "2026-07-02T10:00:00Z", Link: "https://ci/102", CompositeKey: testKeys[1]},
		testKeys[2]: {SHA: "cccccccccccccccc", RunID: 103, Title: "commit 3", Timestamp: "2026-07-03T10:00:00Z", Link: "https://ci/103", CompositeKey: testKeys[2]},
	}
)

const (
	testA = "SpecA::a | ORA-00600: internal error"
	testB = "SpecB::b"
	testC = "SpecC::c | timeout"
)

// makeCollectResult builds a history where testA always fails (stable),
// testB failed once and recovered (fixed), testC fails intermittently (flaky).
func makeCollectResult() *models.CollectResult {
	return &models.CollectResult{
		Summary: map[string]models.StringSet{
			testKeys[0]: models.NewStringSet(testA, testB, testC),
			testKeys[1]: models.NewStringSet(testA),
			testKeys[2]: models.NewStringSet(testA, testC),
		},
		Meta: testMeta,
		AllTestDetails: map[string][]models.TestDetail{
			testA: {{File: "spec/a_spec.rb", LineNum: 12, Context: "ORA-00600: internal error", Project: "hydra"}},
			testB: {{File: "spec/b_spec.rb", LineNum: 5, Context: "expected true", Project: "hydra"}},
		},
		AllTestKeys:  map[string]models.StringSet{},
		MasterFailed: models.NewStringSet(testA),
		OrderedKeys:  testKeys,
	}
}

func TestRunClassification(t *testing.T) {
	ar := Run(makeCollectResult())

	sf, ok := ar.Behavior.StableFailing[testA]
	if !ok {
		t.Fatalf("testA must be stable_failing, got stable=%d fixed=%d flaky=%d",
			len(ar.Behavior.StableFailing), len(ar.Behavior.FixedTests), len(ar.Behavior.FlakyTests))
	}
	if sf.Pattern != "🔴🔴🔴" || sf.FailCount != 3 || sf.PresentCount != 3 || sf.TotalRuns != 3 {
		t.Errorf("testA behavior = pattern %q fail %d present %d total %d",
			sf.Pattern, sf.FailCount, sf.PresentCount, sf.TotalRuns)
	}
	if sf.FirstFailRun == nil || *sf.FirstFailRun != 1 || sf.LastFailRun == nil || *sf.LastFailRun != 3 {
		t.Errorf("testA fail runs = %v..%v, want 1..3", sf.FirstFailRun, sf.LastFailRun)
	}
	if len(sf.FailedRuns) != 3 || sf.FailedRuns[0].SHA != "aaaaaaaaaaaaaaaa" || sf.FailedRuns[0].RunNumber != 1 {
		t.Errorf("testA FailedRuns = %+v", sf.FailedRuns)
	}
	if len(sf.Details) != 1 || sf.Details[0].File != "spec/a_spec.rb" {
		t.Errorf("testA details must be propagated, got %+v", sf.Details)
	}

	fx, ok := ar.Behavior.FixedTests[testB]
	if !ok {
		t.Fatal("testB must be fixed")
	}
	if fx.Pattern != "🔴🟢🟢" || fx.FailCount != 1 {
		t.Errorf("testB behavior = pattern %q fail %d", fx.Pattern, fx.FailCount)
	}
	if fx.NextCommitInfo == nil {
		t.Fatal("fixed test must carry NextCommitInfo of the first run after the failure")
	}
	if fx.NextCommitInfo.SHA != "bbbbbbb" || fx.NextCommitInfo.Title != "commit 2" || fx.NextPRLink != "https://ci/102" {
		t.Errorf("testB NextCommitInfo = %+v, NextPRLink = %q", fx.NextCommitInfo, fx.NextPRLink)
	}

	fl, ok := ar.Behavior.FlakyTests[testC]
	if !ok {
		t.Fatal("testC must be flaky")
	}
	if fl.Pattern != "🔴🟢🔴" || fl.FailCount != 2 {
		t.Errorf("testC behavior = pattern %q fail %d", fl.Pattern, fl.FailCount)
	}
}

func TestRunStatistics(t *testing.T) {
	ar := Run(makeCollectResult())

	want := models.Stats{TotalRuns: 3, UniqueFailedTests: 3, MasterFailedTests: 1, NewFailures: 2}
	if ar.Stats != want {
		t.Errorf("Stats = %+v, want %+v", ar.Stats, want)
	}
}

func TestRunDiffs(t *testing.T) {
	ar := Run(makeCollectResult())

	if len(ar.RunDiffs) != 3 {
		t.Fatalf("len(RunDiffs) = %d, want 3", len(ar.RunDiffs))
	}

	d1, d2, d3 := ar.RunDiffs[0], ar.RunDiffs[1], ar.RunDiffs[2]
	if d1.Added.Len() != 3 || d1.Removed.Len() != 0 {
		t.Errorf("diff1: added %d removed %d, want 3/0", d1.Added.Len(), d1.Removed.Len())
	}
	if d2.Added.Len() != 0 || !d2.Removed.Contains(testB) || !d2.Removed.Contains(testC) {
		t.Errorf("diff2: added %v removed %v", d2.Added.ToSlice(), d2.Removed.ToSlice())
	}
	if !d3.Added.Contains(testC) || d3.Added.Len() != 1 {
		t.Errorf("diff3: added %v", d3.Added.ToSlice())
	}
	// OnlyHere = failures not present in master
	if !d3.OnlyHere.Contains(testC) || d3.OnlyHere.Contains(testA) {
		t.Errorf("diff3 OnlyHere = %v, want {testC}", d3.OnlyHere.ToSlice())
	}
	if d3.SHA != "cccccccccccccccc" {
		t.Errorf("diff3 SHA = %q", d3.SHA)
	}
}

func TestRunWithPresenceData(t *testing.T) {
	const testD = "SpecD::d | flaky assert"
	cr := &models.CollectResult{
		Summary: map[string]models.StringSet{
			testKeys[0]: models.NewStringSet(),
			testKeys[1]: models.NewStringSet(testD),
			testKeys[2]: models.NewStringSet(testD),
		},
		Meta: testMeta,
		AllTestKeys: map[string]models.StringSet{
			// base key "SpecD::d" is absent from run 1 → the test did not exist yet
			testKeys[0]: models.NewStringSet("SpecX::x"),
			testKeys[1]: models.NewStringSet("SpecX::x", "SpecD::d"),
			testKeys[2]: models.NewStringSet("SpecX::x", "SpecD::d"),
		},
		MasterFailed: models.NewStringSet(),
		OrderedKeys:  testKeys,
	}

	ar := Run(cr)
	sf, ok := ar.Behavior.StableFailing[testD]
	if !ok {
		t.Fatal("testD must be stable_failing (⚪🔴🔴)")
	}
	if sf.Pattern != "⚪🔴🔴" || sf.PresentCount != 2 || sf.FailCount != 2 {
		t.Errorf("testD behavior = pattern %q present %d fail %d", sf.Pattern, sf.PresentCount, sf.FailCount)
	}
}

func TestRunEmptyHistory(t *testing.T) {
	ar := Run(&models.CollectResult{})
	if len(ar.Behavior.StableFailing)+len(ar.Behavior.FixedTests)+len(ar.Behavior.FlakyTests) != 0 {
		t.Error("empty history must classify nothing")
	}
	if ar.Stats != (models.Stats{}) {
		t.Errorf("empty history Stats = %+v, want zero", ar.Stats)
	}
	if len(ar.RunDiffs) != 0 {
		t.Errorf("empty history RunDiffs = %d, want 0", len(ar.RunDiffs))
	}
}
