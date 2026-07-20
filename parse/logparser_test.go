package parse

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestVersionToBranch(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"6.2.1.5", "v6.2.1"},  // 4 parts → first 3
		{"6.3.0.9", "v6.3"},    // 4 parts, 3rd is 0 → first 2
		{"6.2.2", "v6.2"},      // 3 parts → first 2
		{"6.3", "v6.3"},        // 2 parts → as-is
		{"7", "v7"},            // 1 part → as-is
		{"6.2.1.11", "v6.2.1"}, // multi-digit build number
	}
	for _, c := range cases {
		if got := versionToBranch(c.version); got != c.want {
			t.Errorf("versionToBranch(%q) = %q, want %q", c.version, got, c.want)
		}
	}
}

const sampleLog = `{"hydra-core":
  {"6.3.0.9" =>
    {tasks: [],
     commits: ["8caba86eb4c3e86d15d5de166e14c27e43bbb278"],
     translations: []}},
 hoper:
  {"6.3.0.7" =>
    {tasks: ["HOPER-5837", "HOPER-5980"],
     commits:
      ["d4d3a57629ab668000b34cf08c4ded9b149fd0e3",
       "70f8ebc7f5edf02f84a62bc49f5dfdcb6b179b01"],
     translations: []},
   "6.2.1.11" =>
    {tasks: ["IGNORED-1"],
     commits: ["6e059d868f19ab0f9ed8f4f868f97bec97ae0d54"],
     translations: []}},
 hupo:
  {"6.2.1.8" =>
    {tasks: ["HUPO-1042"],
     commits: ["63fda2c093089a67ce2111838b3edf62e86d4a52"],
     translations: []},
   "6.3.0.5" =>
    {tasks: ["HUPO-1042"],
     commits: ["0cb2e0463cac8c7578ea02c532c1bbca53d1e811"],
     translations: []}}}
`

func TestParseLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "release.log")
	if err := os.WriteFile(logPath, []byte(sampleLog), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ParseLog(logPath, []string{"IGNORED-1"})
	if err != nil {
		t.Fatalf("ParseLog: %v", err)
	}

	want := map[string][]string{
		// hydra-core is dropped: its only version has an empty tasks list
		// hoper 6.2.1.11 is dropped: all its tasks are ignored
		"hoper": {"v6.3"},
		"hupo":  {"v6.2.1", "v6.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLog = %v, want %v", got, want)
	}
}

func TestParseLogMissingFile(t *testing.T) {
	if _, err := ParseLog(filepath.Join(t.TempDir(), "nope.log"), nil); err == nil {
		t.Error("ParseLog on a missing file must return an error")
	}
}

func TestSaveLoadRepoBranchesRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo_branches.json")
	want := map[string][]string{
		"hoper": {"v6.2.1", "v6.3"},
		"hupo":  {"v6.3"},
	}

	if err := SaveRepoBranches(path, want); err != nil {
		t.Fatalf("SaveRepoBranches: %v", err)
	}
	got, err := LoadRepoBranches(path)
	if err != nil {
		t.Fatalf("LoadRepoBranches: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roundtrip = %v, want %v", got, want)
	}
}
