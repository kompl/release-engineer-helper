# release-engineer-helper v0.1

Анализатор падений тестов в GitHub Actions CI. Собирает данные о запусках workflow, классифицирует поведение тестов (стабильно падающие, flaky, починенные) и генерирует HTML/JSON-отчёты.

Go-реализация с чистой фазовой архитектурой, параллельным сбором данных и конкурентной генерацией отчётов.

## Архитектура

Пайплайн состоит из 5 именованных фаз:

```
Parse   → Collect → Analyze → Enrich → Render
```

| Фаза | Описание |
|------|----------|
| **Parse** | Парсинг лог-файла Ruby hash формата в список репозиториев и веток |
| **Collect** | Сбор данных из GitHub API: скачивание логов/артефактов, парсинг результатов тестов, кэширование в MongoDB. Запуски обрабатываются параллельно (до `max_runs` горутин одновременно) |
| **Analyze** | Классификация поведения тестов по матрице состояний: `stable_failing`, `fixed`, `flaky`, `single_failure`. Вычисление diff между запусками |
| **Enrich** | Обогащение данных: поиск момента начала стабильного падения (`stable_since`) через историю в MongoDB |
| **Render** | Генерация отчётов: HTML (по одному на репозиторий/ветку) и JSON (один общий). HTML и JSON строятся параллельно |

Каждая фаза имеет явный контракт данных (входные/выходные структуры). Фазы выполняются последовательно, но внутри Collect и Render используется параллелизм.

## Структура проекта

```
v2/
├── main.go                        # Точка входа, оркестрация фаз
├── config.yaml                    # Конфигурация
├── config/
│   └── config.go                  # Загрузка YAML-конфигурации
├── parse/
│   └── logparser.go               # Парсинг Ruby hash лога (вывод release-helper -l) → map[repo][]branch
├── collect/
│   ├── collector.go               # Оркестрация сбора данных, worker pool
│   ├── github.go                  # Клиент GitHub API
│   ├── cache.go                   # MongoDB-кэш результатов парсинга
│   ├── extractor_logs.go          # Извлечение результатов из zip-логов
│   ├── extractor_artifacts.go     # Извлечение результатов из JUnit XML артефактов
│   └── models.go                  # CollectResult, RunMeta, вспомогательные структуры
├── analyze/
│   ├── analyzer.go                # Классификация поведения тестов, diff, статистика
│   └── models.go                  # AnalyzeResult, BehaviorAnalysis, RunDiff
├── enrich/
│   ├── enricher.go                # Поиск stable_since в MongoDB
│   └── models.go                  # EnrichResult
├── render/
│   ├── renderer.go                # Параллельная оркестрация HTML + JSON
│   ├── html.go                    # Построение данных для HTML-шаблона
│   ├── json.go                    # Генерация JSON-отчёта
│   └── report.html.tmpl           # Go html/template шаблон отчёта
└── internal/
    └── models.go                  # Общие типы: StringSet, TestDetail, RunMeta
```

## Требования

- Go 1.24+
- MongoDB 6.0+
- GitHub Personal Access Token (classic) с доступом к scope `actions` (чтение workflow runs, скачивание логов и артефактов). Для приватных репозиториев также необходим scope `repo`

## Установка и запуск

### 1. MongoDB

Запуск через docker-compose (из корня проекта):

```bash
docker compose up -d
```

MongoDB будет доступна на `localhost:27017` (логин: `root`, пароль: `example`).

### 2. Сборка

```bash
cd v2
go build -o release-engineer-helper .
```

### 3. Конфигурация

Отредактировать `config.yaml` под нужные параметры (см. раздел ниже).

### 4. Запуск

```bash
export GITHUB_TOKEN="ghp_..."
./release-engineer-helper -config config.yaml
```

Токен читается **только** из переменной окружения `GITHUB_TOKEN`. В конфигурационном файле токен не хранится.

