# Huskwoot — бэкенд-API, pairing и push-уведомления

**Статус:** design, утверждён автором после брейншторминг-сессии 18.04.2026
**Подпроект №1 из платформенного roadmap'а** (core backend + API).
Следующие подпроекты: desktop-клиент, iOS-клиент, Android-клиент, продуктовая обвязка.

## 1. Overview & Scope

### Цель

Превратить текущий монолитный Huskwoot в бэкенд для мультиплатформенного личного ассистента,
сохранив все существующие фичи и сделав это минимально инвазивно.

### Что входит в этот спек

- HTTP-слой поверх существующих `TaskStore`/`Agent`, экспонирующий REST/JSON API с
  OpenAPI 3.1 описанием (источник истины для кодогенерации клиентов).
- Слой use-cases (`internal/usecase/`): `TaskService`, `ProjectService`, `ChatService`,
  `PairingService`. Все бизнес-действия (включая публикацию событий) живут в одном месте.
- Модель устройств (devices) с пэйрингом через Telegram magic-link.
- SSE-эндпоинт для real-time обновлений + replay по монотонному `seq`.
- Отдельный тонкий бинарник `huskwoot-push-relay`, держащий ключи APNs/FCM и
  проксирующий push-запросы от инстансов пользователей.
- Очередь исходящих push-уведомлений в SQLite с retry-семантикой.
- Переход идентификаторов задач и проектов на UUID + per-project монотонный номер
  (`<slug>#<number>` для отображения).
- Перенос задач между проектами как отдельный сценарий.
- Базовая операционная готовность: reverse proxy Caddy с автоматическим Let's Encrypt,
  обновлённый docker-compose, healthchecks.

### Что не входит

- Multi-tenancy. Один инстанс = один владелец. «Делюсь с друзьями» означает «друг
  ставит свою копию».
- Разделение процессов на `core` и `agent`. Остаётся один бинарник `huskwoot` с
  cobra-subcommand'ами.
- Мобильные/десктоп-клиенты (это подпроекты №2–4). В рамках текущего спека клиенты —
  абстрактные HTTP-потребители.
- Offline-first с CRDT/operational transformation. Клиенты используют read-through
  cache + write-through, это деталь их реализации, не серверная.
- Web-UI для администрирования (кроме страницы подтверждения pairing).
- Метрики (Prometheus/OpenTelemetry) — откладываются в следующую итерацию.
- Автоматизация provisioning'а релея (админ-API). Пока ручной список `instance_id +
  secret_hash` в конфиге релея с SIGHUP-reload.

### Архитектурная позиция

**До:** `huskwoot` — один процесс: каналы (Telegram/IMAP) → pipeline → TaskStore (SQLite);
агент работает только через Telegram DM.

**После:** `huskwoot` — тот же процесс плюс:

- HTTP-сервер на `:8080` (за reverse proxy) с REST API + SSE;
- очередь outbound push'ей → исходящий HTTPS на `huskwoot-push-relay`;
- слой use-cases между API/pipeline/agent-tools и store'ами.

`huskwoot-push-relay` — новый отдельный бинарник в том же репозитории, публично хостится
(условно `push.huskwoot.app`), хранит только маппинг `(instance_id, device_id) → APNs/FCM
token` и проксирует push'и.

Клиенты подключаются к `https://<user-instance>` по REST+SSE, получают push-ы через
APNs/FCM после того, как инстанс отправил их в релей.

### Принципы

- **YAGNI по всем решениям:** split не делаем, CRDT не делаем, метрик сразу не делаем,
  web-UI для админа не делаем.
- **Минимум новых зависимостей:** `github.com/go-chi/chi/v5` для роутинга,
  `github.com/pressly/goose/v3` для миграций, APNs/FCM клиенты в релее.
- **Обратная совместимость:** сегодняшний пользователь без клиентов продолжает пользоваться
  Telegram-ботом как раньше. `[api].enabled = false` оставляет инстанс в текущем режиме.

---

## 2. Архитектура и новые компоненты

### Новые пакеты в `internal/`

```
internal/
├── api/                    # HTTP-слой (chi-роутер, middleware, хэндлеры)
│   ├── server.go
│   ├── auth.go            # device-token middleware
│   ├── tasks.go
│   ├── projects.go
│   ├── chat.go
│   ├── events.go          # SSE endpoint
│   ├── pairing.go
│   ├── devices.go
│   └── sync.go            # /v1/sync/snapshot
│
├── usecase/                # Application-level сценарии
│   ├── tasks.go           # Create/Update/Complete/List/Move
│   ├── projects.go        # Create/List/FindByName/ResolveForChannel/EnsureChannelProject
│   ├── chat.go            # HandleMessage (обёртка над Agent.Handle)
│   └── pairing.go         # Request/Confirm/Revoke/ListDevices
│
├── events/                 # SSE-брокер + event store
│   ├── broker.go
│   └── store.go           # чтение/запись events + retention cleanup
│
├── devices/                # Хранилище устройств
│   └── store.go
│
├── push/                   # Исходящая очередь + dispatcher + relay client
│   ├── queue.go
│   ├── dispatcher.go
│   ├── relayclient.go
│   └── templates.go       # шаблоны заголовков/тел push-уведомлений
│
└── pushproto/              # Типы HTTP-протокола инстанс ⇄ релей
    └── types.go
```

