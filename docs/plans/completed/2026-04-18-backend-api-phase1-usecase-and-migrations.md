# Backend API — Фаза 1: use-case слой и миграции UUID/slug/number

> **For agentic workers:** REQUIRED SUB-SKILL — `superpowers:subagent-driven-development` (recommended) или `superpowers:executing-plans` для пошаговой реализации. Шаги используют чекбоксы (`- [ ]`) для прогресса.

**Goal:** Заложить фундамент для последующего HTTP API: ввести use-case слой (TaskService/ProjectService/ChatService), мигрировать схему на UUID + per-project number + slug, и перевести существующие call-sites (pipeline, agent tools, DM-handler) на use-case'ы. После этой фазы внешнее поведение не меняется — Telegram-бот и IMAP работают как раньше, но код готов к добавлению HTTP API.

**Architecture:** Чистый use-case слой между Pipeline/Agent (а в будущем — HTTP) и SQLite-хранилищами. Schema-миграции через goose-as-library + `//go:embed`. Идентификаторы — UUID v4 + per-project monotonic `number` + project `slug`. Пользовательская ссылка `<slug>#<number>` (например, `inbox#42`).

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `github.com/pressly/goose/v3` (новая зависимость), `github.com/google/uuid` (уже подключен).

---

## Overview

Фаза 1 — первый из четырёх планов в серии «Backend API + push» по [спецификации](../superpowers/specs/2026-04-18-backend-api-and-push-design.md). Серия:

- **Фаза 1 (этот план):** use-case слой + миграции UUID/slug/number + перевод существующих call-sites.
- **Фаза 2:** Cobra `serve`-подкоманда, HTTP-инфраструктура (chi + auth middleware), `DeviceStore`, `EventStore`, SSE-брокер, REST-эндпоинты для tasks/projects/chat, OpenAPI 3.1, sync/snapshot.
- **Фаза 3:** Pairing flow (Telegram magic-link DM, HTML confirm с CSRF, long-poll `/v1/pair/status`, rate-limit, `PATCH /v1/devices/me` с push-токенами).
- **Фаза 4:** Push relay (отдельный бинарник `huskwoot-push-relay`), `push_queue` + dispatcher с retry-семантикой, HMAC-протокол инстанс ⇄ релей, Caddy + обновлённый docker-compose.

После Фазы 1 запущенный инстанс продолжает работать в legacy-режиме (без HTTP API), но:

- Все существующие операции (создание задачи из IMAP, агентские tools, DM-обработка) идут через use-case'ы — единственное место, где в Фазе 2 будет добавляться запись событий и push-jobs в одну транзакцию.
- ID задач/проектов — UUID. Имена проектов получают `slug` (slug автогенерится через транслит кириллицы). Задачи — per-project `number`. Появляется человеко-читаемая ссылка `inbox#42`.
- Goose ведёт миграции; новые миграции добавляются как `internal/storage/migrations/NNN_*.sql` или `*.go`.
- Появляется новый агентский tool `move_task` (перенос задачи между проектами с переприсвоением `number`).

## Context (from discovery)

**Текущая схема (`internal/storage/db.go`):**
- `projects.id` и `tasks.id` — `INTEGER PRIMARY KEY AUTOINCREMENT` (вопреки тому, что указано в `CLAUDE.md`).
- `channel_projects.project_id` — `INTEGER`.
- Таблицы создаются через `CREATE TABLE IF NOT EXISTS` в `OpenDB`.

**Файлы, которые меняются:**
- `internal/model/{task,project,interfaces}.go` — расширение типов и интерфейсов.
- `internal/storage/db.go` — переход с inline-`CREATE TABLE` на goose.
- `internal/storage/{task_store,cached_task_store,meta_store}.go` — поддержка UUID, slug, number, MoveTask, FindTaskByRef.
- `internal/pipeline/pipeline.go` — переход на TaskService/ProjectService/ChatService.
- `internal/agent/{create_task,create_project,list_tasks,list_projects,complete_task}.go` — переход на use-case'ы.
- `internal/agent/agent.go` — конструктор принимает ProjectService для `ListProjects`-инъекции в промпт.
- `internal/handler/setproject.go` — переход на `ProjectService.EnsureChannelProject`.
- `cmd/huskwoot/main.go` — wiring.

**Файлы, которые создаются:**
- `internal/storage/migrations/migrations.go` — embed + goose-driver-подключение.
- `internal/storage/migrations/001_baseline.sql` — текущая схема.
- `internal/storage/migrations/002_uuid_slug_number.go` — Go-миграция (генерация UUID + бэкфилл slug/number).
- `internal/storage/migrations/003_tasks_updated_at_index.sql` — индекс.
- `internal/usecase/{slug,projects,tasks,chat}.go` + тесты.
- `internal/model/service.go` — интерфейсы и DTO.
- `internal/agent/move_task.go` + тесты.

**Зависимость:** добавляется `github.com/pressly/goose/v3` (объявлена в спеке как единственная новая зависимость для Фазы 1).

**Паттерны проекта (соблюдать):**
- Интерфейсы в `internal/model/`, реализации — в отдельных пакетах.
- Конструкторы возвращают `(*Type, error)` если возможна ошибка инициализации.
- Все публичные методы принимают `context.Context` первым параметром.
- Ошибки оборачиваются: `fmt.Errorf("операция: %w", err)` (на русском).
- Тесты — table-driven, моки вручную (без testify/gomock).
- Параллельные горутины и общее состояние — через `sync.Mutex`/`sync.RWMutex`.
- Логирование — `log/slog`.
- Сообщения в логах и ошибках — на русском.

## Development Approach

- **Testing approach:** TDD. Каждая задача начинается с failing-теста, затем минимальная реализация, затем рефакторинг.
- Маленькие коммиты — по одному на каждый завершённый цикл «тест → код → зелёная полоса».
- Не вводим обратно несовместимых изменений в публичный API существующих пакетов без тестов на новый контракт.
- В каждой задаче конкретный шаг **MUST include новые/обновлённые тесты** для затронутого кода.
- Все тесты должны проходить (`go test ./...` и `go vet ./...`) перед переходом к следующей задаче.
- При отклонении от плана — обновлять этот файл (➕ для новых задач, ⚠️ для блокеров).

## Testing Strategy

- **Unit-тесты:** обязательны на каждый шаг, см. Development Approach.
- **Integration-тесты:** для миграций и для SQLiteTaskStore — на in-memory SQLite (`file::memory:?cache=shared` или `:memory:`). Идиоматично для проекта (см. `db_test.go`).
- **E2E-тесты:** в проекте отсутствуют, дополнительные не добавляем. Smoke-проверка ручная (запуск `huskwoot serve` после Фазы 2 — в Фазе 1 пока запуск через `go run ./cmd/huskwoot`).
- **Race-detector:** `go test -race ./...` обязательно перед задачей 16 (acceptance).

## Progress Tracking

- Чекбоксы помечаются `[x]` сразу после выполнения (не батчем).
- Новые обнаруженные подзадачи — с префиксом ➕.
- Блокеры/проблемы — с префиксом ⚠️.
- Смена scope или подхода — обновлять разделы Overview/Solution Overview/Implementation Steps в этом файле.

## Solution Overview

**1. Goose как библиотека.** Добавляем `github.com/pressly/goose/v3` и подключаем его в `OpenDB`: после открытия БД вызываем `goose.SetBaseFS(embeddedFS)` и `goose.Up(db, "migrations")`. Текущие inline `CREATE TABLE IF NOT EXISTS` переносятся в `001_baseline.sql`. Goose создаёт таблицу `goose_db_version` для tracking.

