package collect

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"strings"

	"release-engineer-helper/v0.1/internal"
)

// ArtifactExtractor parses JUnit XML from GitHub Actions artifacts.
// Equivalent of Python's ArtifactsTestResultsExtractor.
type ArtifactExtractor struct {
	gh *GitHubClient
}

// NewArtifactExtractor creates a new ArtifactExtractor.
func NewArtifactExtractor(gh *GitHubClient) *ArtifactExtractor {
	return &ArtifactExtractor{gh: gh}
}

// ExtractResult holds the results of artifact extraction.
type ExtractResult struct {
	Details     map[string][]internal.TestDetail
	AllTestKeys []string // base keys (classname::name) for ALL tests, including passed
	HasNoTests  bool
}

// Extract downloads and parses test-reports-* artifacts for a run.
func (ae *ArtifactExtractor) Extract(repo string, runID int) *ExtractResult {
	artifacts, err := ae.gh.ListRunArtifacts(repo, runID)
	if err != nil {
		log.Printf("[artifacts] Error listing artifacts for run %d: %v", runID, err)
		return &ExtractResult{HasNoTests: true}
	}

	if len(artifacts) == 0 {
		fmt.Printf("  [artifacts] No artifacts for run %d\n", runID)
		return &ExtractResult{HasNoTests: true}
	}

	// Filter test-reports-* artifacts
	var reportArtifacts []ghArtifact
	for _, a := range artifacts {
		if strings.HasPrefix(a.Name, "test-reports-") && !a.Expired {
			reportArtifacts = append(reportArtifacts, a)
		}
	}

	if len(reportArtifacts) == 0 {
		fmt.Printf("  [artifacts] No test-reports-* artifacts for run %d\n", runID)
		return &ExtractResult{HasNoTests: true}
	}

	combined := make(map[string][]internal.TestDetail)
	allKeysSet := make(map[string]struct{})
	foundAnyJUnit := false
	globalPos := 0
	firstSeenOrder := make(map[string]int)

	for _, art := range reportArtifacts {
		project := strings.TrimPrefix(art.Name, "test-reports-")
		if art.ArchiveDownloadURL == "" {
			continue
		}

		zipBytes, err := ae.gh.DownloadArtifact(art.ArchiveDownloadURL)
		if err != nil {
			log.Printf("[artifacts] Error downloading artifact '%s' for run %d: %v", art.Name, runID, err)
			continue
		}

		parsed := ae.parseJUnitZip(zipBytes, project)
		if parsed.hasJUnit {
			foundAnyJUnit = true
		}

		for k, v := range parsed.failed {
			if _, seen := firstSeenOrder[k]; !seen {
				firstSeenOrder[k] = globalPos
				globalPos++
			}
			combined[k] = append(combined[k], v...)
		}
		for _, key := range parsed.allTestKeys {
			allKeysSet[key] = struct{}{}
		}
	}

	hasNoTests := !foundAnyJUnit
	if hasNoTests {
		fmt.Printf("  [artifacts] No JUnit reports found in artifacts for run %d\n", runID)
	}

	allTestKeys := make([]string, 0, len(allKeysSet))
	for k := range allKeysSet {
		allTestKeys = append(allTestKeys, k)
	}

	return &ExtractResult{
		Details:     combined,
		AllTestKeys: allTestKeys,
		HasNoTests:  hasNoTests,
	}
}

// junitTestSuites represents a JUnit XML file root (can be <testsuites> or <testsuite>).
type junitTestSuites struct {
	XMLName    xml.Name         `xml:""`
	TestSuites []junitTestSuite `xml:"testsuite"`
	TestCases  []junitTestCase  `xml:"testcase"`
}

type junitTestSuite struct {
	TestCases  []junitTestCase  `xml:"testcase"`
	TestSuites []junitTestSuite `xml:"testsuite"`
}

