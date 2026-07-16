package collect

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"release-engineer-helper/v0.1/config"
	"release-engineer-helper/v0.1/internal"
)

const maxPages = 10

// Run executes the Collect phase for a single repo/branch.
// onProgress is called each time a valid run is collected (may be nil).
func Run(token string, cfg *config.Config, cache *Cache, repo, branch string, onProgress func()) *CollectResult {
	gh := NewGitHubClient(token, cfg.GitHub.Owner, cfg.GitHub.WorkflowFile)
	src := newRunSource(gh, repo)

	logExtractor := NewLogExtractor()
	artifactExtractor := NewArtifactExtractor(gh)

	forceRefresh := cfg.Output.ForceRefreshCache
	owner := cfg.GitHub.Owner

	// --- Master failed tests ---
	masterFailed := internal.NewStringSet()
	if branch != cfg.Analysis.MasterBranch {
		fmt.Printf("  [collect] Looking for failed tests in '%s'...\n", cfg.Analysis.MasterBranch)
		masterFailed = getMasterFailed(src, cache, logExtractor, artifactExtractor, owner, cfg.Analysis.MasterBranch, forceRefresh)
		fmt.Printf("  [collect] Found %d failing tests in %s\n", masterFailed.Len(), cfg.Analysis.MasterBranch)
	}

	// --- Collect valid runs with early stopping ---
	fmt.Printf("  [collect] Collecting up to %d valid runs for %s/%s...\n", cfg.Analysis.MaxRuns, repo, branch)
	validRuns, allBranchRunIDs := collectValidRuns(
		src, cache, logExtractor, artifactExtractor,
		owner, branch, cfg.Analysis.MaxRuns, forceRefresh, onProgress,
	)

	if len(validRuns) == 0 {
		fmt.Println("  [collect] No valid runs with test results found")
		return nil
	}

	// Reverse to chronological order (oldest first)
	for i, j := 0, len(validRuns)-1; i < j; i, j = i+1, j-1 {
		validRuns[i], validRuns[j] = validRuns[j], validRuns[i]
	}

	// --- Build summary, meta, allTestDetails ---
	result := buildSummary(src, validRuns)
	result.MasterFailed = masterFailed
	result.AllBranchRunIDs = allBranchRunIDs

	fmt.Printf("  [collect] Built summary: %d runs, %d unique tests\n", len(result.OrderedKeys), len(result.AllTestDetails))
	return result
}

type processedRun struct {
	run         ghWorkflowRun
	details     map[string][]internal.TestDetail
	allTestKeys []string // base keys of ALL tests (passed+failed)
	title       string
}

// candidateRun wraps a workflow run with its global ordering index.
type candidateRun struct {
	index int
	run   ghWorkflowRun
}

// runResult is the outcome of processing a single candidate.
type runResult struct {
	candidate   candidateRun
	details     map[string][]internal.TestDetail
	allTestKeys []string
	title       string
	valid       bool
}

