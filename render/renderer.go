package render

import (
	"errors"
	"fmt"
	"sync"

	"release-engineer-helper/v0.1/analyze"
	"release-engineer-helper/v0.1/collect"
	"release-engineer-helper/v0.1/config"
	"release-engineer-helper/v0.1/enrich"
)

// RepoResult groups all phase results for a single repo/branch.
type RepoResult struct {
	Repo    string
	Branch  string
	Collect *collect.CollectResult
	Analyze *analyze.AnalyzeResult
	Enrich  *enrich.EnrichResult
}

// RenderAll generates HTML reports (one per repo/branch) and a combined JSON report
// in parallel.
func RenderAll(results []RepoResult, cfg *config.Config) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	// HTML reports — one goroutine per repo/branch
	if cfg.Output.GenerateHTML {
		for _, r := range results {
			wg.Add(1)
			go func(r RepoResult) {
				defer wg.Done()
				if err := RenderHTML(r, cfg); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("HTML %s/%s: %w", r.Repo, r.Branch, err))
					mu.Unlock()
				}
			}(r)
		}
	}

	// JSON report — one goroutine for combined report
	if cfg.Output.GenerateJSON {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RenderJSON(results, cfg); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("JSON: %w", err))
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
