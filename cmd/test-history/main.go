// test-history queries MongoDB for the runs of a specific (owner, repo[, branch])
// and reports pattern/classification statistics per matched test. The --test
// argument is treated as a case-insensitive regex (substring works as-is); pass
// --exact-match for a literal match. Output is always JSON on stdout.
//
// If --branch is omitted, each matched test is reported across every branch
// found in the cache.
//
// Usage:
//   go run ./cmd/test-history --repo hydra-server --test "JobScheduleRoutesSpecIO" --base-key
//   go run ./cmd/test-history --repo hocs --branch master --test "test_billing" --base-key
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"release-engineer-helper/v0.1/analyze"
)

type runDoc struct {
	RunID       int64    `bson:"run_id"`
	Branch      string   `bson:"branch"`
	HasNoTests  bool     `bson:"has_no_tests"`
	AllTestKeys []string `bson:"all_test_keys"`
	DetailsList []struct {
		TestName string `bson:"test_name"`
	} `bson:"details_list"`
}

type runRef struct {
	Index int   `json:"index"`
	RunID int64 `json:"run_id"`
}

type reportCheck struct {
	Found                 bool    `json:"found"`
	Project               string  `json:"project,omitempty"`
	ReportClassification  string  `json:"report_classification,omitempty"`
	ReportFailRatePct     float64 `json:"report_fail_rate_pct,omitempty"`
	ReportPattern         string  `json:"report_pattern,omitempty"`
	ReportPatternNewLogic string  `json:"report_pattern_new_logic,omitempty"`
	PatternMatch          bool    `json:"pattern_match,omitempty"`
}

type branchResult struct {
	Branch         string       `json:"branch"`
	RunsAnalysed   int          `json:"runs_analysed"`
	OldestRunID    int64        `json:"oldest_run_id"`
	NewestRunID    int64        `json:"newest_run_id"`
	Pattern        string       `json:"pattern"`
	Classification string       `json:"classification"`
	Sessions       int          `json:"sessions"`
	FailCount      int          `json:"fail_count"`
	PresentCount   int          `json:"present_count"`
	FailRatePct    float64      `json:"fail_rate_pct"`
	FirstFail      *runRef      `json:"first_fail,omitempty"`
	LastFail       *runRef      `json:"last_fail,omitempty"`
	ReportCheck    *reportCheck `json:"report_check,omitempty"`
}

type matchedTest struct {
	Test     string                  `json:"test"`
	Branches map[string]branchResult `json:"branches"`
}

type output struct {
	Owner        string        `json:"owner"`
	Repo         string        `json:"repo"`
	TestQuery    string        `json:"test_query"`
	MatchMode    string        `json:"match_mode"`
	Runs         int           `json:"runs"`
	MatchedCount int           `json:"matched_count"`
	Truncated    bool          `json:"truncated,omitempty"`
	MatchedTests []matchedTest `json:"matched_tests"`
}

func baseKey(testName string) string {
	if idx := strings.Index(testName, " | "); idx >= 0 {
		return testName[:idx]
	}
	return testName
}