// collectValidRuns paginates GitHub API and processes runs in parallel,
// stopping as soon as maxRuns valid runs are collected.
//
// Architecture:
//   - Paginator goroutine fetches pages and sends completed runs as candidates.
//   - Orchestrator launches maxRuns workers initially.
//   - For each invalid result, a replacement worker is launched for the next candidate.
//   - This keeps maxRuns workers always in flight (no idle time).
//   - Paginator continues collecting all branch run IDs even after enough valid runs are found.
func collectValidRuns(
	src *runSource, cache *Cache,
	logExt *LogExtractor, artExt *ArtifactExtractor,
	owner, branch string,
	maxRuns int,
	forceRefresh bool,
	onProgress func(),
) ([]processedRun, []int) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	candidateCh := make(chan candidateRun, 100)
	allRunIDsCh := make(chan []int, 1)

	// Paginator goroutine: fetches all pages, collects run IDs,
	// sends candidates until cancelled.
	go func() {
		defer close(candidateCh)
		var allIDs []int
		defer func() { allRunIDsCh <- allIDs }()

		cancelled := false
		idx := 0
		for page := 1; page <= maxPages; page++ {
			runs, hasMore, err := src.fetchRunsPage(branch, page)
			if err != nil {
				log.Printf("[collect] Error listing runs page %d: %v", page, err)
				break
			}

			for _, run := range runs {
				allIDs = append(allIDs, run.ID)
				if !cancelled {
					select {
					case candidateCh <- candidateRun{index: idx, run: run}:
						idx++
					case <-ctx.Done():
						cancelled = true
					}
				}
			}

			if !hasMore {
				break
			}
		}
	}()

	resultCh := make(chan runResult, maxRuns)

	// Phase 1: launch up to maxRuns workers
	inFlight := 0
	for i := 0; i < maxRuns; i++ {
		c, ok := <-candidateCh
		if !ok {
			break
		}
		inFlight++
		go processCandidate(cache, logExt, artExt, src, owner, c, forceRefresh, resultCh)
	}

	// Phase 2: collect results, replace invalid runs with next candidates
	var validResults []runResult
	for inFlight > 0 && len(validResults) < maxRuns {
		r := <-resultCh
		inFlight--

		if r.valid {
			validResults = append(validResults, r)
			if onProgress != nil {
				onProgress()
			}
			fmt.Printf("  [collect] Run %d valid (%d/%d)\n", r.candidate.run.ID, len(validResults), maxRuns)
		} else {
			fmt.Printf("  [collect] Run %d has no test results, replacing\n", r.candidate.run.ID)
			c, ok := <-candidateCh
			if ok {
				inFlight++
				go processCandidate(cache, logExt, artExt, src, owner, c, forceRefresh, resultCh)
			}
		}
	}

	// Stop sending candidates; paginator continues collecting IDs from remaining pages
	cancel()
	allRunIDs := <-allRunIDsCh

	// Sort valid results by original API order (newest first)
	sort.Slice(validResults, func(i, j int) bool {
		return validResults[i].candidate.index < validResults[j].candidate.index
	})

	runs := make([]processedRun, len(validResults))
	for i, r := range validResults {
		runs[i] = processedRun{run: r.candidate.run, details: r.details, allTestKeys: r.allTestKeys, title: r.title}
	}

	fmt.Printf("  [collect] Collected %d valid runs, %d total branch run IDs\n", len(runs), len(allRunIDs))
	return runs, allRunIDs
}

func processCandidate(
	cache *Cache, logExt *LogExtractor, artExt *ArtifactExtractor,
	src *runSource, owner string,
	c candidateRun, forceRefresh bool,
	resultCh chan<- runResult,
) {
	entry := loadOrExtract(cache, logExt, artExt, src, owner, c.run.HeadBranch, c.run.ID, forceRefresh)
	title := ""
	if !entry.HasNoTests {
		src.resolveHead(&c.run)
		if c.run.HeadSHA != "" {
			title = src.gh.GetCommitTitle(src.repo, c.run.HeadSHA)
		}
	}
	resultCh <- runResult{
		candidate:   c,
		details:     entry.Details,
		allTestKeys: entry.AllTestKeys,
		title:       title,
		valid:       !entry.HasNoTests,
	}
}

func loadOrExtract(
	cache *Cache, logExt *LogExtractor, artExt *ArtifactExtractor,
	src *runSource, owner, branch string, runID int, forceRefresh bool,
) *CacheEntry {
	// 1. Check cache (keyed by the logical repo regardless of CI delegation)
	if !forceRefresh {
		entry, found := cache.Load(owner, src.repo, runID)
		if found {
			if !entry.HasNoTests {
				fmt.Printf("  [collect] Cache hit for run %d\n", runID)
				return entry
			}
			fmt.Printf("  [collect] Cache invalid for run %d (has_no_tests=true), re-extracting\n", runID)
		}
	}

	// 2. Try artifacts first
	er := artExt.Extract(src.dataRepo(), runID)
	isValid := !er.HasNoTests || len(er.Details) > 0

	// 3. Fallback to logs (no all_test_names available from logs)
	if !isValid {
		fmt.Printf("  [collect] Fallback to logs for run %d\n", runID)
		logBytes, err := src.gh.DownloadLogs(src.dataRepo(), runID)
		if err == nil && len(logBytes) > 0 {
			altDetails, altHasNoTests := logExt.ParseZip(logBytes)
			if !altHasNoTests {
				er.Details = altDetails
				er.HasNoTests = altHasNoTests
			}
		}
	}

	// 4. Save to cache
	if err := cache.Save(owner, src.repo, branch, runID, er.Details, er.AllTestKeys, er.HasNoTests); err != nil {
		log.Printf("[collect] Error saving to cache for run %d: %v", runID, err)
	}

	return &CacheEntry{
		Details:     er.Details,
		AllTestKeys: er.AllTestKeys,
		HasNoTests:  er.HasNoTests,
	}
}