type junitTestCase struct {
	ClassName string         `xml:"classname,attr"`
	Name      string         `xml:"name,attr"`
	Failures  []junitFailure `xml:"failure"`
	Errors    []junitFailure `xml:"error"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// junitZipResult holds the results of parsing a JUnit zip.
type junitZipResult struct {
	failed      map[string][]internal.TestDetail
	allTestKeys []string // base keys (classname::name) for ALL tests
	hasJUnit    bool
}

func (ae *ArtifactExtractor) parseJUnitZip(zipBytes []byte, project string) junitZipResult {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		log.Println("[artifacts] Bad zip for junit artifact")
		return junitZipResult{}
	}

	failed := make(map[string][]internal.TestDetail)
	allKeysSet := make(map[string]struct{})
	foundAnyJUnit := false
	seenOrder := make(map[string]int)
	localPos := 0

	for _, f := range r.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}

		parsed := parseJUnitXML(data)
		if parsed.hasAnyTestCase {
			foundAnyJUnit = true
		}

		// Collect ALL test keys (passed + failed + skipped)
		for _, key := range parsed.allTestKeys {
			allKeysSet[key] = struct{}{}
		}

		// Process failed tests for details
		for _, tc := range parsed.failed {
			testKey := buildTestKey(tc)

			if _, seen := seenOrder[testKey]; !seen {
				seenOrder[testKey] = localPos
				localPos++
			}

			allFailures := append(tc.Failures, tc.Errors...)
			for _, failure := range allFailures {
				message := strings.TrimSpace(failure.Message)
				detailsText := strings.TrimSpace(failure.Text)
				context := strings.Trim(fmt.Sprintf("\n%s\n%s\n---\n", message, detailsText), "\n")

				testName := testKey + " | " + message

				failed[testName] = append(failed[testName], internal.TestDetail{
					File:       f.Name,
					LineNum:    0,
					Context:    context,
					Project:    project,
					OrderIndex: seenOrder[testKey],
				})
			}
		}
	}

	allTestKeys := make([]string, 0, len(allKeysSet))
	for k := range allKeysSet {
		allTestKeys = append(allTestKeys, k)
	}

	return junitZipResult{
		failed:      failed,
		allTestKeys: allTestKeys,
		hasJUnit:    foundAnyJUnit,
	}
}

// buildTestKey creates a base test key from classname and name.
func buildTestKey(tc junitTestCase) string {
	classname := strings.TrimSpace(tc.ClassName)
	name := strings.TrimSpace(tc.Name)

	if classname != "" && name != "" {
		return classname + "::" + name
	} else if name != "" {
		return name
	} else if classname != "" {
		return classname
	}
	return "unknown"
}

// junitParseResult holds base keys of all tests and the failed subset.
type junitParseResult struct {
	hasAnyTestCase bool
	allTestKeys    []string        // base keys (classname::name) for ALL tests
	failed         []junitTestCase // only failed, for error details
}

// parseJUnitXML extracts all testcases from a JUnit XML, handling both
// <testsuites> and <testsuite> root elements.
// Returns base keys of all tests and the failed subset with details.
func parseJUnitXML(data []byte) junitParseResult {
	var suites junitTestSuites
	if err := xml.Unmarshal(data, &suites); err != nil {
		return junitParseResult{}
	}

	var allCases []junitTestCase

	// Collect direct testcases
	allCases = append(allCases, suites.TestCases...)

	// Collect from nested testsuites
	var collectFromSuites func([]junitTestSuite)
	collectFromSuites = func(suites []junitTestSuite) {
		for _, s := range suites {
			allCases = append(allCases, s.TestCases...)
			collectFromSuites(s.TestSuites)
		}
	}
	collectFromSuites(suites.TestSuites)

	// Build base keys for ALL tests and filter failed
	allTestKeys := make([]string, 0, len(allCases))
	var failed []junitTestCase
	for _, tc := range allCases {
		allTestKeys = append(allTestKeys, buildTestKey(tc))
		if len(tc.Failures) > 0 || len(tc.Errors) > 0 {
			failed = append(failed, tc)
		}
	}
	return junitParseResult{
		hasAnyTestCase: len(allCases) > 0,
		allTestKeys:    allTestKeys,
		failed:         failed,
	}
}
