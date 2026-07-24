package config

import (
	"os"
	"path/filepath"
	"testing"
)

// write создаёт временный конфиг с заданным содержимым и возвращает путь к нему.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProjectsFileDefault(t *testing.T) {
	cfg, err := Load(write(t, "input:\n  log_file: \"1.log\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Input.ProjectsFile != "projects.json" {
		t.Errorf("ProjectsFile = %q, want %q", cfg.Input.ProjectsFile, "projects.json")
	}
}

func TestLoadProjectsFileExplicit(t *testing.T) {
	cfg, err := Load(write(t, "input:\n  projects_file: \"custom/projects.json\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Input.ProjectsFile != "custom/projects.json" {
		t.Errorf("ProjectsFile = %q, want %q", cfg.Input.ProjectsFile, "custom/projects.json")
	}
}

func TestLoadDeprecatedSkipParse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *bool
	}{
		{"отсутствует", "input:\n  log_file: \"1.log\"\n", nil},
		{"false", "skip_parse: false\n", boolPtr(false)},
		{"true", "skip_parse: true\n", boolPtr(true)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load(write(t, c.body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := cfg.DeprecatedSkipParse
			switch {
			case c.want == nil && got != nil:
				t.Errorf("DeprecatedSkipParse = %v, want nil", *got)
			case c.want != nil && got == nil:
				t.Errorf("DeprecatedSkipParse = nil, want %v", *c.want)
			case c.want != nil && *got != *c.want:
				t.Errorf("DeprecatedSkipParse = %v, want %v", *got, *c.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }
