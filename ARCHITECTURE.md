# Архитектура release-engineer-helper

## 1. Общее описание проекта

`release-engineer-helper` — инструмент для release-инженера, который автоматизирует анализ падений тестов в GitHub Actions CI. Решает задачу: при подготовке релиза нужно понять, какие тесты падают стабильно (и с какого момента), какие починились, какие нестабильны (flaky), а какие упали впервые.

**Входные данные:** лог-файл release-helper в формате Ruby hash, содержащий перечень проектов, версий и задач, включённых в релиз.

**Выходные данные:** HTML-отчёты (один на каждую пару репозиторий/ветка) и сводный JSON-отчёт с классификацией каждого упавшего теста, паттерном поведения и вероятной причиной регрессии.

**Стек:** Go 1.24, MongoDB (кэш), GitHub Actions REST API, mpb (progress bar), html/template.

---

## 2. Фазы выполнения

Пайплайн состоит из 5 последовательных именованных фаз:

```
Parse  -->  Collect  -->  Analyze  -->  Enrich  -->  Render
```

Фазы Parse, Collect, Analyze, Enrich выполняются в строгом порядке. Внутри Collect и Render используется параллелизм. Каждая фаза имеет явный контракт: типизированный вход и типизированный выход.

---

### 2.1 Фаза Parse

**Файл:** `parse/logparser.go`

**Вход:** файл `1.log` (лог release-helper, формат Ruby hash)

```
{"hydra-core":
  {"6.3.0.3" =>
    {tasks: ["AIS-8967"],
     commits: ["cd2d254c..."],
     translations: []}}}
```

**Выход:** `repo_branches.json` — JSON-файл вида `map[repo][]branch`

```json
{
  "hydra-core": ["v6.3"],
  "hydra-messages-relay": ["v6.2", "v6.2.1"]
}
```

**Что делает:**

1. Читает весь файл в память как строку.
2. Находит секции верхнего уровня (имена проектов) через regex `(?m)^[{ ]"?([a-zA-Z][a-zA-Z0-9_-]*)"?\s*:\s*$`.
3. Внутри каждой секции находит ключи версий `"6.3.0.3" =>` и секции `tasks: [...]`.
4. Фильтрует задачи из списка `ignore_tasks` (конфиг).
5. Конвертирует версии в имена веток через `versionToBranch()`:
   - `6.2.1.5` → `v6.2.1` (4+ частей, 3-я часть != 0)
   - `6.2.0.3` → `v6.2` (4+ частей, 3-я часть == 0)
   - `6.2.2` → `v6.2` (3 части → берём первые 2)
   - `6.3` → `v6.3` (2 части → as-is)
6. Дедуплицирует ветки (несколько версий могут давать одну ветку), сортирует.
7. Сохраняет результат в `repo_branches.json`.

**Опциональность:** если `skip_parse: true` в конфиге — фаза пропускается, `repo_branches.json` загружается напрямую.

---

### 2.2 Фаза Collect

**Файлы:** `collect/collector.go`, `collect/github.go`, `collect/cache.go`, `collect/extractor_artifacts.go`, `collect/extractor_logs.go`

**Вход:** `map[repo][]branch` + конфигурация + GITHUB_TOKEN

**Выход:** `*CollectResult`

```go
type CollectResult struct {
    Summary         map[string]StringSet         // compositeKey → set of failed test names
    Meta            map[string]RunMeta           // compositeKey → run metadata
    AllTestDetails  map[string][]TestDetail      // testName → detail items
    MasterFailed    StringSet                    // tests failing in master
    AllBranchRunIDs []int                        // ALL completed run IDs for the branch
    OrderedKeys     []string                     // composite keys, oldest first
}
```

**Что делает:**

1. Подключается к MongoDB (кэш) и создаёт `GitHubClient`.
2. Если ветка не master — загружает последний запуск master для получения множества `MasterFailed` (тесты, которые уже падали до изменений ветки).
3. Вызывает `collectValidRuns()` — параллельный сбор до `max_runs` валидных запусков.
4. Разворачивает список запусков в хронологический порядок (старые → новые).
5. Вызывает `buildSummary()` — строит `Summary`, `Meta`, `AllTestDetails`.