Токен используется для:
- Получения списка workflow runs (`GET /repos/{owner}/{repo}/actions/runs`)
- Скачивания логов (`GET /repos/{owner}/{repo}/actions/runs/{id}/logs`)
- Скачивания артефактов (`GET /repos/{owner}/{repo}/actions/artifacts`)

Создать токен: GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic) → Generate new token. Выбрать scopes: `actions` (обязательно), `repo` (для приватных репозиториев).

## Конфигурация

```yaml
github:
  owner: "hydra-billing"        # Организация на GitHub
  workflow_file: "ci.yml"       # Имя файла workflow

mongo:
  uri: "mongodb://root:example@localhost:27017"
  db: "rel_cache"        # База данных
  collection: "parsed_results"  # Коллекция для кэша

analysis:
  master_branch: "master"       # Эталонная ветка
  max_runs: 100                 # Максимум запусков для анализа

output:
  dir: "downloaded_logs"        # Директория для отчётов
  save_logs: false              # Сохранять скачанные логи на диск
  force_refresh_cache: false    # Игнорировать кэш, перезагружать данные
  generate_html: true           # Генерировать HTML-отчёты (по одному на repo/branch)
  generate_json: true           # Генерировать JSON-отчёт (один общий)

skip_parse: false                 # Пропустить фазу Parse (если repo_branches.json уже есть)

input:
  log_file: "1.log"                      # Входной лог-файл для фазы Parse (лог release-helper)
  repo_branches_file: "repo_branches.json" # Файл с репозиториями/ветками
  ignore_tasks:                            # Задачи-исключения (не парсить)
    - "ADM-3191"
    - "INT-570"
```

Фаза `Parse` — единственная опциональная. Если `repo_branches.json` уже существует, её можно пропустить через `skip_parse: true`. Остальные фазы (Collect → Analyze → Enrich → Render) образуют неразрывную цепочку и выполняются всегда.

## Фазы

### Parse

