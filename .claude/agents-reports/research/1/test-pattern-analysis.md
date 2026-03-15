# test-pattern-analysis

| Поле     | Значение                              |
|----------|---------------------------------------|
| Branch   | `main`                                |
| Git-HEAD | `eb38f9b`                             |
| Stack    | Go 1.24 / unicode/utf8 / strings      |
| Scope    | `analyze/`, `render/json.go`          |
| Updated  | 2026-03-13                            |

## Summary

Область описывает полный цикл от сбора сырых флагов "тест упал/прошёл" до финального эмодзи-паттерна в JSON-отчёте. Включает построение матрицы состояний, классификацию поведения теста (stable_failing / fixed / flaky / single_failure), а также обработку запусков, в которых тесты вообще не запустились.

---

## Key files

| Путь | Роль |
|------|------|
| `analyze/analyzer.go` | Весь алгоритм классификации: `analyzeTestBehavior`, `analyzeTestPattern`, `hasFlakyBehavior`, `isStableFailingFrom` |
| `analyze/models.go` | Типы `TestBehavior` (содержит поле `Pattern string`), `BehaviorAnalysis`, `FailedRunInfo` |
| `collect/collector.go` | `collectValidRuns` — фильтрация запусков без тестов (`hasNoTests`); `buildSummary` — строит `Summary map[compositeKey]StringSet` и `OrderedKeys` |
| `collect/models.go` | `CollectResult`, `RunProcessResult.HasNoTests bool` |
| `collect/extractor_artifacts.go` | Источник `hasNoTests` при парсинге JUnit: если ни один `<testcase>` не найден — `hasNoTests=true` |
| `collect/extractor_logs.go` | Источник `hasNoTests` для log-fallback: строка "No test results found" или пустой `failed` map |
| `render/json.go` | Читает `behavior.Pattern` и копирует его в `jsonFailedTest.Pattern`; `findStreakStart` разбирает паттерн посимвольно (rune) |

---

## Architecture & patterns

### 1. Откуда берётся `hasNoTests`

Флаг `hasNoTests` производится двумя экстракторами и определяет, является ли запуск "валидным":

**ArtifactExtractor** (`extractor_artifacts.go`):
```go
hasNoTests := !foundAnyJUnit
// foundAnyJUnit = true, если в любом XML встретился хотя бы один <testcase>
// (даже если он прошёл — важен сам факт существования тестов)
```

**LogExtractor** (`extractor_logs.go`):
```go
// Сначала — быстрый сканер строки "No test results found"
if le.patternNoTests.MatchString(ln) {
    hasNoTests = true
}
// ...после прохода по всем файлам:
hasNoTests = len(failed) == 0
// Т.е. если не нашли ни одного упавшего — тоже hasNoTests
```

Важно: `LogExtractor` считает запуск невалидным, если упавших тестов нет — даже если тесты реально запускались и все прошли. `ArtifactExtractor` точнее: он смотрит на `hasAnyTestCase`, а не на наличие провалов.

### 2. Фильтрация в `collectValidRuns`

```go
if r.valid {          // valid = !hasNoTests
    validResults = append(validResults, r)
} else {
    // Запуск без тестов — "No test results, replacing"
    // Берём следующего кандидата из очереди
    c, ok := <-candidateCh
    if ok { go processCandidate(...) }
}
```

Запуски с `hasNoTests=true` **не попадают** в `CollectResult.Summary` и `OrderedKeys`. Таким образом, паттерн строится только по запускам с тестами.

Исключение — кэш: если `has_no_tests=true` сохранено в MongoDB, запись считается некорректной и будет перечитана (`Cache hit invalid for run X (has_no_tests=true), re-extracting`).

### 3. Построение матрицы состояний

В `analyzeTestBehavior` (`analyze/analyzer.go`):

```go
// Для каждого теста, который хотя бы раз упал:
states := make([]bool, len(orderedKeys))
for i, key := range orderedKeys {
    states[i] = cr.Summary[key].Contains(t) // true = тест упал в этом запуске
}
```

`states` — срез `bool` длиной `len(OrderedKeys)`, индексированный хронологически (старший запуск = index 0).
Запуски без тестов в `orderedKeys` **отсутствуют** (отфильтрованы на этапе collect), поэтому в матрице нет "дыр".

### 4. Построение паттерна (`analyzeTestPattern`)

```go
var patternBuilder strings.Builder
for _, s := range states {
    if s {
        patternBuilder.WriteString("🔴") // тест упал
    } else {
        patternBuilder.WriteString("🟢") // тест прошёл
    }
}
```

Паттерн — конкатенация эмодзи в хронологическом порядке (слева = старый запуск, справа = новый). Хранится в `TestBehavior.Pattern`.

Пример: `"🟢🟢🔴🔴🟢🔴"` — тест упал на 3-м и 4-м запусках, затем прошёл, затем снова упал.

### 5. Классификация (`analyzeTestPattern`, продолжение)

Алгоритм после подсчёта `failCount`, `firstFailIdx`, `lastFailIdx`:

```
failCount == 0             → "never_failed"  (не сохраняется)
failCount == 1             → "single_failure" (не сохраняется)
firstFailIdx == lastFailIdx → "single_failure"
lastFailIdx == totalRuns-1  → последний запуск красный:
    isStableFailingFrom(states, firstFailIdx) → "stable_failing"
    иначе                                     → "flaky"
иначе (последний запуск зелёный):
    hasFlakyBehavior(states) → "flaky"
    иначе                   → "fixed"
```