**Логика извлечения результатов для одного запуска** (`loadOrExtract()`):

```
1. Проверить MongoDB cache (owner, repo, run_id)
   cache hit → вернуть детали
   cache miss или has_no_tests=true → перейти к шагу 2

2. Попробовать артефакты (ArtifactExtractor):
   - Запросить список артефактов /actions/runs/{id}/artifacts
   - Найти test-reports-* (не истёкшие)
   - Скачать zip, распарсить JUnit XML внутри
   - Если нашли testcase → валидный результат

3. Если артефактов нет → fallback на LogExtractor:
   - Скачать zip-лог /actions/runs/{id}/logs
   - Парсить .txt файлы по regex-паттернам CI:
     ##[group]🚀 Publish results
     ℹ️ - test results ... N failed
     🧪 - TestName | description
     ##[error] ...

4. Сохранить в MongoDB cache (upsert)
```

**buildSummary()** — финальная сборка результата (выполняется последовательно):

Для каждого валидного запуска:
- Вызывает `GetCommitTitle(repo, sha)` — HTTP GET к GitHub API.
- Строит `compositeKey = "{sha}_{run_id}"`.
- Сортирует упавшие тесты по `order_index`.
- Добавляет в `Summary`, `Meta`, `AllTestDetails`.

---

### 2.3 Фаза Analyze

**Файлы:** `analyze/analyzer.go`, `analyze/models.go`

**Вход:** `*CollectResult`

**Выход:** `*AnalyzeResult`

```go
type AnalyzeResult struct {
    Behavior BehaviorAnalysis  // классифицированные тесты
    RunDiffs []RunDiff         // diff между последовательными запусками
    Stats    Stats             // агрегированная статистика
}
```

**Что делает:** чистая вычислительная фаза, нет I/O.

**Построение матрицы состояний:**

Для каждого теста, встретившегося хотя бы в одном запуске, строится вектор `[]bool` длиной `len(OrderedKeys)`, где `true` означает "тест упал в этом запуске".

```
Запуски:  [run_0, run_1, run_2, run_3, run_4]
TestA:    [false,  true,  true,  true,  true]  → stable_failing
TestB:    [false,  true, false, false, false]  → fixed
TestC:    [false,  true, false,  true, false]  → flaky (3 перехода > 2)
TestD:    [false, false, false, false,  true]  → single_failure
```

**Классификация** (`analyzeTestPattern()`):

| Условие | Тип |
|---------|-----|
| failCount == 1 | `single_failure` |
| lastFailIdx != totalRuns-1 AND переходов <= 2 | `fixed` |
| lastFailIdx == totalRuns-1 AND стабильно с firstFailIdx | `stable_failing` |
| переходов > 2 | `flaky` |

`isStableFailingFrom(states, idx)` — возвращает true если все состояния от idx до конца равны true.

`hasFlakyBehavior(states)` — считает количество переходов (смен значения), flaky если > 2.

**RunDiff** — для каждого запуска вычисляется:
- `Added` = current - prev (новые падения)
- `Removed` = prev - current (починенные)
- `OnlyHere` = current - masterFailed (уникальные, не в master)

**Stats:**
- `TotalRuns` — количество проанализированных запусков
- `UniqueFailedTests` — объединение всех упавших тестов
- `MasterFailedTests` — количество тестов, падающих в master
- `NewFailures` — уникальных упавших тестов, не падающих в master

---

### 2.4 Фаза Enrich

**Файлы:** `enrich/enricher.go`, `enrich/models.go`

**Вход:** `*CollectResult` + `*AnalyzeResult` + конфигурация

**Выход:** `*EnrichResult`

```go
type EnrichResult struct {
    StableSince map[string]StableSinceInfo  // testName → {RunID, CreatedAt}
}
```

**Что делает:**

Для каждого теста из `BehaviorAnalysis.StableFailing` выполняет MongoDB-запрос `FindEarliestRunWithTests()`:

```
db.parsed_results.findOne(
  { owner, repo, has_no_tests: false,
    run_id: { $in: allBranchRunIDs },
    "details_list.test_name": testName },
  sort: { run_id: 1 },
  projection: { run_id: 1, created_at: 1 }
)
```

