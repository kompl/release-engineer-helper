# architecture

Branch: main
Git-HEAD: eb38f9b
Stack: Go 1.24 / MongoDB / GitHub Actions API / mpb / html/template

## Relevant files

- `main.go` — точка входа; оркестрация фаз, управление прогрессом mpb, параллельный запуск per-repo/branch горутин
- `config/config.go` — загрузка YAML-конфигурации, структуры GitHubConfig, MongoConfig, AnalysisConfig, OutputConfig, InputConfig
- `parse/logparser.go` — регулярки для парсинга Ruby hash лога, versionToBranch, SaveRepoBranches/LoadRepoBranches
- `internal/models.go` — общие типы: TestDetail, RunMeta, StringSet (set of strings с операциями Difference/Union)
- `collect/models.go` — CollectResult, RunProcessResult (содержит HasNoTests bool), ghWorkflowRun, ghArtifact и API-ответы
- `collect/collector.go` — Run(), collectValidRuns(), processCandidate(), loadOrExtract(), buildSummary(); ядро всей параллельной механики; GetCommitTitle вызывается в processCandidate (параллельно, не в buildSummary)
- `collect/github.go` — GitHubClient: FetchRunsPage, GetLatestCompletedRun, GetCommitTitle, DownloadLogs, ListRunArtifacts, DownloadArtifact
- `collect/cache.go` — MongoDB cache: Load/Save/FindEarliestRunWithTests; схема документа, upsert
- `collect/extractor_artifacts.go` — ArtifactExtractor: JUnit XML из zip-артефактов
- `collect/extractor_logs.go` — LogExtractor: regex-парсинг текстовых zip-логов GitHub Actions
- `analyze/models.go` — AnalyzeResult, BehaviorAnalysis, TestBehavior, RunDiff, Stats
- `analyze/analyzer.go` — Run(); analyzeTestBehavior(), analyzeTestPattern(), getRunDiffs(), getStatistics()
- `enrich/models.go` — EnrichResult{StableSince map[string]StableSinceInfo}
- `enrich/enricher.go` — RunForRepo(); FindEarliestRunWithTests через MongoDB
- `render/renderer.go` — RenderAll(); параллельный запуск HTML и JSON горутин
- `render/html.go` — RenderHTML(); buildHTMLRuns(), buildLeafLabel(), groupIntoTree()
- `render/json.go` — RenderJSON(); buildRepoJSONData(), buildFailedTests(), findStreakStart(); extractError схлопывает многострочное сообщение в одну строку и усекает до 300 символов

## Patterns in use

- **Фазовая архитектура** — каждая фаза имеет явный входной и выходной тип (`CollectResult`, `AnalyzeResult`, `EnrichResult`), фазы изолированы
- **Worker pool с динамической заменой** — в collectValidRuns() начальный пул из maxRuns горутин, при получении невалидного результата немедленно запускается замена из очереди кандидатов
- **Context cancellation** — paginator-горутина получает ctx; после сбора maxRuns валидных запусков вызывается cancel(), paginator прекращает отправку кандидатов, но продолжает обходить страницы для сбора allRunIDs
- **MongoDB как кэш** — результаты парсинга логов/артефактов кэшируются по ключу (owner, repo, run_id); повторный запуск пропускает дорогие HTTP-скачивания
- **Dual extractor** — артефакты (JUnit XML) приоритетнее логов; fallback на лог-парсер только если артефактов нет
- **mpb progress bar** — spinner-бар на каждый repo/branch, обновляется через decor.Any() с замыканием на phaseState; stdout подавляется, чтобы не ломать отрисовку
- **StringSet** — простой type-alias map[string]struct{} с Difference/Union, используется как ключевая структура для множеств тестов
- **Матрица состояний** — для Analyze: двумерный массив bool[test][run_index], по которому определяется тип поведения теста
- **Composite key** — `"{sha}_{run_id}"` как уникальный идентификатор запуска, обеспечивает уникальность при нескольких запусках на один коммит

## Constraints

- GitHub API: до 100 запусков на страницу, до 10 страниц (maxPages=10) — итого максимум 1000 кандидатов на обработку
- GitHub API timeout: 60 секунд на HTTP-запрос
- MongoDB timeout: 10 секунд на connect, 5 секунд на операцию Load/Save, 30 секунд на FindEarliestRunWithTests
- GetCommitTitle() вызывается в processCandidate() параллельно для каждого валидного запуска (ранее был последовательным в buildSummary — исправлено в 7a4f0c8)
- HTML-шаблон ищется рядом с бинарником (render/report.html.tmpl), fallback — относительный путь от cwd

## Recommended approach

При доработке:
- При добавлении новых фаз соблюдать контракт: фаза принимает typed struct, возвращает typed struct, не знает о соседних фазах
- Кэш MongoDB совместим с Python-версией (schema: 2) — при изменении схемы обновлять версию schema и добавлять миграцию
- LogExtractor работает с форматом вывода конкретной CI-системы (паттерны ##[group], 🚀, 🧪) — при смене CI нужен новый экстрактор
