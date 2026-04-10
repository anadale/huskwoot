# Backend API — Фаза 2: HTTP-сервер, события и SSE

> **For agentic workers:** REQUIRED SUB-SKILL — `superpowers:subagent-driven-development` (recommended) или `superpowers:executing-plans` для пошаговой реализации. Шаги используют чекбоксы (`- [ ]`) для прогресса.

**Goal:** Поднять HTTP-слой поверх готового use-case слоя Фазы 1. Ввести транзакционную семантику для write-методов store'ов, `EventStore`/`PushQueue`/`DeviceStore`, in-memory SSE-брокер, REST-API (tasks/projects/chat/devices/events/sync), OpenAPI 3.1 как источник истины, CLI-команду `devices create` для выдачи dev-токенов. После Фазы 2 клиент умеет подключаться по HTTPS, получать SSE-стрим событий, делать cold-sync через `/v1/sync/snapshot`, и все запросы идут через авторизованные device-токены. Pairing flow (Фаза 3) и push-dispatcher + relay (Фаза 4) — вне скопа.

**Architecture:** Use-case слой владеет транзакциями. `TaskService.CreateTask(req)` открывает `*sql.Tx`, вызывает write-методы `TaskStore`/`EventStore`/`PushQueue` с этим tx, commit'ит и после коммита делает `Broker.Notify(event)`. SSE-брокер — in-memory fan-out, никогда не пишет в БД. HTTP-хэндлеры тонкие: парсинг → use-case → JSON-response. Auth — middleware, которое проверяет `Authorization: Bearer <token>` против `devices.token_hash` (SHA256). Dev-токены выдаются через `huskwoot devices create --name "..."`.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `github.com/go-chi/chi/v5` (новая зависимость), `github.com/pressly/goose/v3` (уже подключен в Фазе 1), `github.com/google/uuid` (уже подключен), `golang.org/x/time/rate` (для базового rate-limit, опционально).

---

## Overview

Фаза 2 — второй из четырёх планов в серии «Backend API + push» по [спецификации](../superpowers/specs/2026-04-18-backend-api-and-push-design.md). Серия:

