package render

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"release-engineer-helper/analyze"
	"release-engineer-helper/config"
	"release-engineer-helper/internal/models"
)

func TestExtractError(t *testing.T) {
	if got := extractError(nil); got != "" {
		t.Errorf("extractError(nil) = %q, want empty", got)
	}

	multiline := []models.TestDetail{{Context: "  ORA-00600:\n   internal error\n   code  "}}
	if got := extractError(multiline); got != "ORA-00600: internal error code" {
		t.Errorf("extractError multiline = %q", got)
	}

	long := []models.TestDetail{{Context: strings.Repeat("x", 350)}}
	got := extractError(long)
	if want := strings.Repeat("x", 300) + "…"; got != want {
		t.Errorf("extractError long: len %d, want 300+ellipsis", len([]rune(got)))
	}
}

func TestClassifyTest(t *testing.T) {
	stable := map[string]*models.TestBehavior{"s": {}}
	fixed := map[string]*models.TestBehavior{"f": {}}
	flaky := map[string]*models.TestBehavior{"y": {}}

	cases := []struct{ name, want string }{
		{"s", "stable_failing"},
		{"f", "fixed"},
		{"y", "flaky"},
		{"unknown", "stable_failing"}, // default for unclassified
	}
	for _, c := range cases {
		if got := classifyTest(c.name, stable, fixed, flaky); got != c.want {
			t.Errorf("classifyTest(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFindStreakStart(t *testing.T) {
	keys := []string{"k1", "k2", "k3"}
	meta := map[string]models.RunMeta{
		"k1": {SHA: "sha1", Title: "c1", Timestamp: "t1", Link: "l1"},
		"k2": {SHA: "sha2", Title: "c2", Timestamp: "t2", Link: "l2"},
		"k3": {SHA: "sha3", Title: "c3", Timestamp: "t3", Link: "l3"},
	}

	cases := []struct {
		name       string
		pattern    string
		wantNil    bool
		wantSHA    string
		wantStreak int
	}{
		{"streak of two", "🟢🔴🔴", false, "sha2", 2},
		{"streak of one after recovery", "🔴🟢🔴", false, "sha3", 1},
		{"full streak", "🔴🔴🔴", false, "sha1", 3},
		{"trailing gap keeps streak", "🔴🔴⚪", false, "sha1", 2},
		{"gap inside streak not counted", "🔴⚪🔴", false, "sha1", 2},
		{"no failures", "🟢🟢🟢", true, "", 0},
		{"ends passing", "🔴🔴🟢", true, "", 0},
		{"empty", "", true, "", 0},
	}
	for _, c := range cases {
		got := findStreakStart(c.pattern, keys, meta)
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: findStreakStart(%q) = %+v, want nil", c.name, c.pattern, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: findStreakStart(%q) = nil", c.name, c.pattern)
			continue
		}
		if got.SHA != c.wantSHA || got.StreakLength != c.wantStreak {
			t.Errorf("%s: findStreakStart(%q) = sha %q streak %d, want %q/%d",
				c.name, c.pattern, got.SHA, got.StreakLength, c.wantSHA, c.wantStreak)
		}
	}
}

// Fixture: testA always fails (stable), testB failed once and recovered (fixed),
// testC fails intermittently (flaky). Mirrors the analyze package fixture.
const (
	testA = "SpecA::a | ORA-00600: internal error"
	testB = "SpecB::b"
	testC = "SpecC::c | timeout"
)

func makeRepoResult() models.RepoResult {
	keys := []string{"aaaaaaaaaaaaaaaa_101", "bbbbbbbbbbbbbbbb_102", "cccccccccccccccc_103"}
	meta := map[string]models.RunMeta{
		keys[0]: {SHA: "aaaaaaaaaaaaaaaa", RunID: 101, Title: "commit 1", Timestamp: "2026-07-01T10:00:00Z", Conclusion: "failure", Link: "https://ci/101", CompositeKey: keys[0]},
		keys[1]: {SHA: "bbbbbbbbbbbbbbbb", RunID: 102, Title: "commit 2", Timestamp: "2026-07-02T10:00:00Z", Conclusion: "failure", Link: "https://ci/102", CompositeKey: keys[1]},
		keys[2]: {SHA: "cccccccccccccccc", RunID: 103, Title: "commit 3", Timestamp: "2026-07-03T10:00:00Z", Conclusion: "failure", Link: "https://ci/103", CompositeKey: keys[2]},
	}
	cr := &models.CollectResult{
		Summary: map[string]models.StringSet{
			keys[0]: models.NewStringSet(testA, testB, testC),
			keys[1]: models.NewStringSet(testA),
			keys[2]: models.NewStringSet(testA, testC),
		},
		Meta: meta,
		AllTestDetails: map[string][]models.TestDetail{
			testA: {{File: "spec/a_spec.rb", LineNum: 12, Context: "ORA-00600: internal error", Project: "hydra"}},
			testB: {{File: "spec/b_spec.rb", LineNum: 5, Context: "expected true", Project: "hydra"}},
		},
		AllTestKeys:  map[string]models.StringSet{},
		MasterFailed: models.NewStringSet(testA),
		OrderedKeys:  keys,
	}
	return models.RepoResult{
		Repo:    "hoper",
		Branch:  "v6.3",
		Collect: cr,
		Analyze: analyze.Run(cr),
		Enrich: &models.EnrichResult{
			StableSince: map[string]models.StableSinceInfo{
				testA: {RunID: 42, CreatedAt: "2026-06-15T00:00:00Z"},
			},
		},
	}
}

func testConfig(dir string) *config.Config {
	cfg := &config.Config{}
	cfg.Analysis.MasterBranch = "master"
	cfg.Output.Dir = dir
	cfg.Output.GenerateJSON = true
	return cfg
}

func TestBuildRepoJSONData(t *testing.T) {
	p := buildRepoJSONData(makeRepoResult(), testConfig(t.TempDir()))

	if p.Repo != "hoper" || p.Branch != "v6.3" || p.MasterBranch != "master" {
		t.Errorf("project header = %s/%s master %s", p.Repo, p.Branch, p.MasterBranch)
	}

	// Latest run: testA (stable) and testC (flaky) failed
	lr := p.LatestRun
	if lr.RunID != 103 || lr.TotalFailed != 2 || len(lr.FailedTests) != 2 {
		t.Fatalf("latest_run = id %d, failed %d (%d entries)", lr.RunID, lr.TotalFailed, len(lr.FailedTests))
	}

	byName := map[string]jsonFailedTest{}
	for _, ft := range lr.FailedTests {
		byName[ft.TestName] = ft
	}

	a := byName[testA]
	if a.Classification != "stable_failing" || !a.InMaster || a.ErrorMessage != "ORA-00600: internal error" || a.Project != "hydra" {
		t.Errorf("testA entry = %+v", a)
	}
	if a.FailRatePct == nil || *a.FailRatePct != 100.0 {
		t.Errorf("testA fail rate = %v, want 100.0", a.FailRatePct)
	}
	if a.ProbableCause == nil || a.ProbableCause.SHA != "aaaaaaaaaaaaaaaa" || a.ProbableCause.StreakLength != 3 {
		t.Errorf("testA probable_cause = %+v", a.ProbableCause)
	}
	if a.FailingSince == nil || a.FailingSince.RunID != 42 {
		t.Errorf("testA failing_since = %+v, want RunID 42 from enrich data", a.FailingSince)
	}
	if a.FirstSeen == nil || a.FirstSeen.RunLink != "https://ci/101" {
		t.Errorf("testA first_seen = %+v", a.FirstSeen)
	}

	c := byName[testC]
	if c.Classification != "flaky" || c.InMaster {
		t.Errorf("testC entry = %+v", c)
	}
	if c.FlakyInfo == nil || c.FlakyInfo.FailCount != 2 || c.FlakyInfo.TotalRuns != 3 {
		t.Errorf("testC flaky_info = %+v", c.FlakyInfo)
	}

	// Additional: only the fixed testB, with history fields
	if len(p.Additional) != 1 {
		t.Fatalf("additional = %d entries, want 1 (fixed testB)", len(p.Additional))
	}
	b := p.Additional[0]
	if b.TestName != testB || b.Classification != "fixed" {
		t.Errorf("additional[0] = %s (%s)", b.TestName, b.Classification)
	}
	if b.ProbableFix == nil || b.ProbableFix.SHA != "bbbbbbb" || b.ProbableFix.CommitTitle != "commit 2" {
		t.Errorf("testB probable_fix = %+v", b.ProbableFix)
	}
	if b.LastFailed == nil || b.LastFailed.RunID != 101 {
		t.Errorf("testB last_failed = %+v", b.LastFailed)
	}

	// Summary
	s := p.Summary
	if s.TotalRunsAnalyzed != 3 || s.UniqueFailedTests != 3 || s.MasterFailedTests != 1 ||
		s.NewFailures != 2 || s.StableFailingCount != 1 || s.FixedCount != 1 || s.FlakyCount != 1 {
		t.Errorf("summary = %+v", s)
	}
}

func TestRenderJSONWritesReport(t *testing.T) {
	dir := t.TempDir()
	if err := RenderJSON([]models.RepoResult{makeRepoResult()}, testConfig(dir)); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("output dir entries = %v, err %v", entries, err)
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "report_") || !strings.HasSuffix(name, ".json") {
		t.Errorf("report filename = %q", name)
	}

	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		GeneratedAt string                     `json:"generated_at"`
		Projects    map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if report.GeneratedAt == "" {
		t.Error("generated_at must be set")
	}
	if _, ok := report.Projects["hoper/v6.3"]; !ok {
		t.Errorf("projects keys = %v, want hoper/v6.3", report.Projects)
	}
}

func TestRenderJSONToWriterLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := RenderJSONTo(&buf, []models.RepoResult{makeRepoResult()}, testConfig(dir)); err != nil {
		t.Fatalf("RenderJSONTo: %v", err)
	}

	var report struct {
		GeneratedAt string                     `json:"generated_at"`
		Projects    map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("stdout payload is not valid JSON: %v", err)
	}
	if _, ok := report.Projects["hoper/v6.3"]; !ok {
		t.Errorf("projects keys = %v, want hoper/v6.3", report.Projects)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Error("вывод должен заканчиваться переводом строки")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("output.dir должен остаться пустым, найдено: %v", entries)
	}
}

func TestRenderAllStdoutBypassesGenerateJSON(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.Output.GenerateHTML = false
	cfg.Output.GenerateJSON = false // явный получатель важнее ключа конфига

	var buf bytes.Buffer
	if err := RenderAll([]models.RepoResult{makeRepoResult()}, cfg, &buf); err != nil {
		t.Fatalf("RenderAll: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("RenderAll с явным jsonOut обязан напечатать отчёт")
	}
}