Запросы выполняются **последовательно** (цикл по `testNames`). Использует список `AllBranchRunIDs` из фазы Collect — дополнительных вызовов GitHub API нет. Это позволяет найти момент начала стабильного падения за пределами анализируемого окна (окно = maxRuns запусков).

---

### 2.5 Фаза Render

**Файлы:** `render/renderer.go`, `render/html.go`, `render/json.go`, `render/report.html.tmpl`

**Вход:** `[]RepoResult` — результаты всех фаз для всех repo/branch

**Выход:** HTML-файлы + JSON-файл

```go
type RepoResult struct {
    Repo    string
    Branch  string
    Collect *CollectResult
    Analyze *AnalyzeResult
    Enrich  *EnrichResult
}
```

**Что делает:**

Параллельно запускает:
- Одну горутину для каждого repo/branch: `RenderHTML()` → `{output_dir}/{branch}/failed_tests_{repo}.html`
- Одну горутину для JSON: `RenderJSON()` → `{output_dir}/report_{YYYYMMDD_HHMMSS}.json`

**HTML-отчёт** содержит:
1. Секции поведения: стабильно падающие / починенные / flaky тесты.
2. Diff-секции для каждого запуска: новые / починенные / уникальные / все падения.
3. Тесты группируются по `::` разделителю в дерево (`groupIntoTree()`).
4. Детали ошибок сериализуются в JSON и встраиваются в `<script>` для раскрытия по клику.

**JSON-отчёт** — единый файл для всех проектов. Для каждого теста в последнем запуске вычисляются:
- `classification` (stable_failing / fixed / flaky / single_failure)
- `fail_rate_pct` — процент запусков с падением
- `pattern` — строка из эмодзи 🔴/🟢
- `probable_cause` — коммит, начавший текущую непрерывную серию падений (`findStreakStart()`)
- `failing_since` — из EnrichResult (дата первого появления в MongoDB)
- `first_seen_in_analysis` — первый запуск с этим тестом в текущем окне анализа

---

## 3. Потоки данных

```
 config.yaml ──────────────────────────────────────────────────────┐
 GITHUB_TOKEN                                                       │
 1.log ──► [Parse] ──► repo_branches.json                         │
                                │                                   │
                    map[repo][]branch                               │
                                │                                   │
              ┌─────────────────┼─────────────────┐               │
              │     per-repo/branch goroutine       │               │
              │                 ▼                   │               │
              │          [Collect]                  │◄──────────────┤
              │    GitHub API + MongoDB cache       │               │
              │                 │                   │               │
              │         CollectResult               │               │
              │   {Summary, Meta, AllTestDetails,   │               │
              │    MasterFailed, AllBranchRunIDs,   │               │
              │    OrderedKeys}                     │               │
              │                 │                   │               │
              │                 ▼                   │               │
              │          [Analyze]                  │               │
              │      pure computation               │               │
              │                 │                   │               │
              │         AnalyzeResult               │               │
              │   {Behavior, RunDiffs, Stats}       │               │
              │                 │                   │               │
              │                 ▼                   │               │
              │          [Enrich]                   │◄──────────────┘
              │     MongoDB FindEarliestRun          │
              │                 │                   │
              │         EnrichResult                │
              │   {StableSince}                     │
              │                 │                   │
              │           RepoResult ──────────────►│
              └─────────────────────────────────────┘
                                │
                       []RepoResult
                                │
                          [Render]
                    ┌───────────┴────────────┐
                    ▼                        ▼
              RenderHTML()            RenderJSON()
         (per-repo/branch)          (один файл)
              .html файл           .json файл
```

### Ключевые структуры данных

