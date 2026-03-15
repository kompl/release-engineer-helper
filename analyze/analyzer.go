package analyze

import (
	"fmt"
	"strings"

	"release-engineer-helper/v0.1/collect"
	"release-engineer-helper/v0.1/internal"
)

// Run executes the Analyze phase. Pure computation, no I/O.
// Exact port of Python's TestAnalysisResults methods.
func Run(cr *collect.CollectResult) *AnalyzeResult {
	behavior := analyzeTestBehavior(cr)
	diffs := getRunDiffs(cr)
	stats := getStatistics(cr)

	fmt.Printf("  [analyze] Results: %d stable failing, %d fixed, %d flaky\n",
		len(behavior.StableFailing), len(behavior.FixedTests), len(behavior.FlakyTests))

	return &AnalyzeResult{
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
func hasAllTestKeys(cr *collect.CollectResult) bool {
	for _, names := range cr.AllTestKeys {
		if names.Len() > 0 {
			return true
		}
	}
	return false
}

// analyzeTestBehavior builds a state matrix for each test across all runs
// and classifies behavior.
func analyzeTestBehavior(cr *collect.CollectResult) BehaviorAnalysis {
	if len(cr.Summary) == 0 {
		return BehaviorAnalysis{
			StableFailing: make(map[string]*TestBehavior),
			FixedTests:    make(map[string]*TestBehavior),
			FlakyTests:    make(map[string]*TestBehavior),
		}
	}

	// Collect all unique test names
	allTests := internal.NewStringSet()
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

	stableFailing := make(map[string]*TestBehavior)
	fixedTests := make(map[string]*TestBehavior)
	flakyTests := make(map[string]*TestBehavior)

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
		// single_failure and never_failed are not stored
	}

	return BehaviorAnalysis{
		StableFailing: stableFailing,
		FixedTests:    fixedTests,
		FlakyTests:    flakyTests,
	}
}

// analyzeTestPattern determines the behavior type of a single test.
// States can be TestFailed, TestPassed, or TestNotPresent.
// Runs where test is not present are skipped for behavior analysis.
func analyzeTestPattern(testName string, states []TestState, compositeKeys []string, cr *collect.CollectResult) *TestBehavior {
	var firstFailIdx, lastFailIdx *int
	failCount := 0
	presentCount := 0

	for i, s := range states {
		if s == TestNotPresent {
			continue
		}
		presentCount++
		if s == TestFailed {
			if firstFailIdx == nil {
				idx := i
				firstFailIdx = &idx
			}
			idx := i
			lastFailIdx = &idx
			failCount++
		}
	}

	if failCount == 0 {
		return &TestBehavior{Type: "never_failed"}
	}

	totalRuns := len(states)

	// Find last present run index (for stable_failing check)
	lastPresentIdx := -1
	for i := totalRuns - 1; i >= 0; i-- {
		if states[i] != TestNotPresent {
			lastPresentIdx = i
			break
		}
	}

	// Determine behavior type
	var behaviorType string
	if failCount == 1 {
		behaviorType = "single_failure"
	} else if *firstFailIdx == *lastFailIdx {
		behaviorType = "single_failure"
	} else if lastPresentIdx >= 0 && *lastFailIdx == lastPresentIdx {
		// Find the start of the last continuous failure block (ignoring NotPresent gaps)
		streakStart := findLastStreakStart(states)
		if isStableFailingFrom(states, streakStart) {
			behaviorType = "stable_failing"
		} else {
			behaviorType = "flaky"
		}
	} else {
		if hasFlakyBehavior(states) {
			behaviorType = "flaky"
		} else {
			behaviorType = "fixed"
		}
	}

	// Collect failed run info
	var failedRuns []FailedRunInfo
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
		failedRuns = append(failedRuns, FailedRunInfo{
			SHA:          sha,
			CompositeKey: key,
			Meta:         meta,
			RunNumber:    i + 1,
		})
	}

	// Find PR/commit info for the first present run after last failure
	var nextPRLink string
	var nextCommitInfo *CommitInfo
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
			nextCommitInfo = &CommitInfo{
				SHA:   sha[:min(len(sha), 7)],
				Title: nextMeta.Title,
				TS:    nextMeta.Timestamp,
				Link:  nextPRLink,
			}
			break
		}
	}

	// Build pattern string (⚪ = not present)
	var patternBuilder strings.Builder
	for _, s := range states {
		switch s {
		case TestFailed:
			patternBuilder.WriteString("🔴")
		case TestPassed:
			patternBuilder.WriteString("🟢")
		case TestNotPresent:
			patternBuilder.WriteString("⚪")
		}
	}

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

	return &TestBehavior{
		Type:           behaviorType,
		TestName:       testName,
		TotalRuns:      totalRuns,
		FailCount:      failCount,
		PresentCount:   presentCount,
		FirstFailRun:   firstRun,
		LastFailRun:    lastRun,
		FailedRuns:     failedRuns,
		Pattern:        patternBuilder.String(),
		Details:        details,
		NextPRLink:     nextPRLink,
		NextCommitInfo: nextCommitInfo,
	}
}

