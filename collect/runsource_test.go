package collect

import "testing"

func TestParseDispatchTitle(t *testing.T) {
	cases := []struct {
		title      string
		wantBranch string
		wantRunID  int
		wantOK     bool
	}{
		{"CI: INT-726 29190545294", "INT-726", 29190545294, true},
		{"CI: v2.8 ", "v2.8", 0, true},
		{"CI: master ", "master", 0, true},
		{"CI: master 123", "master", 123, true},
		{"CI:  ", "", 0, false},
		{"Scheduled builds", "", 0, false},
		{"", "", 0, false},
	}

	for _, c := range cases {
		branch, runID, ok := parseDispatchTitle(c.title)
		if branch != c.wantBranch || runID != c.wantRunID || ok != c.wantOK {
			t.Errorf("parseDispatchTitle(%q) = (%q, %d, %v), want (%q, %d, %v)",
				c.title, branch, runID, ok, c.wantBranch, c.wantRunID, c.wantOK)
		}
	}
}

func TestNewRunSource(t *testing.T) {
	gh := NewGitHubClient("", "hydra-billing", "ci.yml")

	if src := newRunSource(gh, "homs"); src.dataRepo() != "homs-ci" || !src.delegated() {
		t.Errorf("homs must delegate to homs-ci, got dataRepo=%q delegated=%v", src.dataRepo(), src.delegated())
	}
	if src := newRunSource(gh, "hbw"); src.dataRepo() != "hbw" || src.delegated() {
		t.Errorf("hbw must not delegate, got dataRepo=%q delegated=%v", src.dataRepo(), src.delegated())
	}
}