**2. UUID + slug + number миграция (002).**
   - Создаются новые таблицы `projects_new`, `tasks_new`, `channel_projects_new` с `id TEXT PRIMARY KEY`, `project_id TEXT`, `slug TEXT NOT NULL UNIQUE`, `task_counter INTEGER NOT NULL DEFAULT 0`, `number INTEGER NOT NULL`.
   - Go-код в миграции читает старые INT-id, генерирует UUID v4 для каждой строки, строит маппинг `int → uuid`, генерирует slug для каждого проекта (через `usecase/slug` — но миграция импортирует автономный mini-slugify; transliteration mapping мы дублируем сознательно, чтобы миграция оставалась frozen).
   - Для каждой задачи в проекте присваивается `number = ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY created_at, id)`.
   - `task_counter` проекта = `MAX(number)` его задач.
   - DROP старых таблиц, RENAME новых, создание уникальных индексов.

**3. Use-case слой.** Новый пакет `internal/usecase/`. Реализации `TaskService`, `ProjectService`, `ChatService` — узкие, без обращения к БД напрямую: используют `model.TaskStore`/`model.MetaStore`/`Agent` через интерфейсы. В Фазе 1 они тонкие, но в Фазе 2 туда добавится запись `events`/`push_queue` в той же транзакции.

**4. ProjectService.EnsureChannelProject.** Идемпотентная операция: ищет проект по имени, если нет — создаёт со slug, после `MetaStore.Set("project:"+channelID, projectID)`.

**5. Агентский tool `move_task`.** Принимает `task_id` (UUID или `<slug>#<number>` ref) и `project` (id или name). Через `TaskService.MoveTask` — переприсваивает `number` для целевого проекта (incr `task_counter` в одной транзакции), не откатывая counter исходного.

**6. ChatService — тонкая обёртка.** В Фазе 1: делегирует `Agent.Handle(ctx, msg)`. В Фазе 2 в HTTP `POST /v1/chat` сюда добавится изоляция `Source.AccountID = "client:<device_id>"`.

**7. Перевод call-sites.** После того как use-case'ы готовы, рефакторим `pipeline.go`, агентские tools, `SetProjectHandler` и DM-обработку. На старых интерфейсах ничего не остаётся, кроме `model.TaskStore`/`MetaStore` (которые становятся внутренней зависимостью use-case'ов).

## Technical Details

### Изменения в `model.Project`

```go
type Project struct {
    ID          string    // UUID
    Name        string
    Slug        string    // lowercase-kebab, уникален
    Description string
    TaskCounter int       // монотонный счётчик; не откатывается при move
    CreatedAt   time.Time
}
```

### Изменения в `model.Task`

```go
type Task struct {
    ID            string     // UUID (было int64)
    Number        int        // per-project, уникален в (project_id, number)
    ProjectID     string     // UUID
    ProjectSlug   string     // транзитивное поле, заполняется store'ом через JOIN при SELECT; не хранится в tasks
    Summary       string
    Details       string
    Topic         string
    Status        string     // open|done|cancelled
    Deadline      *time.Time
    ClosedAt      *time.Time
    CreatedAt     time.Time
    UpdatedAt     time.Time
    SourceMessage Message
}

func (t Task) DisplayID() string {
    return fmt.Sprintf("%s#%d", t.ProjectSlug, t.Number)
}
```

### Новые интерфейсы (`internal/model/service.go`)

```go
type CreateTaskRequest struct {
    ProjectID string  // если "" — Inbox
    Summary   string
    Details   string
    Topic     string
    Deadline  *time.Time
    Source    Source
}

type CreateTasksRequest struct {
    ProjectID string
    Tasks     []CreateTaskRequest
}

type TaskUpdate struct {
    Summary  *string
    Details  *string
    Topic    *string
    Deadline *time.Time
    Status   *string
}

type TaskFilter struct {
    ProjectID string  // "" — все проекты
    Status    string  // "" — без фильтра
    Since     *time.Time
}

type ChatReply struct {
    Text             string
    TasksTouched     []string
    ProjectsTouched  []string
}

type TaskService interface {
    CreateTask(ctx context.Context, req CreateTaskRequest) (*Task, error)
    CreateTasks(ctx context.Context, req CreateTasksRequest) ([]Task, error)
    UpdateTask(ctx context.Context, id string, upd TaskUpdate) (*Task, error)
    CompleteTask(ctx context.Context, id string) (*Task, error)
    ReopenTask(ctx context.Context, id string) (*Task, error)
    MoveTask(ctx context.Context, id, newProjectID string) (*Task, error)
    ListTasks(ctx context.Context, filter TaskFilter) ([]Task, error)
    GetTask(ctx context.Context, id string) (*Task, error)
    GetTaskByRef(ctx context.Context, projectSlug string, number int) (*Task, error)
}

type ProjectService interface {
    CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error)
    UpdateProject(ctx context.Context, id string, upd ProjectUpdate) (*Project, error)
    ListProjects(ctx context.Context) ([]Project, error)
    FindProjectByName(ctx context.Context, name string) (*Project, error)
    ResolveProjectForChannel(ctx context.Context, channelID string) (string, error)
    EnsureChannelProject(ctx context.Context, channelID, name string) (*Project, error)
}

type ChatService interface {
    HandleMessage(ctx context.Context, msg Message) (ChatReply, error)
}
```

### Расширенный `model.TaskStore`

```go
type TaskStore interface {
    CreateProject(ctx context.Context, p *Project) error            // requires p.Slug != ""
    GetProject(ctx context.Context, id string) (*Project, error)
    ListProjects(ctx context.Context) ([]Project, error)
    FindProjectByName(ctx context.Context, name string) (*Project, error)
    UpdateProject(ctx context.Context, id string, upd ProjectUpdate) error  // новое
    CreateTask(ctx context.Context, task *Task) error                // присваивает Number в tx
    GetTask(ctx context.Context, id string) (*Task, error)
    GetTaskByRef(ctx context.Context, projectSlug string, number int) (*Task, error)  // новое
    ListTasks(ctx context.Context, projectID string, filter TaskFilter) ([]Task, error)
    UpdateTask(ctx context.Context, id string, update TaskUpdate) error
    MoveTask(ctx context.Context, taskID, newProjectID string) error  // новое
    DefaultProjectID() string
}
```

### Slugify

```go
func Slugify(name string) string
```

- Транслит кириллицы по таблице (а→a, б→b, ... я→ya, ь→"", ъ→"").
- Нижний регистр; не-буквы/цифры → `-`; коллапс множественных `-`; trim `-`.
- Пустой результат → `"project"` (fallback). Тест: `"Проект НА Старт!" → "proekt-na-start"`.

### Поток `CreateTask` (Фаза 1)

```
TaskService.CreateTask(req)
└─ taskStore.CreateTask(ctx, task)
   └─ tx.BEGIN
      ├─ SELECT task_counter FROM projects WHERE id = ? FOR UPDATE
      ├─ UPDATE projects SET task_counter = task_counter + 1 WHERE id = ?
      ├─ task.Number = new_counter
      ├─ INSERT INTO tasks (id, number, project_id, ...) VALUES (uuid, n, ...)
      └─ COMMIT
```

В Фазе 2 use-case добавит `INSERT INTO events ... RETURNING seq` и `INSERT INTO push_queue ...` в ту же транзакцию.

### Поток `MoveTask`

```
TaskService.MoveTask(taskID, newProjectID)
└─ taskStore.MoveTask(ctx, taskID, newProjectID)
   └─ tx.BEGIN
      ├─ SELECT task_counter FROM projects WHERE id = newProjectID
      ├─ UPDATE projects SET task_counter = task_counter + 1 WHERE id = newProjectID
      ├─ UPDATE tasks SET project_id = newProjectID, number = new_counter, updated_at = now WHERE id = taskID
      └─ COMMIT
```

`task_counter` исходного проекта **не** откатывается (поэтому number в исходном проекте остаётся монотонно растущим).

## What Goes Where

- **Implementation Steps (`[ ]`):** код, тесты, миграции, обновление `CLAUDE.md`, перенос плана в `docs/plans/completed/`.
- **Post-Completion (без чекбоксов):** ручная проверка legacy-сценариев, бэкап БД перед миграцией на проде, сообщение пользователю инструкции по бэкапу.

---