### Пакеты релея

```
cmd/huskwoot-push-relay/
└── main.go                 # отдельный бинарник, отдельный Dockerfile

internal/relay/
├── server.go              # chi-роутер релея
├── registrar.go           # upsert/delete регистраций
├── pusher.go              # обработка POST /v1/push → APNs/FCM
├── apns.go                # github.com/sideshow/apns2
├── fcm.go                 # firebase.google.com/go/v4
└── store.go               # SQLite-хранилище instances + registrations
```

### Новые интерфейсы в `model/` (файл `model/service.go`)

```go
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

type PairingService interface {
    RequestPairing(ctx context.Context, req PairingRequest) (*PendingPairing, error)
    ConfirmPairing(ctx context.Context, pairID string) (*Device, error)
    PollStatus(ctx context.Context, pairID, clientNonce string) (*PairingResult, error)
    RevokeDevice(ctx context.Context, deviceID string) error
    ListDevices(ctx context.Context) ([]Device, error)
    UpdateDevicePushTokens(ctx context.Context, deviceID string, apnsToken, fcmToken *string) error
}

// Хранит события и отвечает за replay по seq.
type EventStore interface {
    Insert(ctx context.Context, tx *sql.Tx, ev Event) (seq int64, err error)
    SinceSeq(ctx context.Context, afterSeq int64, limit int) ([]Event, error)
    MaxSeq(ctx context.Context) (int64, error)
    DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// Broker делает только in-memory fan-out. Никакой работы с БД.
// Вызывается use-case'ом ПОСЛЕ tx.Commit(), с событием, которое уже имеет seq.
type Broker interface {
    Notify(ev Event)
    Subscribe(deviceID string) (<-chan Event, func())
    IsActive(deviceID string) bool
}

type PushQueue interface {
    Enqueue(ctx context.Context, tx *sql.Tx, deviceID string, eventSeq int64) error
    NextBatch(ctx context.Context, limit int) ([]PushJob, error)
    MarkDelivered(ctx context.Context, id int64) error
    MarkFailed(ctx context.Context, id int64, err error, nextAttempt time.Time) error
    Drop(ctx context.Context, id int64, reason string) error
}
```

Реализации — в соответствующих пакетах `internal/usecase/`, `internal/events/`,
`internal/push/`.

### Ключевой принцип: транзакционная запись + post-commit fan-out

Pipeline, HTTP-handlers, инструменты агента **не** работают с `EventStore`, `Broker` или
`PushQueue` напрямую. Они зовут use-cases. Use-case выполняет атомарную запись данных и
событий в одной транзакции, а in-memory fan-out делает после коммита:

```go
// Пример: TaskService.CreateTask
func (s *taskService) CreateTask(ctx, req) (*Task, error) {
    tx, _ := s.db.BeginTx(ctx, nil)
    defer tx.Rollback()

    task := newTask(req)
    if err := s.taskStore.Create(ctx, tx, task); err != nil { return nil, err }

    ev := Event{Kind: "task_created", EntityID: task.ID, Payload: marshal(task)}
    seq, err := s.eventStore.Insert(ctx, tx, ev)
    if err != nil { return nil, err }
    ev.Seq = seq

    inactive, _ := s.deviceStore.ListInactiveForBroker(ctx, tx, s.broker)
    for _, d := range inactive {
        s.pushQueue.Enqueue(ctx, tx, d.ID, seq)
    }

    if err := tx.Commit(); err != nil { return nil, err }

    s.broker.Notify(ev)   // in-memory SSE fan-out, уже после коммита
    return task, nil
}
```

Эта схема гарантирует:

- **Атомарность:** либо и задача, и событие, и push-jobs записаны, либо ничего.
- **Чистую ответственность:** persistence — у store'ов, in-memory observability — у
  broker'а, доставка push'ей — у dispatcher'а.
- **Невозможность гонок между SSE и store:** fan-out идёт только после COMMIT, так
  что подписчик, получающий событие, всегда может прочитать данные из store'а.

`Broker` никогда не пишет в БД. `EventStore` никогда не держит подписчиков.

### Call-sites, которые меняются