func main() {
	var (
		mongoURI    = flag.String("mongo-uri", "mongodb://root:example@localhost:27017", "MongoDB URI")
		dbName      = flag.String("db", "rel_cache", "MongoDB database")
		collName    = flag.String("collection", "parsed_results", "MongoDB collection")
		owner       = flag.String("owner", "hydra-billing", "GitHub owner")
		repo        = flag.String("repo", "", "GitHub repo (required)")
		branch      = flag.String("branch", "", "Filter by branch (omit to report on every branch in cache)")
		testQuery   = flag.String("test", "", "Substring or regex (case-insensitive) to match test names (required)")
		exactMatch  = flag.Bool("exact-match", false, "Treat --test as a literal full string instead of regex")
		runs        = flag.Int("runs", 30, "Number of latest runs to consider per branch")
		baseKeyOnly = flag.Bool("base-key", false, "Match by base key (classname::name) instead of full name with trace")
		maxTests    = flag.Int("max-tests", 50, "Cap on the number of distinct tests to analyse")
		reportPath  = flag.String("report", "", "Optional path to a JSON report for cross-checking each branch")
	)
	flag.Parse()

	if *repo == "" || *testQuery == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --repo and --test are required")
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(*mongoURI))
	if err != nil {
		fail("connect mongo: %v", err)
	}
	defer client.Disconnect(context.Background())

	if err := client.Ping(ctx, nil); err != nil {
		fail("ping mongo: %v", err)
	}

	coll := client.Database(*dbName).Collection(*collName)

	branches, err := selectBranches(ctx, coll, *owner, *repo, *branch)
	if err != nil {
		fail("%v", err)
	}
	if len(branches) == 0 {
		if *branch != "" {
			fail("no schema=4 runs found for %s/%s on branch %q (re-run the main pipeline to repopulate cache)", *owner, *repo, *branch)
		}
		fail("no schema=4 runs found for %s/%s (re-run the main pipeline to repopulate cache)", *owner, *repo)
	}

	mongoPattern, err := buildMongoPattern(*testQuery, *exactMatch)
	if err != nil {
		fail("invalid --test pattern: %v", err)
	}

	matchedNames, err := findMatchingTests(ctx, coll, *owner, *repo, branches, mongoPattern, *baseKeyOnly)
	if err != nil {
		fail("search tests: %v", err)
	}

	truncated := false
	if len(matchedNames) > *maxTests {
		fmt.Fprintf(os.Stderr, "WARN: %d tests matched, truncating to --max-tests=%d (sorted alphabetically)\n", len(matchedNames), *maxTests)
		matchedNames = matchedNames[:*maxTests]
		truncated = true
	}

	var report *reportRoot
	if *reportPath != "" {
		report, err = loadReport(*reportPath)
		if err != nil {
			fail("load report: %v", err)
		}
	}

	// Fetch last N docs per branch once and reuse for every matched test.
	branchDocs := make(map[string][]runDoc, len(branches))
	for _, br := range branches {
		docs, err := fetchBranchDocs(ctx, coll, *owner, *repo, br, *runs)
		if err != nil {
			fail("branch %s: %v", br, err)
		}
		branchDocs[br] = docs
	}

	matched := make([]matchedTest, 0, len(matchedNames))
	for _, name := range matchedNames {
		mt := matchedTest{Test: name, Branches: make(map[string]branchResult, len(branches))}
		for _, br := range branches {
			res := computeBranchResult(branchDocs[br], br, name, *baseKeyOnly)
			if report != nil {
				rc := crossCheck(report, *repo, br, name, *baseKeyOnly, res.Pattern)
				res.ReportCheck = &rc
			}
			mt.Branches[br] = res
		}
		matched = append(matched, mt)
	}

	out := output{
		Owner:        *owner,
		Repo:         *repo,
		TestQuery:    *testQuery,
		MatchMode:    ifThen(*baseKeyOnly, "base-key", "exact"),
		Runs:         *runs,
		MatchedCount: len(matched),
		Truncated:    truncated,
		MatchedTests: matched,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fail("encode output: %v", err)
	}
}

// buildMongoPattern returns a regex pattern usable with Mongo's $regex.
// In literal mode, special chars in the query are escaped.
func buildMongoPattern(query string, literal bool) (string, error) {
	if literal {
		return regexp.QuoteMeta(query), nil
	}
	if _, err := regexp.Compile(query); err != nil {
		return "", err
	}
	return query, nil
}

// selectBranches resolves which branches to scan.
func selectBranches(ctx context.Context, coll *mongo.Collection, owner, repo, only string) ([]string, error) {
	if only != "" {
		filter := bson.M{"owner": owner, "repo": repo, "schema": 4, "branch": only, "has_no_tests": false}
		n, err := coll.CountDocuments(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("count: %w", err)
		}
		if n == 0 {
			return nil, nil
		}
		return []string{only}, nil
	}

	filter := bson.M{"owner": owner, "repo": repo, "schema": 4, "has_no_tests": false}
	values, err := coll.Distinct(ctx, "branch", filter)
	if err != nil {
		return nil, fmt.Errorf("distinct branches: %w", err)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// findMatchingTests collects distinct test names matching the regex.
// In base-key mode, scans `all_test_keys` (base keys of every test).
// Otherwise, scans `details_list.test_name` (full names of failed tests).
func findMatchingTests(
	ctx context.Context, coll *mongo.Collection,
	owner, repo string, branches []string, regexPattern string, byBaseKey bool,
) ([]string, error) {
	matchStage := bson.M{"owner": owner, "repo": repo, "schema": 4, "has_no_tests": false}
	if len(branches) > 0 {
		matchStage["branch"] = bson.M{"$in": branches}
	}

	var pipeline mongo.Pipeline
	if byBaseKey {
		pipeline = mongo.Pipeline{
			bson.D{{Key: "$match", Value: matchStage}},
			bson.D{{Key: "$unwind", Value: "$all_test_keys"}},
			bson.D{{Key: "$match", Value: bson.M{"all_test_keys": bson.M{"$regex": regexPattern, "$options": "i"}}}},
			bson.D{{Key: "$group", Value: bson.M{"_id": "$all_test_keys"}}},
		}
	} else {
		pipeline = mongo.Pipeline{
			bson.D{{Key: "$match", Value: matchStage}},
			bson.D{{Key: "$unwind", Value: "$details_list"}},
			bson.D{{Key: "$match", Value: bson.M{"details_list.test_name": bson.M{"$regex": regexPattern, "$options": "i"}}}},
			bson.D{{Key: "$group", Value: bson.M{"_id": "$details_list.test_name"}}},
		}
	}

	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	defer cur.Close(ctx)

	var rows []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode aggregate: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.ID)
	}
	sort.Strings(names)
	return names, nil
}

