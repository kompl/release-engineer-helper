package main

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
)

// stringList accumulates repeated flag occurrences, e.g. -b master -b v6.3.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// scopeMode is the pipeline entry point chosen on the command line.
type scopeMode int

const (
	modeByJSON  scopeMode = iota // start from input.repo_branches_file (default)
	modeByLog                    // start from the Parse phase over input.log_file
	modeProject                  // start from a project picked out of input.projects_file
)

// scopeFlags holds the raw command-line values defining the run scope.
type scopeFlags struct {
	projects stringList
	branches stringList
	byLog    bool
	byJSON   bool
}

// runScope is the validated scope of a single run.
type runScope struct {
	mode     scopeMode
	project  string   // modeProject only
	branches []string // modeProject only; empty means "all branches from the reference"
}

// register binds the scope flags to fs. Go's flag package treats -name and
// --name alike, so each pair of short/long names points at one target.
func (f *scopeFlags) register(fs *flag.FlagSet) {
	fs.Var(&f.projects, "p", "проект из projects.json (максимум один)")
	fs.Var(&f.projects, "project", "то же, что -p")
	fs.Var(&f.branches, "b", "ветка проекта, флаг можно повторять (требует -p)")
	fs.Var(&f.branches, "branch", "то же, что -b")
	fs.BoolVar(&f.byLog, "by-log", false, "старт с парсинга лога (input.log_file)")
	fs.BoolVar(&f.byJSON, "by-json", false, "старт с готового input.repo_branches_file (режим по умолчанию)")
}

// validate folds the flags into a single mode. The three modes are mutually
// exclusive, so every conflicting combination is rejected here — before the
// config, MongoDB or the GitHub API come into play.
func (f *scopeFlags) validate() (runScope, error) {
	switch {
	case f.byLog && f.byJSON:
		return runScope{}, errors.New("--by-log и --by-json взаимоисключающие: выберите одну точку входа")
	case len(f.projects) > 0 && (f.byLog || f.byJSON):
		return runScope{}, errors.New("-p задаёт собственную область анализа и не сочетается с --by-log и --by-json")
	case len(f.projects) > 1:
		return runScope{}, fmt.Errorf("допустим максимум один -p, указано %d: %s; для нескольких проектов используйте --by-json",
			len(f.projects), strings.Join(f.projects, ", "))
	case len(f.branches) > 0 && len(f.projects) == 0:
		return runScope{}, errors.New("-b задаёт ветки выбранного проекта и требует -p")
	case len(f.projects) == 1:
		return runScope{mode: modeProject, project: f.projects[0], branches: dedup(f.branches)}, nil
	case f.byLog:
		return runScope{mode: modeByLog}, nil
	default:
		return runScope{mode: modeByJSON}, nil
	}
}

// resolveProjectScope builds the repo→branches map for modeProject. The project
// must be a known key of the reference; branches are taken as given, and the
// reference only supplies the default set when none were requested.
func resolveProjectScope(projects map[string][]string, project string, branches []string) (map[string][]string, error) {
	known, ok := projects[project]
	if !ok {
		return nil, fmt.Errorf("проект %q не найден в справочнике; доступны: %s",
			project, strings.Join(sortedKeys(projects), ", "))
	}

	if len(branches) == 0 {
		branches = dedup(known)
	}
	if len(branches) == 0 {
		return nil, fmt.Errorf("у проекта %q нет веток в справочнике; укажите их через -b", project)
	}

	return map[string][]string{project: branches}, nil
}

// name returns the human-readable mode name for the run header.
func (m scopeMode) name() string {
	switch m {
	case modeByLog:
		return "--by-log (парсинг лога)"
	case modeProject:
		return "-p (точечная выборка)"
	default:
		return "--by-json (готовый repo_branches.json)"
	}
}

// formatRepoBranches renders a resolved scope as "repo: b1, b2; repo2: b1".
func formatRepoBranches(repoBranches map[string][]string) string {
	parts := make([]string, 0, len(repoBranches))
	for _, repo := range sortedKeys(repoBranches) {
		parts = append(parts, fmt.Sprintf("%s: %s", repo, strings.Join(repoBranches[repo], ", ")))
	}
	return strings.Join(parts, "; ")
}

// usageFor builds the usage printer describing the three run modes.
func usageFor(fs *flag.FlagSet) func() {
	return func() {
		out := fs.Output()
		fmt.Fprint(out, "bambai — анализ падений CI-тестов в GitHub Actions.\n\n")
		fmt.Fprint(out, "Режимы запуска (взаимоисключающие):\n")
		fmt.Fprint(out, "  bambai --by-json                           область из input.repo_branches_file (по умолчанию)\n")
		fmt.Fprint(out, "  bambai --by-log                            область из input.log_file, с фазой Parse\n")
		fmt.Fprint(out, "  bambai -p <проект> [-b <ветка> ...]        область из input.projects_file\n\n")
		fmt.Fprint(out, "Примеры:\n")
		fmt.Fprint(out, "  bambai -p hydra-server                     все ветки проекта из projects.json\n")
		fmt.Fprint(out, "  bambai -p hydra-server -b master -b v6.3   только указанные ветки\n")
		fmt.Fprint(out, "  bambai -p hydra-server --stdout | jq       JSON-отчёт в stdout вместо файла\n\n")
		fmt.Fprint(out, "Флаги:\n")
		fs.PrintDefaults()
	}
}

// dedup returns a copy of values without repetitions, preserving order.
func dedup(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