| Место | Было | Стало |
|---|---|---|
| `Pipeline.processPromise` | `taskStore.CreateTask(...)` × N | `taskService.CreateTasks(ctx, req)` |
| `Pipeline.lookupProjectID` | inline | `projectService.ResolveProjectForChannel(...)` |
| `SetProjectHandler` | inline | `projectService.EnsureChannelProject(...)` |
| Агент, tool `create_task` | `taskStore.CreateTask(...)` | `taskService.CreateTask(...)` |
| Агент, tool `complete_task` | `taskStore.UpdateTask(...)` | `taskService.CompleteTask(...)` |
| Агент, tool `list_tasks` | `taskStore.ListTasks(...)` | `taskService.ListTasks(...)` |
| Агент, tool `create_project` | `taskStore.CreateProject(...)` | `projectService.CreateProject(...)` |
| Агент, tool `list_projects` | `taskStore.ListProjects(...)` | `projectService.ListProjects(...)` |
| Агент, `Config.ListProjects` | `taskStore.ListProjects` | `projectService.ListProjects` |
| Новый агентский tool `move_task` | — | `taskService.MoveTask(...)` |
| DM-handler | `agent.Handle(...)` | `chatService.HandleMessage(...)` |
| HTTP `POST /v1/tasks` | — | `taskService.CreateTask(...)` |
| HTTP `POST /v1/chat` | — | `chatService.HandleMessage(...)` |

### Интеграция в `main.go`

`cmd/huskwoot/main.go` превращается в Cobra-root с единственной на первом этапе
подкомандой `serve`. Логика wiring'а:

```
huskwoot serve
├── load config
├── OpenDB + goose.Up
├── stores: TaskStore, MetaStore, History, DeviceStore, PushQueue, EventStore
├── broker = events.NewBroker(db, pushQueue, deviceStore)
├── services: TaskService, ProjectService, ChatService, PairingService (использует TelegramBot)
├── agent = agent.New(...) c инструментами, которые используют services
├── pipeline = pipeline.New(...) c services, broker
├── channels: TelegramChannel[s], IMAPChannel[s]
├── reminderScheduler = reminder.New(..., taskService, ...)
├── pushDispatcher = push.NewDispatcher(pushQueue, relayClient)
├── apiServer = api.New(api.Config{services, broker, ...})
├── goroutines: channels, pipeline, reminderScheduler, pushDispatcher, apiServer
└── wait for SIGINT → cancel ctx → graceful shutdown
```

### Потоки данных

**«Задача из Telegram-группы → push на телефон»:**

```
TelegramChannel.Watch → msg
  → Pipeline.processPromise
     → Classifier (Promise)
     → Extractor → []Task
     → TaskService.CreateTasks (транзакция: INSERT tasks + INSERT events + INSERT push_queue)
       → COMMIT → Broker.Notify → SSE fan-out активным устройствам (push enqueue для остальных уже в tx)
  → pushDispatcher → HTTPS → huskwoot-push-relay → APNs/FCM → устройство
```

**«Клиент → агент → задача»:**

```
Client POST /v1/chat
  → api/chat.go
  → ChatService.HandleMessage(Message{Kind: MessageKindDM, Source: {AccountID: "client:<device_id>"}})
    → Agent.Handle (tool calling цикл)
      → tool create_task → TaskService.CreateTask → Broker.Notify (после COMMIT)
  → HTTP response: { reply, tasks_touched, projects_touched }
```

---

## 3. Модель данных

### ID-модель

- **`projects.id`** и **`tasks.id`** — UUID v4.
- **`projects.slug`** — lowercase-kebab, авто из `name` (транслитерация кириллицы),
  уникален, редактируется через `UpdateProject`.
- **`projects.task_counter`** — монотонный счётчик, не откатывается при переносе.
- **`tasks.number`** — уникален в рамках `(project_id, number)`. Присваивается в
  транзакции при `CreateTask` и при `MoveTask` (переприсваивается для целевого проекта).
- Пользователь-видимая ссылка: `<project_slug>#<number>`, например `inbox#42` или
  `work#7`. Внутренний идентификатор в API и хранилище — UUID.

### Схема инстанса: изменения в существующих таблицах

```sql
ALTER TABLE projects ADD COLUMN slug TEXT;
ALTER TABLE projects ADD COLUMN task_counter INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN number INTEGER;
-- затем Go-миграция заполняет slug и number, добавляет UNIQUE и NOT NULL constraints,
-- при необходимости мигрирует числовые ID в UUID (если текущее состояние схемы это имеет)

CREATE UNIQUE INDEX uniq_projects_slug ON projects(slug);
CREATE UNIQUE INDEX uniq_tasks_project_number ON tasks(project_id, number);
CREATE INDEX idx_tasks_updated_at ON tasks(updated_at);
```

Миграция `007_uuid_and_numbers` — смешанная (SQL + Go), обрабатывается goose'ом.

### Новые таблицы инстанса