func fetchBranchDocs(
	ctx context.Context, coll *mongo.Collection,
	owner, repo, branch string, runs int,
) ([]runDoc, error) {
	filter := bson.M{
		"owner":        owner,
		"repo":         repo,
		"schema":       4,
		"branch":       branch,
		"has_no_tests": false,
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "run_id", Value: -1}}).
		SetLimit(int64(runs))

	cur, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find: %w", err)
	}
	defer cur.Close(ctx)

	var docs []runDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	// Reverse to ascending order (oldest first).
	sort.Slice(docs, func(i, j int) bool { return docs[i].RunID < docs[j].RunID })
	return docs, nil
}

func computeBranchResult(docs []runDoc, branch, matchKey string, byBaseKey bool) branchResult {
	if len(docs) == 0 {
		return branchResult{Branch: branch}
	}

	bk := baseKey(matchKey)
	states := make([]analyze.TestState, len(docs))
	for i, d := range docs {
		failed := false
		for _, t := range d.DetailsList {
			if byBaseKey {
				if baseKey(t.TestName) == matchKey {
					failed = true
					break
				}
			} else if t.TestName == matchKey {
				failed = true
				break
			}
		}
		switch {
		case failed:
			states[i] = analyze.TestFailed
		case contains(d.AllTestKeys, bk):
			states[i] = analyze.TestPassed
		default:
			states[i] = analyze.TestNotPresent
		}
	}

	stats := analyze.ComputePatternStats(states)
	cls := analyze.ClassifyStates(states)
	pattern := analyze.BuildPattern(states)

	denom := stats.PresentCount
	if denom == 0 {
		denom = len(states)
	}
	failRate := 0.0
	if denom > 0 {
		failRate = float64(stats.FailCount) / float64(denom) * 100
	}

	res := branchResult{
		Branch:         branch,
		RunsAnalysed:   len(docs),
		OldestRunID:    docs[0].RunID,
		NewestRunID:    docs[len(docs)-1].RunID,
		Pattern:        pattern,
		Classification: cls,
		Sessions:       stats.SessionCount,
		FailCount:      stats.FailCount,
		PresentCount:   stats.PresentCount,
		FailRatePct:    roundOne(failRate),
	}
	if stats.FirstFailIdx >= 0 {
		res.FirstFail = &runRef{Index: stats.FirstFailIdx + 1, RunID: docs[stats.FirstFailIdx].RunID}
	}
	if stats.LastFailIdx >= 0 {
		res.LastFail = &runRef{Index: stats.LastFailIdx + 1, RunID: docs[stats.LastFailIdx].RunID}
	}
	return res
}

type reportRoot struct {
	Projects map[string]struct {
		LatestRun struct {
			FailedTests []struct {
				TestName       string  `json:"test_name"`
				Classification string  `json:"classification"`
				Pattern        string  `json:"pattern"`
				FailRatePct    float64 `json:"fail_rate_pct"`
			} `json:"failed_tests"`
		} `json:"latest_run"`
	} `json:"projects"`
}

func loadReport(path string) (*reportRoot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r reportRoot
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func crossCheck(rep *reportRoot, repo, branch, needle string, byBaseKey bool, gotPattern string) reportCheck {
	projectKey := repo + "/" + branch
	proj, ok := rep.Projects[projectKey]
	if !ok {
		return reportCheck{Found: false, Project: projectKey}
	}
	for _, t := range proj.LatestRun.FailedTests {
		match := false
		if byBaseKey {
			match = baseKey(t.TestName) == needle
		} else {
			match = t.TestName == needle
		}
		if !match {
			continue
		}
		return reportCheck{
			Found:                 true,
			Project:               projectKey,
			ReportClassification:  t.Classification,
			ReportFailRatePct:     roundOne(t.FailRatePct),
			ReportPattern:         t.Pattern,
			ReportPatternNewLogic: classifyPattern(t.Pattern),
			PatternMatch:          t.Pattern == gotPattern,
		}
	}
	return reportCheck{Found: false, Project: projectKey}
}

func classifyPattern(pattern string) string {
	var states []analyze.TestState
	for _, r := range pattern {
		switch string(r) {
		case "🔴":
			states = append(states, analyze.TestFailed)
		case "🟢":
			states = append(states, analyze.TestPassed)
		case "⚪":
			states = append(states, analyze.TestNotPresent)
		}
	}
	return analyze.ClassifyStates(states)
}

func contains(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}

func ifThen(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

func roundOne(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
