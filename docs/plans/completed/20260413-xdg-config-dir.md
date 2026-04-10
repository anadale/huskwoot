# Поддержка XDG для конфигурационной директории

## Overview

Заменить указание пути к файлу конфигурации (`--config config.toml`) на указание конфигурационной директории (`--config-dir`), с поиском по XDG Base Directory через пакет `adrg/xdg`. Поле `state.dir` становится необязательным — по умолчанию state хранится в подпапке `state/` рядом с конфигом.

Breaking change: флаг `--config` и переменная `$HUSKWOOT_CONFIG` удаляются без обратной совместимости.

## Context

- `cmd/huskwoot/main.go:44-49` — текущая логика `--config` и `$HUSKWOOT_CONFIG`
- `cmd/huskwoot/main.go:55` — вызов `config.Load(configPath)`
- `cmd/huskwoot/main.go:124` — `state.NewFileStateStore(cfg.State.Dir)`
- `internal/config/config.go:155` — `func Load(path string)`
- `internal/config/config.go:269` — валидация `state.dir` как обязательного
- `internal/config/config_test.go` — хелпер `writeConfig` уже создаёт temp dir с `config.toml`
- `Dockerfile:22` — `CMD ["--config", "/app/config/config.toml"]`

## Development Approach

- **testing approach**: TDD (тесты сначала, как указано в CLAUDE.md)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- **unit tests**: table-driven тесты в `config_test.go` для новой сигнатуры `Load(dir)` и optional `state.dir`
- Ручные моки для `adrg/xdg` не нужны — XDG-логика живёт в `main.go`, а `config.Load` принимает уже готовый путь к директории

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with + prefix
- document issues/blockers with ! prefix
- update plan if implementation deviates from original scope

## Solution Overview

Цепочка приоритетов поиска config dir (в `main.go`):
1. `--config-dir /path/to/dir` (флаг CLI)
2. `$HUSKWOOT_CONFIG_DIR` (переменная окружения)
3. `xdg.ConfigHome + "/huskwoot"` (через пакет `github.com/adrg/xdg`)

Внутри найденной директории:
- `config.toml` — конфигурация
- `state/` — подпапка для файлов состояния (default, если `state.dir` не задан в TOML)

`config.Load(dir string)` сама склеивает `filepath.Join(dir, "config.toml")` и дополнительно возвращает резолвленную config dir, чтобы `main.go` мог вычислить default state dir.

## Technical Details

- `config.Load` меняет сигнатуру: `Load(dir string) (*Config, error)` — внутри `filepath.Join(dir, "config.toml")`
- Валидация `state.dir`: пустое значение допустимо (заполняется вызывающим кодом в `main.go`)
- `main.go` после `Load`: если `cfg.State.Dir == ""`, подставляет `filepath.Join(configDir, "state")`
- Зависимость: `github.com/adrg/xdg`

## Implementation Steps

### Task 1: Изменить `config.Load` — принимать директорию вместо пути к файлу

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] обновить хелпер `writeConfig` в тестах: возвращать `dir` вместо `path` к файлу
- [x] написать тест `TestLoad_FromDirectory` — вызов `Load(dir)` находит `config.toml` внутри dir
- [x] написать тест `TestLoad_MissingDirectory` — ошибка при несуществующей директории
- [x] изменить сигнатуру `Load(path string)` на `Load(dir string)` — внутри `filepath.Join(dir, "config.toml")`
- [x] обновить все существующие вызовы `Load` в тестах (передавать `dir` вместо `path`)
- [x] run tests: `go test ./internal/config/...` — must pass before next task

### Task 2: Сделать `state.dir` необязательным

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] написать тест `TestLoad_StateDirOptional` — конфиг без `[state] dir` загружается без ошибки, `State.Dir` == `""`
- [x] написать тест `TestLoad_StateDirExplicit` — конфиг с `state.dir = "/custom"` сохраняет значение
- [x] убрать проверку `state.dir` из `validate()` (строка 269)
- [x] обновить `validBase()` — убрать `state.dir` из базового конфига (проверить, что все тесты, использующие `validBase`, не ломаются)
- [x] run tests: `go test ./internal/config/...` — must pass before next task

### Task 3: Добавить зависимость `adrg/xdg` и обновить `main.go`

**Files:**
- Modify: `go.mod`
- Modify: `cmd/huskwoot/main.go`

- [x] `go get github.com/adrg/xdg`
- [x] заменить флаг `--config` на `--config-dir` c default из `$HUSKWOOT_CONFIG_DIR`, fallback на `filepath.Join(xdg.ConfigHome, "huskwoot")`
- [x] обновить вызов `config.Load(configDir)` вместо `config.Load(configPath)`
- [x] добавить логику: если `cfg.State.Dir == ""`, подставить `filepath.Join(configDir, "state")`
- [x] run tests: `go test ./...` — must pass before next task
- [x] `go vet ./...`

### Task 4: Обновить Dockerfile

**Files:**
- Modify: `Dockerfile`

- [x] заменить `CMD ["--config", "/app/config/config.toml"]` на `CMD ["--config-dir", "/app/config"]`
- [x] убрать `VOLUME ["/app/state", ...]` — state теперь внутри `/app/config/state/` (или оставить один `/app/config`)

### Task 5: Проверка приёмочных критериев

- [x] `go test ./...` — все тесты проходят
- [x] `go vet ./...` — нет замечаний
- [x] `go build -o bin/huskwoot ./cmd/huskwoot/` — бинарник собирается
- [x] verify: `bin/huskwoot --help` показывает `--config-dir` (и не показывает `--config`)
- [x] move this plan to `docs/plans/completed/`

### Task 6: Обновить документацию

- [x] обновить CLAUDE.md если нужно (упоминания `--config`, `$HUSKWOOT_CONFIG`)

## Post-Completion

**Manual verification:**
- проверить запуск с `--config-dir /path` указывающим на реальный конфиг
- проверить запуск без флага — использует `~/.config/huskwoot/`
- проверить что state-файлы создаются в `<config-dir>/state/`
- проверить Docker-образ: `docker build . && docker run ...`