```sql
CREATE TABLE devices (
    id            TEXT PRIMARY KEY,           -- UUID
    name          TEXT NOT NULL,              -- "iPhone 17", "MacBook Pro"
    platform      TEXT NOT NULL,              -- 'ios'|'android'|'macos'|'windows'|'linux'
    token_hash    TEXT NOT NULL UNIQUE,       -- SHA256(bearer_token)
    apns_token    TEXT,
    fcm_token     TEXT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  DATETIME,
    revoked_at    DATETIME
);

CREATE INDEX idx_devices_token_hash
    ON devices(token_hash) WHERE revoked_at IS NULL;

CREATE TABLE pairing_requests (
    id                 TEXT PRIMARY KEY,       -- UUID, одновременно токен в magic-link
    device_name        TEXT NOT NULL,
    platform           TEXT NOT NULL,
    apns_token         TEXT,
    fcm_token          TEXT,
    client_nonce_hash  TEXT NOT NULL,          -- SHA256(client_nonce)
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         DATETIME NOT NULL,
    confirmed_at       DATETIME,
    issued_device_id   TEXT REFERENCES devices(id),
    csrf_token_hash    TEXT                    -- для HTML-confirm
);

CREATE INDEX idx_pairing_requests_expires ON pairing_requests(expires_at);

CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL,                  -- task_created|task_updated|task_completed|
                                                -- task_moved|task_reopened|project_created|
                                                -- project_updated|chat_reply|reminder_summary
    entity_id   TEXT NOT NULL,
    payload     TEXT NOT NULL,                  -- JSON snapshot сущности
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_events_created_at ON events(created_at);

CREATE TABLE push_queue (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id         TEXT NOT NULL REFERENCES devices(id),
    event_seq         INTEGER NOT NULL REFERENCES events(seq),
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    next_attempt_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at      DATETIME,
    dropped_at        DATETIME,
    dropped_reason    TEXT
);

CREATE INDEX idx_push_queue_pending
    ON push_queue(next_attempt_at)
    WHERE delivered_at IS NULL AND dropped_at IS NULL;
```

### Retention и cleanup

- **events:** удаляются после 7 дней (`events_retention = "168h"`). Фоновая
  горутина раз в час. При попытке SSE-replay от `Last-Event-ID` старее порога —
  сервер шлёт событие `reset` и закрывает коннект, клиент делает cold re-sync через
  `/v1/sync/snapshot`.
- **push_queue:** строки со `delivered_at IS NOT NULL` либо `dropped_at IS NOT NULL`
  удаляются вместе с их `events`.
- **pairing_requests:** удаляются через 1 час после `expires_at` (на случай аудита
  неуспешных попыток).

### Схема push-relay

```sql
CREATE TABLE instances (
    id             TEXT PRIMARY KEY,
    owner_contact  TEXT NOT NULL,
    secret_hash    TEXT NOT NULL,              -- HMAC-ключ, хранится хешем
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    disabled_at    DATETIME
);

CREATE TABLE registrations (
    instance_id    TEXT NOT NULL REFERENCES instances(id),
    device_id      TEXT NOT NULL,
    apns_token     TEXT,
    fcm_token      TEXT,
    platform       TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at   DATETIME,
    PRIMARY KEY (instance_id, device_id)
);
```

Релей не хранит задачи, проекты, пользовательские данные. Только routing-таблица.

### Миграции

Goose как библиотека (`github.com/pressly/goose/v3`), SQL-файлы через `//go:embed`.

```
internal/storage/migrations/
├── 001_initial.sql         # текущая схема
├── 002_devices.sql
├── 003_pairing_requests.sql
├── 004_events.sql
├── 005_push_queue.sql
├── 006_tasks_updated_at_index.sql
└── 007_uuid_and_numbers.go # смешанная (SQL + Go-код для бэкфилла)
```

Отдельный `relay/migrations/` для релея:

```
├── 001_instances.sql
└── 002_registrations.sql
```

Только Up-направление. Down не пишем.

---

## 4. API surface

### Общие конвенции

- Версионирование через `/v1/...`; breaking-изменения → `/v2/...`.
- JSON (`application/json`). Даты — ISO 8601 UTC.
- Идентификаторы — UUID v4 (кроме `events.seq`).
- `Authorization: Bearer <device_token>` — обязателен везде, кроме `/v1/pair/*` и
  `/pair/confirm/*`.
- Опциональный заголовок `Idempotency-Key` на всех `POST` с созданием.
- Ошибки: `{ "error": { "code": "...", "message": "..." } }`, HTTP-коды 400/401/403/404/409/422/500.
- Пагинация: курсорная `?cursor=<opaque>&limit=50`, плюс `?since=<iso8601>` для tasks.