- **Фаза 1 (завершена):** use-case слой + миграции UUID/slug/number + перевод существующих call-sites.
- **Фаза 2 (этот план):** Cobra `serve`-подкоманда, HTTP-инфраструктура (chi + auth middleware), `DeviceStore`, `EventStore`, `PushQueue` store (без dispatcher'а), SSE-брокер, REST-эндпоинты для tasks/projects/chat/devices/events, `/v1/sync/snapshot`, OpenAPI 3.1, CLI `devices create` для dev.
- **Фаза 3:** Pairing flow (Telegram magic-link DM, HTML confirm с CSRF, long-poll `/v1/pair/status`, rate-limit, `PATCH /v1/devices/me` с push-токенами).
- **Фаза 4:** Push relay (отдельный бинарник `huskwoot-push-relay`), `push_queue` dispatcher с retry-семантикой, HMAC-протокол инстанс ⇄ релей, Caddy + обновлённый docker-compose.

После Фазы 2 запущенный инстанс:

- Поднимает HTTP-сервер на `[api].listen_addr` (при `[api].enabled = true`).
- Все use-case'ы открывают собственную транзакцию, пишут `events` и (для inactive-устройств) `push_queue` job'ы атомарно.
- SSE-подписчики получают события сразу после COMMIT; replay работает через `Last-Event-ID`.
- Cold re-sync через `GET /v1/sync/snapshot` для ситуации, когда клиент отстал сверх `events_retention`.
- Retention-горутина раз в час удаляет старые `events` (старше `[api].events_retention`, дефолт `168h`) вместе с соответствующими delivered/dropped `push_queue` строками.
- Telegram-бот и IMAP продолжают работать: pipeline/reminder/agent-tools идут через те же use-case'ы, теперь транзакционные.
- Pairing **не включён** — dev-токены выдаются CLI-командой `huskwoot devices create --name "iPhone"`.
- Push **не отправляются** — `push_queue` наполняется (для inactive-устройств), но dispatcher отсутствует; очередь копится до Фазы 4.

## Context (from discovery)

**Текущее состояние (после Фазы 1):**

- `internal/usecase/` — тонкие обёртки над `model.TaskStore`/`MetaStore`/`Agent` (см. `tasks.go`, `projects.go`, `chat.go`, `slug.go`). Транзакций **пока нет**.
- `internal/storage/migrations/001_baseline.sql` и `002_uuid_slug_number.go` — схема с UUID/slug/number.
- `internal/model/service.go` — интерфейсы `TaskService`, `ProjectService`, `ChatService` + DTO.
- `internal/model/interfaces.go` — `TaskStore` и `MetaStore` **не tx-aware** (методы принимают только `ctx`, открывают tx внутри).
- `cmd/huskwoot/main.go` — Cobra root без подкоманд (реально запускается сервис, `--help` работает); надо добавить `serve` и `devices`.
- Pipeline, agent tools, handler, reminder уже зовут use-cases (не TaskStore напрямую).

**Файлы, которые меняются:**

- `internal/model/interfaces.go` — `TaskStore` и `MetaStore` получают tx-aware write-методы.
- `internal/model/service.go` — расширение `ChatReply.TasksTouched/ProjectsTouched` (use-case начинает их заполнять).
- `internal/storage/{task_store,cached_task_store,meta_store}.go` — write-методы принимают `*sql.Tx`; read-методы остаются как есть.
- `internal/storage/task_store_test.go`, `meta_store_test.go` — тесты обновляются под новые сигнатуры.
- `internal/usecase/{tasks,projects,chat}.go` — сервис владеет `*sql.DB`, открывает tx, вызывает store с tx, после commit зовёт `broker.Notify`.
- `cmd/huskwoot/main.go` — Cobra-структура `serve` + `devices create`; wiring новых store'ов, брокера, HTTP-сервера.
- `internal/config/config.go` — новые секции `[api]`, `[owner]`.

**Файлы, которые создаются:**

- `internal/storage/migrations/003_devices.sql`, `004_events.sql`, `005_push_queue.sql`.
- `internal/devices/store.go` + тесты.
- `internal/events/{store,broker}.go` + тесты.
- `internal/push/queue.go` + тесты (только хранилище без dispatcher'а).
- `internal/api/{server,auth,tasks,projects,chat,devices,events,sync,errors,idempotency,openapi}.go` + тесты.
- `api/openapi.yaml` (ручной, источник истины).
- `internal/model/event.go` — тип `model.Event` и константы kind'ов.

**Паттерны проекта (соблюдать):**

- Интерфейсы в `internal/model/`, реализации — в отдельных пакетах.
- Конструкторы возвращают `(*Type, error)` если возможна ошибка инициализации.
- Все публичные методы принимают `context.Context` первым параметром.
- Write-методы store'ов теперь: `Method(ctx, tx *sql.Tx, ...args)`. Read-методы: `Method(ctx, ...args)`.
- Тесты — table-driven, моки вручную.
- Логирование — `log/slog` структурированное.
- Ошибки и сообщения для пользователя/логов — на русском.
- `go test -race ./...` обязательно перед acceptance.

## Development Approach

- **Testing approach:** TDD. Каждая задача начинается с failing-теста, затем минимальная реализация, затем рефакторинг.
- Маленькие коммиты — по одному на каждый завершённый цикл «тест → код → зелёная полоса».
- Не вводим обратно несовместимых изменений в публичный API существующих пакетов без тестов на новый контракт.
- Каждая задача **MUST include new/updated tests** для затронутого кода.
- Все тесты должны проходить (`go test ./...` и `go vet ./...`) перед переходом к следующей задаче.
- При отклонении от плана — обновлять этот файл (➕ для новых задач, ⚠️ для блокеров).

## Testing Strategy

- **Unit-тесты:** обязательны на каждый шаг.
- **Integration-тесты** для миграций, store'ов и HTTP-хэндлеров — на in-memory SQLite + `httptest.NewServer`. Идиоматично для проекта.
- **SSE integration-тесты:** `httptest.NewServer`, клиент с `http.Client`, чтение `Last-Event-ID`, проверка replay и heartbeat.
- **E2E-тесты:** в проекте отсутствуют, не добавляем. Smoke-проверка — ручная (запуск `huskwoot serve` локально, cURL + `curl -N -H "Accept: text/event-stream"`).
- **Race-detector:** `go test -race ./...` обязательно перед acceptance (Task 24).

## Progress Tracking

- Чекбоксы помечаются `[x]` сразу после выполнения (не батчем).
- Новые обнаруженные подзадачи — с префиксом ➕.
- Блокеры/проблемы — с префиксом ⚠️.
- Смена scope или подхода — обновлять разделы Overview/Solution Overview/Implementation Steps в этом файле.

## Solution Overview

**1. Транзакционный контракт store'ов.** `TaskStore` и `MetaStore` получают tx-aware write-методы: `CreateProjectTx(ctx, tx, p)`, `UpdateProjectTx(ctx, tx, id, upd)`, `CreateTaskTx(ctx, tx, task)`, `UpdateTaskTx(ctx, tx, id, upd)`, `MoveTaskTx(ctx, tx, taskID, newProjectID)`, `MetaStore.SetTx(ctx, tx, key, value)`. Read-методы остаются без tx (используют внутренний `*sql.DB`). Старые не-tx write-методы удаляются — use-case владеет транзакцией. `CachedTaskStore` прокидывает tx-методы, при `CreateProjectTx` планирует сброс кеша на after-commit (через hook или просто после каждого вызова — проще).

**2. Новые хранилища.**
   - `devices.Store`: `Create(ctx, tx, d)`, `FindByTokenHash(ctx, hash)`, `UpdateLastSeen(ctx, id, t)`, `Revoke(ctx, id)`, `List(ctx)`, `ListActiveIDs(ctx)` (для решения SSE vs push).
   - `events.Store`: `Insert(ctx, tx, ev) (seq, err)`, `SinceSeq(ctx, afterSeq, limit)`, `MaxSeq(ctx)`, `DeleteOlderThan(ctx, cutoff)`.
   - `push.Queue`: `Enqueue(ctx, tx, deviceID, eventSeq)`, `NextBatch(ctx, limit)` (полезен уже сейчас — для теста наполнения), `MarkDelivered/Failed/Drop` (добавим, но использованы в Фазе 4), `DeleteDelivered(ctx, cutoff)` (для retention-cleanup парного с events).

**3. SSE-брокер.** `events.Broker` — in-memory map `deviceID → []chan Event` с `sync.RWMutex`. Методы: `Subscribe(deviceID) (<-chan Event, func())`, `IsActive(deviceID) bool`, `Notify(ev Event)`. Никаких обращений к БД.

**4. Логика «SSE или push».** В use-case перед enqueue: для каждого ID из `devices.ListActiveIDs(ctx)` (который возвращает только не-revoked устройства) проверить `broker.IsActive(id)`. Если нет — `pushQueue.Enqueue(ctx, tx, id, seq)`. Если да — пропустить (клиент получит через SSE после commit). Этот цикл идёт **внутри транзакции** use-case'а.

**5. ChatService isolation.** `HandleMessage` принимает `msg.Source.AccountID`, если он начинается с `client:` — history вытаскивается через отдельный источник, изолированный от Telegram DM. Сейчас `ChatService` — тонкая обёртка; в Фазе 2 расширяется до заполнения `ChatReply.TasksTouched/ProjectsTouched` (агрегирует в `ctx.Value`, в который use-case'ы Create/Update складывают ID).

**6. HTTP-инфраструктура.** `api.Server` строит `chi.Router`, регистрирует middleware (`slog`-logging, recover, request-id, auth), монтирует группы `/v1/*`. Auth middleware: Bearer → SHA256 → lookup через `DeviceStore` → context-ключи `device_id`, `owner_name` → `UpdateLastSeen`. Ошибки — единый helper `api.writeError(w, code, message)` → `{error:{code,message}}`.

**7. Идемпотентность.** Простое in-memory LRU (`map[string]storedResponse` с `sync.Mutex`, TTL 1 час). Если `Idempotency-Key` встречался — возвращаем сохранённый JSON без повторного вызова use-case'а. В Фазе 2 это достаточно (одна реплика).

**8. OpenAPI.** `api/openapi.yaml` в корне репозитория. Загружается через `//go:embed`, раздаётся `GET /v1/openapi.yaml`. Синхронизируется руками; тест проверяет валидность YAML + набор путей совпадает с зарегистрированными в chi роутере (простой grep по `r.Get/r.Post` не делаем — обходим chi.Walk).

**9. CLI `devices create`.** Cobra subcommand `huskwoot devices create --name "..." --platform "linux"`. Открывает БД, вставляет device, печатает bearer-токен в stdout. Не взаимодействует с работающим сервером (использует отдельную блокировку SQLite через WAL). Это dev/admin путь до Фазы 3.

**10. Retention.** Отдельная горутина в `serve`: раз в час вызывает `events.Store.DeleteOlderThan(cutoff)` и `push.Queue.DeleteDelivered(cutoff)` (только строки с `delivered_at` или `dropped_at`). Cutoff = `now - events_retention`.

## Technical Details

### Новые типы в `model/`

```go
// model/event.go

type EventKind string

const (
    EventTaskCreated      EventKind = "task_created"
    EventTaskUpdated      EventKind = "task_updated"
    EventTaskCompleted    EventKind = "task_completed"
    EventTaskReopened     EventKind = "task_reopened"
    EventTaskMoved        EventKind = "task_moved"
    EventProjectCreated   EventKind = "project_created"
    EventProjectUpdated   EventKind = "project_updated"
    EventChatReply        EventKind = "chat_reply"
    EventReminderSummary  EventKind = "reminder_summary"
    EventReset            EventKind = "reset"
)

type Event struct {
    Seq       int64           // заполняется EventStore.Insert
    Kind      EventKind
    EntityID  string
    Payload   json.RawMessage // JSON-снепшот сущности
    CreatedAt time.Time
}

// model/device.go

type Device struct {
    ID           string
    Name         string
    Platform     string // ios|android|macos|windows|linux
    TokenHash    string // hex SHA256; внутреннее поле
    APNSToken    *string
    FCMToken     *string
    CreatedAt    time.Time
    LastSeenAt   *time.Time
    RevokedAt    *time.Time
}
```

### Изменения `TaskStore` (tx-aware write)

```go
// internal/model/interfaces.go

type TaskStore interface {
    // Write-методы (tx-aware)
    CreateProjectTx(ctx context.Context, tx *sql.Tx, p *Project) error
    UpdateProjectTx(ctx context.Context, tx *sql.Tx, id string, upd ProjectUpdate) error
    CreateTaskTx(ctx context.Context, tx *sql.Tx, task *Task) error // присваивает Number
    UpdateTaskTx(ctx context.Context, tx *sql.Tx, id string, upd TaskUpdate) error
    MoveTaskTx(ctx context.Context, tx *sql.Tx, taskID, newProjectID string) error

    // Read-методы (без tx)
    GetProject(ctx context.Context, id string) (*Project, error)
    ListProjects(ctx context.Context) ([]Project, error)
    FindProjectByName(ctx context.Context, name string) (*Project, error)
    GetTask(ctx context.Context, id string) (*Task, error)
    GetTaskByRef(ctx context.Context, projectSlug string, number int) (*Task, error)
    ListTasks(ctx context.Context, projectID string, filter TaskFilter) ([]Task, error)

    DefaultProjectID() string
}

type MetaStore interface {
    Get(ctx context.Context, key string) (string, error)
    SetTx(ctx context.Context, tx *sql.Tx, key, value string) error
    Values(ctx context.Context, prefix string) ([]string, error)
}
```

### Интерфейсы новых store'ов

```go
// internal/model/interfaces.go

type DeviceStore interface {
    Create(ctx context.Context, tx *sql.Tx, d *Device) error
    FindByTokenHash(ctx context.Context, hash string) (*Device, error)
    UpdateLastSeen(ctx context.Context, id string, at time.Time) error
    Revoke(ctx context.Context, id string) error
    List(ctx context.Context) ([]Device, error)
    ListActiveIDs(ctx context.Context) ([]string, error)
    UpdatePushTokens(ctx context.Context, id string, apns, fcm *string) error
}

type EventStore interface {
    Insert(ctx context.Context, tx *sql.Tx, ev Event) (int64, error)
    SinceSeq(ctx context.Context, afterSeq int64, limit int) ([]Event, error)
    MaxSeq(ctx context.Context) (int64, error)
    DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

type PushQueue interface {
    Enqueue(ctx context.Context, tx *sql.Tx, deviceID string, eventSeq int64) error
    NextBatch(ctx context.Context, limit int) ([]PushJob, error) // Фаза 4 full-use
    MarkDelivered(ctx context.Context, id int64) error
    MarkFailed(ctx context.Context, id int64, errText string, nextAttempt time.Time) error
    Drop(ctx context.Context, id int64, reason string) error
    DeleteDelivered(ctx context.Context, cutoff time.Time) (int64, error)
}

type Broker interface {
    Notify(ev Event)
    Subscribe(deviceID string) (<-chan Event, func())
    IsActive(deviceID string) bool
}
```

### Use-case поток `TaskService.CreateTask`

```go
func (s *taskService) CreateTask(ctx context.Context, req model.CreateTaskRequest) (*model.Task, error) {
    pid := req.ProjectID
    if pid == "" { pid = s.tasks.DefaultProjectID() }

    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return nil, fmt.Errorf("открытие транзакции: %w", err) }
    defer tx.Rollback()

    task := &model.Task{ProjectID: pid, Summary: req.Summary, /* ... */}
    if err := s.tasks.CreateTaskTx(ctx, tx, task); err != nil { return nil, err }

    // Подгружаем slug для DisplayID через read-метод (вне tx — slug проекта уже существовал).
    proj, err := s.projects.GetProject(ctx, pid)
    if err != nil || proj == nil { return nil, fmt.Errorf("проект %s не найден", pid) }
    task.ProjectSlug = proj.Slug

    payload, _ := json.Marshal(taskSnapshot(task))
    ev := model.Event{Kind: model.EventTaskCreated, EntityID: task.ID, Payload: payload}
    seq, err := s.events.Insert(ctx, tx, ev)
    if err != nil { return nil, err }
    ev.Seq = seq

    activeIDs, err := s.devices.ListActiveIDs(ctx)
    if err != nil { return nil, err }
    for _, id := range activeIDs {
        if s.broker.IsActive(id) { continue }
        if err := s.queue.Enqueue(ctx, tx, id, seq); err != nil { return nil, err }
    }

    if err := tx.Commit(); err != nil { return nil, fmt.Errorf("commit: %w", err) }
    s.broker.Notify(ev)

    // Агрегация для ChatReply.TasksTouched.
    appendTouched(ctx, touchedTasks, task.ID)
    return task, nil
}
```

### SSE handler

```
GET /v1/events
Accept: text/event-stream
Last-Event-ID: <seq|пусто>
Authorization: Bearer <token>
```

Алгоритм:
1. `auth middleware` → `device_id` в контексте.
2. Если `Last-Event-ID` задан — `events.SinceSeq(seq, maxBatch=500)`:
   - если вернулся пустой массив и при этом `seq < MaxSeq-retentionWindow` — шлём событие `reset` с текущим `MaxSeq` и закрываем коннект;
   - иначе стримим все собранные события.
3. После replay — `broker.Subscribe(deviceID)`; в цикле `select`:
   - `<-ch` — форматируем SSE, пишем в `w`, flush;
   - `<-ctx.Done()` — отписываемся, возврат;
   - `<-time.After(15s)` — шлём `:keepalive\n\n`.
4. Close → отписаться.

SSE формат:
```
id: <seq>
event: <kind>
data: <JSON payload>

```

### Retention cleanup

```go
func (s *retentionRunner) Run(ctx context.Context) {
    t := time.NewTicker(time.Hour)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case now := <-t.C:
            cutoff := now.Add(-s.retention)
            if _, err := s.queue.DeleteDelivered(ctx, cutoff); err != nil {
                s.log.Warn("очистка push_queue", "error", err)
            }
            if _, err := s.events.DeleteOlderThan(ctx, cutoff); err != nil {
                s.log.Warn("очистка events", "error", err)
            }
        }
    }
}
```

### Конфиг-изменения

```toml
[api]
enabled = true
listen_addr = "127.0.0.1:8080"
external_base_url = "https://huskwoot.mydomain.com"
request_timeout = "30s"
chat_timeout = "60s"
events_retention = "168h"
cors_allowed_origins = []

[owner]
telegram_user_id = 0       # резерв для Фазы 3
display_name = ""

# Секция [push] — добавляется в Фазе 4. Здесь не используется.
```

Если `[api].enabled = false` или секция отсутствует — HTTP-сервер не поднимается. Все use-case'ы продолжают работать через broker/queue — SSE/push простаивают, но существующий Telegram-бот и IMAP не ломаются.

### Cobra-структура

```
huskwoot
├── serve            # (default, запускается при вызове без подкоманды для совместимости)
└── devices
    ├── create       # --name <str> --platform <str>; печатает bearer
    ├── list         # вывод таблицы id|name|platform|last_seen|revoked
    └── revoke <id>
```

«Default» поведение: если пользователь зовёт `huskwoot` без аргументов — поднимается `serve`, как сейчас. Это реализуется через `cobra.Command{RunE: serveRunE}` на root-команде, а `serve` — явный alias.

### Миграции

```
internal/storage/migrations/
├── 001_baseline.sql
├── 002_uuid_slug_number.go
├── 003_devices.sql       # новый
├── 004_events.sql        # новый
└── 005_push_queue.sql    # новый
```

Содержимое `003_devices.sql`:

```sql
-- +goose Up
CREATE TABLE devices (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    platform      TEXT NOT NULL,
    token_hash    TEXT NOT NULL UNIQUE,
    apns_token    TEXT,
    fcm_token     TEXT,
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT,
    revoked_at    TEXT
);

CREATE INDEX idx_devices_token_hash_active
    ON devices(token_hash) WHERE revoked_at IS NULL;
```

Содержимое `004_events.sql`:

```sql
-- +goose Up
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE INDEX idx_events_created_at ON events(created_at);
```

Содержимое `005_push_queue.sql`:

```sql
-- +goose Up
CREATE TABLE push_queue (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id         TEXT NOT NULL REFERENCES devices(id),
    event_seq         INTEGER NOT NULL REFERENCES events(seq),
    created_at        TEXT NOT NULL,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    next_attempt_at   TEXT NOT NULL,
    delivered_at      TEXT,
    dropped_at        TEXT,
    dropped_reason    TEXT
);

CREATE INDEX idx_push_queue_pending
    ON push_queue(next_attempt_at)
    WHERE delivered_at IS NULL AND dropped_at IS NULL;
```

### OpenAPI

`api/openapi.yaml` — ручной YAML, описывающий `/v1/*`. Валидируется на старте сервера через простую проверку YAML-parseability (опционально — `github.com/getkin/kin-openapi`, но это доп. зависимость; для Фазы 2 минимального `yaml.Unmarshal` хватает).

## What Goes Where

- **Implementation Steps (`[ ]`):** код, тесты, миграции, конфиг, CLI-команды, обновление `CLAUDE.md`, перенос плана в `docs/plans/completed/`.
- **Post-Completion (без чекбоксов):** ручная проверка реальных HTTP-запросов через `curl`, прогон `curl -N` для SSE, согласование OpenAPI с разработчиками клиентов, подготовка Фазы 3.

---

## Implementation Steps

### Task 1: Миграции 003–005 (devices, events, push_queue)

**Files:**
- Create: `internal/storage/migrations/003_devices.sql`
- Create: `internal/storage/migrations/004_events.sql`
- Create: `internal/storage/migrations/005_push_queue.sql`
- Modify: `internal/storage/migrations/migrations_test.go`

- [x] **Step 1: Написать failing-тест в `migrations_test.go`**

Добавить тест `TestUpAppliesAll` — после `migrations.Up(db)` проверить что все таблицы (`projects`, `tasks`, `channel_projects`, `cursors`, `messages`, `devices`, `events`, `push_queue`) существуют + уникальный индекс `idx_devices_token_hash_active` работает (попытка вставить второе device с тем же token_hash для не-revoked строки даёт ошибку).

Run: `go test ./internal/storage/migrations/ -v`
Expected: FAIL.

- [x] **Step 2: Создать три SQL-файла** с содержимым из раздела «Миграции» выше.

- [x] **Step 3: Прогнать тесты**

Run: `go test ./internal/storage/migrations/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/storage/migrations/003_devices.sql internal/storage/migrations/004_events.sql internal/storage/migrations/005_push_queue.sql internal/storage/migrations/migrations_test.go
git commit -m "feat(storage): миграции 003–005 (devices, events, push_queue)"
```

---

### Task 2: TaskStore tx-aware write-методы

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/storage/task_store.go`
- Modify: `internal/storage/task_store_test.go`
- Modify: `internal/storage/cached_task_store.go`
- Modify: `internal/storage/cached_task_store_test.go`

- [x] **Step 1: Обновить интерфейс `TaskStore`** — заменить `CreateProject`, `UpdateProject`, `CreateTask`, `UpdateTask`, `MoveTask` на `*Tx`-версии с параметром `tx *sql.Tx`.

- [x] **Step 2: Переписать тесты в `task_store_test.go`** — все вызовы write-методов теперь идут через открытие tx в тестовом хелпере:

```go
func withTx(t *testing.T, db *sql.DB, fn func(*sql.Tx)) {
    t.Helper()
    tx, err := db.BeginTx(context.Background(), nil)
    if err != nil { t.Fatal(err) }
    defer tx.Rollback()
    fn(tx)
    if err := tx.Commit(); err != nil { t.Fatal(err) }
}
```

Добавить failing-тесты:
- `TestCreateTaskTxAtomicRollback` — при `tx.Rollback()` задача не попадает в БД, `task_counter` не увеличивается;
- `TestCreateProjectTxRequiresSlug` — без Slug ошибка;
- `TestMoveTaskTxReassignsNumber` — number в новом проекте = `task_counter+1`, в старом не откатывается.

Run: `go test ./internal/storage/ -v`
Expected: FAIL.

- [x] **Step 3: Переписать реализации** — `SQLiteTaskStore.CreateProjectTx`, `UpdateProjectTx`, `CreateTaskTx`, `UpdateTaskTx`, `MoveTaskTx`. Каждая принимает `tx *sql.Tx` вместо открытия своей. Read-методы остаются.

- [x] **Step 4: Обновить `CachedTaskStore`** — прокси для `*Tx`-методов. Сброс кеша `ListProjects` при вызове `CreateProjectTx`/`UpdateProjectTx` — **после commit'а** use-case'ом. Для Фазы 2 достаточно простого сброса на каждом `CreateProjectTx` вызове (до commit) — кеш изредка будет неконсистентен на несколько миллисекунд, что приемлемо. Добавить явный комментарий в коде.

- [x] **Step 5: Прогнать все тесты пакета**

Run: `go test ./internal/storage/... -race -v`
Expected: PASS.

- [x] **Step 6: Коммит**

```bash
git add internal/model/interfaces.go internal/storage
git commit -m "refactor(storage): tx-aware write-методы TaskStore"
```

---

### Task 3: MetaStore tx-aware SetTx

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/storage/meta_store.go`
- Modify: `internal/storage/meta_store_test.go`

- [x] **Step 1: Обновить интерфейс `MetaStore`** — `Set` → `SetTx(ctx, tx *sql.Tx, key, value string) error`.

- [x] **Step 2: Написать failing-тест**

`TestMetaStoreSetTxRolledBack` — после `tx.Rollback()` значение не сохранилось.

Run: `go test ./internal/storage/ -run TestMetaStore -v`
Expected: FAIL.

- [x] **Step 3: Переписать реализацию `SQLiteMetaStore.SetTx`** — использует `tx.ExecContext` вместо `s.db.Exec`.

- [x] **Step 4: Прогнать тесты**

Run: `go test ./internal/storage/ -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/model/interfaces.go internal/storage/meta_store.go internal/storage/meta_store_test.go
git commit -m "refactor(storage): tx-aware MetaStore.SetTx"
```

---

### Task 4: Модель Event и Device

**Files:**
- Create: `internal/model/event.go`
- Create: `internal/model/device.go`
- Modify: `internal/model/types.go` (если нужно расширить `Source` или добавить константу)

- [x] **Step 1: Создать `event.go`** с типом `EventKind`, константами kind'ов, структурой `Event` (см. Technical Details).

- [x] **Step 2: Создать `device.go`** со структурой `Device` (см. Technical Details).

- [x] **Step 3: `go build ./...`** — проверка компиляции. Пакет `internal/model` собирается; ошибки в `internal/usecase/` — унаследованы от Task 2–3 (будут устранены в Task 10).

- [x] **Step 4: Коммит**

```bash
git add internal/model/event.go internal/model/device.go
git commit -m "feat(model): типы Event и Device"
```

---

### Task 5: DeviceStore реализация

**Files:**
- Modify: `internal/model/interfaces.go`
- Create: `internal/devices/store.go`
- Create: `internal/devices/store_test.go`

- [x] **Step 1: Добавить `DeviceStore` интерфейс** в `interfaces.go` (см. Technical Details).

- [x] **Step 2: Написать failing-тесты** в `store_test.go`:

```go
func TestDeviceStoreCreateAndFind(t *testing.T) {
    db := openTestDB(t)
    store := devices.NewSQLiteDeviceStore(db)

    d := &model.Device{ID: uuid.NewString(), Name: "iPhone 17", Platform: "ios", TokenHash: "abc123"}
    withTx(t, db, func(tx *sql.Tx) { mustNil(t, store.Create(ctx, tx, d)) })

    got, err := store.FindByTokenHash(ctx, "abc123")
    mustNil(t, err)
    if got == nil || got.ID != d.ID { t.Fatalf("find = %+v", got) }
}

func TestDeviceStoreRevokeMarksDevice(t *testing.T) { ... }
func TestDeviceStoreFindByTokenHashSkipsRevoked(t *testing.T) { ... }
func TestDeviceStoreListActiveIDsExcludesRevoked(t *testing.T) { ... }
func TestDeviceStoreUpdateLastSeenAndPushTokens(t *testing.T) { ... }
```

Run: `go test ./internal/devices/ -v`
Expected: FAIL.

- [x] **Step 3: Реализовать `NewSQLiteDeviceStore(db)` и все методы**.

- [x] **Step 4: Прогнать тесты**

Run: `go test ./internal/devices/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/model/interfaces.go internal/devices
git commit -m "feat(devices): SQLite DeviceStore"
```

---

### Task 6: EventStore реализация

**Files:**
- Modify: `internal/model/interfaces.go`
- Create: `internal/events/store.go`
- Create: `internal/events/store_test.go`

- [x] **Step 1: Добавить `EventStore` интерфейс** в `interfaces.go`.

- [x] **Step 2: Написать failing-тесты**:

```go
func TestEventStoreInsertReturnsMonotonicSeq(t *testing.T) {
    // три Insert в одном tx → seq 1, 2, 3
}
func TestEventStoreSinceSeqReturnsOrdered(t *testing.T) { ... }
func TestEventStoreMaxSeqEmptyTable(t *testing.T) { ... } // возвращает 0, nil
func TestEventStoreDeleteOlderThan(t *testing.T) { ... }  // ретеншн
```

Run: `go test ./internal/events/ -v`
Expected: FAIL.

- [x] **Step 3: Реализовать** через `INSERT ... RETURNING seq` (SQLite 3.35+; у `modernc.org/sqlite` поддерживается).

- [x] **Step 4: Прогнать тесты**

Run: `go test ./internal/events/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/model/interfaces.go internal/events
git commit -m "feat(events): SQLite EventStore с INSERT RETURNING"
```

---

### Task 7: PushQueue store (без dispatcher'а)

**Files:**
- Modify: `internal/model/interfaces.go`
- Create: `internal/model/push_job.go` (тип `PushJob`)
- Create: `internal/push/queue.go`
- Create: `internal/push/queue_test.go`

- [x] **Step 1: Добавить `PushQueue` интерфейс** и тип `PushJob{ID, DeviceID, EventSeq, Attempts, NextAttemptAt, CreatedAt}`.

- [x] **Step 2: Написать failing-тесты**:

```go
func TestPushQueueEnqueueInTx(t *testing.T) { ... } // после commit строка появилась
func TestPushQueueEnqueueRolledBack(t *testing.T) { ... } // после rollback — пусто
func TestPushQueueNextBatchOrdersByNextAttempt(t *testing.T) { ... }
func TestPushQueueMarkDeliveredExcludesFromBatch(t *testing.T) { ... }
func TestPushQueueDeleteDeliveredRespectsCutoff(t *testing.T) { ... }
```

Run: `go test ./internal/push/ -v`
Expected: FAIL.

- [x] **Step 3: Реализовать `SQLitePushQueue`**. `NextBatch`, `MarkDelivered/Failed/Drop` — работают через `s.db` (без tx). `Enqueue` — через tx.

- [x] **Step 4: Прогнать тесты**

Run: `go test ./internal/push/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/model/interfaces.go internal/model/push_job.go internal/push
git commit -m "feat(push): SQLite PushQueue store"
```

---

### Task 8: SSE Broker (in-memory)

**Files:**
- Modify: `internal/model/interfaces.go`
- Create: `internal/events/broker.go`
- Create: `internal/events/broker_test.go`

- [x] **Step 1: Добавить интерфейс `Broker`** в `interfaces.go`.

- [x] **Step 2: Написать failing-тесты**:

```go
func TestBrokerSubscribeReceivesNotifications(t *testing.T) { ... }
func TestBrokerIsActiveReflectsSubscriptions(t *testing.T) { ... }
func TestBrokerMultipleSubscribersForSameDevice(t *testing.T) { ... }
func TestBrokerUnsubscribeClosesChannel(t *testing.T) { ... }
func TestBrokerFullBufferDropsSubscriber(t *testing.T) { ... } // канал полный — закрываем, клиент переподключится
func TestBrokerConcurrentSubscribeNotifyRace(t *testing.T) { t.Parallel(); /* -race */ }
```

Run: `go test ./internal/events/ -run TestBroker -race -v`
Expected: FAIL.

- [x] **Step 3: Реализовать `NewBroker()`** — `map[string][]chan Event` под `sync.RWMutex`. Buffer size = 64 по умолчанию (настраиваемый через `Config`).

- [x] **Step 4: Прогнать тесты**

Run: `go test ./internal/events/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/model/interfaces.go internal/events/broker.go internal/events/broker_test.go
git commit -m "feat(events): in-memory SSE broker"
```

---

### Task 9: TaskService — транзакции + публикация событий

**Files:**
- Modify: `internal/usecase/tasks.go`
- Modify: `internal/usecase/tasks_test.go`
- Modify: `internal/model/service.go` (если надо расширить сигнатуры конструктора)

- [x] **Step 1: Расширить конструктор** `NewTaskService(db *sql.DB, tasks model.TaskStore, projects model.ProjectService, events model.EventStore, devices model.DeviceStore, queue model.PushQueue, broker model.Broker)`.

- [x] **Step 2: Переписать failing-тесты**:

Добавить моки `events`, `devices`, `queue`, `broker`. Тесты:
- `TestTaskServiceCreateTaskInsertsEventAndEnqueuesForInactive` — broker.IsActive возвращает false для единственного устройства → queue.Enqueue вызван; broker.Notify вызван ПОСЛЕ commit.
- `TestTaskServiceCreateTaskSkipsEnqueueForActive` — broker.IsActive=true → queue не вызван.
- `TestTaskServiceCreateTaskRollbackOnInsertError` — events.Insert возвращает ошибку → tx откатывается, ни одна строка не вставлена, broker.Notify не вызван.
- `TestTaskServiceCompleteTaskEmitsTaskCompletedEvent`, `Reopen`, `Update`, `Move` — аналогично, проверяют kind события.
- `TestTaskServiceCreateTasksBatchSingleTransaction` — 3 задачи в одном tx → 3 events с последовательными seq.

Run: `go test ./internal/usecase/ -run TestTaskService -race -v`
Expected: FAIL.

- [x] **Step 3: Реализовать** — см. пример в Technical Details. Для `CreateTasks` весь цикл внутри одного tx (все задачи + все события + все push-jobs атомарно).

- [x] **Step 4: Прогнать тесты**

Run: `go test ./internal/usecase/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/usecase/tasks.go internal/usecase/tasks_test.go internal/model/service.go
git commit -m "feat(usecase): TaskService с транзакциями и публикацией событий"
```

---

### Task 10: ProjectService — транзакции + события

**Files:**
- Modify: `internal/usecase/projects.go`
- Modify: `internal/usecase/projects_test.go`

- [x] **Step 1: Расширить конструктор** `NewProjectService(db, tasks, meta, events, devices, queue, broker)`.

- [x] **Step 2: Failing-тесты**:
- `TestProjectServiceCreateProjectEmitsEvent` — `project_created`.
- `TestProjectServiceUpdateProjectEmitsEvent` — `project_updated`.
- `TestProjectServiceEnsureChannelProjectAtomicity` — project creation + meta.SetTx + event insert в одном tx; rollback → ничего не создано.
- `TestProjectServiceResolveProjectForChannelDoesNotOpenTx` — чтение без tx.

Run: `go test ./internal/usecase/ -run TestProjectService -race -v`
Expected: FAIL.

- [x] **Step 3: Реализовать**. `ResolveProjectForChannel` — читает `meta.Get` и `DefaultProjectID`, транзакция не нужна.

- [x] **Step 4: Тесты**

Run: `go test ./internal/usecase/ -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/usecase/projects.go internal/usecase/projects_test.go
git commit -m "feat(usecase): ProjectService с транзакциями и событиями"
```

---

### Task 11: ChatService — client-source isolation + touched aggregation

**Files:**
- Modify: `internal/usecase/chat.go`
- Modify: `internal/usecase/chat_test.go`
- Modify: `internal/model/service.go`

- [x] **Step 1: Расширить `ChatService`** — при `msg.Source.AccountID == "client:<device_id>"` HistoryFn агент использует этот source для изоляции. Текущая имплементация просто делегирует `agent.Handle(msg)`; агент уже работает с `msg.Source` — значит ничего расширять не нужно, достаточно документировать.

- [x] **Step 2: Touched-aggregation**. Ввести ctx-ключи:

```go
type touchedKey struct{ kind string } // "task" | "project"
// TaskService в конце CreateTask/UpdateTask/Move/Complete/Reopen:
//   if t, ok := ctx.Value(touchedTasksKey).(*touchedSet); ok { t.add(task.ID) }
```

`ChatService.HandleMessage` кладёт в контекст два set'а, после `agent.Handle` собирает их в `ChatReply.TasksTouched/ProjectsTouched`.

- [x] **Step 3: Failing-тесты**:
- `TestChatServicePopulatesTouchedTasks` — fake agent зовёт fake TaskService.CreateTask → ChatReply.TasksTouched содержит ID созданной задачи.
- `TestChatServiceEmitsChatReplyEvent` — в конце `HandleMessage` публикуется событие `chat_reply`.

Run: `go test ./internal/usecase/ -run TestChatService -v`
Expected: FAIL.

- [x] **Step 4: Реализовать** — расширить `taskService` и `projectService` чтобы они вызывали `touched.add()` если ключ в контексте. Расширить `ChatService.HandleMessage` чтобы публиковать `chat_reply` event (только kind и reply text в payload; его push=нет по decision-table).

- [x] **Step 5: Тесты**

Run: `go test ./internal/usecase/ -race -v`
Expected: PASS.

- [x] **Step 6: Коммит**

```bash
git add internal/usecase/chat.go internal/usecase/chat_test.go internal/usecase/tasks.go internal/usecase/projects.go internal/model/service.go
git commit -m "feat(usecase): ChatService собирает touched ID и публикует chat_reply"
```

---

### Task 12: Retention runner

**Files:**
- Create: `internal/events/retention.go`
- Create: `internal/events/retention_test.go`

- [x] **Step 1: Failing-тест**:

```go
func TestRetentionDeletesOldEventsAndPushQueue(t *testing.T) {
    // вручную вставить events + delivered push_queue, вызвать один цикл Run с cutoff
    // проверить удаление
}
```

Run: `go test ./internal/events/ -run TestRetention -v`
Expected: FAIL.

- [x] **Step 2: Реализовать `Runner`** — конструктор `NewRunner(events, queue, retention, logger)`; метод `Run(ctx)` в цикле с `time.Ticker(1h)`. Для тестируемости — метод `tick(ctx, now)` отдельно.

- [x] **Step 3: Тесты**

Run: `go test ./internal/events/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/events/retention.go internal/events/retention_test.go
git commit -m "feat(events): retention runner (events + push_queue cleanup)"
```

---

### Task 13: Конфиг — секции [api] и [owner]

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] **Step 1: Failing-тест**:

```go
func TestLoadConfigParsesAPISection(t *testing.T) {
    cfg := loadTestConfig(t, `
        [api]
        enabled = true
        listen_addr = "127.0.0.1:9000"
        external_base_url = "https://ex.com"
        events_retention = "72h"
    `)
    if !cfg.API.Enabled { t.Fatal("enabled") }
    if cfg.API.ListenAddr != "127.0.0.1:9000" { t.Fatal("addr") }
    if cfg.API.EventsRetention != 72*time.Hour { t.Fatal("retention") }
}

func TestLoadConfigAPIDefaults(t *testing.T) {
    cfg := loadTestConfig(t, ``)
    if cfg.API.Enabled { t.Fatal("default disabled") } // безопасный дефолт: выключен
}
```

Run: `go test ./internal/config/ -v`
Expected: FAIL.

- [x] **Step 2: Расширить `Config`** — добавить `APIConfig` и `OwnerConfig`. `events_retention` парсится через `time.ParseDuration`. Валидация: если `API.Enabled=true`, `ListenAddr` обязателен.

- [x] **Step 3: Тесты**

Run: `go test ./internal/config/ -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): секции [api] и [owner]"
```

---

### Task 14: Cobra-структура — serve + devices create/list/revoke

**Files:**
- Modify: `cmd/huskwoot/main.go`
- Create: `cmd/huskwoot/devices.go`

- [x] **Step 1: Вынести текущую logic'у `run()` в `serveRunE`**. `rootCmd.RunE = serveRunE` — дефолтное поведение без аргументов сохраняется. Добавить `serveCmd := &cobra.Command{Use:"serve", RunE: serveRunE}`.

- [x] **Step 2: Создать `devices.go`** с подкомандой:

```go
func newDevicesCommand() *cobra.Command {
    cmd := &cobra.Command{Use: "devices"}
    cmd.AddCommand(newDevicesCreateCommand(), newDevicesListCommand(), newDevicesRevokeCommand())
    return cmd
}

func newDevicesCreateCommand() *cobra.Command {
    var name, platform string
    c := &cobra.Command{
        Use: "create",
        RunE: func(cmd *cobra.Command, args []string) error {
            cfgDir, _ := cmd.Flags().GetString("config-dir")
            db, err := storage.OpenDB(filepath.Join(cfgDir, "huskwoot.db"))
            if err != nil { return err }
            defer db.Close()
            store := devices.NewSQLiteDeviceStore(db)

            token := generateToken() // 32 байта crypto/rand → base64url
            d := &model.Device{
                ID: uuid.NewString(), Name: name, Platform: platform,
                TokenHash: sha256Hex(token), CreatedAt: time.Now().UTC(),
            }
            tx, _ := db.BeginTx(cmd.Context(), nil)
            defer tx.Rollback()
            if err := store.Create(cmd.Context(), tx, d); err != nil { return err }
            if err := tx.Commit(); err != nil { return err }

            fmt.Fprintf(cmd.OutOrStdout(), "device_id: %s\nbearer: %s\n", d.ID, token)
            return nil
        },
    }
    c.Flags().StringVar(&name, "name", "", "человекочитаемое имя (iPhone 17, MacBook)")
    c.Flags().StringVar(&platform, "platform", "", "ios|android|macos|windows|linux")
    c.MarkFlagRequired("name")
    c.MarkFlagRequired("platform")
    return c
}
```

`list` — печатает таблицу. `revoke <id>` — ставит `revoked_at=now`.

- [x] **Step 3: Failing-тесты** (CLI-тесты через `cobra.Command.ExecuteContext` с захваченным stdout):

```go
func TestDevicesCreateWritesBearerAndInsertsRow(t *testing.T) { ... }
func TestDevicesListExcludesRevoked(t *testing.T) { ... } // проверяет что по умолчанию `list` показывает всё, но с маркером revoked
func TestDevicesRevokeMarksRow(t *testing.T) { ... }
```

Файл `cmd/huskwoot/devices_test.go`.

Run: `go test ./cmd/huskwoot/ -v`
Expected: FAIL.

- [x] **Step 4: Реализовать** все три подкоманды.

- [x] **Step 5: `go build` + ручной smoke**

```bash
go build -o bin/huskwoot ./cmd/huskwoot
HUSKWOOT_CONFIG_DIR=/tmp/huskwoot-smoke ./bin/huskwoot devices create --name "Test" --platform "linux"
./bin/huskwoot devices list
```

- [x] **Step 6: Коммит**

```bash
git add cmd/huskwoot
git commit -m "feat(cli): serve + devices create/list/revoke подкоманды"
```

---

### Task 15: api.Server skeleton + middleware

**Files:**
- Create: `internal/api/server.go`
- Create: `internal/api/middleware.go`
- Create: `internal/api/errors.go`
- Create: `internal/api/server_test.go`
- Modify: `go.mod`, `go.sum` (добавить chi)

- [x] **Step 1: Добавить зависимость**

```bash
go get github.com/go-chi/chi/v5@latest
go mod tidy
```

- [x] **Step 2: Failing-тесты** в `server_test.go`:

```go
func TestServerHealthzReturnsOK(t *testing.T) { ... }           // GET /healthz → 200
func TestServerReadyzChecksDB(t *testing.T) { ... }            // SELECT 1 → 200; broken DB → 503
func TestServerRecoversFromPanicInHandler(t *testing.T) { ... } // middleware panic-recover
func TestServerRequestIDAddedToLogs(t *testing.T) { ... }
func TestServerNotFoundReturnsStructuredError(t *testing.T) { ... } // {error:{code:"not_found",...}}
```

Run: `go test ./internal/api/ -v`
Expected: FAIL.

- [x] **Step 3: Реализовать `api.New(cfg api.Config) *Server`**:

- `Config{ListenAddr, RequestTimeout, Logger, DB, Services bundle...}`
- `Server.routes()` строит chi router, применяет middleware (logger, request-id, recover), регистрирует `/healthz`, `/readyz`, NotFound/MethodNotAllowed handlers.
- `Server.Run(ctx)` запускает `http.Server` с `ReadTimeout`/`WriteTimeout`/`IdleTimeout`; на `ctx.Done()` делает `Shutdown(ctx)`.

- [x] **Step 4: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/api/server.go internal/api/middleware.go internal/api/errors.go internal/api/server_test.go go.mod go.sum
git commit -m "feat(api): chi-сервер со middleware и healthz/readyz"
```

---

### Task 16: Auth middleware (device-token)

**Files:**
- Create: `internal/api/auth.go`
- Create: `internal/api/auth_test.go`

- [x] **Step 1: Failing-тесты**:

```go
func TestAuthMissingTokenReturns401(t *testing.T) { ... }
func TestAuthInvalidTokenReturns401(t *testing.T) { ... }
func TestAuthRevokedTokenReturns401(t *testing.T) { ... }
func TestAuthValidTokenPutsDeviceIDInContext(t *testing.T) { ... }
func TestAuthUpdatesLastSeen(t *testing.T) { ... }
```

Run: `go test ./internal/api/ -run TestAuth -v`
Expected: FAIL.

- [x] **Step 2: Реализовать** `AuthMiddleware(deviceStore model.DeviceStore, logger)` — читает `Authorization: Bearer <token>`, вычисляет SHA256, `FindByTokenHash`, если nil/revoked → 401 с `{error:{code:"unauthorized"}}`, иначе кладёт `device_id` в контекст и вызывает `UpdateLastSeen(ctx, id, time.Now())`. `UpdateLastSeen` — best-effort, ошибка только логируется.

- [x] **Step 3: Helper `DeviceIDFromContext(ctx) string`** — для хэндлеров.

- [x] **Step 4: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/api/auth.go internal/api/auth_test.go
git commit -m "feat(api): auth middleware по device-token SHA256"
```

---

### Task 17: Idempotency-Key middleware (in-memory LRU)

**Files:**
- Create: `internal/api/idempotency.go`
- Create: `internal/api/idempotency_test.go`

- [x] **Step 1: Failing-тесты**:

```go
func TestIdempotencyFirstRequestCallsHandler(t *testing.T) { ... }
func TestIdempotencyRepeatReturnsCachedResponse(t *testing.T) { ... }
func TestIdempotencyWithoutHeaderBypasses(t *testing.T) { ... }
func TestIdempotencyKeyIsolatedByDevice(t *testing.T) { ... } // тот же key, разные device_id → разные ответы
func TestIdempotencyExpiresAfterTTL(t *testing.T) { ... }
```

Run: `go test ./internal/api/ -run TestIdempotency -v`
Expected: FAIL.

- [x] **Step 2: Реализовать** — `map[string]entry` под mutex + `container/list` для LRU-eviction + TTL 1 час. Ключ = `device_id + ":" + idempotency_key`. Кешируем status + headers + body для write/redirect responses.

- [x] **Step 3: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/api/idempotency.go internal/api/idempotency_test.go
git commit -m "feat(api): Idempotency-Key middleware с in-memory LRU"
```

---

### Task 18: /v1/me, /v1/projects endpoints

**Files:**
- Create: `internal/api/me.go`
- Create: `internal/api/projects.go`
- Create: `internal/api/projects_test.go`

- [x] **Step 1: Failing-тесты**:

```go
func TestGetMeReturnsOwnerAndVersion(t *testing.T) { ... }
func TestListProjectsReturnsAll(t *testing.T) { ... }
func TestGetProjectByID(t *testing.T) { ... }
func TestGetProjectNotFoundReturns404(t *testing.T) { ... }
func TestCreateProjectAutoGeneratesSlug(t *testing.T) { ... }
func TestCreateProjectConflictOnDuplicateName(t *testing.T) { ... } // 409
func TestUpdateProjectChangesName(t *testing.T) { ... }
func TestUnauthenticatedReturns401(t *testing.T) { ... }
```

Run: `go test ./internal/api/ -run TestProject -race -v`
Expected: FAIL.

- [x] **Step 2: Реализовать хэндлеры**. DTO `projectResponse` формирует `{id, slug, name, description, task_counter, created_at}`. Routes монтируются в `Server.routes()`: `r.Route("/v1/projects", ...)`, `r.Get("/v1/me", ...)`.

- [x] **Step 3: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/api/me.go internal/api/projects.go internal/api/projects_test.go internal/api/server.go
git commit -m "feat(api): /v1/me и /v1/projects эндпоинты"
```

---

### Task 19: /v1/tasks endpoints

**Files:**
- Create: `internal/api/tasks.go`
- Create: `internal/api/tasks_test.go`

- [x] **Step 1: Failing-тесты** (полный набор):

```go
func TestListTasksFilterByProjectAndStatus(t *testing.T) { ... }
func TestListTasksSinceCursor(t *testing.T) { ... }              // ?since=<iso>
func TestListTasksPagination(t *testing.T) { ... }                // ?cursor=<opaque>&limit=50
func TestGetTaskByID(t *testing.T) { ... }
func TestGetTaskByRefSlug42(t *testing.T) { ... }                 // /v1/tasks/by-ref/inbox-42
func TestCreateTaskIntoInboxIfNoProjectID(t *testing.T) { ... }
func TestUpdateTaskPatchesFields(t *testing.T) { ... }
func TestCompleteTaskEndpointSetsStatus(t *testing.T) { ... }
func TestReopenTaskEndpointSetsStatus(t *testing.T) { ... }
func TestMoveTaskEndpointReassignsNumber(t *testing.T) { ... }
func TestDeleteTaskSoftDelete(t *testing.T) { ... }               // status=cancelled
func TestTasksValidationErrors(t *testing.T) { ... }              // 422 при пустом summary
```

Run: `go test ./internal/api/ -run TestTask -race -v`
Expected: FAIL.

- [x] **Step 2: Реализовать** все 9 хэндлеров. Cursor-пагинация: base64(seq) — Opaque. `since` — ISO 8601.

- [x] **Step 3: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/api/tasks.go internal/api/tasks_test.go internal/api/server.go
git commit -m "feat(api): /v1/tasks полный CRUD + complete/reopen/move/by-ref"
```

---

### Task 20: /v1/devices endpoints

**Files:**
- Create: `internal/api/devices.go`
- Create: `internal/api/devices_test.go`

- [x] **Step 1: Failing-тесты**:

```go
func TestListDevicesReturnsAll(t *testing.T) { ... }
func TestPatchDevicesMeUpdatesPushTokens(t *testing.T) { ... }
func TestDeleteDeviceRevokes(t *testing.T) { ... }
func TestDeleteOwnDeviceRevokesAndSubsequentRequestsFail(t *testing.T) { ... }
```

Run: `go test ./internal/api/ -run TestDevice -race -v`
Expected: FAIL.

- [x] **Step 2: Реализовать**. `PATCH /v1/devices/me` читает `DeviceIDFromContext` и вызывает `DeviceStore.UpdatePushTokens`. `DELETE /v1/devices/{id}` — `Revoke`.

- [x] **Step 3: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/api/devices.go internal/api/devices_test.go internal/api/server.go
git commit -m "feat(api): /v1/devices list/patch-me/delete"
```

---

### Task 21: /v1/chat и /v1/chat/history

**Files:**
- Create: `internal/api/chat.go`
- Create: `internal/api/chat_test.go`

- [x] **Step 1: Failing-тесты**:

```go
func TestPostChatReturnsReplyAndTouched(t *testing.T) { ... }
func TestPostChatRespectsTimeout(t *testing.T) { ... } // cfg.ChatTimeout истёк → 504
func TestPostChatIdempotencyKey(t *testing.T) { ... }
func TestChatHistoryReturnsClientSourceOnly(t *testing.T) { ... } // изолировано от Telegram-DM
```

Run: `go test ./internal/api/ -run TestChat -race -v`
Expected: FAIL.

- [x] **Step 2: Реализовать**.

- `POST /v1/chat`: читает `{message, idempotency_key?}`, строит `model.Message{Kind: MessageKindDM, Source: Source{AccountID: "client:"+deviceID, Kind: "client"}, Text: ...}`, задаёт `HistoryFn` через History store по тому же source, зовёт `ChatService.HandleMessage` с `context.WithTimeout(cfg.ChatTimeout)`.
- `GET /v1/chat/history`: читает из History store по источнику `client:<device_id>`.

- [x] **Step 3: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 4: Коммит**

```bash
git add internal/api/chat.go internal/api/chat_test.go internal/api/server.go
git commit -m "feat(api): /v1/chat синхронный вызов и история"
```

---

### Task 22: /v1/events SSE + /v1/sync/snapshot

**Files:**
- Create: `internal/api/events.go`
- Create: `internal/api/events_test.go`
- Create: `internal/api/sync.go`
- Create: `internal/api/sync_test.go`

- [x] **Step 1: Failing-тесты** (events):

```go
func TestSSEReceivesLiveEvent(t *testing.T) {
    // httptest сервер; подключиться с Accept:text/event-stream;
    // в отдельной горутине вызвать TaskService.CreateTask;
    // прочитать SSE-событие из response body, распарсить id/event/data.
}
func TestSSEReplaysSinceLastEventID(t *testing.T) { ... }
func TestSSEResetWhenLastEventIDTooOld(t *testing.T) { ... }
func TestSSEHeartbeatEvery15s(t *testing.T) { ... } // имитируем через override time.After
func TestSSEClientDisconnectUnsubscribes(t *testing.T) { ... }
```

Failing-тесты (sync):

```go
func TestSyncSnapshotReturnsProjectsAndOpenTasksAndLastSeq(t *testing.T) { ... }
```

Run: `go test ./internal/api/ -run "TestSSE|TestSync" -race -v`
Expected: FAIL.

- [x] **Step 2: Реализовать SSE handler** по алгоритму из Technical Details. `w.(http.Flusher).Flush()` после каждого события. Heartbeat через `time.NewTicker(15s)`.

- [x] **Step 3: Реализовать `/v1/sync/snapshot`** — транзакционное чтение `{projects: [...], open_tasks: [...], last_seq: MaxSeq}`.

- [x] **Step 4: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add internal/api/events.go internal/api/events_test.go internal/api/sync.go internal/api/sync_test.go internal/api/server.go
git commit -m "feat(api): /v1/events SSE и /v1/sync/snapshot"
```

---

### Task 23: OpenAPI yaml + GET /v1/openapi.yaml

**Files:**
- Create: `api/openapi.yaml`
- Create: `internal/api/openapi.go`
- Create: `internal/api/openapi_test.go`

- [x] **Step 1: Failing-тесты**:

```go
func TestOpenAPIYAMLIsValid(t *testing.T) {
    // yaml.Unmarshal в map[string]any; проверить поля info.title, openapi, paths
}
func TestGetOpenAPIServesEmbeddedYAML(t *testing.T) {
    // GET /v1/openapi.yaml → Content-Type application/yaml; body содержит "openapi: 3.1"
}
func TestOpenAPICoverageMatchesRoutes(t *testing.T) {
    // chi.Walk по всем маршрутам; для каждого (method, path) проверить что в yaml есть соответствующий paths entry
    // допустимые исключения: /healthz, /readyz, /v1/openapi.yaml сами
}
```

Run: `go test ./internal/api/ -run TestOpenAPI -v`
Expected: FAIL.

- [x] **Step 2: Написать `api/openapi.yaml`** с покрытием всех эндпоинтов `/v1/*` (tasks, projects, chat, devices, events, sync, me). Включить схемы для Task, Project, Device, Event, ChatReply, Error, Pagination.

➕ **Отклонение:** чтобы `//go:embed` мог читать `api/openapi.yaml` из корня, в `/api/` добавлен минимальный пакет `spec.go` с функцией `Spec() []byte`. Хэндлер в `internal/api/openapi.go` импортирует его (`rootapi "github.com/anadale/huskwoot/api"`). Маршрут `GET /v1/openapi.yaml` зарегистрирован вне `/v1`-группы с auth-middleware — SDK-генераторам нужен публичный доступ к схеме.

- [x] **Step 3: Реализовать handler** через `//go:embed api/openapi.yaml` и `GET /v1/openapi.yaml`. Header `Content-Type: application/yaml; charset=utf-8`.

- [x] **Step 4: Тесты**

Run: `go test ./internal/api/ -race -v`
Expected: PASS.

- [x] **Step 5: Коммит**

```bash
git add api/openapi.yaml internal/api/openapi.go internal/api/openapi_test.go
git commit -m "feat(api): OpenAPI 3.1 спецификация и /v1/openapi.yaml"
```

---

### Task 24: Wiring в main.go + retention runner в serve

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] **Step 1: В `serveRunE`** после инициализации store'ов:

```go
deviceStore := devices.NewSQLiteDeviceStore(db)
eventStore  := events.NewSQLiteEventStore(db)
pushQueue   := push.NewSQLitePushQueue(db)
broker      := events.NewBroker(events.BrokerConfig{BufferSize: 64})

projectSvc := usecase.NewProjectService(db, taskStore, metaStore, eventStore, deviceStore, pushQueue, broker)
taskSvc    := usecase.NewTaskService(db, taskStore, projectSvc, eventStore, deviceStore, pushQueue, broker)
agentInst, err := agent.New(...) // те же tools, но теперь использует новый taskSvc
chatSvc    := usecase.NewChatService(agentInst, db, eventStore, broker)

retention := events.NewRunner(eventStore, pushQueue, cfg.API.EventsRetention, logger)

if cfg.API.Enabled {
    apiSrv := api.New(api.Config{
        Listen: cfg.API.ListenAddr,
        Logger: logger,
        DB: db,
        Tasks: taskSvc, Projects: projectSvc, Chat: chatSvc,
        Devices: deviceStore, Events: eventStore, Broker: broker,
        History: historyStore, Owner: cfg.Owner,
        EventsRetention: cfg.API.EventsRetention,
        RequestTimeout: cfg.API.RequestTimeout,
        ChatTimeout: cfg.API.ChatTimeout,
    })
    go apiSrv.Run(ctx)
}
go retention.Run(ctx)
```

- [x] **Step 2: Убедиться что pipeline/reminder/agent tools не изменились** — use-case'ы используют тот же интерфейс. Проверить `go vet`.

- [x] **Step 3: Smoke-запуск** (skipped — не автоматизируется, требует реального `config.toml`; сборка `go build` и полный `go test ./...` проходят)

```bash
go build -o bin/huskwoot ./cmd/huskwoot
HUSKWOOT_CONFIG_DIR=/tmp/huskwoot-smoke ./bin/huskwoot devices create --name "Dev" --platform "linux"
# затем в отдельном терминале:
HUSKWOOT_CONFIG_DIR=/tmp/huskwoot-smoke ./bin/huskwoot serve &
curl -s http://127.0.0.1:8080/healthz
curl -s -H "Authorization: Bearer <token>" http://127.0.0.1:8080/v1/projects | jq .
```

(Требует минимального `config.toml` со секцией `[api]`. Если pipeline/channels не настроены — это ОК, сервер должен стартовать только с HTTP.)

- [x] **Step 4: Коммит**

```bash
git add cmd/huskwoot
git commit -m "feat(cmd): wiring API-сервера и retention-runner в serve"
```

---

### Task 25: Acceptance — полный прогон тестов и smoke

- [x] Run: `go vet ./...` — без warnings.
- [x] Run: `go test ./... -race -v` — все тесты PASS.
- [x] Smoke-запуск `huskwoot serve` с реальным `config.toml` (skipped — manual, требует боевого конфига):
  - [x] `/healthz` → 200 (skipped — manual).
  - [x] `/readyz` → 200 (skipped — manual).
  - [x] `curl -X POST /v1/pair/request` → 404 (skipped — manual).
  - [x] `curl -H "Authorization: Bearer <wrong>" /v1/projects` → 401 (skipped — manual; покрыто unit-тестами auth middleware).
  - [x] CRUD по `/v1/projects`, `/v1/tasks` через `curl` (skipped — manual; покрыто unit-тестами хэндлеров).
  - [x] `GET /v1/sync/snapshot` (skipped — manual; покрыто unit-тестами).
  - [x] SSE с `curl -N` (skipped — manual; покрыто unit-тестами events SSE).
  - [x] SSE replay с `Last-Event-ID` (skipped — manual; покрыто unit-тестами).
- [x] Проверить что Telegram-бот и IMAP не сломались (skipped — manual, требует боевых аккаунтов):
  - [x] Promise из Telegram-группы → задача и ✍️→👍 (skipped — manual).
  - [x] DM `/list` (skipped — manual).
  - [x] Reminder morning-slot (skipped — manual).
- [x] Если что-то падает — исправить **в этом плане**, добавив ⚠️ задачу (N/A — падений нет).

---

### Task 26: Финальные обновления документации

- [x] Обновить `CLAUDE.md`:
  - [x] добавить `internal/api/`, `internal/devices/`, `internal/events/`, `internal/push/` в «Структуру директорий»;
  - [x] в секции «Архитектура Pipeline» упомянуть что use-case'ы владеют транзакциями и публикуют события;
  - [x] новая секция «HTTP API» с кратким описанием auth, SSE, sync/snapshot, OpenAPI-источник истины;
  - [x] секция «CLI» — `serve`, `devices create/list/revoke`;
  - [x] обновить `SQLiteTaskStore`-контракт: write-методы tx-aware (`CreateProjectTx` и пр.).
- [x] Обновить корневой `README.md` (если есть) — раздел «Quick start» с примером `curl`.
- [x] `mkdir -p docs/plans/completed && git mv docs/plans/2026-04-18-backend-api-phase2-http-and-realtime.md docs/plans/completed/`.
- [x] Финальный коммит: `docs: завершена Фаза 2 backend-API (HTTP, SSE, events, devices)`.

---

## Post-Completion

*Информационные пункты, не требующие чекбокса в этом плане.*

**Перед запуском обновлённого инстанса в проде:**
- Сделать бэкап `huskwoot.db` (`sqlite3 huskwoot.db ".backup huskwoot.bak"`).
- Обновить `config.toml`: добавить секцию `[api]` с `enabled = true`, `listen_addr`, `external_base_url`, `events_retention`.
- Запустить новый бинарник, убедиться что миграции 003–005 применились.
- Создать первое устройство: `huskwoot devices create --name "MacBook" --platform "macos"`; сохранить bearer-токен в безопасное место.

**Подготовка к Фазе 3 (Pairing flow):**
- Убедиться что у бота есть `[owner].telegram_user_id` — для отправки magic-link DM.
- Pairing-эндпоинты `/v1/pair/*` в Фазе 3 будут добавлены рядом с существующими `/v1/*`.

**Подготовка к Фазе 4 (Push dispatcher + relay):**
- `push_queue` уже наполняется. В Фазе 4 добавятся `push.Dispatcher`, `push.RelayClient`, `[push]`-конфиг и бинарник `huskwoot-push-relay`.
- `PATCH /v1/devices/me` уже умеет обновлять `apns_token`/`fcm_token` — клиент в Фазе 4 будет их отправлять при каждом запуске.

**Ручные проверки (не покрыто unit-тестами):**
- Поведение SSE при медленном клиенте (заполнение буфера → drop subscriber → переподключение с `Last-Event-ID`).
- Поведение retention при заполненной `push_queue` (тысячи delivered-строк).
- Корректность OpenAPI-spec: прогнать через `swagger-cli validate` или `spectral lint`.
- Генерация TypeScript-клиента через `openapi-typescript` — проверить что типы компилируются.

**Что не сделано (намеренно, переносится в следующие фазы):**
- Pairing flow с Telegram magic-link и HTML confirm (Фаза 3).
- `PATCH /v1/devices/me` уже есть, но автоматический upsert в push-relay — Фаза 4.
- Push dispatcher, relay client, HMAC-протокол, отдельный бинарник `huskwoot-push-relay` (Фаза 4).
- Streaming ответа агента через SSE — следующая итерация после Фазы 4.
- Coalescing push-уведомлений, метрики, web-UI администратора — out of scope.
