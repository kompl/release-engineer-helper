package analyze

import (
	"fmt"
	"strings"

	"release-engineer-helper/internal/models"
)

// Run executes the Analyze phase. Pure computation, no I/O.
// Exact port of Python's TestAnalysisResults methods.
func Run(cr *models.CollectResult) *models.AnalyzeResult {
	behavior := analyzeTestBehavior(cr)
	diffs := getRunDiffs(cr)
	stats := getStatistics(cr)

	fmt.Printf("  [analyze] Results: %d stable failing, %d fixed, %d flaky\n",
		len(behavior.StableFailing), len(behavior.FixedTests), len(behavior.FlakyTests))

	return &models.AnalyzeResult{
		Behavior: behavior,
		RunDiffs: diffs,
		Stats:    stats,
	}
}

// baseTestKey extracts the base key (classname::name) from a full test name
// that may include " | error_message".
func baseTestKey(testName string) string {
	if idx := strings.Index(testName, " | "); idx >= 0 {
		return testName[:idx]
	}
	return testName
}

// hasAllTestKeys returns true if AllTestKeys data is available for at least one run.
// When true, the analyzer uses 3-state logic (failed/passed/not_present).
// For runs without AllTestKeys data (e.g. old cache entries), the per-run guard
// `namesForRun.Len() > 0` in analyzeTestBehavior falls back to TestPassed,
// preserving backward-compatible behavior.
func hasAllTestKeys(cr *models.CollectResult) bool {
	for _, names := range cr.AllTestKeys {
		if names.Len() > 0 {
			return true
		}
	}
	return false
}

// analyzeTestBehavior builds a state matrix for each test across all runs
// and classifies behavior.
func analyzeTestBehavior(cr *models.CollectResult) models.BehaviorAnalysis {
	if len(cr.Summary) == 0 {
		return models.BehaviorAnalysis{
			StableFailing: make(map[string]*models.TestBehavior),
			FixedTests:    make(map[string]*models.TestBehavior),
			FlakyTests:    make(map[string]*models.TestBehavior),
		}
	}

	// Collect all unique test names
	allTests := models.NewStringSet()
	for _, failedSet := range cr.Summary {
		for t := range failedSet {
			allTests.Add(t)
		}
	}

	orderedKeys := cr.OrderedKeys
	usePresence := hasAllTestKeys(cr)

	fmt.Printf("  [analyze] Analyzing behavior of %d unique tests across %d runs (presence data: %v)\n",
		allTests.Len(), len(orderedKeys), usePresence)

	// Build state matrix: test → [TestFailed/TestPassed/TestNotPresent per run]
	testStates := make(map[string][]TestState)
	for t := range allTests {
		states := make([]TestState, len(orderedKeys))
		bk := baseTestKey(t)
		for i, key := range orderedKeys {
			if cr.Summary[key].Contains(t) {
				states[i] = TestFailed
			} else if usePresence {
				namesForRun, hasData := cr.AllTestKeys[key]
				if hasData && !namesForRun.Contains(bk) {
					states[i] = TestNotPresent
				} else {
					states[i] = TestPassed
				}
			} else {
				states[i] = TestPassed
			}
		}
		testStates[t] = states
	}

	stableFailing := make(map[string]*models.TestBehavior)
	fixedTests := make(map[string]*models.TestBehavior)
	flakyTests := make(map[string]*models.TestBehavior)

	for test, states := range testStates {
		behavior := analyzeTestPattern(test, states, orderedKeys, cr)
		switch behavior.Type {
		case "stable_failing":
			stableFailing[test] = behavior
		case "fixed":
			fixedTests[test] = behavior
		case "flaky":
			flakyTests[test] = behavior
		}
		// never_failed is not stored
	}

	return models.BehaviorAnalysis{
		StableFailing: stableFailing,
		FixedTests:    fixedTests,
		FlakyTests:    flakyTests,
	}
}