## Implementation Steps

### Task 1: Подключить goose как библиотеку, baseline-миграция

**Files:**
- Create: `internal/storage/migrations/migrations.go`
- Create: `internal/storage/migrations/001_baseline.sql`
- Modify: `internal/storage/db.go`
- Modify: `go.mod`, `go.sum`
- Create: `internal/storage/migrations/migrations_test.go`

- [x] **Step 1: Добавить зависимость goose**

```bash
go get github.com/pressly/goose/v3@latest
go mod tidy
```

- [x] **Step 2: Создать `internal/storage/migrations/001_baseline.sql`**

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS cursors (
    id     TEXT PRIMARY KEY,
    cursor TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS channel_projects (
    channel_id TEXT PRIMARY KEY,
    project_id INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT    NOT NULL,
    author_name TEXT    NOT NULL,
    text        TEXT    NOT NULL,
    timestamp   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_source_timestamp
    ON messages(source_id, timestamp);

CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    summary     TEXT NOT NULL,
    details     TEXT NOT NULL DEFAULT '',
    topic       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open',
    deadline    TEXT,
    closed_at   TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    source_kind TEXT NOT NULL DEFAULT '',
    source_id   TEXT NOT NULL DEFAULT ''
);
```

> Скопировать ровно текущую схему из `internal/storage/db.go`. Не добавлять и не удалять колонок — это baseline для существующих БД.

- [x] **Step 3: Создать `internal/storage/migrations/migrations.go`**

```go
package migrations

import (
    "database/sql"
    "embed"
    "fmt"

    "github.com/pressly/goose/v3"
)

//go:embed *.sql *.go
var FS embed.FS

func Up(db *sql.DB) error {
    goose.SetBaseFS(FS)
    if err := goose.SetDialect("sqlite3"); err != nil {
        return fmt.Errorf("установка диалекта goose: %w", err)
    }
    if err := goose.Up(db, "."); err != nil {
        return fmt.Errorf("применение миграций: %w", err)
    }
    return nil
}
```

- [x] **Step 4: Написать failing-тест в `migrations_test.go`**

```go
package migrations_test

import (
    "database/sql"
    "testing"

    _ "modernc.org/sqlite"

    "github.com/anadale/huskwoot/internal/storage/migrations"
)

func TestUpAppliesBaseline(t *testing.T) {
    db, err := sql.Open("sqlite", ":memory:")
    if err != nil {
        t.Fatalf("открытие БД: %v", err)
    }
    defer db.Close()

    if err := migrations.Up(db); err != nil {
        t.Fatalf("Up: %v", err)
    }

    var n int
    if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('projects','tasks','cursors','channel_projects','messages')`).Scan(&n); err != nil {
        t.Fatalf("проверка таблиц: %v", err)
    }
    if n != 5 {
        t.Fatalf("ожидалось 5 таблиц, получено %d", n)
    }
}
```

Run: `go test ./internal/storage/migrations/ -run TestUpAppliesBaseline -v`
Expected: FAIL (пакет ещё не компилируется или таблицы не созданы).

- [x] **Step 5: Запустить тест, убедиться что зелёный**

Run: `go test ./internal/storage/migrations/ -run TestUpAppliesBaseline -v`
Expected: PASS.

- [x] **Step 6: Изменить `internal/storage/db.go` — заменить inline-CREATE на `migrations.Up`**

```go
import (
    // ...
    "github.com/anadale/huskwoot/internal/storage/migrations"
)

func OpenDB(path string) (*sql.DB, error) {
    db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
    if err != nil {
        return nil, fmt.Errorf("открытие БД: %w", err)
    }
    if err := migrations.Up(db); err != nil {
        db.Close()
        return nil, err
    }
    return db, nil
}
```

Удалить старый блок `db.Exec(...)` с `CREATE TABLE IF NOT EXISTS`.

- [x] **Step 7: Прогнать существующие тесты**

Run: `go test ./internal/storage/... -v`
Expected: PASS.

- [x] **Step 8: Коммит**

```bash
git add internal/storage go.mod go.sum
git commit -m "feat(storage): подключение goose и baseline-миграция"
```

---

### Task 2: Slugify-утилита

**Files:**
- Create: `internal/usecase/slug.go`
- Create: `internal/usecase/slug_test.go`

- [x] **Step 1: Написать failing-тест с table-driven кейсами**

```go
package usecase_test

import (
    "testing"

    "github.com/anadale/huskwoot/internal/usecase"
)

func TestSlugify(t *testing.T) {
    cases := []struct {
        name string
        in   string
        want string
    }{
        {"latin", "Hello World", "hello-world"},
        {"cyrillic_simple", "Проект", "proekt"},
        {"cyrillic_phrase", "На Старт!", "na-start"},
        {"mixed", "Проект NA-Старт", "proekt-na-start"},
        {"yo", "Ёлка", "yolka"},
        {"hard_soft_signs", "объявление", "obyavlenie"},
        {"sch_zh_ts", "Щука Жук Цветок", "schuka-zhuk-tsvetok"},
        {"digits", "Проект 2026", "proekt-2026"},
        {"trim_dashes", "  ---hello---  ", "hello"},
        {"empty_fallback", "!!!", "project"},
        {"only_spaces_fallback", "   ", "project"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := usecase.Slugify(tc.in)
            if got != tc.want {
                t.Fatalf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
            }
        })
    }
}
```

Run: `go test ./internal/usecase/ -run TestSlugify -v`
Expected: FAIL (пакет ещё не существует).

- [x] **Step 2: Реализовать `Slugify` в `internal/usecase/slug.go`**

```go
package usecase

import (
    "strings"
    "unicode"
)

var translit = map[rune]string{
    'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
    'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
    'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
    'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
    'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func Slugify(name string) string {
    var b strings.Builder
    for _, r := range strings.ToLower(name) {
        switch {
        case unicode.IsDigit(r) || (r >= 'a' && r <= 'z'):
            b.WriteRune(r)
        case translit[r] != "":
            b.WriteString(translit[r])
        default:
            b.WriteByte('-')
        }
    }
    s := collapseDashes(b.String())
    s = strings.Trim(s, "-")
    if s == "" {
        return "project"
    }
    return s
}

func collapseDashes(s string) string {
    var b strings.Builder
    prev := byte(0)
    for i := 0; i < len(s); i++ {
        if s[i] == '-' && prev == '-' {
            continue
        }
        b.WriteByte(s[i])
        prev = s[i]
    }
    return b.String()
}
```

Run: `go test ./internal/usecase/ -run TestSlugify -v`
Expected: PASS.

- [x] **Step 3: Коммит**

```bash
git add internal/usecase/slug.go internal/usecase/slug_test.go
git commit -m "feat(usecase): транслит кириллицы в slug"
```

---

### Task 3: Миграция UUID + slug + task_counter + number

**Files:**
- Create: `internal/storage/migrations/002_uuid_slug_number.go`
- Create: `internal/storage/migrations/002_uuid_slug_number_test.go`

- [x] **Step 1: Написать failing-тест миграции**

```go
package migrations_test

import (
    "database/sql"
    "testing"
    "time"

    _ "modernc.org/sqlite"

    "github.com/anadale/huskwoot/internal/storage/migrations"
    "github.com/pressly/goose/v3"
)

func TestUUIDMigrationConvertsExistingRows(t *testing.T) {
    db, err := sql.Open("sqlite", ":memory:")
    if err != nil { t.Fatalf("open: %v", err) }
    defer db.Close()

    // Применить только baseline.
    goose.SetBaseFS(migrations.FS)
    _ = goose.SetDialect("sqlite3")
    if err := goose.UpTo(db, ".", 1); err != nil { t.Fatalf("baseline: %v", err) }

    // Засеять старые данные.
    now := time.Now().UTC().Format(time.RFC3339)
    if _, err := db.Exec(`INSERT INTO projects(name, description, created_at) VALUES (?, ?, ?)`, "Inbox", "", now); err != nil { t.Fatal(err) }
    if _, err := db.Exec(`INSERT INTO projects(name, description, created_at) VALUES (?, ?, ?)`, "Проект НА Старт", "", now); err != nil { t.Fatal(err) }
    if _, err := db.Exec(`INSERT INTO tasks(project_id, summary, created_at, updated_at) VALUES (1, 't1', ?, ?)`, now, now); err != nil { t.Fatal(err) }
    if _, err := db.Exec(`INSERT INTO tasks(project_id, summary, created_at, updated_at) VALUES (1, 't2', ?, ?)`, now, now); err != nil { t.Fatal(err) }
    if _, err := db.Exec(`INSERT INTO tasks(project_id, summary, created_at, updated_at) VALUES (2, 't3', ?, ?)`, now, now); err != nil { t.Fatal(err) }
    if _, err := db.Exec(`INSERT INTO channel_projects(channel_id, project_id) VALUES ('chat:42', 2)`); err != nil { t.Fatal(err) }

    // Применить 002.
    if err := goose.UpTo(db, ".", 2); err != nil { t.Fatalf("002: %v", err) }

    // Проверить структуру projects.
    rows, err := db.Query(`SELECT id, slug, task_counter FROM projects ORDER BY name`)
    if err != nil { t.Fatal(err) }
    type pr struct { id, slug string; cnt int }
    var got []pr
    for rows.Next() {
        var p pr
        if err := rows.Scan(&p.id, &p.slug, &p.cnt); err != nil { t.Fatal(err) }
        if len(p.id) != 36 { t.Fatalf("id не похож на UUID: %q", p.id) }
        got = append(got, p)
    }
    rows.Close()
    if len(got) != 2 { t.Fatalf("ожидалось 2 проекта, получено %d", len(got)) }
    // Inbox: counter=2, slug=inbox
    var inbox, navstart pr
    for _, p := range got {
        if p.slug == "inbox" { inbox = p }
        if p.slug == "proekt-na-start" { navstart = p }
    }
    if inbox.cnt != 2 { t.Fatalf("inbox.task_counter=%d, want 2", inbox.cnt) }
    if navstart.cnt != 1 { t.Fatalf("proekt-na-start.task_counter=%d, want 1", navstart.cnt) }

    // Проверить tasks.
    var n int
    if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_id = ? AND number IN (1, 2)`, inbox.id).Scan(&n); err != nil { t.Fatal(err) }
    if n != 2 { t.Fatalf("tasks в inbox: %d, want 2", n) }

    // Проверить channel_projects: project_id строковый и совпадает.
    var pid string
    if err := db.QueryRow(`SELECT project_id FROM channel_projects WHERE channel_id = 'chat:42'`).Scan(&pid); err != nil { t.Fatal(err) }
    if pid != navstart.id { t.Fatalf("channel_projects.project_id=%q, want %q", pid, navstart.id) }

    // Проверить уникальные индексы.
    if _, err := db.Exec(`INSERT INTO tasks(id, project_id, number, summary, created_at, updated_at) VALUES ('dup', ?, 1, 's', ?, ?)`, inbox.id, now, now); err == nil {
        t.Fatalf("ожидалась ошибка уникальности (project_id, number)")
    }
}
```

Run: `go test ./internal/storage/migrations/ -run TestUUIDMigration -v`
Expected: FAIL.

- [x] **Step 2: Реализовать миграцию `002_uuid_slug_number.go`**

```go
package migrations