```
internal.TestDetail
├── File       string   // имя файла внутри zip-архива
├── LineNum    int      // номер строки (0 для JUnit)
├── Context    string   // текст ошибки
├── Project    string   // имя артефакта (e.g. "hydra-core-tests")
└── OrderIndex int      // позиция в исходном порядке

internal.RunMeta
├── SHA          string   // git commit sha
├── RunID        int      // GitHub Actions run ID
├── Title        string   // первая строка commit message
├── Timestamp    string   // "2006-01-02 15:04:05"
├── Conclusion   string   // "success" | "failure"
├── Link         string   // URL в GitHub
├── Branch       string   // имя ветки
├── Order        []string // имена упавших тестов в порядке парсинга
└── CompositeKey string   // "{sha}_{run_id}"

collect.CollectResult
├── Summary         map[compositeKey]StringSet   // key → set упавших тестов
├── Meta            map[compositeKey]RunMeta     // key → метаданные
├── AllTestDetails  map[testName][]TestDetail    // имя теста → детали ошибок
├── MasterFailed    StringSet                    // упавшие в master
├── AllBranchRunIDs []int                        // все run_id ветки
└── OrderedKeys     []string                     // ключи, oldest-first

analyze.AnalyzeResult
├── Behavior
│   ├── StableFailing  map[testName]*TestBehavior
│   ├── FixedTests     map[testName]*TestBehavior
│   └── FlakyTests     map[testName]*TestBehavior
├── RunDiffs   []RunDiff   // diff между последовательными запусками
└── Stats      Stats

analyze.TestBehavior
├── Type           string   // "stable_failing" | "fixed" | "flaky" | "single_failure"
├── TestName       string
├── TotalRuns      int
├── FailCount      int
├── Pattern        string   // "🔴🟢🔴🔴" (по числу запусков)
├── FailedRuns     []FailedRunInfo
├── NextPRLink     string   // ссылка на коммит, починивший тест (для fixed)
└── NextCommitInfo *CommitInfo
```

---

## 4. Параллелизм

### 4.1 Уровень main.go — горутины per-repo/branch

`main.go` запускает по одной горутине на каждую пару `(repo, branch)`. Все горутины стартуют одновременно. Результаты собираются через буферизированный канал `resultCh`:

```
main goroutine
     │
     ├── go func(repo="hydra-core", branch="v6.3")
     │       Collect → Analyze → Enrich → resultCh
     │
     ├── go func(repo="hydra-core", branch="v6.2")
     │       Collect → Analyze → Enrich → resultCh
     │
     ├── go func(repo="hydra-messages-relay", branch="v6.2")
     │       Collect → Analyze → Enrich → resultCh
     │
     └── ...
           │
           ▼
     wg.Wait() → close(resultCh)
           │
     for r := range resultCh → allResults
           │
     p.Wait() (mpb прогресс)
           │
     RenderAll(allResults)
```

Каждая горутина:
1. Вызывает `collect.Run()` (долго, I/O).
2. Вызывает `analyze.Run()` (быстро, CPU).
3. Вызывает `enrich.RunForRepo()` (MongoDB-запросы).
4. Отправляет `RepoResult` в `resultCh`.

### 4.2 Уровень collectValidRuns — paginator + worker pool

Это наиболее сложная concurrency-конструкция в проекте. Три участника: paginator, workers, orchestrator (вызывающий код).

```
collectValidRuns()
│
├── ctx, cancel = context.WithCancel()
│
├── candidateCh (chan candidateRun, buf=100)  ← буферизованный
├── allRunIDsCh  (chan []int, buf=1)
├── resultCh     (chan runResult, buf=maxRuns)
│
├── [Paginator goroutine]
│   ├── for page := 1..maxPages (10):
│   │   ├── FetchRunsPage() → []ghWorkflowRun  (HTTP)
│   │   ├── фильтр: status==completed, conclusion==success|failure
│   │   ├── append allIDs (всегда, даже после cancel)
│   │   └── if !cancelled: candidateCh <- candidateRun{index, run}
│   │                       или ctx.Done() → cancelled=true
│   ├── defer close(candidateCh)
│   └── defer allRunIDsCh <- allIDs
│
├── [Phase 1: запуск начального пула]
│   for i := 0; i < maxRuns; i++:
│       c := <-candidateCh  (блокирует пока paginator не пришлёт)
│       inFlight++
│       go processCandidate(c) → resultCh
│
└── [Phase 2: оркестрация - сбор результатов + замена невалидных]
    for inFlight > 0 AND len(validResults) < maxRuns:
        r := <-resultCh
        inFlight--
        if r.valid:
            validResults = append(validResults, r)
            onProgress()
        else:
            c, ok := <-candidateCh  // взять следующий кандидат
            if ok:
                inFlight++
                go processCandidate(c) → resultCh
    │
    cancel()   ← сигнал paginator'у прекратить отправку
    allRunIDs := <-allRunIDsCh  ← ждём paginator'а
    sort validResults by candidate.index
```