// findLastStreakStart finds the start of the last continuous failure block,
// treating NotPresent as transparent (not breaking the streak).
// Walks backward from the end, skipping NotPresent and Failed, stopping at Passed.
func findLastStreakStart(states []TestState) int {
	if len(states) == 0 {
		return 0
	}
	start := len(states) - 1
	for start > 0 {
		if states[start-1] == TestPassed {
			break
		}
		start--
	}
	// Skip leading NotPresent to find the first actual Failed
	for start < len(states) && states[start] == TestNotPresent {
		start++
	}
	return start
}

// isStableFailingFrom checks if the test fails in all present runs from startIdx to the end.
// Runs where test is not present are skipped.
// Returns false if no present runs exist (test was removed).
func isStableFailingFrom(states []TestState, startIdx int) bool {
	if startIdx >= len(states) {
		return false
	}
	foundPresent := false
	for i := startIdx; i < len(states); i++ {
		if states[i] == TestNotPresent {
			continue
		}
		foundPresent = true
		if states[i] != TestFailed {
			return false
		}
	}
	return foundPresent
}

// hasFlakyBehavior checks if a test has alternating pass/fail pattern.
// Skips runs where test is not present.
func hasFlakyBehavior(states []TestState) bool {
	if len(states) < 2 {
		return false
	}
	transitions := 0
	var lastPresent TestState
	first := true
	for _, s := range states {
		if s == TestNotPresent {
			continue
		}
		if !first && s != lastPresent {
			transitions++
		}
		lastPresent = s
		first = false
	}
	return transitions > 2
}

// getRunDiffs computes the diff between consecutive runs.
// Exact port of Python's get_run_diffs().
func getRunDiffs(cr *collect.CollectResult) []RunDiff {
	var diffs []RunDiff
	prev := internal.NewStringSet()
	var prevKey string

	for _, compositeKey := range cr.OrderedKeys {
		curr := cr.Summary[compositeKey]
		added := curr.Difference(prev)
		removed := prev.Difference(curr)

		onlyHere := internal.NewStringSet()
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

		diffs = append(diffs, RunDiff{
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
func getStatistics(cr *collect.CollectResult) Stats {
	if len(cr.Summary) == 0 {
		return Stats{}
	}

	allFailed := internal.NewStringSet()
	for _, failedSet := range cr.Summary {
		allFailed = allFailed.Union(failedSet)
	}

	newFailures := 0
	if cr.MasterFailed.Len() > 0 {
		newFailures = allFailed.Difference(cr.MasterFailed).Len()
	}

	return Stats{
		TotalRuns:         len(cr.Summary),
		UniqueFailedTests: allFailed.Len(),
		MasterFailedTests: cr.MasterFailed.Len(),
		NewFailures:       newFailures,
	}
}
