package render

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"release-engineer-helper/config"
	"release-engineer-helper/internal/models"
)

// RenderJSON generates the combined JSON report for all repo/branch pairs and
// writes it to a timestamped file in output.dir.
func RenderJSON(results []models.RepoResult, cfg *config.Config) error {
	now := time.Now()

	data, err := buildJSONReport(results, cfg, now)
	if err != nil {
		return err
	}

	filename := fmt.Sprintf("report_%s.json", now.Format("20060102_150405"))
	reportPath := filepath.Join(cfg.Output.Dir, filename)
	if err := os.MkdirAll(filepath.Dir(reportPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	if err := os.WriteFile(reportPath, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", reportPath, err)
	}

	fmt.Printf("  [render] Generated JSON: %s\n", reportPath)
	return nil
}

// RenderJSONTo writes the same combined JSON report to w, so callers can pipe
// it (e.g. into jq) instead of leaving a file behind.
func RenderJSONTo(w io.Writer, results []models.RepoResult, cfg *config.Config) error {
	data, err := buildJSONReport(results, cfg, time.Now())
	if err != nil {
		return err
	}

	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	return nil
}

// buildJSONReport assembles the report for all repo/branch pairs and marshals it.
func buildJSONReport(results []models.RepoResult, cfg *config.Config, now time.Time) ([]byte, error) {
	report := jsonReport{
		GeneratedAt: now.Format(time.RFC3339),
		Projects:    make(map[string]jsonProject),
	}

	for _, r := range results {
		key := fmt.Sprintf("%s/%s", r.Repo, r.Branch)
		report.Projects[key] = buildRepoJSONData(r, cfg)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	return data, nil
}

type jsonReport struct {
	GeneratedAt string                 `json:"generated_at"`
	Projects    map[string]jsonProject `json:"projects"`
}

type jsonProject struct {
	Repo                     string            `json:"repo"`
	Branch                   string            `json:"branch"`
	MasterBranch             string            `json:"master_branch"`
	LatestRun                jsonRunWithTests  `json:"latest_run"`
	LatestRunWithTestResults *jsonRunWithTests `json:"latest_run_with_test_results,omitempty"`
	Additional               []jsonFailedTest  `json:"additional,omitempty"`
	Summary                  jsonSummary       `json:"summary"`
}

type jsonRunWithTests struct {
	RunID       int              `json:"run_id"`
	SHA         string           `json:"sha"`
	CommitTitle string           `json:"commit_title"`
	Timestamp   string           `json:"timestamp"`
	Conclusion  string           `json:"conclusion"`
	Link        string           `json:"link"`
	TotalFailed int              `json:"total_failed"`
	FailedTests []jsonFailedTest `json:"failed_tests"`
}

type jsonSummary struct {
	TotalRunsAnalyzed  int `json:"total_runs_analyzed"`
	UniqueFailedTests  int `json:"unique_failed_tests"`
	MasterFailedTests  int `json:"master_failed_tests"`
	NewFailures        int `json:"new_failures"`
	StableFailingCount int `json:"stable_failing_count"`
	FixedCount         int `json:"fixed_count"`
	FlakyCount         int `json:"flaky_count"`
}

type jsonFailedTest struct {
	TestName       string             `json:"test_name"`
	ErrorMessage   string             `json:"error_message"`
	Classification string             `json:"classification"`
	InMaster       bool               `json:"in_master"`
	Project        string             `json:"project"`
	FailRatePct    *float64           `json:"fail_rate_pct,omitempty"`
	Pattern        string             `json:"pattern,omitempty"`
	ProbableCause  *jsonProbableCause `json:"probable_cause,omitempty"`
	FailingSince   *jsonFailingSince  `json:"failing_since,omitempty"`
	FirstSeen      *jsonFirstSeen     `json:"first_seen_in_analysis,omitempty"`
	FlakyInfo      *jsonFlakyInfo     `json:"flaky_info,omitempty"`
	LastFailed     *jsonLastFailed    `json:"last_failed,omitempty"`
	ProbableFix    *jsonProbableFix   `json:"probable_fix,omitempty"`
}

type jsonProbableCause struct {
	SHA          string `json:"sha"`
	CommitTitle  string `json:"commit_title"`
	Timestamp    string `json:"timestamp"`
	RunLink      string `json:"run_link"`
	StreakLength int    `json:"streak_length"`
}

type jsonFailingSince struct {
	RunID int    `json:"run_id"`
	Date  string `json:"date"`
}

type jsonFirstSeen struct {
	Timestamp string `json:"timestamp"`
	Commit    string `json:"commit"`
	RunLink   string `json:"run_link"`
}

type jsonFlakyInfo struct {
	FailCount    int `json:"fail_count"`
	TotalRuns    int `json:"total_runs"`
	PresentCount int `json:"present_count"` // runs where test actually existed
}

type jsonLastFailed struct {
	RunID     int    `json:"run_id"`
	Timestamp string `json:"timestamp"`
	RunLink   string `json:"run_link"`
}

type jsonProbableFix struct {
	SHA         string `json:"sha"`
	CommitTitle string `json:"commit_title"`
	Timestamp   string `json:"timestamp"`
	RunLink     string `json:"run_link"`
}

func buildRepoJSONData(r models.RepoResult, cfg *config.Config) jsonProject {
	cr := r.Collect
	ar := r.Analyze

	stableFailing := ar.Behavior.StableFailing
	fixedTests := ar.Behavior.FixedTests
	flakyTests := ar.Behavior.FlakyTests

	allBehavior := make(map[string]*models.TestBehavior)
	for k, v := range stableFailing {
		allBehavior[k] = v
	}
	for k, v := range fixedTests {
		allBehavior[k] = v
	}
	for k, v := range flakyTests {
		allBehavior[k] = v
	}

	orderedKeys := cr.OrderedKeys
	var latestKey string
	if len(orderedKeys) > 0 {
		latestKey = orderedKeys[len(orderedKeys)-1]
	}
	latestMeta := cr.Meta[latestKey]
	latestFailed := cr.Summary[latestKey]

	latestRun := jsonRunWithTests{
		RunID:       latestMeta.RunID,
		SHA:         latestMeta.SHA,
		CommitTitle: latestMeta.Title,
		Timestamp:   latestMeta.Timestamp,
		Conclusion:  latestMeta.Conclusion,
		Link:        latestMeta.Link,
		TotalFailed: latestFailed.Len(),
		FailedTests: buildFailedTests(r, latestFailed, allBehavior, latestMeta, false),
	}

	project := jsonProject{
		Repo:         r.Repo,
		Branch:       r.Branch,
		MasterBranch: cfg.Analysis.MasterBranch,
		LatestRun:    latestRun,
		Summary: jsonSummary{
			TotalRunsAnalyzed:  ar.Stats.TotalRuns,
			UniqueFailedTests:  ar.Stats.UniqueFailedTests,
			MasterFailedTests:  ar.Stats.MasterFailedTests,
			NewFailures:        ar.Stats.NewFailures,
			StableFailingCount: len(stableFailing),
			FixedCount:         len(fixedTests),
			FlakyCount:         len(flakyTests),
		},
	}

	// If the latest run failed before tests ran, find the last run with test results
	if latestFailed.Len() == 0 && latestMeta.Conclusion == "failure" {
		for i := len(orderedKeys) - 2; i >= 0; i-- {
			key := orderedKeys[i]
			failed := cr.Summary[key]
			if failed.Len() > 0 {
				meta := cr.Meta[key]
				project.LatestRunWithTestResults = &jsonRunWithTests{
					RunID:       meta.RunID,
					SHA:         meta.SHA,
					CommitTitle: meta.Title,
					Timestamp:   meta.Timestamp,
					Conclusion:  meta.Conclusion,
					Link:        meta.Link,
					TotalFailed: failed.Len(),
					FailedTests: buildFailedTests(r, failed, allBehavior, meta, true),
				}
				break
			}
		}
	}

	project.Additional = buildAdditional(r, project, allBehavior, latestMeta)

	return project
}

// buildAdditional lists historical data for every classified test that is not
// already mentioned in the failed_tests of latest_run / latest_run_with_test_results
// (fixed tests and flaky/stable ones that did not fail in those runs).
func buildAdditional(
	r models.RepoResult,
	project jsonProject,
	allBehavior map[string]*models.TestBehavior,
	latestMeta models.RunMeta,
) []jsonFailedTest {
	mentioned := models.NewStringSet()
	for _, t := range project.LatestRun.FailedTests {
		mentioned.Add(t.TestName)
	}
	if project.LatestRunWithTestResults != nil {
		for _, t := range project.LatestRunWithTestResults.FailedTests {
			mentioned.Add(t.TestName)
		}
	}

	rest := models.NewStringSet()
	for testName := range allBehavior {
		if !mentioned.Contains(testName) {
			rest.Add(testName)
		}
	}
	if rest.Len() == 0 {
		return nil
	}

	additional := buildFailedTests(r, rest, allBehavior, latestMeta, true)

	// Actionable first: still-failing tests ahead of fixed ones
	rank := map[string]int{"stable_failing": 0, "flaky": 1, "fixed": 2}
	sort.SliceStable(additional, func(i, j int) bool {
		return rank[additional[i].Classification] < rank[additional[j].Classification]
	})
	return additional
}

// withHistory adds last_failed / probable_fix — retrospective fields that only
// make sense for tests not taken from the run being described.
func buildFailedTests(
	r models.RepoResult,
	failedSet models.StringSet,
	allBehavior map[string]*models.TestBehavior,
	runMeta models.RunMeta,
	withHistory bool,
) []jsonFailedTest {
	cr := r.Collect
	ar := r.Analyze
	er := r.Enrich

	stableFailing := ar.Behavior.StableFailing
	fixedTests := ar.Behavior.FixedTests
	flakyTests := ar.Behavior.FlakyTests
	orderedKeys := cr.OrderedKeys

	testNames := failedSet.ToSlice()
	sort.Strings(testNames)

	var failedTests []jsonFailedTest
	for _, testName := range testNames {
		detailsItems := cr.AllTestDetails[testName]
		errorMsg := extractError(detailsItems)
		project := extractProject(detailsItems)
		classification := classifyTest(testName, stableFailing, fixedTests, flakyTests)

		entry := jsonFailedTest{
			TestName:       testName,
			ErrorMessage:   errorMsg,
			Classification: classification,
			InMaster:       cr.MasterFailed.Contains(testName),
			Project:        project,
		}

		behavior := allBehavior[testName]
		if behavior != nil {
			total := behavior.PresentCount
			if total == 0 {
				total = behavior.TotalRuns
			}
			total = max(total, 1)
			rate := float64(behavior.FailCount) / float64(total) * 100
			roundedRate := float64(int(rate*10)) / 10
			entry.FailRatePct = &roundedRate
			entry.Pattern = behavior.Pattern
		}

		// Probable cause (only for stable_failing — flaky patterns have no meaningful streak)
		if behavior != nil && behavior.Type == "stable_failing" {
			streak := findStreakStart(behavior.Pattern, orderedKeys, cr.Meta)
			if streak != nil {
				entry.ProbableCause = streak
			}
		}
		if entry.ProbableCause == nil && behavior == nil {
			entry.ProbableCause = &jsonProbableCause{
				SHA:          runMeta.SHA,
				CommitTitle:  runMeta.Title,
				Timestamp:    runMeta.Timestamp,
				RunLink:      runMeta.Link,
				StreakLength: 1,
			}
		}

		// Stable failing: add duration info
		if _, ok := stableFailing[testName]; ok {
			if er != nil {
				if since, ok := er.StableSince[testName]; ok {
					entry.FailingSince = &jsonFailingSince{
						RunID: since.RunID,
						Date:  since.CreatedAt,
					}
				}
			}
			sf := stableFailing[testName]
			if len(sf.FailedRuns) > 0 {
				first := sf.FailedRuns[0]
				entry.FirstSeen = &jsonFirstSeen{
					Timestamp: first.Meta.Timestamp,
					Commit:    first.Meta.Title,
					RunLink:   first.Meta.Link,
				}
			}
		}

		// Flaky info
		if fi, ok := flakyTests[testName]; ok {
			entry.FlakyInfo = &jsonFlakyInfo{
				FailCount:    fi.FailCount,
				TotalRuns:    fi.TotalRuns,
				PresentCount: fi.PresentCount,
			}
		}

		if withHistory && behavior != nil {
			if n := len(behavior.FailedRuns); n > 0 {
				last := behavior.FailedRuns[n-1]
				entry.LastFailed = &jsonLastFailed{
					RunID:     last.Meta.RunID,
					Timestamp: last.Meta.Timestamp,
					RunLink:   last.Meta.Link,
				}
			}
			if behavior.Type == "fixed" && behavior.NextCommitInfo != nil {
				fix := behavior.NextCommitInfo
				entry.ProbableFix = &jsonProbableFix{
					SHA:         fix.SHA,
					CommitTitle: fix.Title,
					Timestamp:   fix.TS,
					RunLink:     fix.Link,
				}
			}
		}

		failedTests = append(failedTests, entry)
	}

	return failedTests
}

func extractError(items []models.TestDetail) string {
	if len(items) == 0 {
		return ""
	}
	msg := strings.TrimSpace(items[0].Context)
	// Collapse multiline error into a single line
	msg = strings.Join(strings.Fields(msg), " ")
	// Truncate to keep JSON readable (max ~300 chars)
	const maxLen = 300
	runes := []rune(msg)
	if len(runes) > maxLen {
		msg = string(runes[:maxLen]) + "…"
	}
	return msg
}

func extractProject(items []models.TestDetail) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].Project
}

func classifyTest(testName string, stable, fixed, flaky map[string]*models.TestBehavior) string {
	if _, ok := stable[testName]; ok {
		return "stable_failing"
	}
	if _, ok := fixed[testName]; ok {
		return "fixed"
	}
	if _, ok := flaky[testName]; ok {
		return "flaky"
	}
	return "stable_failing"
}

// findStreakStart finds the run that started the current consecutive failure streak.
// Port of Python's _find_streak_start().
func findStreakStart(pattern string, orderedKeys []string, meta map[string]models.RunMeta) *jsonProbableCause {
	if pattern == "" {
		return nil
	}

	// Check if last character is 🔴
	runes := []rune(pattern)
	if len(runes) == 0 {
		return nil
	}

	redCircle, _ := utf8.DecodeRuneInString("🔴")
	greenCircle, _ := utf8.DecodeRuneInString("🟢")
	whiteCircle, _ := utf8.DecodeRuneInString("⚪")
	_ = greenCircle

	// Count emoji positions (each emoji is one "logical" position)
	var positions []rune
	for _, r := range runes {
		if r == redCircle || r == greenCircle || r == whiteCircle {
			positions = append(positions, r)
		}
	}

	// Find last non-⚪ symbol; stable_failing patterns may have trailing ⚪
	lastNonWhite := len(positions) - 1
	for lastNonWhite >= 0 && positions[lastNonWhite] == whiteCircle {
		lastNonWhite--
	}
	if lastNonWhite < 0 || positions[lastNonWhite] != redCircle {
		return nil
	}

	// Walk backwards through 🔴 and ⚪, then advance idx to the first 🔴
	idx := len(positions) - 1
	for idx > 0 && (positions[idx-1] == redCircle || positions[idx-1] == whiteCircle) {
		idx--
	}
	// Skip past any leading ⚪ to find the actual first 🔴
	for idx < len(positions) && positions[idx] == whiteCircle {
		idx++
	}

	if idx >= len(orderedKeys) {
		return nil
	}

	// Count only 🔴 in the streak (exclude ⚪)
	streakLength := 0
	for i := idx; i < len(positions); i++ {
		if positions[i] == redCircle {
			streakLength++
		}
	}

	key := orderedKeys[idx]
	runMeta := meta[key]
	return &jsonProbableCause{
		SHA:          runMeta.SHA,
		CommitTitle:  runMeta.Title,
		Timestamp:    runMeta.Timestamp,
		RunLink:      runMeta.Link,
		StreakLength: streakLength,
	}
}
