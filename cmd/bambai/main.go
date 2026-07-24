package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"

	"release-engineer-helper/analyze"
	"release-engineer-helper/collect"
	"release-engineer-helper/config"
	"release-engineer-helper/enrich"
	"release-engineer-helper/internal/models"
	"release-engineer-helper/parse"
	"release-engineer-helper/render"
)

// phaseState tracks progress for a single repo/branch.
type phaseState struct {
	mu        sync.Mutex
	phase     string
	collected int
	maxRuns   int
}

func (s *phaseState) set(phase string) {
	s.mu.Lock()
	s.phase = phase
	s.mu.Unlock()
}

func (s *phaseState) incr() {
	s.mu.Lock()
	s.collected++
	s.mu.Unlock()
}

func (s *phaseState) render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.phase {
	case "collect":
		return s.collectBar()
	case "analyze":
		return " Analyze"
	case "enrich":
		return " Enrich"
	case "done":
		return " ✓"
	case "nodata":
		return " — нет данных"
	}
	return ""
}

func (s *phaseState) collectBar() string {
	const w = 30
	filled := 0
	if s.maxRuns > 0 {
		filled = s.collected * w / s.maxRuns
	}
	if filled > w {
		filled = w
	}
	tip := ""
	pad := w - filled
	if filled < w && s.collected > 0 {
		tip = ">"
		pad--
	}
	return fmt.Sprintf(" [%s%s%s] Collect %d/%d",
		strings.Repeat("=", filled), tip, strings.Repeat(" ", pad),
		s.collected, s.maxRuns)
}

// resolveRepoBranches builds the repo→branches map for the chosen mode. Each
// mode touches only its own input file: modeProject never reads or writes
// input.repo_branches_file, and the other two never read input.projects_file.
func resolveRepoBranches(scope runScope, cfg *config.Config) (map[string][]string, error) {
	switch scope.mode {
	case modeByLog:
		fmt.Println("\n=== Parse: Парсинг лога → repo_branches.json ===")
		repoBranches, err := parse.ParseLog(cfg.Input.LogFile, cfg.Input.IgnoreTasks)
		if err != nil {
			return nil, fmt.Errorf("parse phase failed: %w", err)
		}

		if len(repoBranches) == 0 {
			fmt.Println("  Не удалось извлечь данные из лога")
			fmt.Println("=== Parse завершена ===")
			return nil, nil
		}

		if err := parse.SaveRepoBranches(cfg.Input.RepoBranchesFile, repoBranches); err != nil {
			return nil, fmt.Errorf("save repo_branches: %w", err)
		}
		fmt.Printf("  Собрано %d проектов, сохранено в %s\n", len(repoBranches), cfg.Input.RepoBranchesFile)
		data, _ := json.MarshalIndent(repoBranches, "  ", "  ")
		fmt.Printf("  %s\n", string(data))
		fmt.Println("=== Parse завершена ===")
		return repoBranches, nil

	case modeProject:
		projects, err := parse.LoadProjects(cfg.Input.ProjectsFile)
		if err != nil {
			return nil, err
		}
		return resolveProjectScope(projects, scope.project, scope.branches)

	default: // modeByJSON
		return parse.LoadRepoBranches(cfg.Input.RepoBranchesFile)
	}
}