import (
    "context"
    "database/sql"
    "fmt"
    "strings"
    "unicode"

    "github.com/google/uuid"
    "github.com/pressly/goose/v3"
)

func init() {
    goose.AddMigrationContext(upUUIDSlugNumber, nil)
}

func upUUIDSlugNumber(ctx context.Context, tx *sql.Tx) error {
    // 1. Создать новые таблицы.
    schema := []string{
        `CREATE TABLE projects_new (
            id           TEXT PRIMARY KEY,
            name         TEXT NOT NULL UNIQUE,
            slug         TEXT NOT NULL UNIQUE,
            description  TEXT NOT NULL DEFAULT '',
            task_counter INTEGER NOT NULL DEFAULT 0,
            created_at   TEXT NOT NULL
        )`,
        `CREATE TABLE tasks_new (
            id          TEXT PRIMARY KEY,
            project_id  TEXT NOT NULL REFERENCES projects_new(id),
            number      INTEGER NOT NULL,
            summary     TEXT NOT NULL,
            details     TEXT NOT NULL DEFAULT '',
            topic       TEXT NOT NULL DEFAULT '',
            status      TEXT NOT NULL DEFAULT 'open',
            deadline    TEXT,
            closed_at   TEXT,
            created_at  TEXT NOT NULL,
            updated_at  TEXT NOT NULL,
            source_kind TEXT NOT NULL DEFAULT '',
            source_id   TEXT NOT NULL DEFAULT ''
        )`,
        `CREATE TABLE channel_projects_new (
            channel_id TEXT PRIMARY KEY,
            project_id TEXT NOT NULL
        )`,
    }
    for _, s := range schema {
        if _, err := tx.ExecContext(ctx, s); err != nil {
            return fmt.Errorf("создание таблицы: %w", err)
        }
    }

    // 2. Перенести projects.
    rows, err := tx.QueryContext(ctx, `SELECT id, name, description, created_at FROM projects ORDER BY id`)
    if err != nil { return fmt.Errorf("чтение projects: %w", err) }
    type proj struct {
        oldID int64
        newID string
        name  string
    }
    var projects []proj
    usedSlugs := map[string]int{}
    for rows.Next() {
        var (
            oldID                 int64
            name, descr, createdAt string
        )
        if err := rows.Scan(&oldID, &name, &descr, &createdAt); err != nil {
            rows.Close()
            return fmt.Errorf("сканирование projects: %w", err)
        }
        slug := uniqueSlug(name, usedSlugs)
        newID := uuid.NewString()
        if _, err := tx.ExecContext(ctx, `INSERT INTO projects_new(id, name, slug, description, task_counter, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
            newID, name, slug, descr, createdAt); err != nil {
            rows.Close()
            return fmt.Errorf("insert projects_new: %w", err)
        }
        projects = append(projects, proj{oldID: oldID, newID: newID, name: name})
    }
    rows.Close()

    pidMap := make(map[int64]string, len(projects))
    for _, p := range projects {
        pidMap[p.oldID] = p.newID
    }

    // 3. Перенести tasks (с присвоением number в порядке created_at, id).
    counters := make(map[string]int, len(projects))
    rows, err = tx.QueryContext(ctx, `SELECT id, project_id, summary, details, topic, status, deadline, closed_at, created_at, updated_at, source_kind, source_id FROM tasks ORDER BY project_id, created_at, id`)
    if err != nil { return fmt.Errorf("чтение tasks: %w", err) }
    for rows.Next() {
        var (
            oldID                                                                                       int64
            oldPID                                                                                      int64
            summary, details, topic, status, createdAt, updatedAt, sourceKind, sourceID                string
            deadline, closedAt                                                                          sql.NullString
        )
        if err := rows.Scan(&oldID, &oldPID, &summary, &details, &topic, &status, &deadline, &closedAt, &createdAt, &updatedAt, &sourceKind, &sourceID); err != nil {
            rows.Close()
            return fmt.Errorf("сканирование tasks: %w", err)
        }
        newPID, ok := pidMap[oldPID]
        if !ok {
            rows.Close()
            return fmt.Errorf("осиротевшая задача %d, project_id=%d не найден", oldID, oldPID)
        }
        counters[newPID]++
        n := counters[newPID]
        if _, err := tx.ExecContext(ctx, `INSERT INTO tasks_new(id, project_id, number, summary, details, topic, status, deadline, closed_at, created_at, updated_at, source_kind, source_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
            uuid.NewString(), newPID, n, summary, details, topic, status, deadline, closedAt, createdAt, updatedAt, sourceKind, sourceID); err != nil {
            rows.Close()
            return fmt.Errorf("insert tasks_new: %w", err)
        }
    }
    rows.Close()

    // 4. Обновить task_counter.
    for pid, c := range counters {
        if _, err := tx.ExecContext(ctx, `UPDATE projects_new SET task_counter = ? WHERE id = ?`, c, pid); err != nil {
            return fmt.Errorf("update task_counter: %w", err)
        }
    }

    // 5. Перенести channel_projects.
    rows, err = tx.QueryContext(ctx, `SELECT channel_id, project_id FROM channel_projects`)
    if err != nil { return fmt.Errorf("чтение channel_projects: %w", err) }
    for rows.Next() {
        var (
            channelID string
            oldPID    int64
        )
        if err := rows.Scan(&channelID, &oldPID); err != nil {
            rows.Close()
            return fmt.Errorf("сканирование channel_projects: %w", err)
        }
        newPID, ok := pidMap[oldPID]
        if !ok {
            // Пропускаем устаревшие маппинги.
            continue
        }
        if _, err := tx.ExecContext(ctx, `INSERT INTO channel_projects_new(channel_id, project_id) VALUES (?, ?)`, channelID, newPID); err != nil {
            rows.Close()
            return fmt.Errorf("insert channel_projects_new: %w", err)
        }
    }
    rows.Close()

    // 6. DROP старых, RENAME новых.
    drops := []string{`DROP TABLE channel_projects`, `DROP TABLE tasks`, `DROP TABLE projects`}
    for _, s := range drops {
        if _, err := tx.ExecContext(ctx, s); err != nil { return fmt.Errorf("drop: %w", err) }
    }
    renames := []string{
        `ALTER TABLE projects_new RENAME TO projects`,
        `ALTER TABLE tasks_new RENAME TO tasks`,
        `ALTER TABLE channel_projects_new RENAME TO channel_projects`,
        `CREATE UNIQUE INDEX uniq_tasks_project_number ON tasks(project_id, number)`,
        `CREATE INDEX idx_tasks_project_status ON tasks(project_id, status)`,
    }
    for _, s := range renames {
        if _, err := tx.ExecContext(ctx, s); err != nil { return fmt.Errorf("rename/index: %w", err) }
    }

    return nil
}

// uniqueSlug — автономная мини-копия Slugify (frozen в миграции, чтобы будущие
// изменения usecase.Slugify не влияли на старые миграции). Если slug уже занят,
// добавляется числовой суффикс.
func uniqueSlug(name string, used map[string]int) string {
    base := slugifyFrozen(name)
    s := base
    n := used[base]
    for n > 0 {
        s = fmt.Sprintf("%s-%d", base, n+1)
        if _, taken := used[s]; !taken { break }
        n++
    }
    used[base] = n + 1
    used[s] = 1
    return s
}

// translitFrozen — frozen-копия таблицы транслита.
var translitFrozen = map[rune]string{
    'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
    'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
    'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
    'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
    'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// slugifyFrozen — frozen-копия usecase.Slugify на момент миграции 002.
// Не вызывать usecase.Slugify напрямую: миграция должна давать
// одинаковый результат на любой версии бинарника.
func slugifyFrozen(name string) string {
    var b strings.Builder
    for _, r := range strings.ToLower(name) {
        switch {
        case unicode.IsDigit(r) || (r >= 'a' && r <= 'z'):
            b.WriteRune(r)
        case translitFrozen[r] != "":
            b.WriteString(translitFrozen[r])
        default:
            b.WriteByte('-')
        }
    }
    s := b.String()
    var out strings.Builder
    var prev byte
    for i := 0; i < len(s); i++ {
        if s[i] == '-' && prev == '-' {
            continue
        }
        out.WriteByte(s[i])
        prev = s[i]
    }
    s = strings.Trim(out.String(), "-")
    if s == "" {
        return "project"
    }
    return s
}
```

> **ВАЖНО:** код `slugifyFrozen` дублируется намеренно. Миграция должна давать одинаковый результат на любой версии бинарника. Если в будущем `usecase.Slugify` изменится, это не должно повлиять на 002.

Run: `go test ./internal/storage/migrations/ -run TestUUIDMigration -v`
Expected: PASS.

- [x] **Step 3: Прогнать все тесты модуля storage**

Run: `go test ./internal/storage/... -v`
Expected: некоторые упадут, потому что `task_store.go` и `meta_store.go` всё ещё работают с INT — это OK, исправим в задачах 5–6. Главное — миграции зелёные.

- [x] **Step 4: Коммит**

```bash
git add internal/storage/migrations/002_uuid_slug_number.go internal/storage/migrations/002_uuid_slug_number_test.go
git commit -m "feat(storage): миграция 002 — UUID, slug, task_counter, number"
```

---

### Task 4: Расширить `model.Project` и `model.Task`

**Files:**
- Modify: `internal/model/project.go` (или `internal/model/task.go` — где сейчас определены)
- Modify: `internal/model/task.go`
- Create/Modify: `internal/model/task_test.go`

- [x] **Step 1: Написать failing-тест на `Task.DisplayID`**

```go
func TestTaskDisplayID(t *testing.T) {
    task := model.Task{Number: 42, ProjectSlug: "inbox"}
    if got := task.DisplayID(); got != "inbox#42" {
        t.Fatalf("DisplayID = %q, want %q", got, "inbox#42")
    }
}
```

Run: `go test ./internal/model/ -v`
Expected: FAIL.

- [x] **Step 2: Расширить структуры**

В `internal/model/project.go`:
```go
type Project struct {
    ID          string
    Name        string
    Slug        string
    Description string
    TaskCounter int
    CreatedAt   time.Time
}
```

В `internal/model/task.go`:
```go
type Task struct {
    ID            string
    Number        int
    ProjectID     string
    ProjectSlug   string  // заполняется store'ом через JOIN, не хранится в tasks
    Summary       string
    Details       string
    Topic         string
    Status        string
    Deadline      *time.Time
    ClosedAt      *time.Time
    CreatedAt     time.Time
    UpdatedAt     time.Time
    SourceMessage Message
}

func (t Task) DisplayID() string {
    return fmt.Sprintf("%s#%d", t.ProjectSlug, t.Number)
}
```

- [x] **Step 3: Запустить тест**

Run: `go test ./internal/model/ -v`
Expected: PASS.

- [x] **Step 4: `go vet` и компиляция всего проекта**

Run: `go build ./...`
Expected: будет много ошибок типизации в потребителях (storage, agent, pipeline) — это ожидается; чиним в следующих задачах.

- [x] **Step 5: Коммит**

```bash
git add internal/model
git commit -m "refactor(model): UUID-id для Project/Task, добавлены Slug/Number/TaskCounter"
```

---

### Task 5: SQLiteTaskStore — поддержка UUID, slug, number, MoveTask, GetTaskByRef

**Files:**
- Modify: `internal/storage/task_store.go`
- Modify: `internal/storage/task_store_test.go`
- Modify: `internal/storage/cached_task_store.go`
- Modify: `internal/storage/cached_task_store_test.go`
- Modify: `internal/model/interfaces.go`

- [x] **Step 1: Расширить интерфейс `TaskStore` в `model/interfaces.go`**

```go
type TaskStore interface {
    CreateProject(ctx context.Context, p *Project) error            // требует p.Slug != ""
    UpdateProject(ctx context.Context, id string, upd ProjectUpdate) error
    GetProject(ctx context.Context, id string) (*Project, error)
    ListProjects(ctx context.Context) ([]Project, error)
    FindProjectByName(ctx context.Context, name string) (*Project, error)
    CreateTask(ctx context.Context, task *Task) error               // присваивает Number в tx
    GetTask(ctx context.Context, id string) (*Task, error)
    GetTaskByRef(ctx context.Context, projectSlug string, number int) (*Task, error)
    ListTasks(ctx context.Context, projectID string, filter TaskFilter) ([]Task, error)
    UpdateTask(ctx context.Context, id string, update TaskUpdate) error
    MoveTask(ctx context.Context, taskID, newProjectID string) error
    DefaultProjectID() string
}
```

- [x] **Step 2: Написать failing-тесты для нового SQLiteTaskStore**

Заменить все `int64`-ID на `string` в существующих тестах. Добавить кейсы:

```go
func TestCreateTaskAssignsMonotonicNumber(t *testing.T) { /* проверяет что 3 последовательных CreateTask дают number 1, 2, 3 и task_counter растёт */ }
func TestCreateTaskUniquenessProjectNumber(t *testing.T) { /* конкуррентные CreateTask не порождают дубль (project_id, number) */ }
func TestGetTaskByRef(t *testing.T) { /* lookup по (slug, number) */ }
func TestMoveTaskReassignsNumber(t *testing.T) { /* number в новом проекте = task_counter+1; в старом не откатывается */ }
func TestCreateProjectRequiresSlug(t *testing.T) { /* CreateProject без Slug возвращает ошибку */ }
func TestUpdateProjectRenamesSlug(t *testing.T) { /* UpdateProject с новым slug меняет связанные ссылки */ }
```

Run: `go test ./internal/storage/ -v`
Expected: FAIL.

- [x] **Step 3: Переписать `SQLiteTaskStore.CreateTask`**

```go
func (s *SQLiteTaskStore) CreateTask(ctx context.Context, task *model.Task) error {
    if task.ProjectID == "" {
        return fmt.Errorf("CreateTask: project_id обязателен")
    }
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return fmt.Errorf("BEGIN: %w", err) }
    defer tx.Rollback()

    var counter int
    if err := tx.QueryRowContext(ctx, `SELECT task_counter FROM projects WHERE id = ?`, task.ProjectID).Scan(&counter); err != nil {
        return fmt.Errorf("чтение counter: %w", err)
    }
    counter++
    if _, err := tx.ExecContext(ctx, `UPDATE projects SET task_counter = ? WHERE id = ?`, counter, task.ProjectID); err != nil {
        return fmt.Errorf("UPDATE counter: %w", err)
    }

    task.ID = uuid.NewString()
    task.Number = counter
    task.Status = "open"
    now := time.Now().UTC()
    task.CreatedAt = now
    task.UpdatedAt = now

    if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, summary, details, topic, status, deadline, created_at, updated_at, source_kind, source_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        task.ID, task.ProjectID, task.Number, task.Summary, task.Details, task.Topic, task.Status,
        nullableTime(task.Deadline), now.Format(time.RFC3339), now.Format(time.RFC3339),
        task.SourceMessage.Kind, task.SourceMessage.ID,
    ); err != nil {
        return fmt.Errorf("INSERT task: %w", err)
    }
    return tx.Commit()
}
```

- [x] **Step 4: Переписать `CreateProject`, `MoveTask`, `GetTaskByRef`, `UpdateProject` аналогично, опираясь на тесты**

- [x] **Step 5: Обновить `CachedTaskStore` под новый интерфейс**

CachedTaskStore не должен ничего ломать: добавить прокси-методы для новых сигнатур (`GetTaskByRef`, `MoveTask`, `UpdateProject`). Сброс кеша `ListProjects` — также при `UpdateProject`.

- [x] **Step 6: Прогнать все тесты пакета**

Run: `go test ./internal/storage/... -race -v`
Expected: PASS.

- [x] **Step 7: Коммит**

```bash
git add internal/storage internal/model/interfaces.go
git commit -m "feat(storage): UUID-aware TaskStore с number/slug, MoveTask, GetTaskByRef"
```

---

### Task 6: SQLiteMetaStore — string project_id

**Files:**
- Modify: `internal/storage/meta_store.go`
- Modify: `internal/storage/meta_store_test.go`

- [x] **Step 1: Обновить failing-тест**

```go
func TestMetaStoreSetGetStringProjectID(t *testing.T) {
    db := openTestDB(t)
    s := storage.NewSQLiteMetaStore(db)
    pid := uuid.NewString()
    if err := s.Set(context.Background(), "project:chat:42", pid); err != nil { t.Fatal(err) }
    got, err := s.Get(context.Background(), "project:chat:42")
    if err != nil { t.Fatal(err) }
    if got != pid { t.Fatalf("Get=%q, want %q", got, pid) }
}
```

Run: `go test ./internal/storage/ -run TestMetaStore -v`
Expected: FAIL (миграция 002 уже сделала колонку TEXT, но методы Set/Get принимают int).

- [x] **Step 2: Обновить `Set`/`Get`/`Values` — заменить int на string в сигнатурах и SQL**

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/storage/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/storage/meta_store.go internal/storage/meta_store_test.go
git commit -m "refactor(storage): MetaStore хранит project_id как string"
```