func getMasterFailed(
	src *runSource, cache *Cache,
	logExt *LogExtractor, artExt *ArtifactExtractor,
	owner, masterBranch string, forceRefresh bool,
) internal.StringSet {
	run := src.latestCompletedRun(masterBranch)
	if run == nil {
		return internal.NewStringSet()
	}

	entry := loadOrExtract(cache, logExt, artExt, src, owner, run.HeadBranch, run.ID, forceRefresh)
	if entry.HasNoTests || len(entry.Details) == 0 {
		return internal.NewStringSet()
	}

	result := internal.NewStringSet()
	for testName := range entry.Details {
		result.Add(testName)
	}
	return result
}

func buildSummary(src *runSource, runs []processedRun) *CollectResult {
	result := &CollectResult{
		Summary:        make(map[string]internal.StringSet),
		Meta:           make(map[string]internal.RunMeta),
		AllTestDetails: make(map[string][]internal.TestDetail),
		AllTestKeys:    make(map[string]internal.StringSet),
	}

	var orderedKeys []string

	for _, pr := range runs {
		run := pr.run
		sha := run.HeadSHA
		runID := run.ID
		compositeKey := fmt.Sprintf("%s_%d", sha, runID)

		title := pr.title
		ts := parseTimestamp(run.RunStartedAt, run.CreatedAt)
		// The run link points at the repo where the tests actually ran
		link := run.HTMLURL
		if link == "" {
			link = src.gh.RunURL(src.dataRepo(), runID)
		}

		// Build ordered list of test names, sorted by order_index
		// (Python dict preserves insertion order; Go map does not,
		// so we reconstruct order from the order_index in TestDetail)
		failedOrder := make([]string, 0, len(pr.details))
		for testName := range pr.details {
			failedOrder = append(failedOrder, testName)
		}
		sort.Slice(failedOrder, func(i, j int) bool {
			oi, oj := math.MaxInt, math.MaxInt
			if items := pr.details[failedOrder[i]]; len(items) > 0 {
				oi = items[0].OrderIndex
			}
			if items := pr.details[failedOrder[j]]; len(items) > 0 {
				oj = items[0].OrderIndex
			}
			return oi < oj
		})

		failedSet := internal.NewStringSet(failedOrder...)

		// Merge test details (append, not overwrite — same test may fail in multiple runs)
		for testName, items := range pr.details {
			result.AllTestDetails[testName] = append(result.AllTestDetails[testName], items...)
		}

		result.Summary[compositeKey] = failedSet
		if len(pr.allTestKeys) > 0 {
			result.AllTestKeys[compositeKey] = internal.NewStringSet(pr.allTestKeys...)
		}
		result.Meta[compositeKey] = internal.RunMeta{
			SHA:          sha,
			RunID:        runID,
			Title:        title,
			Timestamp:    ts,
			Conclusion:   run.Conclusion,
			Link:         link,
			Branch:       run.HeadBranch,
			Order:        failedOrder,
			CompositeKey: compositeKey,
		}
		orderedKeys = append(orderedKeys, compositeKey)
	}

	result.OrderedKeys = orderedKeys
	return result
}

func parseTimestamp(runStartedAt, createdAt string) string {
	raw := runStartedAt
	if raw == "" {
		raw = createdAt
	}
	raw = strings.Replace(raw, "Z", "+00:00", 1)

	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Try alternate format
		t, err = time.Parse("2006-01-02T15:04:05+00:00", raw)
		if err != nil {
			return raw
		}
	}
	return t.Format("2006-01-02 15:04:05")
}