### 6. `hasFlakyBehavior`

```go
func hasFlakyBehavior(states []bool) bool {
    transitions := 0
    for i := 1; i < len(states); i++ {
        if states[i] != states[i-1] {
            transitions++
        }
    }
    return transitions > 2
}
```

Критерий: **более 2 переходов** между состояниями (красный↔зелёный). Порог `> 2` означает, что одиночный "прошёл-упал-прошёл" (2 перехода) ещё не является flaky — нужно минимум 3 смены.

### 7. `isStableFailingFrom`

```go
func isStableFailingFrom(states []bool, startIdx int) bool {
    for i := startIdx; i < len(states); i++ {
        if !states[i] { return false }
    }
    return true
}
```

Проверяет: все состояния от `startIdx` до конца = `true` (красные). Если да — тест стабильно падает с определённого момента.

### 8. Передача паттерна в JSON-рендерер

В `render/json.go`, `buildFailedTests`:

```go
behavior := allBehavior[testName]  // *analyze.TestBehavior
if behavior != nil {
    entry.Pattern = behavior.Pattern  // уже готовая эмодзи-строка
}
```

Паттерн не перестраивается — он просто копируется из `TestBehavior.Pattern` в `jsonFailedTest.Pattern`.

### 9. `findStreakStart` — разбор паттерна обратно

Функция в `render/json.go` определяет начало текущей серии провалов для поля `probable_cause`:

```go
runes := []rune(pattern)
// Фильтрует только 🔴 и 🟢, строит срез positions []rune
// Проверяет: последний символ должен быть 🔴 (иначе nil)
// Идёт назад пока positions[idx-1] == redCircle
// orderedKeys[idx] — это и есть начало серии
```

Важный нюанс: итерация по `[]rune` (не `[]byte`) нужна потому что каждый emoji занимает несколько байт, но один rune. Без этого индексация `orderedKeys` была бы неверной.

### 10. "Нет тестов" / "Build failed"

Концепция "запуск без тестов" (`hasNoTests`) существует только на уровне collect-фазы. В `AnalyzeResult` таких запусков нет — они отфильтрованы.

Однако в `render/json.go` есть специальная обработка случая, когда последний валидный запуск имеет `Conclusion == "failure"` и при этом `latestFailed.Len() == 0` (то есть тесты не упали, но билд сломан):

```go
if latestFailed.Len() == 0 && latestMeta.Conclusion == "failure" {
    // Ищем последний запуск с реальными результатами тестов
    for i := len(orderedKeys) - 2; i >= 0; i-- {
        key := orderedKeys[i]
        failed := cr.Summary[key]
        if failed.Len() > 0 {
            project.LatestRunWithTestResults = &jsonRunWithTests{...}
            break
        }
    }
}
```

Это заполняет поле `latest_run_with_test_results` в JSON, отдельное от `latest_run`. Т.е. потребитель JSON получает оба: последний запуск (возможно без тестов) и последний с реальными результатами.

---

## Conventions

- **Тест-ключ** формируется как `classname::name` (JUnit) или `testKey + " | " + description` (logs). Формат одинаков в обоих экстракторах.
- **CompositeKey** = `"{sha}_{run_id}"` — уникален, даже если два запуска на один коммит.
- **Индексация 1-based** — `FirstFailRun`, `LastFailRun`, `RunNumber` в `FailedRunInfo` — единица-базированные для UI; внутри кода используется 0-based `firstFailIdx`.
- **Паттерн хранится в `TestBehavior`**, не пересчитывается при рендеринге.

---

## Constraints & gotchas

- `hasFlakyBehavior` вызывается только когда `lastFailIdx != totalRuns-1` (последний зелёный). Если последний запуск красный и тест нестабильный — он получит `"flaky"` через ветку `isStableFailingFrom → false`, а не через `hasFlakyBehavior`.
- `"single_failure"` и `"never_failed"` не попадают в `BehaviorAnalysis` и не видны в JSON-отчёте вообще.
- `LogExtractor` устанавливает `hasNoTests = len(failed) == 0` в конце — это значит, что запуск где все тесты прошли, LogExtractor отфильтрует как "нет тестов". Только `ArtifactExtractor` корректно обрабатывает этот случай через `foundAnyJUnit`.
- `findStreakStart` возвращает `nil` если последний символ паттерна не `🔴` — т.е. для `fixed` тестов `probable_cause` будет `nil` через этот путь.
- В `buildFailedTests` если `behavior == nil` (тест не был в `allBehavior`, т.е. `single_failure`), `probable_cause` заполняется метаданными текущего запуска напрямую, без анализа паттерна.
- После фикса в `7a4f0c8`: `GetCommitTitle` вызывается в `processCandidate` (параллельно), а не в `buildSummary` (последовательно) — это устранило N последовательных HTTP-вызовов.

---

## Reference commits

| SHA (short) | Что показывает |
|-------------|----------------|
| `eb38f9b`   | Текущее состояние: GetCommitTitle параллелен, extractError обрезает и схлопывает сообщение об ошибке |
| `7a4f0c8`   | Fix collector: перенос GetCommitTitle из buildSummary в processCandidate |
| `e10deb8`   | Fix json report: extractError начал схлопывать многострочный текст + усечение до 300 символов |