---

### Task 7: Интерфейсы use-case'ов в `model/service.go`

**Files:**
- Create: `internal/model/service.go`

- [x] **Step 1: Создать файл с DTO и интерфейсами**

Скопировать определения `CreateTaskRequest`, `CreateTasksRequest`, `TaskUpdate`, `TaskFilter`, `CreateProjectRequest`, `ProjectUpdate`, `ChatReply` и интерфейсы `TaskService`, `ProjectService`, `ChatService` (см. Technical Details выше). Compile-only — без реализации.

- [x] **Step 2: `go build ./...` для проверки компиляции**

Expected: PASS (все типы согласованы).

- [x] **Step 3: Коммит**

```bash
git add internal/model/service.go
git commit -m "feat(model): интерфейсы TaskService/ProjectService/ChatService"
```

---

### Task 8: ProjectService — реализация

**Files:**
- Create: `internal/usecase/projects.go`
- Create: `internal/usecase/projects_test.go`

- [x] **Step 1: Написать failing-тесты с моками `TaskStore` и `MetaStore`**

```go
func TestProjectServiceEnsureChannelProjectCreatesAndSetsMapping(t *testing.T) {
    store := &mockTaskStore{}
    meta := &mockMetaStore{}
    svc := usecase.NewProjectService(store, meta)
    p, err := svc.EnsureChannelProject(ctx, "chat:42", "Новый Проект")
    if err != nil { t.Fatal(err) }
    if p.Slug != "novyy-proekt" { t.Fatalf("slug=%q", p.Slug) }
    if meta.set["project:chat:42"] != p.ID { t.Fatalf("маппинг не записан") }
}

func TestProjectServiceEnsureChannelProjectReturnsExisting(t *testing.T) { /* FindProjectByName возвращает существующий */ }

func TestProjectServiceResolveFallbackInbox(t *testing.T) { /* MetaStore.Get вернул "" → DefaultProjectID() */ }

func TestProjectServiceCreateAutoSlug(t *testing.T) { /* CreateProject без явного slug — генерится из name */ }

func TestProjectServiceCreateExplicitSlug(t *testing.T) { /* req.Slug передан → используется как есть */ }
```