// PatternStats summarises raw counters extracted from a state sequence.
type PatternStats struct {
	FailCount    int
	PresentCount int
	SessionCount int
	FirstFailIdx int // -1 if never failed
	LastFailIdx  int // -1 if never failed
	LastPresent  int // -1 if all NotPresent
}

// ComputePatternStats counts failure sessions and present runs.
// NotPresent runs are transparent: they don't break a session.
func ComputePatternStats(states []TestState) PatternStats {
	stats := PatternStats{FirstFailIdx: -1, LastFailIdx: -1, LastPresent: -1}
	inSession := false
	for i, s := range states {
		if s == TestNotPresent {
			continue
		}
		stats.PresentCount++
		stats.LastPresent = i
		if s == TestFailed {
			if stats.FirstFailIdx == -1 {
				stats.FirstFailIdx = i
			}
			stats.LastFailIdx = i
			stats.FailCount++
			if !inSession {
				stats.SessionCount++
				inSession = true
			}
		} else {
			inSession = false
		}
	}
	return stats
}

// ClassifyStates returns the behavior type for a sequence of test states.
//   - test never present in any run                  → absent
//   - present earlier, but missing from the latest   → undefined
//     (could be removed, renamed, filtered out, or
//     simply absent from the artifact — we can't tell)
//   - present, never failed                          → never_failed
//   - 1 session ending on the last present run       → stable_failing
//   - 1 session ending earlier                       → fixed
//   - 2+ sessions                                    → flaky
func ClassifyStates(states []TestState) string {
	stats := ComputePatternStats(states)
	if stats.PresentCount == 0 {
		return "absent"
	}
	if len(states) > 0 && states[len(states)-1] == TestNotPresent {
		return "undefined"
	}
	if stats.FailCount == 0 {
		return "never_failed"
	}
	switch {
	case stats.SessionCount >= 2:
		return "flaky"
	case stats.LastFailIdx == stats.LastPresent:
		return "stable_failing"
	default:
		return "fixed"
	}
}

// BuildPattern renders a state sequence as a 🔴/🟢/⚪ string.
func BuildPattern(states []TestState) string {
	var b strings.Builder
	for _, s := range states {
		switch s {
		case TestFailed:
			b.WriteString("🔴")
		case TestPassed:
			b.WriteString("🟢")
		case TestNotPresent:
			b.WriteString("⚪")
		}
	}
	return b.String()
}

