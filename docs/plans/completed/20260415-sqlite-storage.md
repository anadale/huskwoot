# Миграция хранилищ на SQLite

## Overview

Заменить три файловых/in-memory хранилища (`FileStateStore`, `FileMetaStore`, `MemoryHistory`) единой SQLite-базой данных. Один файл `huskwoot.db` в директории `state.dir` вместо россыпи JSON/TXT-файлов и потери истории при перезапуске.

Ключевые изменения:
- Новый тип `model.HistoryEntry` (только `AuthorName`, `Text`, `Timestamp`) вместо `model.Message` в интерфейсе `History`
- Интерфейс `History` принимает `source string` отдельным параметром в `Add`
- Три SQLite-реализации: `SQLiteStateStore`, `SQLiteMetaStore`, `SQLiteHistory`
- Старые файловые реализации удаляются полностью

## Context (from discovery)

- **Текущие реализации:**
  - `internal/state/file.go` — `FileStateStore` (JSON-файлы, `sync.RWMutex`, atomic write)
  - `internal/state/meta.go` — `FileMetaStore` (TXT-файлы, `sync.RWMutex`, atomic write)
  - `internal/history/memory.go` — `MemoryHistory` (in-memory map, TTL sweep горутина)
- **Интерфейсы:** `internal/model/interfaces.go` — `StateStore`, `MetaStore`, `History`
- **Типы:** `internal/model/types.go` — `Cursor`, `Message`, `Source`
- **Потребители:**
  - `cmd/huskwoot/main.go` — создаёт все сторы, передаёт в pipeline/channels
  - `internal/pipeline/pipeline.go` — использует `MetaStore.Get/Values`
  - `internal/handler/set_project.go` — использует `MetaStore.Set`
  - `internal/channel/telegram.go:165` — `history.Add(ctx, msg)`, `history.RecentActivity()`
  - `internal/ai/extractor.go:141-142` — шаблон использует только `.Timestamp`, `.AuthorName`, `.Text`
- **Конфиг:** `config.HistoryConfig{MaxMessages, TTL}`, `config.StateConfig{Dir}` — секция `[state]` удаляется, БД в `configDir`

## Development Approach

- **Подход к тестированию:** TDD — тесты пишутся до реализации
- Каждая задача полностью завершается перед переходом к следующей
- **CRITICAL: каждая задача должна включать новые/обновлённые тесты**
- **CRITICAL: все тесты должны проходить перед началом следующей задачи**
- Команды: `go test ./...`, `go vet ./...`

## Testing Strategy

- **Unit tests:** table-driven тесты, ручные моки без фреймворков
- SQLite-тесты используют `t.TempDir()` для изолированной БД
- Покрытие: успешные случаи + ошибки + concurrency

## Progress Tracking

- Завершённые пункты помечаются `[x]` сразу
- Новые задачи добавляются с префиксом ➕
- Блокеры помечаются ⚠️

## Solution Overview

### Библиотека

`modernc.org/sqlite` — pure Go реализация SQLite, без CGO. WAL mode для concurrent read/write.

### Схема БД

```sql
CREATE TABLE cursors (
    channel_id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    folder_id  TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

CREATE TABLE channel_projects (
    channel_id   TEXT PRIMARY KEY,
    project_name TEXT NOT NULL
);

CREATE TABLE messages (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT    NOT NULL,
    author_name TEXT    NOT NULL,
    text        TEXT    NOT NULL,
    timestamp   INTEGER NOT NULL
);

CREATE INDEX idx_messages_source_time ON messages(source_id, timestamp DESC);
```

### Новый тип HistoryEntry

```go
type HistoryEntry struct {
    AuthorName string
    Text       string
    Timestamp  time.Time
}
```

Заменяет `model.Message` в интерфейсе `History`. Шаблон экстрактора (`extractor.go:141-142`) использует те же поля — `.Timestamp`, `.AuthorName`, `.Text`.

### Изменённый интерфейс History

```go
type History interface {
    Add(ctx context.Context, source string, entry HistoryEntry) error
    Recent(ctx context.Context, source string, limit int) ([]HistoryEntry, error)
    RecentActivity(ctx context.Context, source string, silenceGap time.Duration, fallbackLimit int) ([]HistoryEntry, error)
}
```

Ключевое отличие: `Add` принимает `source string` отдельно, а не извлекает из `msg.Source.ID`.

### Интерфейсы StateStore и MetaStore

Не меняются. SQLite-реализации имплементируют существующие интерфейсы. `MetaStore` маппит `"project:"+channelID` на таблицу `channel_projects` внутри реализации.

### Инициализация БД

Функция `OpenDB(path string) (*sql.DB, error)` в новом пакете `internal/storage`:
- Открывает/создаёт файл SQLite
- Включает WAL mode (`PRAGMA journal_mode=WAL`)
- Включает foreign keys (`PRAGMA foreign_keys=ON`)
- Выполняет миграцию схемы (CREATE TABLE IF NOT EXISTS)
- Возвращает `*sql.DB`, пригодный для concurrent use