### Эндпоинты — задачи

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/v1/tasks` | Список с фильтрами (`project_id`, `status`, `since`, `cursor`, `limit`) |
| `GET` | `/v1/tasks/{id}` | Одна задача по UUID |
| `GET` | `/v1/tasks/by-ref/{slug}-{number}` | Lookup по human-friendly ссылке |
| `POST` | `/v1/tasks` | Создать (не через агента) |
| `PATCH` | `/v1/tasks/{id}` | Обновить поля |
| `POST` | `/v1/tasks/{id}/complete` | Пометить выполненной |
| `POST` | `/v1/tasks/{id}/reopen` | Вернуть в open |
| `POST` | `/v1/tasks/{id}/move` | Перенести в другой проект (переприсвоение `number`) |
| `DELETE` | `/v1/tasks/{id}` | Soft-delete (status=cancelled) |

Ответ задачи:

```json
{
  "id": "9d4a1b...",
  "number": 42,
  "project_id": "uuid",
  "project_slug": "work",
  "display_id": "work#42",
  "summary": "...",
  "details": "...",
  "topic": "...",
  "status": "open",
  "deadline": "2026-04-19T12:00:00Z",
  "created_at": "...",
  "updated_at": "...",
  "source": {
    "kind": "telegram_group",
    "account_id": "main",
    "subject": "",
    "reference": "..."
  }
}
```

### Эндпоинты — проекты

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/v1/projects` | Список |
| `POST` | `/v1/projects` | Создать (slug авто, можно переопределить) |
| `GET` | `/v1/projects/{id}` | Один проект |
| `PATCH` | `/v1/projects/{id}` | Обновить name/description/slug |

### Эндпоинты — чат с агентом

| Метод | Путь | Назначение |
|---|---|---|
| `POST` | `/v1/chat` | Синхронный вызов агента |
| `GET` | `/v1/chat/history` | История DM-диалога клиента с агентом |

Запрос/ответ:

```json
POST /v1/chat
{ "message": "Запиши: позвонить Свете завтра к обеду",
  "idempotency_key": "uuid" }

→ 200 OK (таймаут 30 секунд)
{
  "reply": "Создал work#42: «Позвонить Свете», срок завтра 12:00.",
  "tasks_touched": ["uuid"],
  "projects_touched": []
}
```

Клиентский чат изолируется от Telegram-DM и email-истории:
`Source.AccountID = "client:<device_id>"`. `HistoryFn` вытягивает историю по этому же
`source`.

Streaming ответа через SSE — в следующую итерацию; первая версия синхронна.

### Эндпоинты — устройства

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/v1/devices` | Список моих устройств |
| `PATCH` | `/v1/devices/me` | Обновить `apns_token`/`fcm_token` текущего устройства |
| `DELETE` | `/v1/devices/{id}` | Revoke |

### Эндпоинты — pairing (без авторизации)

| Метод | Путь | Назначение |
|---|---|---|
| `POST` | `/v1/pair/request` | Начать пэйринг, вернуть `pair_id` |
| `GET` | `/v1/pair/status/{id}?nonce=...` | Long-poll (60s), возвращает токен при `confirmed` |
| `GET` | `/pair/confirm/{id}` | HTML-страница для владельца |
| `POST` | `/pair/confirm/{id}` | Подтверждение из формы (с CSRF) |

### Эндпоинты — события и cold sync

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/v1/events` | SSE-стрим с `Last-Event-ID` replay |
| `GET` | `/v1/sync/snapshot` | Полный снепшот (projects + open tasks + `last_seq`) |

Формат SSE-события:

```
id: 42
event: task_created
data: {"task": {...}, "origin": "telegram_group"}
```

Kinds событий: `task_created`, `task_updated`, `task_completed`, `task_reopened`,
`task_moved`, `project_created`, `project_updated`, `chat_reply`,
`reminder_summary`, `reset`.

### Служебные

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/v1/me` | Владелец, версия, включённые фичи |
| `GET` | `/v1/openapi.yaml` | OpenAPI спецификация |
| `GET` | `/healthz` | Liveness |
| `GET` | `/readyz` | Readiness (DB + broker + dispatcher) |

### OpenAPI

`api/openapi.yaml` в репозитории — источник истины. Раздаётся через эндпоинт.
Клиентский код генерируется (Swift через `swift-openapi-generator`, TypeScript через
`openapi-typescript`). Серверный код **не** генерируется — хэндлеры пишутся вручную.

---

## 5. Auth & Pairing flow

### Токены устройств

- 32 байта из `crypto/rand`, base64url → bearer token.
- В БД хранится только `SHA256(token)` в `devices.token_hash`.
- Клиент хранит открытый токен в защищённом хранилище платформы (Keychain/Keystore/…).
- Middleware `api/auth.go` на каждом запросе: `Authorization` → SHA256 → lookup в
  `devices WHERE revoked_at IS NULL` → `request.Context` получает `device_id` и
  `owner`. Обновляет `devices.last_seen_at`.

### Pairing flow

```
Client (new device)                 Instance                         Owner's Telegram
-------------------                 --------                         ----------------
POST /v1/pair/request
  { device_name, platform,
    client_nonce }
                                    insert pairing_requests
                                    (expires = now + 5m)
                                    store SHA256(client_nonce)
                                    send DM via bot ─ ─ ─ ─ ─ ─ ─>  "Подключить 'iPhone 17'?
                                                                     https://.../pair/confirm/<id>"
← 202 Accepted { pair_id, poll_url }

