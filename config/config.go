package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type GitHubConfig struct {
	Owner        string `yaml:"owner"`
	WorkflowFile string `yaml:"workflow_file"`
}

type MongoConfig struct {
	URI        string `yaml:"uri"`
	DB         string `yaml:"db"`
	Collection string `yaml:"collection"`
}

type AnalysisConfig struct {
	MasterBranch string `yaml:"master_branch"`
	MaxRuns      int    `yaml:"max_runs"`
}

type OutputConfig struct {
	Dir               string `yaml:"dir"`
	SaveLogs          bool   `yaml:"save_logs"`
	ForceRefreshCache bool   `yaml:"force_refresh_cache"`
	GenerateHTML      bool   `yaml:"generate_html"`
	GenerateJSON      bool   `yaml:"generate_json"`
}

type InputConfig struct {
	LogFile          string   `yaml:"log_file"`
	RepoBranchesFile string   `yaml:"repo_branches_file"`
	IgnoreTasks      []string `yaml:"ignore_tasks"`
}

type Config struct {
	GitHub    GitHubConfig   `yaml:"github"`
	Mongo     MongoConfig    `yaml:"mongo"`
	Analysis  AnalysisConfig `yaml:"analysis"`
	Output    OutputConfig   `yaml:"output"`
	SkipParse bool           `yaml:"skip_parse"`
	Input     InputConfig    `yaml:"input"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{
		// Defaults
		GitHub: GitHubConfig{
			Owner:        "hydra-billing",
			WorkflowFile: "ci.yml",
		},
		Mongo: MongoConfig{
			URI:        "mongodb://root:example@localhost:27017",
			DB:         "rel_cache",
			Collection: "parsed_results",
		},
		Analysis: AnalysisConfig{
			MasterBranch: "master",
			MaxRuns:      100,
		},
		Output: OutputConfig{
			Dir:          "downloaded_logs",
			GenerateHTML: true,
			GenerateJSON: true,
		},
		Input: InputConfig{
			LogFile:          "1.log",
			RepoBranchesFile: "repo_branches.json",
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	return cfg, nil
}