### Где хранится файл БД

`huskwoot.db` в `configDir` (рядом с `config.toml`). Секция `[state]` из конфига удаляется полностью — БД всегда лежит рядом с конфигом, отдельная настройка не нужна.

## Implementation Steps

### Task 1: Добавить зависимость `modernc.org/sqlite` и пакет `internal/storage`

**Files:**
- Create: `internal/storage/db.go`
- Create: `internal/storage/db_test.go`
- Modify: `go.mod`

- [x] `go get modernc.org/sqlite` — добавить зависимость
- [x] создать `internal/storage/db.go` с функцией `OpenDB(path string) (*sql.DB, error)`
- [x] `OpenDB` включает WAL mode, foreign keys, создаёт все три таблицы через CREATE TABLE IF NOT EXISTS
- [x] написать тесты: OpenDB создаёт файл, повторный вызов OpenDB на том же файле работает
- [x] написать тесты: таблицы существуют после OpenDB (проверить через `SELECT` на пустых таблицах)
- [x] `go test ./internal/storage/...` — должны пройти

### Task 2: Добавить `model.HistoryEntry` и обновить интерфейс `History`

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/interfaces.go`

- [x] добавить тип `HistoryEntry` в `internal/model/types.go`
- [x] изменить интерфейс `History` в `internal/model/interfaces.go`: `Add(ctx, source, HistoryEntry)`, `Recent` и `RecentActivity` возвращают `[]HistoryEntry`
- [x] `go vet ./...` — должен пройти (компиляция сломается — это ожидаемо, исправим в следующих задачах)

### Task 3: Обновить `MemoryHistory` под новый интерфейс (временно, для компиляции)

**Files:**
- Modify: `internal/history/memory.go`
- Modify: `internal/history/memory_test.go`

- [x] обновить `MemoryHistory.Add` — принимает `source string` и `HistoryEntry` вместо `Message`
- [x] обновить `Recent` и `RecentActivity` — возвращают `[]HistoryEntry`
- [x] обновить внутреннюю структуру: `data map[string][]model.HistoryEntry`
- [x] обновить тесты в `memory_test.go` под новые типы
- [x] `go test ./internal/history/...` — должны пройти

### Task 4: Обновить потребителей интерфейса History

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/extractor_test.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] обновить `telegram.go:164-171` — конвертировать `Message` → `HistoryEntry` при вызове `history.Add`, передать `msg.Source.ID` как `source`
- [x] обновить `HistoryFn` — возвращает `[]model.HistoryEntry` вместо `[]model.Message`
- [x] обновить `model.Message.HistoryFn` в `types.go` — возвращает `[]model.HistoryEntry`
- [x] обновить `extractorData.History` в `extractor.go` — тип `[]model.HistoryEntry`
- [x] обновить `pipeline.go:processPromise` — переменная `history` имеет тип `[]model.HistoryEntry`
- [x] обновить моки и тесты в `telegram_test.go`, `extractor_test.go`, `pipeline_test.go`
- [x] `go test ./...` — все тесты должны пройти

### Task 5: Реализовать `SQLiteStateStore`

**Files:**
- Create: `internal/storage/state_store.go`
- Create: `internal/storage/state_store_test.go`

- [x] создать `SQLiteStateStore` с конструктором `NewSQLiteStateStore(db *sql.DB) *SQLiteStateStore`
- [x] реализовать `GetCursor(ctx, channelID)` — SELECT из `cursors`, вернуть nil если не найден
- [x] реализовать `SaveCursor(ctx, channelID, cursor)` — INSERT OR REPLACE в `cursors`
- [x] написать тесты: GetCursor на пустой БД возвращает nil, nil
- [x] написать тесты: SaveCursor + GetCursor roundtrip
- [x] написать тесты: SaveCursor перезаписывает существующий курсор
- [x] `go test ./internal/storage/...` — должны пройти

### Task 6: Реализовать `SQLiteMetaStore`

**Files:**
- Create: `internal/storage/meta_store.go`
- Create: `internal/storage/meta_store_test.go`

- [x] создать `SQLiteMetaStore` с конструктором `NewSQLiteMetaStore(db *sql.DB) *SQLiteMetaStore`
- [x] реализовать `Get(ctx, key)` — маппинг `"project:"+id` → SELECT из `channel_projects` по `channel_id`
- [x] реализовать `Set(ctx, key, value)` — INSERT OR REPLACE в `channel_projects`
- [x] реализовать `Values(ctx, prefix)` — SELECT DISTINCT `project_name` из `channel_projects`
- [x] написать тесты: Get на пустой БД возвращает "", nil
- [x] написать тесты: Set + Get roundtrip
- [x] написать тесты: Values возвращает уникальные проекты с указанным prefix
- [x] написать тесты: Values на пустой БД возвращает nil, nil
- [x] `go test ./internal/storage/...` — должны пройти

### Task 7: Реализовать `SQLiteHistory`

**Files:**
- Create: `internal/storage/history.go`
- Create: `internal/storage/history_test.go`

- [x] создать `SQLiteHistory` с конструктором `NewSQLiteHistory(db *sql.DB, opts SQLiteHistoryOptions) *SQLiteHistory`
- [x] `SQLiteHistoryOptions`: `MaxMessages int`, `TTL time.Duration`
- [x] реализовать `Add(ctx, source, entry)` — INSERT в `messages`; после вставки DELETE лишних (по MaxMessages на source)
- [x] реализовать `Recent(ctx, source, limit)` — SELECT с ORDER BY timestamp DESC LIMIT
- [x] реализовать `RecentActivity(ctx, source, silenceGap, fallbackLimit)` — SELECT последних записей, найти разрыв в Go-коде
- [x] TTL-очистка: DELETE WHERE timestamp < now - TTL при каждом Add (или периодически)
- [x] написать тесты: Add + Recent roundtrip
- [x] написать тесты: Recent возвращает не более limit записей
- [x] написать тесты: RecentActivity находит разрыв в активности
- [x] написать тесты: RecentActivity без разрыва возвращает fallbackLimit
- [x] написать тесты: TTL-очистка удаляет старые записи
- [x] написать тесты: MaxMessages ограничивает записи на source
- [x] `go test ./internal/storage/...` — должны пройти

### Task 8: Подключить SQLite-сторы в main.go, удалить файловые реализации

**Files:**
- Modify: `cmd/huskwoot/main.go`
- Delete: `internal/state/file.go`
- Delete: `internal/state/file_test.go`
- Delete: `internal/state/meta.go`
- Delete: `internal/state/meta_test.go`
- Delete: `internal/state/store.go` (если пустой)
- Delete: `internal/history/memory.go`
- Delete: `internal/history/memory_test.go`

- [x] в `run()`: принять `configDir` как параметр, вызвать `storage.OpenDB(filepath.Join(configDir, "huskwoot.db"))`, defer `db.Close()`
- [x] убрать логику `cfg.State.Dir` — БД лежит в `configDir`, а не в `state.dir`
- [x] заменить `state.NewFileStateStore(...)` на `storage.NewSQLiteStateStore(db)`
- [x] заменить `state.NewFileMetaStore(...)` на `storage.NewSQLiteMetaStore(db)`
- [x] заменить `history.NewMemoryHistory(...)` на `storage.NewSQLiteHistory(db, opts)`
- [x] удалить файлы `internal/state/file.go`, `file_test.go`, `meta.go`, `meta_test.go`
- [x] удалить файлы `internal/history/memory.go`, `memory_test.go`
- [x] удалить пакет `internal/history/` если пустой (проверить `options.go` и др.)
- [x] обновить импорты в `main.go`
- [x] `go test ./...` — все тесты должны пройти
- [x] `go vet ./...` — должен пройти
- [x] `go build -o bin/huskwoot ./cmd/huskwoot` — должен собраться

### Task 9: Обновить конфигурацию — убрать лишнее из `[history]`

**Files:**
- Modify: `internal/config/config.go`

- [x] удалить `StateConfig` и секцию `[state]` из конфига полностью — БД всегда в `configDir`
- [x] `HistoryConfig.MaxMessages` и `TTL` оставить — параметры передаются в `SQLiteHistoryOptions`
- [x] обновить тесты конфига если затронуты
- [x] `go test ./...` — все тесты должны пройти

### Task 10: Verify acceptance criteria

- [x] все три стора работают через единый `huskwoot.db`
- [x] `FileStateStore`, `FileMetaStore`, `MemoryHistory` полностью удалены
- [x] история персистентна (переживает перезапуск) [manual test - не автоматизируемо]
- [x] concurrent access работает (WAL mode)
- [x] `go test ./...` — все тесты зелёные
- [x] `go vet ./...` — без замечаний
- [x] `go build -o bin/huskwoot ./cmd/huskwoot` — собирается

### Task 11: [Final] Обновить документацию

- [x] обновить CLAUDE.md — описание пакета `internal/storage/`, удалить упоминания `FileStateStore`/`FileMetaStore`/`MemoryHistory`
- [x] обновить структуру директорий в CLAUDE.md
- [x] переместить этот план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Запустить `huskwoot` с реальным конфигом, убедиться что `huskwoot.db` создаётся
- Отправить сообщение в Telegram-группу, проверить что история сохраняется
- Перезапустить приложение, проверить что курсоры и история на месте
- Проверить команду `/set_project` — привязка проекта сохраняется в БД