Run: `go test ./internal/usecase/ -run TestProjectService -v`
Expected: FAIL.

- [x] **Step 2: Реализовать `internal/usecase/projects.go`**

```go
package usecase

import (
    "context"
    "fmt"

    "github.com/anadale/huskwoot/internal/model"
)

type projectService struct {
    store model.TaskStore
    meta  model.MetaStore
}

func NewProjectService(store model.TaskStore, meta model.MetaStore) *projectService {
    return &projectService{store: store, meta: meta}
}

func (s *projectService) CreateProject(ctx context.Context, req model.CreateProjectRequest) (*model.Project, error) {
    p := &model.Project{Name: req.Name, Description: req.Description, Slug: req.Slug}
    if p.Slug == "" {
        p.Slug = Slugify(p.Name)
    }
    if err := s.store.CreateProject(ctx, p); err != nil {
        return nil, fmt.Errorf("создание проекта: %w", err)
    }
    return p, nil
}

// FindProjectByName конвенция: (nil, nil) если проект не найден,
// (*Project, nil) если найден, (nil, err) при сбое БД. Сейчас интерфейс
// model.TaskStore уже использует эту конвенцию (см. internal/storage/task_store.go).
func (s *projectService) EnsureChannelProject(ctx context.Context, channelID, name string) (*model.Project, error) {
    existing, err := s.store.FindProjectByName(ctx, name)
    if err != nil {
        return nil, fmt.Errorf("поиск проекта: %w", err)
    }
    p := existing
    if p == nil {
        p, err = s.CreateProject(ctx, model.CreateProjectRequest{Name: name})
        if err != nil {
            return nil, err
        }
    }
    if err := s.meta.Set(ctx, "project:"+channelID, p.ID); err != nil {
        return nil, fmt.Errorf("сохранение маппинга канала: %w", err)
    }
    return p, nil
}

func (s *projectService) ResolveProjectForChannel(ctx context.Context, channelID string) (string, error) {
    pid, err := s.meta.Get(ctx, "project:"+channelID)
    if err != nil { return "", fmt.Errorf("чтение маппинга: %w", err) }
    if pid != "" { return pid, nil }
    return s.store.DefaultProjectID(), nil
}

// ... остальные методы (UpdateProject, ListProjects, FindProjectByName) — прокидывают в store.
```

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/usecase/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/usecase/projects.go internal/usecase/projects_test.go
git commit -m "feat(usecase): ProjectService с EnsureChannelProject и ResolveProjectForChannel"
```

---

### Task 9: TaskService — реализация

**Files:**
- Create: `internal/usecase/tasks.go`
- Create: `internal/usecase/tasks_test.go`

- [x] **Step 1: Написать failing-тесты**

```go
func TestTaskServiceCreateUsesInboxIfProjectIDEmpty(t *testing.T) { /* req.ProjectID == "" → DefaultProjectID() */ }
func TestTaskServiceCompleteSetsStatusDone(t *testing.T) { /* UpdateTask со статусом done */ }
func TestTaskServiceReopenSetsStatusOpen(t *testing.T) { /* симметрично */ }
func TestTaskServiceMoveDelegatesToStore(t *testing.T) { /* проверяет что MoveTask проброшен */ }
func TestTaskServiceGetByRefLookup(t *testing.T) { /* ProjectService возвращает slug → number */ }
func TestTaskServiceCreateTasksAtomicity(t *testing.T) { /* батч создания: 3 задачи в один проект */ }
```

Run: `go test ./internal/usecase/ -run TestTaskService -v`
Expected: FAIL.

- [x] **Step 2: Реализовать `internal/usecase/tasks.go`**

Тонкая обёртка над `model.TaskStore`. Поля `Status`-перевода: `CompleteTask` → `UpdateTask{Status: ptr("done"), ClosedAt: now}`, `ReopenTask` → `UpdateTask{Status: ptr("open"), ClosedAt: nil}`.

`CreateTasks` итерирует по `req.Tasks`, для каждой задачи вызывает `CreateTask`. В Фазе 2 батч-логика будет переписана на одну транзакцию (вместе с записью events). Сейчас — N независимых вызовов, это OK для legacy-семантики.

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/usecase/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/usecase/tasks.go internal/usecase/tasks_test.go
git commit -m "feat(usecase): TaskService с CRUD, MoveTask, GetTaskByRef"
```

