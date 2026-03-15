# save_logs Config Option

| Поле         | Значение                          |
|--------------|-----------------------------------|
| Branch       | `main`                            |
| Git-HEAD     | `eb38f9b`                         |
| Stack        | Go 1.23 / yaml.v3                 |
| Scope        | `config.OutputConfig`             |
| Updated      | 2026-03-13                        |

## Summary

`save_logs` — булевый флаг в секции `output` конфигурационного файла. Объявлен в Go-структуре и документирован в README, однако **нигде не используется в исполняемом коде**: ни один пакет (`collect`, `enrich`, `render`, `main`) не читает `cfg.Output.SaveLogs`.

## Key files

| Путь | Роль |
|------|------|
| `config/config.go` | Определение структуры `OutputConfig` с полем `SaveLogs bool \`yaml:"save_logs"\`` |
| `config.sample.yaml` | Эталонный конфиг, значение по умолчанию `save_logs: false` |
| `config.yaml` | Рабочий конфиг (в .gitignore нет, значение `save_logs: true`) |
| `README.md` | Комментарий: «Сохранять скачанные логи на диск» |
| `collect/github.go` | `DownloadLogs` — метод, который скачивает zip-логи; мог бы сохранять на диск, но не делает этого |

## Architecture & patterns

- **Поле структуры с yaml-тегом** — стандартный паттерн конфига проекта:
  ```go
  // config/config.go, строка 26-32
  type OutputConfig struct {
      Dir               string `yaml:"dir"`
      SaveLogs          bool   `yaml:"save_logs"`
      ForceRefreshCache bool   `yaml:"force_refresh_cache"`
      GenerateHTML      bool   `yaml:"generate_html"`
      GenerateJSON      bool   `yaml:"generate_json"`
  }
  ```
- По умолчанию значение `false` — поле не инициализируется явно в `config.Load`, то есть нулевое значение Go (`false`) и является дефолтом.
- `cfg.Output.ForceRefreshCache` и `cfg.Output.GenerateHTML/GenerateJSON` — соседние флаги того же блока — **активно используются** в `collect/collector.go` и `render/renderer.go`. `SaveLogs` — исключение.

## Conventions

- Naming: поля `OutputConfig` названы в PascalCase в Go, в snake_case в YAML.
- Конфиг загружается через `config.Load(path)` в `main.go` и передаётся как `*config.Config` во все фазы.
- Флаги конфига не валидируются; неизвестные YAML-ключи игнорируются (`yaml.Unmarshal` без `KnownFields`).

## Constraints & gotchas

- **`save_logs` не имеет эффекта** — флаг объявлен, документирован, присутствует в конфигах, но ни разу не читается в рантайме. Скачанные байты zip-логов (`collect/github.go: DownloadLogs`) передаются напрямую в `LogExtractor.ParseZip` и нигде не записываются на диск, независимо от значения флага.
- Предполагаемое поведение при `save_logs: true` — сохранение zip-архива логов в `cfg.Output.Dir` — не реализовано. Это либо заготовка для будущей функциональности, либо регрессия после рефакторинга.
- `config.yaml` в рабочей копии имеет `save_logs: true`, что никак не влияет на поведение программы.

## Reference commits

| SHA (short) | Что показывает |
|-------------|----------------|
| `0962c56`   | Init — первоначальное появление `save_logs` в структуре конфига |
| `eb38f9b`   | Fix collector — рефакторинг collect-фазы, `save_logs` по-прежнему не задействован |
