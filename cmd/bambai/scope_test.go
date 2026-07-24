package main

import (
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
)

// parseArgs registers the scope flags on a fresh FlagSet and applies args.
func parseArgs(t *testing.T, args ...string) scopeFlags {
	t.Helper()
	var f scopeFlags
	fs := flag.NewFlagSet("bambai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}
	return f
}

func TestValidateModes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want runScope
	}{
		{"без флагов → by-json", nil, runScope{mode: modeByJSON}},
		{"--by-json", []string{"--by-json"}, runScope{mode: modeByJSON}},
		{"--by-log", []string{"--by-log"}, runScope{mode: modeByLog}},
		{"короткий -by-log", []string{"-by-log"}, runScope{mode: modeByLog}},
		{"-p", []string{"-p", "hydra-server"},
			runScope{mode: modeProject, project: "hydra-server", branches: []string{}}},
		{"--project", []string{"--project", "hydra-server"},
			runScope{mode: modeProject, project: "hydra-server", branches: []string{}}},
		{"-p с одной -b", []string{"-p", "hydra-server", "-b", "master"},
			runScope{mode: modeProject, project: "hydra-server", branches: []string{"master"}}},
		{"-p с несколькими -b", []string{"-p", "hydra-server", "-b", "master", "-b", "v6.3"},
			runScope{mode: modeProject, project: "hydra-server", branches: []string{"master", "v6.3"}}},
		{"--branch вперемешку с -b", []string{"-p", "hoper", "--branch", "master", "-b", "v6.2"},
			runScope{mode: modeProject, project: "hoper", branches: []string{"master", "v6.2"}}},
		{"повторы -b схлопываются", []string{"-p", "hoper", "-b", "master", "-b", "master"},
			runScope{mode: modeProject, project: "hoper", branches: []string{"master"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := parseArgs(t, c.args...)
			got, err := f.validate()
			if err != nil {
				t.Fatalf("validate(%v): %v", c.args, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("validate(%v) = %+v, want %+v", c.args, got, c.want)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // фрагмент, который обязан быть в сообщении
	}{
		{"--by-log с --by-json", []string{"--by-log", "--by-json"}, "взаимоисключающие"},
		{"-p с --by-json", []string{"-p", "hoper", "--by-json"}, "не сочетается"},
		{"-p с --by-log", []string{"-p", "hoper", "--by-log"}, "не сочетается"},
		{"два -p", []string{"-p", "hoper", "-p", "hupo"}, "максимум один -p"},
		{"-p и --project", []string{"-p", "hoper", "--project", "hupo"}, "максимум один -p"},
		{"-b без -p", []string{"-b", "master"}, "требует -p"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := parseArgs(t, c.args...)
			_, err := f.validate()
			if err == nil {
				t.Fatalf("validate(%v) = nil error, want error", c.args)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("validate(%v) error = %q, want substring %q", c.args, err, c.want)
			}
		})
	}
}

var reference = map[string][]string{
	"hydra-server": {"master", "v6.3", "v6.2.1"},
	"homs":         {"master", "v2.9"},
	"empty-repo":   {},
}

func TestResolveProjectScope(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		branches []string
		want     map[string][]string
	}{
		{
			name:    "все ветки из справочника",
			project: "hydra-server",
			want:    map[string][]string{"hydra-server": {"master", "v6.3", "v6.2.1"}},
		},
		{
			name:     "одна ветка",
			project:  "hydra-server",
			branches: []string{"master"},
			want:     map[string][]string{"hydra-server": {"master"}},
		},
		{
			name:     "несколько веток",
			project:  "hydra-server",
			branches: []string{"master", "v6.3"},
			want:     map[string][]string{"hydra-server": {"master", "v6.3"}},
		},
		{
			name:     "ветка вне справочника собирается как есть",
			project:  "hydra-server",
			branches: []string{"feature/HPD-42"},
			want:     map[string][]string{"hydra-server": {"feature/HPD-42"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveProjectScope(reference, c.project, c.branches)
			if err != nil {
				t.Fatalf("resolveProjectScope: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("resolveProjectScope = %v, want %v", got, c.want)
			}
		})
	}
}

func TestResolveProjectScopeUnknownProject(t *testing.T) {
	_, err := resolveProjectScope(reference, "unknown-repo", nil)
	if err == nil {
		t.Fatal("неизвестный проект обязан приводить к ошибке")
	}
	for _, want := range []string{"unknown-repo", "homs", "hydra-server"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want substring %q", err, want)
		}
	}
}

func TestResolveProjectScopeNoBranchesInReference(t *testing.T) {
	if _, err := resolveProjectScope(reference, "empty-repo", nil); err == nil {
		t.Error("проект без веток в справочнике и без -b обязан приводить к ошибке")
	}
}

func TestResolveProjectScopeDoesNotAliasReference(t *testing.T) {
	got, err := resolveProjectScope(reference, "homs", nil)
	if err != nil {
		t.Fatalf("resolveProjectScope: %v", err)
	}
	got["homs"][0] = "перезаписано"
	if reference["homs"][0] != "master" {
		t.Errorf("справочник изменён через результат: %v", reference["homs"])
	}
}