**Вход:** файл `1.log` (лог из CI в формате Ruby hash)
```log
{"hydra-core":
  {"6.3.0.3" =>
    {tasks: ["AIS-8967"],
     commits:
      ["cd2d254c089f65c937aab43bff0b1637dabcf7e0",
       "132ebcf2401599d3bb944a6f9a2d4c3785a5c4b4"],
     translations: []}}}
```
**Выход:** `repo_branches.json` (JSON: `{repo: [branch, ...]}`
```json
{
   "hydra-core": [
      "v6.3"
   ]
}
```
Парсит лог-файл регулярными выражениями, извлекает секции проектов и ключи версий. Преобразует версии в имена веток (`versionToBranch`). Фильтрует задачи из `ignore_tasks`.

### Collect

**Вход:** список репозиториев/веток + конфигурация
**Выход:** `CollectResult` (метаданные запусков, упавшие тесты, детали ошибок)

Работа фазы:
1. Запрос списка завершённых запусков workflow через GitHub API
2. Параллельная обработка каждого запуска:
   - Проверка кэша в MongoDB
   - Извлечение результатов из артефактов (JUnit XML)
   - Fallback: извлечение из zip-логов
   - Сохранение в MongoDB
3. Построение сводки: упавшие тесты по запускам, метаданные, общий пул деталей
4. Отдельно загружается последний запуск на master для сравнения

### Analyze

**Вход:** `CollectResult`
**Выход:** `AnalyzeResult` (классификация, diff между запусками, статистика)

Чистая вычислительная фаза без I/O. Строит матрицу состояний (pass/fail) для каждого теста по всем запускам и классифицирует поведение:

- **stable_failing** — падает стабильно от определённого запуска до последнего
- **fixed** — падал, но в последнем запуске прошёл
- **flaky** — нестабильный (больше 2 переходов pass/fail)
- **single_failure** — единичное падение

Также вычисляет diff между последовательными запусками (новые падения, починенные, уникальные).

### Enrich

**Вход:** `CollectResult` + `AnalyzeResult`
**Выход:** `EnrichResult` (дата начала стабильного падения для каждого теста)

Для каждого `stable_failing` теста ищет в MongoDB самый ранний запуск, в котором этот тест присутствует. Использует список ID запусков ветки из фазы Collect (без дополнительных вызовов GitHub API).

### Render

**Вход:** результаты всех фаз для каждого репозитория/ветки
**Выход:** HTML-файлы + JSON-файл

HTML-отчёты генерируются автономно для каждой пары репозиторий/ветка. JSON-отчёт объединяет данные по всем проектам. HTML и JSON строятся параллельно через `sync.WaitGroup`.

HTML-отчёт содержит:
- Секции поведения (стабильно падающие, починенные, flaky)
- Diff по каждому запуску (новые падения, починенные, уникальные, все)
- Древовидная группировка тестов по `::` разделителю
- Детали ошибок с возможностью раскрытия

JSON-отчёт (`report_YYYYMMDD_HHMMSS.json`) — структура:

```json
{
  "generated_at": "2026-03-05T12:00:00+03:00",
  "projects": {
    "repo/branch": {
      "repo": "hydra-core",
      "branch": "HCD-1234",
      "master_branch": "master",
      "latest_run": {
        "run_id": 12345678,
        "sha": "abc1234",
        "commit_title": "Описание коммита",
        "timestamp": "2026-03-05T10:00:00Z",
        "conclusion": "failure",
        "link": "https://github.com/.../actions/runs/12345678",
        "total_failed": 5
      },
      "summary": {
        "total_runs_analyzed": 10,
        "unique_failed_tests": 15,
        "master_failed_tests": 3,
        "new_failures": 2,
        "stable_failing_count": 8,
        "fixed_count": 1,
        "flaky_count": 4
      },
      "failed_tests": [
        {
          "test_name": "SomeClass::test_name | error",
          "error_message": "Expected X got Y",
          "classification": "stable_failing",
          "in_master": false,
          "project": "hydra-core-tests",
          "fail_rate_pct": 80.0,
          "pattern": "🟢🔴🔴🔴🔴",
          "probable_cause": {
            "sha": "def5678",
            "commit_title": "Коммит, сломавший тест",
            "timestamp": "2026-03-01T10:00:00Z",
            "run_link": "https://github.com/.../actions/runs/...",
            "streak_length": 4
          },
          "failing_since": { "run_id": 11111111, "date": "2026-02-28T..." },
          "first_seen_in_analysis": { "timestamp": "...", "commit": "...", "run_link": "..." },
          "flaky_info": { "fail_count": 3, "total_runs": 10 }
        }
      ]
    }
  }
}
```

| Поле | Описание |
|------|----------|
| `classification` | `stable_failing` / `fixed` / `flaky` / `single_failure` |
| `in_master` | Тест также падает в master |
| `fail_rate_pct` | Процент запусков с падением |
| `pattern` | Визуальный паттерн pass/fail по запускам |
| `probable_cause` | Коммит, начавший текущую серию падений |
| `failing_since` | Самый ранний запуск с этим падением (из MongoDB, за пределами анализируемого окна) |
| `first_seen_in_analysis` | Первый запуск с падением в рамках текущего анализа |
| `flaky_info` | Количество падений / общее число запусков (только для flaky) |

## MongoDB

БД: `rel_cache`, коллекция: `parsed_results`.

Схема документа совместима с Python-версией (те же имена полей):

```json
{
  "schema": 2,
  "owner": "hydra-billing",
  "repo": "hydra-core",
  "run_id": 12345678,
  "created_at": "2026-03-02T12:00:00Z",
  "has_no_tests": false,
  "details_list": [
    {
      "test_name": "SomeClass::test_name | error message",
      "items": [
        {
          "file": "test-run.txt",
          "line_num": 0,
          "context": "Error details...",
          "project": "hydra-core-tests",
          "order_index": 0
        }
      ]
    }
  ]
}
```

Уникальный индекс: `(owner, repo, run_id)`.