GET /v1/pair/status/{id}?nonce=...
(long-poll, 60s)
                                                                     [User taps link]
                                              GET /pair/confirm/<id> ← HTML с CSRF
                                              POST /pair/confirm/<id>

                                    set confirmed_at, csrf validated
                                    create device row with token_hash
                                    issued_device_id = device.id

← 200 OK { device_id, bearer_token, capabilities }
```

### Защита

- **`client_nonce` против кражи `pair_id`:** `GET /v1/pair/status` валидирует
  `SHA256(nonce) == stored`. Если несовпадение — 403 и отмена pairing.
- **CSRF на HTML-confirm:** `crypto/rand` токен в cookie `__Host-csrf` (SameSite=Strict)
  + скрытое поле формы. `POST` проверяет совпадение.
- **Rate limit `POST /v1/pair/request`:** 5 в час на IP через
  `golang.org/x/time/rate` in-memory.
- **Длительность magic-link:** 5 минут.
- **Кнопка «Это не я» в DM:** отложено в следующую итерацию.

### Lifecycle токенов

| Событие | Действие |
|---|---|
| Pairing succeeded | Клиент сохраняет токен, сервер хранит только хеш |
| Token lost | Клиент делает новый pairing |
| Device lost | С другого устройства: `DELETE /v1/devices/{id}` |
| DB leak | Токены не раскрываются (только хеши) |
| Token rotation | Не делаем. Lifetime = infinity |

### Обновление push-токенов

APNs/FCM могут ротировать token на клиенте. Клиент при каждом запуске (или на
событии обновления от OS):

```
PATCH /v1/devices/me
{ "apns_token": "..." }
```

Инстанс обновляет `devices`, параллельно делает upsert в push-relay.

### Резервный путь первичного pairing (без Telegram)

Не реализуется в этой итерации, но зарезервирован:

```
huskwoot generate-pair-token --device-name "..."
```

CLI-команда в том же бинарнике, печатает 6-значный одноразовый код, живущий 10 минут.
Клиент вводит URL + код, инстанс выдаёт токен. Добавится, если появится реальный
пользователь без Telegram.

---

## 6. Real-time & Push

### Decision matrix

| Сценарий клиента | Транспорт |
|---|---|
| Foreground (SSE открыт) | SSE |
| Background/closed | APNs/FCM через релей |
| Оба | Только SSE, push не дублируется |

Решение «SSE или push» принимается use-case'ом в момент записи события: для каждого
устройства спрашивается `broker.IsActive(device_id)`. Если активен — push-job не
создаётся (клиент получит событие через SSE сразу после COMMIT). Иначе — `push_queue`
строка ставится в ту же транзакцию.

### SSE-брокер

`events.Broker` — центральная точка in-memory fan-out'а. Use-case (см. секция 2)
сам отвечает за транзакционную запись events + push_queue; broker'у остаётся
доставить уже закоммиченное событие подписчикам:

1. Use-case в одной транзакции делает `INSERT INTO events RETURNING seq`,
   а затем для каждого устройства без активного SSE — `INSERT INTO push_queue`.
   `broker.IsActive(device_id)` отвечает на вопрос «есть ли активный SSE»
   из in-memory карты подписчиков.
2. `tx.Commit()`.
3. Use-case вызывает `broker.Notify(event)`.
4. Broker итерирует по своим подписчикам и шлёт в соответствующие каналы.
   Best-effort: если канал подписчика переполнен, закрываем его, клиент
   переподключится с `Last-Event-ID` и догонит через replay.

### SSE handler

```
GET /v1/events
Accept: text/event-stream
Last-Event-ID: <seq|empty>
Authorization: Bearer <device_token>
```

1. Auth → `device_id`.
2. Если `Last-Event-ID` задан — replay: `SELECT * FROM events WHERE seq > ? ORDER BY seq`.
3. После replay — `broker.Subscribe(device_id)` и стрим live-событий.
4. Heartbeat `:keepalive\n\n` каждые 15 секунд.
5. Client close → cancel subscriber, удалить из брокера.

### Replay / reset

- Если `Last-Event-ID` указывает на `seq`, которого уже нет (retention отработал) —
  сервер шлёт одно событие `{kind: "reset", seq: <current_max>}` и закрывает
  коннект.
- Клиент по `reset` зовёт `GET /v1/sync/snapshot`, заменяет локальную БД, запоминает
  новый `last_seq`, переоткрывает `/v1/events`.

### Протокол инстанс ⇄ релей

Заголовки HMAC-подписи на каждом запросе:

```
X-Huskwoot-Instance: <instance_id>
X-Huskwoot-Timestamp: <unix-seconds>
X-Huskwoot-Signature: hex(HMAC-SHA256(instance_secret, method|path|body|timestamp))
```

Релей:
1. Проверяет timestamp (±5 минут).
2. Lookup `instances` по `instance_id`, проверяет `disabled_at IS NULL`.
3. Верифицирует HMAC.

#### Эндпоинты релея

| Метод | Путь | Назначение |
|---|---|---|
| `PUT` | `/v1/registrations/{device_id}` | Upsert APNs/FCM токенов устройства |
| `DELETE` | `/v1/registrations/{device_id}` | Удаление при revoke |
| `POST` | `/v1/push` | Отправить push |
| `GET` | `/healthz` | Healthcheck |

#### Тело `POST /v1/push`

```json
{
  "device_id": "uuid",
  "priority": "high",
  "collapse_key": "tasks",
  "notification": {
    "title": "Новая задача",
    "body": "work#42: Позвонить Свете (срок завтра 12:00)",
    "badge": 3
  },
  "data": {
    "kind": "task_created",
    "event_seq": 142,
    "task_id": "uuid",
    "display_id": "work#42"
  }
}
```

Релей формирует платформо-специфичный payload (APNs / FCM) и отсылает его.

#### Ответы релея

| Ответ | Действие инстанса |
|---|---|
| `{status: "sent"}` | `push_queue.delivered_at = now` |
| `{status: "invalid_token"}` | clear `devices.apns_token/fcm_token`; drop job |
| `{status: "upstream_error", retry_after: N}` | backoff |
| `{status: "bad_payload"}` | drop + log (баг) |

### Backoff

5с → 30с → 5мин → 30мин. После 4-й неудачной попытки — `push_queue.dropped_at = now`.

### Decision-table: event → push?

| Event kind | Push | Priority | Template |
|---|---|---|---|
| `task_created` | Да | high | «Новая задача» / `{display_id}: {summary}` |
| `task_updated` (summary/deadline) | Да | normal | «Задача обновлена» / `{display_id}` |
| `task_updated` (прочее) | Нет | — | — |
| `task_completed` | Нет | — | — |
| `task_moved` | Нет | — | — |
| `task_reopened` | Нет | — | — |
| `project_created` | Нет | — | — |
| `reminder_summary` | Да | normal | «Утренняя сводка: N задач» |
| `chat_reply` | Нет (следующая итерация) | — | — |

Шаблоны — `html/template` в `internal/push/templates.go`, только русский.

### Provisioning инстанса в релее

Релей ведёт статический список `instances` в конфиге. Добавление нового друга:

1. Владелец релея генерирует `instance_id` (UUID) и `instance_secret` (32 байта).
2. Сохраняет `(id, owner_contact, sha256(secret))` в `relay config.toml`.
3. `docker kill -s HUP huskwoot-push-relay` — релей перечитывает конфиг.
4. Отправляет `instance_id` и открытый `instance_secret` другу.
5. Друг вписывает в `[push]` своего `config.toml`:

```toml
[push]
relay_url = "https://push.huskwoot.app"
instance_id = "..."
instance_secret = "${HUSKWOOT_PUSH_SECRET}"
```

Если `[push]` отсутствует — push отключены, SSE работает. Это важно для dev и для
друзей до onboarding'а в релей.

### Debounce / coalescing

Первая итерация — без coalescing'а. Если окажется проблемой (5 push'ей подряд при
batch-обработке письма) — реализуется в следующей итерации: буферизация на 3 секунды,
объединение в «Новых задач: N».

---

## 7. Operational concerns

### Конфиг: новые секции в `config.toml`

```toml
[api]
enabled = true
listen_addr = "127.0.0.1:8080"
external_base_url = "https://huskwoot.mydomain.com"
request_timeout = "30s"
chat_timeout = "60s"
events_retention = "168h"
rate_limit_pair_per_hour = 5
cors_allowed_origins = []