func main() {
	configPath := flag.String("config", config.DefaultPath(), "path to config file")
	jsonStdout := flag.Bool("stdout", false, "печатать JSON-отчёт в stdout вместо файла; остальной вывод уходит в stderr")
	var flags scopeFlags
	flags.register(flag.CommandLine)
	flag.Usage = usageFor(flag.CommandLine)
	flag.Parse()

	// Область анализа проверяется до конфига, MongoDB и GitHub —
	// неверная комбинация флагов падает без побочных эффектов.
	scope, err := flags.validate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n\n", err)
		flag.Usage()
		os.Exit(2)
	}

	// В режиме --stdout поток stdout принадлежит отчёту: всё человекочитаемое,
	// включая прогресс-бары и log, уходит в stderr, чтобы вывод можно было пайпить.
	var reportOut io.Writer
	if *jsonStdout {
		reportOut = os.Stdout
		os.Stdout = os.Stderr
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.DeprecatedSkipParse != nil {
		log.Printf("Ключ конфига skip_parse устарел и на запуск не влияет: "+
			"точку входа задают флаги --by-log / --by-json (сейчас %s)", scope.mode.name())
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN env var is required")
	}

	// Ensure output directory exists
	if err := os.MkdirAll(cfg.Output.Dir, 0755); err != nil {
		log.Fatalf("Failed to create output dir: %v", err)
	}

	// ========== Область анализа ==========
	repoBranches, err := resolveRepoBranches(scope, cfg)
	if err != nil {
		log.Fatalf("Failed to resolve scope: %v", err)
	}

	if len(repoBranches) == 0 {
		fmt.Println("Нет проектов для анализа")
		// stdout всегда получает валидный JSON — пустой отчёт не ломает пайп в jq.
		if reportOut != nil {
			if err := render.RenderJSONTo(reportOut, nil, cfg); err != nil {
				log.Fatalf("Render phase failed: %v", err)
			}
		}
		return
	}

	fmt.Printf("\nРежим: %s\nОбласть: %s\n", scope.mode.name(), formatRepoBranches(repoBranches))

	// ========== Collect → Analyze → Enrich per repo/branch ==========
	fmt.Println("\n=== Collect → Analyze → Enrich ===")

	cache, err := collect.NewCache(cfg.Mongo.URI, cfg.Mongo.DB, cfg.Mongo.Collection)
	if err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}
	defer cache.Close()

	// Calculate max name width for alignment
	nameWidth := 0
	for repo, branches := range repoBranches {
		for _, branch := range branches {
			if n := len(repo) + 1 + len(branch); n > nameWidth {
				nameWidth = n
			}
		}
	}

	maxRuns := cfg.Analysis.MaxRuns

	// Suppress verbose stdout during progress bar display;
	// capture log (stderr) messages in a buffer to show after.
	origStdout := os.Stdout
	devNull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout = devNull

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)

	p := mpb.New(mpb.WithOutput(origStdout))

	resultCh := make(chan models.RepoResult, 64)
	var wg sync.WaitGroup

	for repo, branches := range repoBranches {
		for _, branch := range branches {
			name := fmt.Sprintf("%s/%s", repo, branch)
			state := &phaseState{phase: "collect", maxRuns: maxRuns}

			spinner := p.New(0,
				mpb.SpinnerStyle(),
				mpb.BarWidth(1),
				mpb.BarFillerClearOnComplete(),
				mpb.PrependDecorators(
					decor.Name(name, decor.WC{W: nameWidth + 2, C: decor.DindentRight}),
				),
				mpb.AppendDecorators(
					decor.Any(func(s decor.Statistics) string {
						return state.render()
					}),
				),
			)

			wg.Add(1)
			go func(repo, branch string, spinner *mpb.Bar, state *phaseState) {
				defer wg.Done()

				// Collect — spinner animates, text shows progress bar
				cr := collect.Run(token, cfg, cache, repo, branch, func() {
					state.incr()
				})

				if cr == nil {
					state.set("nodata")
					spinner.SetTotal(1, true)
					return
				}

				// Analyze — spinner animates, text shows "Analyze"
				state.set("analyze")
				ar := analyze.Run(cr)

				// Enrich — spinner animates, text shows "Enrich"
				state.set("enrich")
				er := enrich.RunForRepo(cache, cfg.GitHub.Owner, cr, ar, repo)

				// Done — spinner clears, text shows "✓"
				state.set("done")
				spinner.SetTotal(1, true)

				resultCh <- models.RepoResult{
					Repo:    repo,
					Branch:  branch,
					Collect: cr,
					Analyze: ar,
					Enrich:  er,
				}
			}(repo, branch, spinner, state)
		}
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var allResults []models.RepoResult
	for r := range resultCh {
		allResults = append(allResults, r)
	}

	p.Wait()

	// Restore stdout and log output
	os.Stdout = origStdout
	devNull.Close()
	log.SetOutput(os.Stderr)

	if logBuf.Len() > 0 {
		fmt.Fprint(os.Stderr, logBuf.String())
	}

	// ========== Render phase ==========
	if len(allResults) > 0 || reportOut != nil {
		fmt.Println("\n=== Render: Генерация отчётов ===")
		if err := render.RenderAll(allResults, cfg, reportOut); err != nil {
			log.Fatalf("Render phase failed: %v", err)
		}
		fmt.Println("=== Render завершена ===")
	}

	fmt.Println("\n=== Готово ===")
}
