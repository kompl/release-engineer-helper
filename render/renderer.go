package render

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"release-engineer-helper/config"
	"release-engineer-helper/internal/models"
)

// RenderAll generates HTML reports (one per repo/branch) and a combined JSON report
// in parallel. When jsonOut is non-nil the JSON report goes there instead of a
// file in output.dir, and output.generate_json is bypassed — an explicit
// destination is a request for the report.
func RenderAll(results []models.RepoResult, cfg *config.Config, jsonOut io.Writer) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	// HTML reports — one goroutine per repo/branch
	if cfg.Output.GenerateHTML {
		for _, r := range results {
			wg.Add(1)
			go func(r models.RepoResult) {
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
	if jsonOut != nil || cfg.Output.GenerateJSON {
		wg.Add(1)
		go func() {
			defer wg.Done()

			render := func() error { return RenderJSON(results, cfg) }
			if jsonOut != nil {
				render = func() error { return RenderJSONTo(jsonOut, results, cfg) }
			}

			if err := render(); err != nil {
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