[push]
relay_url = "https://push.huskwoot.app"
instance_id = "..."
instance_secret = "${HUSKWOOT_PUSH_SECRET}"
retry_max_attempts = 4

[owner]
telegram_user_id = 123456789
display_name = "Nickon"
```

Если `[api].enabled = false` — HTTP-сервер не поднимается, инстанс работает в
legacy-режиме. Это safety-свитч для первого релиза.

### Reverse proxy и TLS

Инстанс сам TLS не делает. Рекомендуемая конфигурация — Caddy в соседнем
docker-сервисе с автоматическим Let's Encrypt:

```yaml
# docker-compose.yml (дополнение к существующему)
services:
  huskwoot:
    build: .
    restart: unless-stopped
    volumes:
      - ./config.toml:/etc/huskwoot/config.toml:ro
      - huskwoot_data:/var/lib/huskwoot
    environment:
      - HUSKWOOT_CONFIG_DIR=/etc/huskwoot
      - OPENAI_API_KEY=${OPENAI_API_KEY}
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
      - IMAP_PASSWORD=${IMAP_PASSWORD}
      - HUSKWOOT_PUSH_SECRET=${HUSKWOOT_PUSH_SECRET}
    expose: ["8080"]

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config

volumes:
  huskwoot_data:
  caddy_data:
  caddy_config:
```

```
# Caddyfile
huskwoot.mydomain.com {
    reverse_proxy huskwoot:8080
}
```

Альтернативы (Traefik, nginx) — поддерживаем, но рекомендуем Caddy как простейший.

### Docker

**Существующий `Dockerfile`** обновляется — ENTRYPOINT переходит на
`huskwoot serve`.

**Новый `Dockerfile.push-relay`** в корне репозитория — для сборки релея отдельным
образом. Релей деплоится в отдельном `deploy/push-relay/docker-compose.yml` на хосте
автора проекта.

### Observability

- **Логи:** `log/slog` структурированные, формат из конфига (JSON в prod, text в
  dev). Уровень через `[log] level`.
- **Метрики:** отложено в следующую итерацию.
- **Healthchecks:**
  - `GET /healthz` — 200 если Server работает.
  - `GET /readyz` — проверяет `SELECT 1` по БД + alive статус dispatcher'а и broker'а.
- Dockerfile healthcheck на `/healthz`.

### Backup/restore

Один файл SQLite + WAL. Рекомендация в README: cron'овый `sqlite3 .backup`
ежедневно. Litestream — опционально, не включаем в docker-compose по умолчанию.

### Обновление инстанса

```sh
git pull
docker compose pull && docker compose up -d
```

Миграции применяются автоматически в `OpenDB` через goose. Schema versioning — в
таблице `schema_migrations`.

**Правило совместимости:** миграции additive в пределах major-версии. Breaking —
новая major-версия + ручная инструкция.

### Первая миграция моего инстанса

1. `docker compose down`
2. Бэкап БД.
3. `git pull && docker compose up -d`
4. Миграции 002–007 применяются (devices, events, pairing, push_queue, UUID + number).
5. Существующие проекты получают slug, задачи — number. ID конвертируются в UUID,
   если сейчас числовые.
6. API становится доступен на `https://huskwoot.mydomain.com/v1/openapi.yaml`.
7. Telegram-бот и IMAP продолжают работать без изменений.

### Провижнинг релея

Релей конфигурируется через `config.toml`:

```toml
[server]
listen_addr = "127.0.0.1:8080"

[apns]
key_file = "/run/secrets/apns-key.p8"
key_id = "..."
team_id = "..."
bundle_id = "com.huskwoot.client"

[fcm]
service_account_file = "/run/secrets/fcm-sa.json"

[instances]
[[instances.registered]]
id = "nickon"
owner_contact = "@nickon"
secret_hash = "sha256:..."
```

Добавление инстанса — правка TOML + `docker kill -s HUP`. Автоматизация отложена.

---

## 8. Out of scope / future work

Зарезервировано для будущих итераций этого же подпроекта (необязательно до начала
подпроектов клиентов):

- Streaming ответа агента через SSE (`POST /v1/chat` возвращает message_id
  мгновенно, содержание приходит токен-за-токеном как событие).
- Push для `chat_reply`.
- Debounce/coalescing push-уведомлений.
- Prometheus метрики и opentelemetry-трейсинг.
- Кнопка «Это не я → отменить» в Telegram magic-link DM.
- Резервный CLI-based pairing (для друзей без Telegram).
- Web-UI администратора инстанса (список устройств, логи, настройки).
- Админ-API релея (добавление инстансов без ручной правки TOML).

За рамками всего Подпроекта №1:

- Desktop-клиент (Подпроект №2).
- iOS/iPadOS клиент (Подпроект №3).
- Android-клиент / kiosk-таскборд (Подпроект №4).
- Мультитенантность, биллинг, landing, onboarding (Подпроект №5).

---

## Решения, принятые в процессе брейншторминга

Для справки и для будущих изменений:

| Решение | Выбор | Альтернатива (отвергнута) |
|---|---|---|
| Deployment инстанса | VPS, публичный | Home-only / гибрид |
| Разделение на бинарники | Один (`huskwoot`) | Два (`core` + `agent`) |
| Возможности клиента | Task-manager + AI-чат + push | Read-only / чистый task-manager |
| Транспорт обновлений | SSE + APNs/FCM через shared relay автора | Ntfy / отдельные Apple-аккаунты у друзей |
| Модель синхронизации | Read-through cache + write-through | Online-only / CRDT offline-first |
| Pairing | Telegram magic-link; web-UI-QR как фолбэк позже | Shared токен в конфиге |
| API стиль | REST/JSON + OpenAPI, клиенты кодогенерируются | gRPC / GraphQL |
| Роутер | chi | echo / stdlib / gin |
| Разделение логики | Use-case слой (`TaskService` etc.) | Hooks/декораторы над store |
| ID-модель | UUID + per-project `number` + `slug` | Числовые ID / только UUID |
| Миграции | goose как библиотека + embed | Свой минимальный рантайм |
| TLS | Caddy с Let's Encrypt | Traefik / nginx / certbot |
| Push-protocol auth | HMAC-SHA256 на теле + timestamp | mTLS / OAuth client credentials |