**Ключевые свойства:**
- В любой момент времени не более `maxRuns` горутин `processCandidate` работают одновременно.
- При инвалидном результате (нет тестов) слот немедленно заполняется следующим кандидатом.
- Paginator продолжает обходить страницы после cancel() для сбора `allRunIDs` (нужны для фазы Enrich).
- `candidateCh` буферизован на 100 — paginator не блокируется при заполнении начального пула.

### 4.3 ASCII-диаграмма concurrency модели внутри collectValidRuns

```
Paginator goroutine
┌──────────────────────────────────────────────┐
│ page 1 → FetchRunsPage() [HTTP]              │
│   run_101 → candidateCh (idx=0)             │
│   run_102 → candidateCh (idx=1)             │
│   ...                                        │
│ page 2 → FetchRunsPage() [HTTP]              │
│   run_201 → candidateCh (idx=N)             │
│   ...                                        │
│ <ctx.Done()> → cancelled=true               │
│   (продолжает накапливать allIDs)            │
│ close(candidateCh)                           │
│ allRunIDsCh <- allIDs                        │
└──────────────────────────────────────────────┘
         │ candidateCh (buf=100)
         ▼
Orchestrator (main goroutine внутри collectValidRuns)
┌──────────────────────────────────────────────┐
│ Phase 1: запуск начальных maxRuns воркеров   │
│   candidateCh → go processCandidate #1       │──► Worker #1
│   candidateCh → go processCandidate #2       │──► Worker #2
│   ...                                        │
│   candidateCh → go processCandidate #maxRuns │──► Worker #N
│                                              │
│ Phase 2: читаем resultCh                     │
│   result.valid=true  → validResults++        │◄── Worker #K (cache hit)
│   result.valid=false → candidateCh →         │◄── Worker #M (no tests)
│                         go processCandidate  │──► Worker #M+1 (замена)
│   ...                                        │
│   len(validResults)==maxRuns → cancel()      │
│   <-allRunIDsCh                              │◄── Paginator (финал)
└──────────────────────────────────────────────┘

Worker (processCandidate) — N параллельных горутин:
┌──────────────────────────────────────────────┐
│ loadOrExtract(runID):                        │
│   1. cache.Load() [MongoDB]                  │
│   2. artExt.Extract() [HTTP: artifacts]      │
│      → ListRunArtifacts()                    │
│      → DownloadArtifact() × N               │
│      → parseJUnitZip()                       │
│   3. (fallback) gh.DownloadLogs() [HTTP]     │
│      → logExt.ParseZip()                     │
│   4. cache.Save() [MongoDB]                  │
│ resultCh <- runResult{valid, details}        │
└──────────────────────────────────────────────┘

Каналы:
  candidateCh  chan candidateRun  buf=100   Paginator → Orchestrator/Workers
  resultCh     chan runResult     buf=maxRuns  Workers → Orchestrator
  allRunIDsCh  chan []int         buf=1     Paginator → Orchestrator (финал)
```

### 4.4 mpb progress bar и горутины

`mpb.New()` создаёт отдельный поток отрисовки. Каждый spinner (`p.New()`) обновляется через `decor.Any()` с замыканием на `phaseState`:

```
main goroutine                   mpb render goroutine
     │                                    │
     │  p.New(spinner, AppendDecorators(  │
     │    decor.Any(func() {              │
     │      state.render()  ◄─────────────┼── вызывается mpb при каждом кадре
     │    })))                            │
     │                                    │
     │  go func(repo, branch):           │
     │    collect.Run(... onProgress: func() {
     │      state.incr()  // mu.Lock/Unlock
     │      state.phase = "collect"
     │    })                              │
     │    state.set("analyze")           │
     │    state.set("enrich")            │
     │    state.set("done")              │
     │    spinner.SetTotal(1, true)  ◄───┼── сигнал завершения bar'у
     │                                   │
     │  p.Wait() ◄───────────────────────┼── ждём все spinners done
```

