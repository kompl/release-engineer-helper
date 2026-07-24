## 1. Конфиг

- [x] 1.1 Добавить в `config.InputConfig` поле `ProjectsFile string \`yaml:"projects_file"\`` и дефолт `"projects.json"` в `config.Load`
- [x] 1.2 Заменить `Config.SkipParse bool` на `Config.DeprecatedSkipParse *bool \`yaml:"skip_parse"\`` (значение используется только для предупреждения о миграции)
- [x] 1.3 Проверить тестом, что конфиг без ключа `projects_file` загружается с дефолтом, а конфиг с явным путём — с указанным значением
- [x] 1.4 Проверить тестом, что конфиг с `skip_parse: false` загружается успешно и заполняет `DeprecatedSkipParse`

## 2. Загрузка справочника проектов

- [x] 2.1 Выделить в `parse/logparser.go` общий приватный декодер `map[string][]string` из JSON-файла
- [x] 2.2 Перевести `LoadRepoBranches` на общий декодер, сохранив текст ошибки со ссылкой на `repo_branches` файл
- [x] 2.3 Добавить `parse.LoadProjects(path)` поверх общего декодера с собственным текстом ошибки со ссылкой на projects-файл
- [x] 2.4 Добавить тесты `LoadProjects`: валидный файл, отсутствующий файл, невалидный JSON

## 3. Флаги CLI

- [x] 3.1 Создать `cmd/bambai/scope.go` с типом-накопителем `stringList` (реализует `flag.Value`)
- [x] 3.2 Зарегистрировать в `main` флаги `-p`/`--project` и `-b`/`--branch` через `flag.Var` на два накопителя, а также булевы `--by-log` и `--by-json`
- [x] 3.3 Задать `flag.Usage` с описанием трёх режимов запуска и примерами (`--by-log`, `--by-json`, `-p hydra-server -b master -b v6.3`)

## 4. Валидация и разрешение области

- [x] 4.1 Реализовать `scopeMode` (`modeByJSON` | `modeByLog` | `modeProject`) и функцию валидации флагов в `cmd/bambai/scope.go`
- [x] 4.2 Реализовать правила: `--by-log` + `--by-json` → ошибка; `-p` + любой `--by-*` → ошибка; больше одного `-p` → ошибка; `-b` без `-p` → ошибка; пусто → `modeByJSON`
- [x] 4.3 Реализовать чистую функцию `resolveProjectScope(projects map[string][]string, project string, branches []string) (map[string][]string, error)`: неизвестный проект → ошибка со списком доступных ключей; пустой `branches` → ветки из справочника; непустой — как есть, без сверки со справочником
- [x] 4.4 Покрыть валидацию флагов табличными тестами по всем комбинациям из 4.2
- [x] 4.5 Покрыть `resolveProjectScope` табличными тестами: все ветки из справочника, одна ветка, несколько веток, ветка вне справочника, неизвестный проект

## 5. Оркестрация в main

- [x] 5.1 Вызвать разбор и валидацию флагов до загрузки конфига и подключения к MongoDB; при ошибке — usage и ненулевой код выхода
- [x] 5.2 Печатать предупреждение о миграции, когда `cfg.DeprecatedSkipParse != nil`: ключ устарел, точку входа задают `--by-log` / `--by-json`
- [x] 5.3 Заменить условие `if !cfg.SkipParse` ветвлением по `scopeMode`: `modeByLog` — фаза Parse с сохранением в `input.repo_branches_file`; `modeByJSON` — `parse.LoadRepoBranches`; `modeProject` — `parse.LoadProjects` + `resolveProjectScope`
- [x] 5.4 Убедиться, что в режиме `modeProject` файл `input.repo_branches_file` не читается и не пишется, а `input.projects_file` читается только в этом режиме
- [x] 5.5 Печатать перед фазой Collect строку с выбранным режимом и итоговой областью (репозиторий и список веток)

## 6. Конфиги и документация

- [x] 6.1 Удалить `skip_parse` и добавить `input.projects_file: "projects.json"` в `config.sample.yaml` и `config.yaml`
- [x] 6.2 Обновить `README.md`: три режима запуска с примерами вместо описания ключа `skip_parse`, описание `projects.json` и `input.projects_file`
- [x] 6.3 Обновить `ARCHITECTURE.md`: опциональность фазы Parse описывается через режимы запуска, а не через `skip_parse`
- [x] 6.4 Обновить `CLAUDE.md`: в разделе «Команды» добавить примеры точечного запуска, в «Конфигурации» — `projects.json`

## 7. Проверка

- [x] 7.1 `make lint` (go vet + go-arch-lint) проходит без нарушений
- [x] 7.2 `make test` проходит целиком
- [x] 7.3 Ручной прогон `./bambai -p hydra-server -b master`: отчёт построен по одной паре, `repo_branches.json` не изменился
- [x] 7.4 Ручной прогон `./bambai -p hydra-server`: собраны все ветки проекта из `projects.json`
- [x] 7.5 Ручной прогон `./bambai --by-json` и `./bambai` без флагов дают одинаковую область анализа
- [x] 7.6 Ручная проверка ошибок: `-p unknown`, `-p a -p b`, `-b master` без `-p`, `--by-log --by-json` — каждая падает с понятным сообщением и usage