---

### Task 10: ChatService — реализация

**Files:**
- Create: `internal/usecase/chat.go`
- Create: `internal/usecase/chat_test.go`

- [x] **Step 1: Написать failing-тест**

```go
func TestChatServiceDelegatesToAgent(t *testing.T) {
    a := &mockAgent{reply: "ответ"}
    svc := usecase.NewChatService(a)
    rep, err := svc.HandleMessage(ctx, model.Message{Text: "привет"})
    if err != nil { t.Fatal(err) }
    if rep.Text != "ответ" { t.Fatalf("Text=%q", rep.Text) }
    if a.calls != 1 { t.Fatalf("agent.Handle called %d times", a.calls) }
}
```

Run: `go test ./internal/usecase/ -run TestChatService -v`
Expected: FAIL.

- [x] **Step 2: Реализовать `internal/usecase/chat.go`**

```go
type Agent interface {
    Handle(ctx context.Context, msg model.Message) (string, error)
}

type chatService struct{ agent Agent }

func NewChatService(a Agent) *chatService { return &chatService{agent: a} }

func (s *chatService) HandleMessage(ctx context.Context, msg model.Message) (model.ChatReply, error) {
    text, err := s.agent.Handle(ctx, msg)
    if err != nil { return model.ChatReply{}, fmt.Errorf("агент: %w", err) }
    return model.ChatReply{Text: text}, nil
}
```

> `TasksTouched`/`ProjectsTouched` в Фазе 1 остаются пустыми. В Фазе 2 они заполнятся, когда use-case'ы начнут возвращать туда созданные/изменённые сущности.

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/usecase/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/usecase/chat.go internal/usecase/chat_test.go
git commit -m "feat(usecase): ChatService — обёртка над Agent.Handle"
```

---

### Task 11: Pipeline — переход на TaskService/ProjectService/ChatService

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] **Step 1: Адаптировать тесты pipeline**

В `pipeline_test.go`: заменить моки `TaskStore`/`MetaStore` на моки `TaskService`/`ProjectService`/`ChatService`. Базовые сценарии (Promise → CreateTasks; Command → CommandHandler; DM → ChatService.HandleMessage) должны остаться зелёными.

- [x] **Step 2: Переписать `pipeline.go`**

```go
type Pipeline struct {
    classifiers      map[model.MessageKind]model.Classifier
    extractors       map[model.MessageKind]model.Extractor
    commandExtractor model.CommandExtractor
    commandHandlers  []model.CommandHandler
    notifiers        []model.Notifier
    tasks            model.TaskService
    projects         model.ProjectService
    chat             model.ChatService
    logger           *slog.Logger
}
```

- В `processPromise`: заменить `lookupProjectID(msg)` + `taskStore.CreateTask` на `projects.ResolveProjectForChannel(channelID)` + `tasks.CreateTasks(req)`.
- В обработке DM/GroupDirect: вместо `agent.Handle(ctx, msg)` — `chat.HandleMessage(ctx, msg)`.

- [x] **Step 3: Запустить тесты**

Run: `go test ./internal/pipeline/ -race -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/pipeline
git commit -m "refactor(pipeline): переход на use-case слой (Task/Project/ChatService)"
```

---

### Task 12: Агентские tools — переход на use-case слой

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/create_task.go`, `complete_task.go`, `list_tasks.go`
- Modify: `internal/agent/create_project.go`, `list_projects.go`
- Modify: соответствующие `*_test.go`

- [x] **Step 1: Сначала обновить `Agent.Config`**

В `internal/agent/agent.go` `Config.ListProjects` остаётся как есть (тип `func(ctx) ([]Project, error)`); в `main.go` будет передан `projectService.ListProjects`. Сам `Agent` в Фазе 1 НЕ переписывается на ChatService — наоборот, `ChatService` теперь оборачивает `Agent` (см. Task 10).

- [x] **Step 2: В каждом tool — заменить тип конструктора**

Пример для `create_task.go`:

```go
func NewCreateTaskTool(svc model.TaskService, projects model.ProjectService) Tool { ... }

func (t *createTaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    var p struct {
        ProjectID string  `json:"project_id"`
        Project   string  `json:"project"`  // имя/slug — для удобства модели
        Summary   string  `json:"summary"`
        // ...
    }
    if err := json.Unmarshal(args, &p); err != nil { return "", err }
    pid := p.ProjectID
    if pid == "" && p.Project != "" {
        proj, err := t.projects.FindProjectByName(ctx, p.Project)
        if err != nil { return "", err }
        if proj != nil { pid = proj.ID }
    }
    task, err := t.tasks.CreateTask(ctx, model.CreateTaskRequest{
        ProjectID: pid,
        Summary:   p.Summary,
        // ... остальные поля копируются из p
    })
    if err != nil { return "", err }
    // task.ProjectSlug заполняется TaskService после JOIN-чтения проекта.
    return fmt.Sprintf("Создана %s: %s", task.DisplayID(), task.Summary), nil
}
```