`phaseState` защищён `sync.Mutex`. Метод `render()` вызывается из mpb-потока, `incr()`/`set()` — из горутин repo/branch. Конкурентный доступ корректен.

**Подавление stdout:** во время отрисовки `os.Stdout` перенаправлен в `/dev/null` (иначе `fmt.Printf` из collect/analyze ломает выравнивание spinners). Сообщения log-пакета буферизуются в `logBuf` и выводятся после завершения.

### 4.5 Фаза Render — параллелизм

```
RenderAll([]RepoResult)
     │
     ├── cfg.Output.GenerateHTML == true:
     │   ├── wg.Add(1); go RenderHTML(results[0]) → HTML файл
     │   ├── wg.Add(1); go RenderHTML(results[1]) → HTML файл
     │   └── ...
     │
     ├── cfg.Output.GenerateJSON == true:
     │   └── wg.Add(1); go RenderJSON(results)    → JSON файл
     │
     └── wg.Wait()
```

Все HTML-горутины + JSON-горутина работают одновременно. Ошибки собираются через `mu.Lock` в `[]error`.

---

## 5. Узкие места (bottlenecks)

### 5.1 buildSummary — последовательные GetCommitTitle()

**Самое значимое узкое место.** После сбора всех валидных запусков `buildSummary()` вызывает `GetCommitTitle(repo, sha)` для каждого из них в цикле:

```go
for _, pr := range runs {  // len(runs) == maxRuns
    title := gh.GetCommitTitle(repo, sha)  // HTTP GET к GitHub API
    ...
}
```

При `maxRuns=100` это 100 последовательных HTTP-запросов. Каждый занимает 100-500ms → суммарно 10-50 секунд на один repo/branch. Это происходит после того, как `collectValidRuns()` уже завершил параллельный сбор данных.

**Потенциальное решение:** распараллелить через goroutines + channel или `sync.WaitGroup`, собирая titles конкурентно.

### 5.2 getMasterFailed — блокирующий вызов перед collectValidRuns()

```go
masterFailed = getMasterFailed(...)   // HTTP + MongoDB/IO
validRuns, allIDs := collectValidRuns(...)
```

Загрузка master-ветки выполняется последовательно до начала основного сбора. Однако это одна страница + один zip, обычно быстро.

### 5.3 enrich.FindEarliestRunWithTests — N последовательных MongoDB-запросов

В `enrich/enricher.go` запросы к MongoDB выполняются в цикле:

```go
for _, testName := range testNames {
    c.coll.FindOne(ctx, filter, opts)  // один запрос на тест
}
```

При большом количестве `stable_failing` тестов (десятки-сотни) это N последовательных round-trip'ов к MongoDB. Потенциально можно ускорить через агрегацию или параллельные запросы.

### 5.4 Paginator — не все страницы нужны

Paginator обходит до 10 страниц по 100 запусков = 1000 кандидатов. При `maxRuns=100` и хорошем cache hit rate все 100 валидных запусков набираются на первых 1-2 страницах, но paginator продолжает работать для сбора `allRunIDs`. Это необходимо по дизайну (для EnrichResult), но создаёт лишние HTTP-запросы к GitHub API.

### 5.5 LogExtractor — fallback с полной загрузкой логов

Zip-лог запуска может весить десятки мегабайт. Если артефакты недоступны, `DownloadLogs()` скачивает и держит весь zip в памяти. При maxRuns параллельных воркерах — пиковое потребление памяти: `maxRuns × размер_zip`.

---

## 6. Зависимости (go.mod)

| Пакет | Назначение |
|-------|-----------|
| `go.mongodb.org/mongo-driver` | MongoDB client для кэша |
| `gopkg.in/yaml.v3` | Парсинг config.yaml |
| `github.com/vbauerster/mpb/v8` | Progress bar в терминале |
| `golang.org/x/sync` | (транзитивная зависимость) |

Стандартная библиотека Go используется для: `archive/zip`, `encoding/xml`, `encoding/json`, `html/template`, `net/http`, `regexp`, `sync`.