// analyzeTestPattern determines the behavior type of a single test.
// See ClassifyStates for the canonical classification rules. Only stable_failing,
// fixed and flaky behaviours are stored downstream; absent / undefined /
// never_failed are not considered actively-failing tests.
func analyzeTestPattern(testName string, states []TestState, compositeKeys []string, cr *models.CollectResult) *models.TestBehavior {
	stats := ComputePatternStats(states)
	failCount := stats.FailCount
	presentCount := stats.PresentCount

	if failCount == 0 {
		return &models.TestBehavior{Type: "never_failed"}
	}

	totalRuns := len(states)
	behaviorType := ClassifyStates(states)

	var firstFailIdx, lastFailIdx *int
	if stats.FirstFailIdx >= 0 {
		v := stats.FirstFailIdx
		firstFailIdx = &v
	}
	if stats.LastFailIdx >= 0 {
		v := stats.LastFailIdx
		lastFailIdx = &v
	}

	// Collect failed run info
	var failedRuns []models.FailedRunInfo
	for i, s := range states {
		if s != TestFailed {
			continue
		}
		key := compositeKeys[i]
		meta := cr.Meta[key]
		sha := meta.SHA
		if sha == "" {
			parts := strings.SplitN(key, "_", 2)
			if len(parts) > 0 {
				sha = parts[0]
			}
		}
		failedRuns = append(failedRuns, models.FailedRunInfo{
			SHA:          sha,
			CompositeKey: key,
			Meta:         meta,
			RunNumber:    i + 1,
		})
	}

	// Find PR/commit info for the first present run after last failure
	var nextPRLink string
	var nextCommitInfo *models.CommitInfo
	if lastFailIdx != nil {
		for i := *lastFailIdx + 1; i < totalRuns; i++ {
			if states[i] == TestNotPresent {
				continue
			}
			nextKey := compositeKeys[i]
			nextMeta := cr.Meta[nextKey]
			nextPRLink = nextMeta.Link
			sha := nextMeta.SHA
			if sha == "" {
				parts := strings.SplitN(nextKey, "_", 2)
				if len(parts) > 0 {
					sha = parts[0]
				}
			}
			nextCommitInfo = &models.CommitInfo{
				SHA:   sha[:min(len(sha), 7)],
				Title: nextMeta.Title,
				TS:    nextMeta.Timestamp,
				Link:  nextPRLink,
			}
			break
		}
	}

	pattern := BuildPattern(states)

	// 1-based run numbers
	var firstRun, lastRun *int
	if firstFailIdx != nil {
		v := *firstFailIdx + 1
		firstRun = &v
	}
	if lastFailIdx != nil {
		v := *lastFailIdx + 1
		lastRun = &v
	}

	details := cr.AllTestDetails[testName]

	return &models.TestBehavior{
		Type:           behaviorType,
		TestName:       testName,
		TotalRuns:      totalRuns,
		FailCount:      failCount,
		PresentCount:   presentCount,
		FirstFailRun:   firstRun,
		LastFailRun:    lastRun,
		FailedRuns:     failedRuns,
		Pattern:        pattern,
		Details:        details,
		NextPRLink:     nextPRLink,
		NextCommitInfo: nextCommitInfo,
	}
}

// getRunDiffs computes the diff between consecutive runs.
// Exact port of Python's get_run_diffs().
func getRunDiffs(cr *models.CollectResult) []models.RunDiff {
	var diffs []models.RunDiff
	prev := models.NewStringSet()
	var prevKey string

	for _, compositeKey := range cr.OrderedKeys {
		curr := cr.Summary[compositeKey]
		added := curr.Difference(prev)
		removed := prev.Difference(curr)

		onlyHere := models.NewStringSet()
		if cr.MasterFailed.Len() > 0 {
			onlyHere = curr.Difference(cr.MasterFailed)
		}

		meta := cr.Meta[compositeKey]
		sha := meta.SHA
		if sha == "" {
			parts := strings.SplitN(compositeKey, "_", 2)
			if len(parts) > 0 {
				sha = parts[0]
			}
		}

		var prevOrder []string
		if prevKey != "" {
			prevOrder = cr.Meta[prevKey].Order
		}

		diffs = append(diffs, models.RunDiff{
			SHA:          sha,
			CompositeKey: compositeKey,
			Meta:         meta,
			Order:        meta.Order,
			PrevOrder:    prevOrder,
			Added:        added,
			Removed:      removed,
			OnlyHere:     onlyHere,
			Current:      curr,
		})

		prev = curr
		prevKey = compositeKey
	}

	return diffs
}

// getStatistics computes aggregate statistics.
// Exact port of Python's get_statistics().
func getStatistics(cr *models.CollectResult) models.Stats {
	if len(cr.Summary) == 0 {
		return models.Stats{}
	}

	allFailed := models.NewStringSet()
	for _, failedSet := range cr.Summary {
		allFailed = allFailed.Union(failedSet)
	}

	newFailures := 0
	if cr.MasterFailed.Len() > 0 {
		newFailures = allFailed.Difference(cr.MasterFailed).Len()
	}

	return models.Stats{
		TotalRuns:         len(cr.Summary),
		UniqueFailedTests: allFailed.Len(),
		MasterFailedTests: cr.MasterFailed.Len(),
		NewFailures:       newFailures,
	}
}