- [x] **Step 3: Аналогично для остальных tools (`complete_task`, `list_tasks`, `create_project`, `list_projects`)**

- [x] **Step 4: Обновить тесты — моки TaskStore заменяются моками TaskService/ProjectService**

- [x] **Step 5: Прогнать тесты**

Run: `go test ./internal/agent/ -race -v`
Expected: PASS.

- [x] **Step 6: Коммит**

```bash
git add internal/agent
git commit -m "refactor(agent): tools работают через TaskService/ProjectService"
```

---

### Task 13: Новый агентский tool `move_task`

**Files:**
- Create: `internal/agent/move_task.go`
- Create: `internal/agent/move_task_test.go`

- [x] **Step 1: Написать failing-тест**

```go
func TestMoveTaskToolByRef(t *testing.T) {
    tasks := &mockTaskService{
        getByRef: &model.Task{ID: "uuid-1", Number: 5, ProjectID: "src"},
    }
    projects := &mockProjectService{
        findByName: &model.Project{ID: "dst", Slug: "work"},
    }
    tool := agent.NewMoveTaskTool(tasks, projects)
    out, err := tool.Execute(ctx, json.RawMessage(`{"task_ref":"inbox#5","project":"Работа"}`))
    if err != nil { t.Fatal(err) }
    if !strings.Contains(out, "Задача перенесена") { t.Fatalf("output: %s", out) }
    if tasks.moveCalls != 1 { t.Fatalf("MoveTask not called") }
    if tasks.lastMoveTo != "dst" { t.Fatalf("moved to %q", tasks.lastMoveTo) }
}
```

Run: `go test ./internal/agent/ -run TestMoveTaskTool -v`
Expected: FAIL.

- [x] **Step 2: Реализовать tool**

```go
func NewMoveTaskTool(tasks model.TaskService, projects model.ProjectService) Tool { ... }

// Параметры:
//   task_id  — UUID задачи (один из task_id / task_ref)
//   task_ref — "<slug>#<number>"
//   project_id или project — куда перенести
// DMOnly: false (доступен и в DM, и в GroupDirect)
```

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/agent/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/agent/move_task.go internal/agent/move_task_test.go
git commit -m "feat(agent): tool move_task для переноса задач между проектами"
```

---

### Task 14: SetProjectHandler — переход на ProjectService.EnsureChannelProject

**Files:**
- Modify: `internal/handler/setproject.go`
- Modify: `internal/handler/setproject_test.go`

- [x] **Step 1: Адаптировать тест: вместо моков TaskStore + MetaStore — мок ProjectService**

- [x] **Step 2: Переписать `Handle`**

```go
func (h *SetProjectHandler) Handle(ctx context.Context, cmd model.Command) error {
    p, err := h.projects.EnsureChannelProject(ctx, cmd.ChannelID, cmd.Args["project"])
    if err != nil { return fmt.Errorf("привязка проекта: %w", err) }
    return cmd.OriginMessage.ReplyFn(ctx, fmt.Sprintf("Чат привязан к проекту «%s» (%s)", p.Name, p.Slug))
}
```

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/handler/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/handler
git commit -m "refactor(handler): SetProjectHandler через ProjectService.EnsureChannelProject"
```

---

### Task 15: cmd/huskwoot/main.go — wiring use-cases

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] **Step 1: Добавить wiring перед инициализацией pipeline и agent**

```go
projectSvc := usecase.NewProjectService(taskStore, metaStore)
taskSvc    := usecase.NewTaskService(taskStore, projectSvc)
agentInst, err := agent.New(aiClient, []agent.Tool{
    agent.NewCreateTaskTool(taskSvc, projectSvc),
    agent.NewListTasksTool(taskSvc),
    agent.NewCompleteTaskTool(taskSvc),
    agent.NewCreateProjectTool(projectSvc),
    agent.NewListProjectsTool(projectSvc),
    agent.NewMoveTaskTool(taskSvc, projectSvc),
}, taskStore, metaStore, logger, agent.Config{
    Now:          nowFn,
    ListProjects: projectSvc.ListProjects,
})
if err != nil { return err }
chatSvc := usecase.NewChatService(agentInst)

pipe := pipeline.New(pipeline.Config{
    Tasks: taskSvc, Projects: projectSvc, Chat: chatSvc,
    // ... остальное как раньше ...
})
```

- [x] **Step 2: Удалить вызовы старого API (taskStore.CreateTask напрямую, metaStore.Get для project lookup и т.п.) — теперь это инкапсулировано в use-case'ах**

- [x] **Step 3: `go build ./...` и smoke-запуск**

```bash
go build -o bin/huskwoot ./cmd/huskwoot
HUSKWOOT_CONFIG_DIR=/tmp/huskwoot-smoke ./bin/huskwoot --help
```

Expected: компилируется, `--help` отображается.

- [x] **Step 4: Коммит**

```bash
git add cmd/huskwoot
git commit -m "refactor(main): wiring use-case слоя"
```

---

### Task 16: Acceptance — полный прогон тестов и smoke-запуск

- [x] Run: `go vet ./...` — без warnings.
- [x] Run: `go test ./... -race -v` — все тесты PASS.
- [x] Запустить инстанс на тестовой БД с парой ранее существующих проектов и задач (восстановить из бэкапа dev-SQLite или создать через старую версию): [x] manual test (skipped - not automatable)
  - проверить, что миграция 002 прошла без ошибок;
  - проверить, что проекты получили slug, задачи получили number, `task_counter` совпадает с MAX(number);
  - проверить, что Telegram-команда `/project Тест` создаёт проект и привязывает чат;
  - проверить, что Promise из тестового группового чата создаёт задачу с number=1 в этом проекте;
  - проверить, что DM `/list` показывает задачи с display_id `<slug>#<n>`;
  - проверить, что новый tool `move_task` работает (DM `перенеси inbox#1 в проект Работа`).
- [x] Если что-то падает: исправить **в этом плане**, добавив ⚠️ задачу. [x] manual test (skipped - not automatable)

---

### Task 17: Финальные обновления

- [x] Обновить `CLAUDE.md`:
  - в разделе «Структура директорий» добавить `internal/usecase/` и `internal/storage/migrations/`;
  - в разделе «SQLite-хранилище» отразить, что миграции через goose, а ID — UUID;
  - в разделе «Архитектура Pipeline» отразить переход на use-case слой;
  - в разделе «Агент и инструменты» добавить `move_task` в таблицу инструментов.
- [x] `mkdir -p docs/plans/completed && git mv docs/plans/2026-04-18-backend-api-phase1-usecase-and-migrations.md docs/plans/completed/`.
- [x] Финальный коммит: `docs: завершена Фаза 1 backend-API (use-case + миграции)`.

---

## Post-Completion

*Информационные пункты, не требующие чекбокса в этом плане.*

**Перед запуском обновлённого инстанса в проде (личная VPS автора):**
- Сделать бэкап `huskwoot.db` (`sqlite3 huskwoot.db ".backup huskwoot.bak"`).
- Запустить новый бинарник, убедиться что миграции применились без ошибок.
- В случае падения миграции 002 — восстановить из бэкапа, проанализировать лог, при необходимости открыть отдельную задачу.

**Передача в Фазу 2:**
- Use-case'ы готовы принять дополнительный параметр `EventStore`, `Broker`, `PushQueue` в Фазе 2 (расширение конструкторов).
- `pipeline.go` и агентские tools уже не имеют прямых обращений к store'ам — это критическое условие для Фазы 2.
- Все ID — UUID-строки, готовые к экспонированию в API.

**Что не сделано (намеренно, переносится в следующие фазы):**
- HTTP API (Фаза 2).
- DeviceStore + auth middleware (Фаза 2).
- EventStore + SSE-брокер (Фаза 2).
- Pairing flow (Фаза 3).
- Push relay + push_queue (Фаза 4).
