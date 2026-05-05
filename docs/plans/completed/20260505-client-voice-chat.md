# Client voice chat (HTTP)

## Overview

Дать клиентам (macOS, iOS, Android) возможность отправлять агенту голосовые
команды — те же запросы, что в Telegram 1:1 DM, но по HTTP API. Клиент
записывает аудио и шлёт `POST /v1/chat/voice` (multipart). Сервер кладёт job
в существующий `voice_queue`, `transcribe.Worker` асинхронно транскрибирует
(whisper/yandex), и текст уходит в `ChatService.HandleMessage` так же, как
текстовый `/v1/chat`. Ответ агента и распознанный transcript доставляются
клиенту через существующий `/v1/events` (SSE) новыми событиями
`chat_message_created`. Текстовый `/v1/chat` дополнительно начинает эмитить
те же события — ради единообразного multi-device live-стрима.

Бенефит: единый агентский UX между Telegram и нативными клиентами; отсутствие
on-device STT (тонкий клиент); реюз 90% уже существующего голосового
конвейера (`voice_queue`, `transcribe.Worker`, `Transcriber` интерфейс).

## Context (from discovery)

Существующая инфраструктура (видна из CLAUDE.md и кода):

- **`internal/transcribe/worker.go`** — async Worker с backoff 30s→2m→10m→drop@4.
  Сейчас знает про `*TelegramChannel.BuildVoiceMessage`. Будет рефакториться
  под `map[string]VoiceConsumer` + отдельный `VoiceDownloader`.
- **`internal/storage/voice_queue.go`** + миграция `011_voice_queue.sql` —
  таблица с `chat_id, message_id, file_id, mime_type, duration_ms, …`.
  Колонки `chat_id`, `message_id`, `file_id` помечены `NOT NULL` —
  для клиентских job-ов потребуется recreate-table миграция (SQLite
  не умеет `ALTER COLUMN DROP NOT NULL`).
- **`internal/model/interfaces.go`** — `History.Add(ctx, source, entry)`
  (не `Append` — поправлено относительно брейншторма), `VoiceQueue` интерфейс,
  `VoiceJob` структура (живёт в `internal/model/voice_job.go`).
- **`internal/api/chat.go`** — текущий `POST /v1/chat`: пишет в `History`,
  зовёт `ChatService.HandleMessage`, возвращает reply. SSE-событий не эмитит.
- **`internal/api/pairing.go`** — есть готовый паттерн in-memory
  per-IP token-bucket rate limiter (`pairingRateLimiter`); реюзаем подход
  для per-device rate limit `/v1/chat/voice`.
- **`internal/api/idempotency.go`** — middleware для `Idempotency-Key`
  (LRU `(deviceID, key) → response`, TTL 1h). Применим к новому хендлеру.
- **`internal/events/runner.go`** — раз в час: pairing/devices/push/events
  retention. Подходящее место для sweep-а orphan voice-uploads.
- **`internal/channel/telegram.go`** — текущий путь TG voice:
  `enqueueVoice` (pre-flight) → `voice_queue` → Worker → `BuildVoiceMessage`
  → `Pipeline.Process(MessageKindDM)`. Большая часть логики перенесётся
  в `telegramVoiceConsumer` без поведенческих изменений.
- **`api/openapi.yaml`** — источник правды для REST API; обновляется первым.

Все ключевые транзакционные паттерны (commit → `Broker.Notify`) уже
устоялись (см. `processForwardBatch` в `internal/pipeline/`).

## Development Approach

- **testing approach**: TDD (тесты пишутся первыми в каждой задаче).
- мелкие, последовательные изменения; одну задачу — до конца.
- **CRITICAL: каждая задача обязательно содержит новые/обновлённые тесты**
  для всего кода, который в ней меняется.
  - тесты — обязательная часть чек-листа задачи, а не опция.
  - покрываем и happy path, и ошибочные/граничные случаи.
  - стиль: table-driven, рукописные моки (никаких testify/mock — см. CLAUDE.md).
- **CRITICAL: все тесты должны проходить перед началом следующей задачи.**
- **CRITICAL: при изменении scope обновляем этот план.**
- после каждого изменения: `go test ./...` и `go vet ./...`.
- backward compatibility: `POST /v1/chat` сохраняет тот же JSON-ответ;
  существующие миграции не трогаются (новая 012); схемы `events` не меняются.

## Testing Strategy

- **unit-тесты**: обязательны в каждой задаче (см. выше).
- **e2e/smoke**: проект не использует Playwright/Cypress; вместо этого —
  один Go-тест в `cmd/huskwoot/` или в `internal/api/`, поднимающий весь
  HTTP-стек на `httptest.Server` с in-memory SQLite, мок-`Transcriber` и
  мок-`ChatService`. Проверяем end-to-end: POST аудио → 202 → SSE-события
  пришли в правильном порядке (`chat_message_created` user → agent).
- **stress/perf**: вне scope; rate-limit и MaxBytesReader покрываются
  unit-тестами на конкретные сценарии (превышения, idempotency-повтор).

## Progress Tracking

- помечать выполненное `[x]` сразу, не батчами.
- новые задачи, обнаруженные в процессе — с префиксом ➕.
- блокеры — с префиксом ⚠️.
- если scope меняется — апдейтим план до продолжения работы.

## Solution Overview

### Архитектура потока

```
client → POST /v1/chat/voice (multipart: audio + metadata)
           │
           ├─ pre-flight: auth, rate-limit, MaxBytes, MIME, durationMs
           ├─ stream audio → uploads_dir/.tmp/<uuid>.<ext>
           ├─ atomic rename → uploads_dir/<YYYY>/<MM>/<jobID>.<ext>
           └─ voice_queue.Enqueue(VoiceJob{SourceKind:"client", Payload: rel-path,
                                            DeviceID, MimeType, Duration})
           → 202 {jobId, createdAt}

transcribe.Worker (async)
  ├─ Claim → VoiceJob
  ├─ downloader[SourceKind].Download(job) → io.ReadCloser (telegram | local-file)
  ├─ Transcriber.Transcribe → text
  ├─ consumers[SourceKind].Consume(ctx, job, text)
  │     telegram → BuildVoiceMessage + Pipeline.Process(DM)         [unchanged]
  │     client   → tx1: history.AddTx(user, transcript) +
  │                     events.InsertTx("chat_message_created") → broker.Notify
  │                chatService.HandleMessage → reply
  │                tx2: history.AddTx(agent, reply) +
  │                     events.InsertTx("chat_message_created") → broker.Notify
  ├─ on success: voice_queue.Complete + os.Remove(file) [client]
  └─ on failure (≥4 attempts): consumers[Kind].Fail(ctx, job, err) → drop
        telegram → reply i18n voice.error
        client   → events.Insert("voice_failed") + broker.Notify

events.Runner.Tick (hourly)
  └─ orphan-sweep: для файлов в uploads_dir старше 24h без ряда в voice_queue → os.Remove
```

### Ключевые решения

- **Серверный STT** (option B из брейншторма). Реюз `Transcriber` как есть.
- **Async + SSE** (option B из брейншторма): `POST /v1/chat/voice` сразу
  возвращает 202; результат — через `/v1/events`. В будущем — опциональный
  `voice_status` event для прогресс-индикатора.
- **`VoiceConsumer` + `VoiceDownloader` интерфейсы** (option B из вопроса 6):
  Worker становится источниконезависимым; `*TelegramChannel` уезжает
  в свой consumer; `clientVoiceConsumer` живёт в `internal/usecase`.
- **Унификация `chat_message_created`** (option B из вопроса 5): и текст, и
  голос эмитят одни и те же события. `/v1/chat` (текст) расширяется. На старте
  Telegram-DM **не** включаем в этот event-поток (вне scope).
- **Хранение файла**: на диске (option A из вопроса 4). 10 MiB / 5 минут
  делают BLOB-в-SQLite неоправданным.
- **Миграция voice_queue: recreate-table.** SQLite не умеет снимать `NOT NULL`
  с существующих колонок, а для client-job-ов `chat_id/message_id/file_id`
  не имеют смысла. Стандартный паттерн SQLite: новая таблица → INSERT SELECT
  → DROP старой → RENAME.
- **JobID для клиента — UUID v4.** Колонка `client_job_id TEXT` (nullable;
  заполнена только для `source_kind='client'`) хранит UUID. API возвращает
  его в `202`-ответе, SSE-события `chat_message_created.audioJobId` и
  `voice_failed.jobId` используют его. Внутренний `voice_queue.id` остаётся
  INTEGER PK для индексов.
- **`messages.message_id TEXT UNIQUE` — отдельная миграция.** Существующая
  таблица `messages` (см. `001_baseline.sql`) не имеет UUID-колонки. Без
  такой колонки `History.AddTx` не сможет вернуть стабильный UUID для
  `chat_message_created.messageId`. Отдельная миграция `013_messages_message_id.sql`
  добавляет `message_id TEXT` (генерируется приложением как UUID v4) и
  UNIQUE индекс. При накатывании на существующие строки заполняем UUID-ами.
- **`VoiceDownloader.Download` возвращает `[]byte`** — соответствует текущей
  сигнатуре `WorkerDeps.DownloadFn` и `Transcriber.Transcribe(AudioInput{Data})`.
  Размер ограничен 10 MiB (`api.voice_max_bytes`); держим целиком в памяти.
- **Client-side dedup в `voice_queue` — не нужен.** Поскольку `payload`
  для клиента содержит свежегенерированный UUID-jobID, два вызова
  никогда не столкнутся в БД. Дедупликация повторов целиком на
  `Idempotency-Key` middleware. Partial unique индекс для client'а
  не создаём (ловушка: дал бы ложное чувство безопасности).
- **SSE failure-mode contract.** В `/v1/chat` (текст) теперь два commit-а
  с зазором, во время которого только user-сообщение в SSE. Контракт для
  потребителей: «после `chat_message_created` с `role=user` ждать
  `chat_message_created` с `role=agent` от того же `source.id` — таймаут
  ~2 минуты; если не пришло, считать запрос упавшим и **не повторять**
  на стороне клиента (повтор вручную пользователем). При краше сервера
  между commit-ами user-сообщение остаётся в истории-сироте — это
  допустимая семантика "сообщение отправлено, ответ потерян".»
- **`projectId` как контекстный hint.** Опциональное поле `metadata.projectId`
  в `POST /v1/chat/voice` позволяет клиенту, который сейчас показывает
  конкретный проект, передать его UUID. Сервер валидирует существование,
  сохраняет в `voice_queue.project_id`, после транскрипции `clientVoiceConsumer`
  резолвит проект и пробрасывает в системный промпт агента как «текущий
  контекст пользователя». Hint **не переопределяет** явные команды
  («в проекте Inbox …») — агент сам решает. Это hint, не директива.

## Technical Details

### Миграция `012_voice_queue_sources.sql`

```sql
-- +goose Up

CREATE TABLE voice_queue_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source_kind     TEXT    NOT NULL,                  -- 'telegram' | 'client'
    payload         TEXT    NOT NULL,                  -- file_id | rel-path
    mime_type       TEXT    NOT NULL,
    duration_ms     INTEGER NOT NULL,
    received_at     INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT,
    chat_id         INTEGER,                            -- nullable, telegram-only
    message_id      INTEGER,                            -- nullable, telegram-only
    device_id       TEXT,                               -- nullable, client-only
    client_job_id   TEXT,                               -- nullable; UUID для client
    project_id      TEXT                                -- nullable; UUID проекта-контекста (client-only)
);

INSERT INTO voice_queue_new
    (id, source_kind, payload, mime_type, duration_ms, received_at,
     attempts, next_attempt_at, last_error, chat_id, message_id, device_id,
     client_job_id, project_id)
SELECT
    id, 'telegram', file_id, mime_type, duration_ms, received_at,
    attempts, next_attempt_at, last_error, chat_id, message_id, NULL, NULL, NULL
FROM voice_queue;

DROP INDEX IF EXISTS idx_voice_queue_pending;
DROP TABLE voice_queue;
ALTER TABLE voice_queue_new RENAME TO voice_queue;

CREATE INDEX idx_voice_queue_pending ON voice_queue(next_attempt_at);

-- Partial UNIQUE для дедупа Telegram-голосов (как раньше делал
-- UNIQUE(chat_id, message_id)). На client-стороне дедуп не нужен:
-- payload содержит свежий UUID, коллизий быть не может, повторы
-- ловятся на уровне Idempotency-Key middleware.
CREATE UNIQUE INDEX idx_voice_queue_telegram_uniq
    ON voice_queue(chat_id, message_id) WHERE source_kind = 'telegram';

-- UUID, по которому клиент находит свой job в SSE-событиях.
CREATE UNIQUE INDEX idx_voice_queue_client_job_id
    ON voice_queue(client_job_id) WHERE client_job_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_voice_queue_client_job_id;
DROP INDEX IF EXISTS idx_voice_queue_telegram_uniq;
DROP INDEX IF EXISTS idx_voice_queue_pending;

CREATE TABLE voice_queue_old (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id         INTEGER NOT NULL,
    message_id      INTEGER NOT NULL,
    file_id         TEXT    NOT NULL,
    mime_type       TEXT    NOT NULL,
    duration_ms     INTEGER NOT NULL,
    received_at     INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT,
    UNIQUE(chat_id, message_id)
);

INSERT INTO voice_queue_old
SELECT id, chat_id, message_id, payload, mime_type, duration_ms, received_at,
       attempts, next_attempt_at, last_error
FROM voice_queue
WHERE source_kind = 'telegram';

DROP TABLE voice_queue;
ALTER TABLE voice_queue_old RENAME TO voice_queue;
CREATE INDEX idx_voice_queue_pending ON voice_queue(next_attempt_at);
```

### `model.VoiceJob` (расширение)

```go
type VoiceJob struct {
    ID            int64           // INTEGER PK; внутренний идентификатор
    SourceKind    string          // "telegram" | "client"
    Payload       string          // file_id (telegram) | "<YYYY>/<MM>/<uuid>.<ext>" (client)
    MimeType      string
    Duration      time.Duration
    ReceivedAt    time.Time
    Attempts      int
    NextAttemptAt time.Time
    LastError     string

    // Telegram-only:
    ChatID    int64
    MessageID int

    // Client-only:
    DeviceID    string
    ClientJobID string                // UUID v4; то, что клиент видит в API/SSE
    ProjectID   string                // опциональный UUID проекта-контекста (может быть "")
}
```

### Новые интерфейсы (`internal/model/interfaces.go`)

```go
// VoiceConsumer обрабатывает результат транскрипции/ошибку для конкретного источника.
type VoiceConsumer interface {
    Consume(ctx context.Context, job VoiceJob, transcript string) error
    Fail(ctx context.Context, job VoiceJob, cause error)
}

// VoiceDownloader скачивает аудио для job по конкретному источнику.
// Возвращает []byte для совместимости с Transcriber.AudioInput.Data.
// Размер ограничен api.voice_max_bytes (10 MiB), что приемлемо для in-memory.
type VoiceDownloader interface {
    Download(ctx context.Context, job VoiceJob) ([]byte, error)
}
```

### `History.AddTx`

```go
type History interface {
    Add(ctx context.Context, source string, entry HistoryEntry) error
    AddTx(ctx context.Context, tx *sql.Tx, source string, entry HistoryEntry) (messageID string, err error)
    Recent(ctx context.Context, source string, limit int) ([]HistoryEntry, error)
    RecentActivity(ctx context.Context, source string, silenceGap time.Duration, fallbackLimit int) ([]HistoryEntry, error)
}
```

`Add` остаётся как обёртка, делающая собственный `BeginTx` и игнорирующая
возвращаемый `messageID`.

### API: `POST /v1/chat/voice`

```
POST /v1/chat/voice
  Authorization: Bearer <device-token>
  Content-Type:  multipart/form-data; boundary=…
  Idempotency-Key: <uuid>

  Part "audio"    (file)  — required
  Part "metadata" (JSON)  — required:
    {
      "durationMs": int,         // длительность аудио в мс
      "mimeType":   string,      // должен совпадать с Content-Type части audio
      "projectId":  string?      // ОПЦИОНАЛЬНО: UUID проекта-контекста
    }

→ 202 Accepted
  Content-Type: application/json
  { "jobId": "<uuid>", "createdAt": "2026-05-05T12:00:00Z" }

Errors:
  400 invalid_audio | invalid_metadata | duration_too_long | project_not_found
  401 unauthorized
  413 audio_too_large
  415 unsupported_media_type
  429 rate_limited
  503 transcriber_disabled
```

**Семантика `projectId`** (опциональный hint от клиента):
- Когда клиент показывает экран конкретного проекта и пользователь надиктовывает
  команду — клиент шлёт `projectId` этого проекта.
- Сервер валидирует, что проект существует (через `TaskStore.GetProject`);
  несуществующий UUID → `400 project_not_found`. Inbox (default-проект) —
  валидное значение.
- Hint **не переопределяет** явное указание проекта в самой голосовой команде.
  Агент использует его как default для `create_task` / `move_task` / etc.,
  если в транскрипте нет явной привязки к другому проекту.
- Реализация: `clientVoiceConsumer` пробрасывает `ProjectID` в системный промпт
  агента через дополнительный context-block: «Текущий контекст пользователя:
  проект `<slug>` (`<UUID>`).» — детали в Task 8.

Допустимые `mimeType`:
- `audio/ogg;codecs=opus`
- `audio/mp4`
- `audio/webm;codecs=opus`

### Новые SSE event-kinds

```jsonc
// chat_message_created:
{
  "messageId":  "<uuid>",
  "source":     {"kind": "client", "id": "<deviceId>"},
  "role":       "user" | "agent",
  "text":       "...",
  "audioJobId": "<uuid>",          // present only when text came from voice
  "createdAt":  "2026-05-05T12:00:00Z"
}

// voice_failed:
{
  "jobId":   "<uuid>",
  "code":    "transcription_failed" | "invalid_audio" | "downloader_failed",
  "message": "human-readable ru/en string"
}
```

### Конфиг

```toml
[transcriber]
provider     = "whisper"          # без изменений
max_duration = "5m"               # без изменений
uploads_dir  = ""                 # default: <configDir>/voice_uploads

[api]
voice_max_bytes              = "10MiB"   # MaxBytesReader для POST /v1/chat/voice
voice_rate_limit_per_minute  = 10        # per-device, 0 → default 10
```

`uploads_dir`: при пустом значении — `filepath.Join(configDir, "voice_uploads")`,
создаётся в `main.go` через `os.MkdirAll(..., 0o700)`.

### `/v1/me` extension

Поле `voiceEnabled bool` в response. `true` ⟺ `transcriber.provider != ""`.
Клиент скрывает кнопку записи, если `false`.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes) — всё, что лежит в этом репозитории.
- **Post-Completion** (без чек-боксов):
  - native-клиенты (macOS/iOS/Android) — отдельные репозитории.
  - продовый деплой `huskwoot serve` (новый каталог `voice_uploads/` нужно
    смонтировать как persistent volume).
  - ручная проверка whisper/yandex на реальных аудио-файлах.

## Implementation Steps

### Task 1: Спецификация API в `openapi.yaml`

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `internal/api/openapi_test.go`

- [x] добавить `POST /v1/chat/voice` (multipart: `audio` + `metadata`),
      response `202` с `{jobId, createdAt}`, error-коды 400/401/413/415/429/503
- [x] описать в `components.schemas` новые event-payload-ы:
      `ChatMessageCreatedEvent`, `VoiceFailedEvent` (camelCase, с `audioJobId?`)
- [x] добавить kind-значения `chat_message_created`, `voice_failed`
      в перечисление событий SSE
- [x] добавить поле `voiceEnabled: boolean` в schema ответа `GET /v1/me`
- [x] обновить `internal/api/openapi_test.go`, чтобы тест валидации YAML
      проходил с новой версией (если в нём прибит конкретный список путей —
      добавить новый путь)
- [x] прогнать `go test ./internal/api/...`, убедиться что openapi-тест зелёный

### Task 2: Миграция `voice_queue` под мульти-источник

**Files:**
- Create: `internal/storage/migrations/012_voice_queue_sources.sql`
- Modify: `internal/storage/migrations/migrations_test.go`

- [x] написать тест: накатить миграции до 011, вставить 2 telegram-job-а;
      накатить 012; проверить что записи на месте, `source_kind='telegram'`,
      `payload = file_id`, `chat_id/message_id` сохранены, `device_id IS NULL`
- [x] написать тест: на схеме после 012 вставить client-job
      (`source_kind='client', payload='2026/05/abc.ogg', device_id='dev-1',
      chat_id=NULL, message_id=NULL, project_id=NULL`) — должно работать
- [x] написать тест: client-job с `project_id='<uuid>'` сохраняется и
      читается обратно как-есть (валидации FK здесь нет — она на уровне
      handler-а, см. Task 9a)
- [x] написать тест: дубль `(chat_id, message_id)` для telegram отвергается
      partial unique index — **проверить именно через `INSERT OR IGNORE`**
      (как делает текущий `SQLiteVoiceQueue.Enqueue`): первый INSERT даёт
      строку, второй INSERT OR IGNORE с теми же `chat_id, message_id` —
      noop без ошибки, COUNT(*) остаётся 1
- [x] написать тест: дубль `client_job_id` отвергается UNIQUE индексом
      (этот случай не должен происходить в норме — UUID v4, — но индекс
      защищает от двойного `Enqueue` одного UUID, например при ретрае
      воркера)
- [x] написать тест Down: откат с **смешанными строками** (telegram + client)
      сохраняет telegram-строки c корректным `file_id = payload` и
      **молча отбрасывает client-строки** (это известная односторонняя
      потеря данных, явно задокументированная в Down-блоке)
- [x] реализовать SQL миграцию по схеме из секции Technical Details
      (с комментарием в Down-блоке про деструктивность для client-строк)
- [x] прогнать `go test ./internal/storage/migrations/...`

### Task 3: `model.VoiceJob` + storage `voice_queue.go` под новые поля

**Files:**
- Modify: `internal/model/voice_job.go`
- Modify: `internal/storage/voice_queue.go`
- Modify: `internal/storage/voice_queue_test.go`

- [x] обновить тесты `voice_queue_test.go`: `Enqueue`/`Claim`/`Reschedule`
      должны корректно сохранять и читать `SourceKind`, `Payload`, `DeviceID`,
      nullable `ChatID/MessageID` (для client — нули/пустота из ZeroValue ОК)
- [x] добавить тест: `Enqueue` с `SourceKind="client"` без `ChatID/MessageID`
      проходит; `Claim` отдаёт обратно ровно те же поля
- [x] добавить тест: `Enqueue` дубль с тем же `(device_id, payload)` →
      возвращает `nil` без вставки (INSERT OR IGNORE), как сейчас для telegram
- [x] тест: `Enqueue` для client записывает `client_job_id = job.ClientJobID`;
      `Claim` восстанавливает то же значение
- [x] тест: `Enqueue` для client с `ProjectID="<uuid>"` сохраняет в
      `project_id`; `Claim` восстанавливает; пустой `ProjectID` маппится
      в `NULL`
- [x] добавить поля `SourceKind, Payload, DeviceID, ClientJobID, ProjectID`
      в `model.VoiceJob`; `ChatID/MessageID` сделать просто `int64`/`int`
      (zero-value = «не используется», NULL в БД маппится как 0)
- [x] переписать SQL в `voice_queue.go`: `INSERT`, `Claim` (`SELECT … FROM
      voice_queue WHERE next_attempt_at <= ?`), `Reschedule`, `Complete` —
      под новый набор колонок
- [x] прогнать `go test ./internal/storage/...`

### Task 4a: Миграция `messages.message_id`

**Files:**
- Create: `internal/storage/migrations/013_messages_message_id.sql`
- Modify: `internal/storage/migrations/migrations_test.go`

Существующая `messages` (см. `001_baseline.sql`) имеет только `id INTEGER PK`
без UUID-колонки. SSE-event `chat_message_created.messageId` обещан как UUID
в OpenAPI, поэтому нужна стабильная строковая колонка.

- [x] тест: накатить до 012, вставить пару messages-строк с разными
      `source_id`; накатить 013; убедиться, что у всех существующих
      строк колонка `message_id` заполнена ненулевым уникальным значением
      (UUID v4)
- [x] тест: после 013 INSERT с дублирующим `message_id` отвергается UNIQUE
- [x] тест Down: колонка убрана, существующие данные сохранены
- [x] реализовать `+goose Up`:
      ```sql
      ALTER TABLE messages ADD COLUMN message_id TEXT NOT NULL DEFAULT '';
      -- backfill: уникальный UUID на каждую существующую строку
      -- (через goose Go-миграцию или WITH ROWID + hex(randomblob))
      ```
      Простейший вариант — Go-миграция (goose поддерживает `.go` миграции),
      где код проходит по `messages` и `UPDATE` каждой строки `message_id =
      <UUID v4>`. Затем `CREATE UNIQUE INDEX idx_messages_message_id ON
      messages(message_id)`.
      Альтернатива: использовать `lower(hex(randomblob(16)))` прямо в
      SQL — даёт hex-строку 32 символа (не строго UUID v4, но уникальную).
      Берём вариант с `lower(hex(randomblob(16)))` — проще, нет Go-кода.
- [x] `+goose Down`: `DROP INDEX idx_messages_message_id` + `ALTER TABLE
      messages DROP COLUMN message_id` (SQLite 3.35+, или recreate-table)
- [x] прогнать `go test ./internal/storage/migrations/...`

### Task 4b: `History.AddTx` + расширение интерфейса

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/storage/history.go`
- Modify: `internal/storage/history_test.go`

- [x] тест: `AddTx` в открытой транзакции вставляет запись, возвращает
      не-пустой `messageID` (UUID v4); после `tx.Commit()` запись видна
      через `Recent`; `messageID` совпадает с тем, что в `messages.message_id`
- [x] тест: `AddTx` + `tx.Rollback()` → запись не появляется в `Recent`
- [x] тест: `Add` (старый метод) продолжает работать без явной транзакции
      и тоже заполняет `messages.message_id` (regression)
- [x] тест: два последовательных `AddTx` дают разные `messageID`
- [x] расширить интерфейс `model.History`:
      `AddTx(ctx, tx, source, entry) (messageID string, err error)`
- [x] реализовать `SQLiteHistory.AddTx`: генерируем UUID v4 через
      `github.com/google/uuid` (или `crypto/rand`), используем
      `tx.ExecContext`, INSERT с `message_id` колонкой; возвращаем UUID
- [x] переделать `Add` как обёртку: `BeginTx` → `AddTx` → `Commit`;
      возвращаемый `messageID` игнорируется (signature: `error`)
- [x] обновить все usage `History.Add` в репо — должны продолжать работать
      без изменений
- [x] прогнать `go test ./internal/storage/... ./internal/usecase/...`

### Task 5: `transcribe.Worker` рефакторинг под consumers + downloaders

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/transcribe/worker.go`
- Modify: `internal/transcribe/worker_test.go`
- Create: `internal/transcribe/downloader.go`
- Create: `internal/transcribe/downloader_test.go`

- [x] определить интерфейсы `model.VoiceConsumer` и `model.VoiceDownloader`
      в `internal/model/interfaces.go` (см. секцию Technical Details).
      `VoiceDownloader.Download` возвращает `[]byte` для совместимости
      с `Transcriber.Transcribe(AudioInput{Data})`.
- [x] написать тесты `worker_test.go`: Worker берёт job c
      `SourceKind="telegram"` → зовёт `downloaders["telegram"].Download` →
      `Transcriber.Transcribe` → `consumers["telegram"].Consume(ctx, job, text)`
- [x] тест: Worker берёт job с `SourceKind="client"` → аналогичный путь
      через `consumers["client"]` и `downloaders["client"]`
- [x] тест: при ошибке `Download` — `Reschedule(nextAt, err)`, `attempts++`,
      backoff верный
- [x] тест: при 4-й попытке (attempts=3 → попытка проваливается) —
      `Consumer.Fail` вызван, `Complete` (или `Drop`) вызван
- [x] тест: при `SourceKind` отсутствующем в `consumers` map — лог-error
      + `Drop` (помечаем `Complete` чтобы не зацикливаться) — защита от
      мусора в БД
- [x] тест: при пустом transcript (после `strings.TrimSpace`) — Worker
      вызывает `Consumer.Fail(ctx, job, errEmptyTranscript)`, **не**
      `Consume` (telegram-consumer ответит i18n `voice.empty`)
- [x] тест `downloader_test.go::telegramDownloader.Download` —
      мок `BotAPI.GetFile` + http-сервер, отдающий байты; проверить чтение
      и `MaxBytesReader`-обрезку до 10 MiB
- [x] тест `downloader_test.go::localFileDownloader.Download` —
      создать файл в `t.TempDir()/<uploads_dir>/2026/05/abc.ogg`, прочитать
      через `os.ReadFile`; ошибка для несуществующего файла; защита от
      path traversal (`payload` не должен содержать `..` или `/` в начале)
- [x] реализовать `WorkerConfig`: убираем `DownloadFn`, добавляем
      `Downloaders map[string]model.VoiceDownloader` и
      `Consumers map[string]model.VoiceConsumer`. Worker диспатчит по
      `job.SourceKind`. Никакого composite-обёртки — две голые map-ы
- [x] реализовать `internal/transcribe/downloader.go`:
      `telegramDownloader{bot *tgbotapi.BotAPI}` и
      `localFileDownloader{uploadsDir string}`. Без composite-типа
- [x] обновить `worker.go`: убрать прямую зависимость от `*TelegramChannel`
      и `BuildVoiceMessage`; диспатчить через map-ы по `job.SourceKind`
- [x] прогнать `go test ./internal/transcribe/...`

### Task 6: `telegramVoiceConsumer` — выделение из `telegram.go`

**Files:**
- Create: `internal/channel/telegram_voice_consumer.go`
- Create: `internal/channel/telegram_voice_consumer_test.go`
- Modify: `internal/channel/telegram.go`

- [x] написать тест `telegram_voice_consumer_test.go`: `Consume` зовёт
      `BuildVoiceMessage` + `Pipeline.Process(MessageKindDM)` с правильным
      `model.Message` (Source.Kind="telegram", Text=transcript, ReplyFn
      работает через мок `BotAPI`)
- [x] тест: `Fail` — отправляет реплай i18n `voice.error` в `ChatID`
- [x] тест: при пустом transcript — `Consume` отвечает i18n `voice.empty`
      без вызова Pipeline
- [x] реализовать `telegramVoiceConsumer{channel *TelegramChannel,
      pipeline Pipeline, localizer *i18n.Localizer}`
- [x] перенести логику `transcribeVoice`-успеха и `replyVoiceI18n("voice.error/empty")`
      из `telegram.go`/`worker.go` в новый consumer; в `telegram.go`
      остаётся только pre-flight + `Enqueue` (как сейчас в `enqueueVoice`)
- [x] `BuildVoiceMessage` остаётся public-методом `*TelegramChannel`
      (consumer его зовёт)
- [x] прогнать `go test ./internal/channel/... ./internal/transcribe/...`
- [x] **Регрессионная проверка**: поведение Telegram-голоса не должно
      измениться — старые тесты `telegram_test.go` зелёные

### Task 7: `chat_message_created` для текстового `/v1/chat`

**Files:**
- Modify: `internal/api/chat.go`
- Modify: `internal/api/chat_test.go`

- [x] добавить тест: после `POST /v1/chat` в `EventStore` появилось 2
      события `chat_message_created` (user, agent), payload содержит
      `messageId`, `source.kind="client"`, `source.id=deviceID`,
      правильный `role`, `text`, отсутствие `audioJobId`
- [x] тест: `Broker.Notify` вызван дважды (через мок-broker)
- [x] тест: HTTP-ответ JSON не изменился (regression)
- [x] тест: при ошибке `ChatService.HandleMessage` user-сообщение
      и его событие — на месте, agent-события нет
- [x] переделать `chat.go::post` на двухтранзакционный паттерн:
      `tx1` → `History.AddTx(user) + EventStore.InsertTx(chat_message_created)`
      → commit → `Broker.Notify`; затем `ChatService.HandleMessage`;
      `tx2` → `History.AddTx(agent) + EventStore.InsertTx(...)` → commit
      → `Broker.Notify`; вернуть JSON-ответ как раньше
- [x] добавить новый event-kind `chat_message_created` в существующее
      перечисление (если оно есть в `internal/events/`); если такого нет
      — просто использовать строковое значение
- [x] передать в handler зависимость на `*sql.DB`, `EventStore`, `Broker`
      (через newChatHandler)
- [x] зафиксировать **SSE failure-mode contract** в комментарии над
      `chatHandler.post`: «между tx1 и tx2 окно, в котором SSE уже видел
      user-сообщение, но agent-сообщения ещё нет. На крах сервера в этом
      окне — user-сообщение остаётся в истории-сироте; клиенты по контракту
      ждут agent-event ~2 минуты и считают запрос упавшим без авто-ретрая.»
- [x] прогнать `go test ./internal/api/...`

### Task 8: `clientVoiceConsumer`

**Files:**
- Create: `internal/usecase/client_voice_consumer.go`
- Create: `internal/usecase/client_voice_consumer_test.go`

- [x] тест: `Consume` пишет user-сообщение в `History.AddTx`,
      эмитит `chat_message_created` (с `audioJobId`!), commit, `Broker.Notify`;
      затем зовёт `ChatService.HandleMessage`; пишет agent-сообщение,
      эмитит второе событие, commit, `Broker.Notify`. Все вызовы — в
      правильном порядке
- [x] тест: `audioJobId` в payload равен `job.ClientJobID` (UUID v4
      из таблицы `voice_queue.client_job_id`), а не внутренний `job.ID`
- [x] тест: ошибка `ChatService.HandleMessage` → user-сообщение и его
      событие сохранены, `Fail(ctx, job, err)` вызывается из Worker-а
      (тест стороны Worker-а, не consumer-а)
- [x] тест: `Fail(ctx, job, cause)` эмитит `voice_failed` event
      с `jobId = job.ClientJobID`, `code` (выводимым из типа ошибки) и
      `message`; `Broker.Notify` вызван
- [x] тест: `Source` для `History.AddTx` корректный —
      `chatSourceID(deviceID)` (повторное использование функции из
      `internal/api/chat.go`; вынести её в `internal/usecase` если нужно)
- [x] тест: при непустом `job.ProjectID` consumer резолвит проект через
      `TaskStore.GetProject` и передаёт его как контекстный hint в
      `ChatService.HandleMessage` (через расширение `model.Message` —
      добавить поле `ProjectHint *Project` или просто `ProjectHintID string`,
      агент читает его в системном промпте). При пустом `ProjectID` —
      hint не передаётся; агент работает как раньше через MetaStore-резолв
- [x] тест: hint **не подменяет** явные команды — если транскрипт
      содержит «в проекте Inbox …», агент решает сам; этот тест на стороне
      агента, не consumer-а; здесь проверяем только пробрасывание hint-а
- [x] тест: если `TaskStore.GetProject` падает (проект удалили между
      Enqueue и Consume) — consumer логирует warning и продолжает без hint-а
      (не валит обработку), а **не** эмитит `voice_failed`
- [x] тест: транзакционность — если `EventStore.InsertTx` падает,
      `History.AddTx` откатывается (проверяется через мок, возвращающий
      ошибку, и `Recent` после)
- [x] реализовать `clientVoiceConsumer` с зависимостями
      `*sql.DB, History, EventStore, Broker, ChatService, TaskStore,
      Localizer`. `TaskStore` нужен для резолва `ProjectID → *model.Project`
- [x] расширение `model.Message` (или соседнее поле в передаваемой
      структуре): `ProjectHint *Project` — null когда нет контекста.
      Агент читает hint в системном промпте: «Текущий проект-контекст
      пользователя: <name> (<slug>). Используй как default для новых
      задач, если в команде нет явного указания другого проекта.»
      Изменения в `internal/agent/` — отдельный sub-пункт; держим
      минимальные правки промпта
- [x] прогнать `go test ./internal/usecase/...`

### Task 9a: `POST /v1/chat/voice` — multipart, файл на диск, базовая валидация

**Files:**
- Create: `internal/api/chat_voice.go`
- Create: `internal/api/chat_voice_test.go`
- Modify: `internal/api/server.go`

Цель: handler принимает аудио, валидирует, сохраняет файл, кладёт в очередь.
Без rate-limit и взаимодействия с idempotency-кешем — это в 9b.

- [x] тесты:
      - happy path: валидный multipart с opus → 202 + `{jobId, createdAt}`;
        в `voice_queue` есть запись `source_kind='client', device_id,
        client_job_id, payload`, `project_id=NULL`; файл существует под
        `<uploadsDir>/<YYYY>/<MM>/<clientJobID>.ogg`
      - happy path с `metadata.projectId="<existing-uuid>"` →
        запись в `voice_queue.project_id` равна переданному UUID
      - 400 `project_not_found` при `metadata.projectId="<non-existing-uuid>"`
        — handler делает `TaskStore.GetProject(ctx, projectID)` до записи
        файла; tmp-file не создаётся
      - 400 `invalid_metadata` при `projectId` не-UUID-формате (не парсится)
      - 401 без bearer
      - 413 при теле > `voice_max_bytes` (через `MaxBytesReader`)
      - 415 при `mimeType` вне allow-list
      - 400 при `durationMs > max_duration`
      - 400 при отсутствии `audio` или `metadata` part
      - 400 при невалидном JSON `metadata`
      - 400 при расхождении `metadata.mimeType` и `audio` Content-Type
- [x] тест: при ошибке `Enqueue` после записи tmp-файла — tmp-файл удаляется
      (no orphans)
- [x] реализовать handler `chatVoiceHandler.post`:
      - `MaxBytesReader(w, r.Body, voiceMaxBytes)`
      - `r.ParseMultipartForm(...)` → достать `audio` и `metadata`
      - валидация: MIME в allow-list, `durationMs ≤ maxDuration`,
        `metadata.mimeType == audio.ContentType`
      - валидация `metadata.projectId` (если не пустой): UUID-формат,
        `TaskStore.GetProject` возвращает не-nil (ProjectService.Resolve…
        тоже подойдёт). Несуществующий → `400 project_not_found`
      - сгенерировать `clientJobID` (UUID v4)
      - `os.CreateTemp(uploadsDir/.tmp, clientJobID+"-*")` → `io.Copy` →
        `os.Rename` в `uploadsDir/<YYYY>/<MM>/<clientJobID>.<ext>`
      - `voice_queue.Enqueue(VoiceJob{SourceKind:"client",
        Payload:<relPath>, MimeType, Duration, DeviceID,
        ClientJobID:clientJobID, ProjectID:metadata.ProjectID,
        ReceivedAt:now})`
      - на ошибке после tmp-write — `os.Remove(tmpPath)` через defer
      - 202 c `{jobId: clientJobID, createdAt}`
- [x] зарегистрировать ручку в `server.go::Routes()` (без rate-limiter — пока
      нет), пробросить через `ServerConfig`: `VoiceQueue, UploadsDir,
      VoiceMaxBytes int64, Transcriber model.Transcriber, TaskStore
      model.TaskStore` (последний — для валидации `projectId`)
- [x] прогнать `go test ./internal/api/...`

### Task 9b: `POST /v1/chat/voice` — rate limit, idempotency, 503

**Files:**
- Modify: `internal/api/chat_voice.go`
- Modify: `internal/api/chat_voice_test.go`
- Modify: `internal/api/server.go`
- Create: `internal/api/voice_rate_limiter.go` *(или в `chat_voice.go` как
  unexported тип, по аналогии с `pairingRateLimiter`)*

- [x] тест: 429 при превышении per-device rate-limit (10/min default)
- [x] тест: лимит per-device — два разных device-id не мешают друг другу
- [x] тест: 503 при `Transcriber == nil` (provider="") до парсинга тела
- [x] тест: `Idempotency-Key` повтор с тем же ключом — тот же ответ
      (`{jobId, createdAt}` идентичны), `voice_queue` не дублирует
      (один ряд), файл не дублируется
- [x] тест: `Idempotency-Key` повтор с другим телом — undefined behavior
      по контракту (документируем, тест не пишем)
- [x] тест: разные device-id с одним `Idempotency-Key` — независимые ответы
      (idempotency middleware кеширует по `(deviceID, key)`)
- [x] реализовать `clientVoiceRateLimiter` (per-device token-bucket по
      аналогии с `pairingRateLimiter`); прокинуть через `ServerConfig.VoiceRateLimit`
- [x] добавить early-return 503 в начале handler-а (до `MaxBytesReader`),
      если `Transcriber == nil`
- [x] обернуть handler в существующий `idempotency` middleware
- [x] прогнать `go test ./internal/api/...`

### Task 10: `/v1/me` — `voiceEnabled`

**Files:**
- Modify: `internal/api/me.go`
- Modify: `internal/api/me_test.go` (создать, если нет)

- [x] тест: `voiceEnabled=true` когда `Transcriber != nil`
- [x] тест: `voiceEnabled=false` когда `Transcriber == nil`
- [x] тест: backwards-compat — остальные поля JSON остались как раньше
- [x] добавить поле в response struct + проводку через `ServerConfig`
- [x] прогнать `go test ./internal/api/...`

### Task 11: Orphan voice-uploads sweep в `events.Runner`

**Files:**
- Modify: `internal/events/runner.go`
- Modify: `internal/events/runner_test.go`

- [x] тест: создать `t.TempDir()` с двумя файлами в `<dir>/2026/05/`,
      один из них зарегистрирован в `voice_queue` (мок), другой — нет;
      запустить `Tick` (mtime обоих > 24h назад через `os.Chtimes`);
      проверить, что только незарегистрированный удалён, а директория
      `2026/05/` если стала пустой — удалена; зарегистрированный — на месте
- [x] тест: файл моложе 24h не удаляется даже если orphan
- [x] тест: ошибка чтения каталога логируется как warn, остальные шаги
      retention-а продолжаются
- [x] тест: при `uploads_dir==""` sweep no-op
- [x] реализовать sweep-функцию: `filepath.WalkDir(uploadsDir)`, для каждого
      файла спрашивать у `VoiceQueue` (или просто `SELECT 1 FROM voice_queue
      WHERE source_kind='client' AND payload=?`) — наличие; если нет и
      `now - mtime > 24h` → `os.Remove`
- [x] добавить шаг в `Runner.Tick` после device sweep — порядок не критичен
      (нет cross-зависимостей с events/devices/push), кладём перед
      `EventStore.DeleteOlderThan` для группировки «старые данные» в конец
- [x] прогнать `go test ./internal/events/...`

### Task 12: Конфиг — новые ключи + валидация

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] тест: парсинг TOML с `[transcriber] uploads_dir = "/var/x"` —
      значение пробрасывается
- [x] тест: при пустом `uploads_dir` — после `ApplyDefaults(configDir)`
      получаем `<configDir>/voice_uploads`
- [x] тест: парсинг `[api] voice_max_bytes = "10MiB"` →
      целочисленное значение `10 * 1024 * 1024`; пустое значение → дефолт
- [x] тест: `voice_rate_limit_per_minute = 0` → дефолт 10;
      отрицательное → ошибка валидации
- [x] тест: невалидный `voice_max_bytes` (например, `"abc"`) → ошибка
- [x] добавить поля в config-структуры, реализовать парсинг
      (humanize-байты — взять стандартный pattern из других проектов
      или простой helper `parseBytes`)
- [x] прогнать `go test ./internal/config/...`

### Task 13: `cmd/huskwoot/main.go` — проводка

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] создать `uploads_dir` (`os.MkdirAll` с 0o700) перед стартом сервисов
- [x] построить две map-ы:
      `downloaders := map[string]model.VoiceDownloader{
          "telegram": transcribe.NewTelegramDownloader(bot),
          "client":   transcribe.NewLocalFileDownloader(uploadsDir),
      }`
      `consumers := map[string]model.VoiceConsumer{
          "telegram": channel.NewTelegramVoiceConsumer(...),
          "client":   usecase.NewClientVoiceConsumer(...),
      }`
- [x] передать новые зависимости в `transcribe.Worker` (новый WorkerConfig)
- [x] передать в `api.NewServer(cfg)` новые поля: `VoiceQueue, UploadsDir,
      VoiceMaxBytes, VoiceRateLimit, Transcriber`
- [x] передать `*sql.DB, EventStore, Broker` в `chatHandler` для
      двухтранзакционного эмита `chat_message_created`
- [x] (тестов нет — это проводка; проверка зелёных
      `go build` и `go test ./...`)
- [x] прогнать `go test ./...` целиком

### Task 14: Verify acceptance criteria

- [x] вся функциональность из секции Overview реализована
- [x] `POST /v1/chat/voice` принимает opus/mp4/webm, возвращает 202+jobId
- [x] результат приходит в `/v1/events` двумя `chat_message_created`
- [x] ошибка транскрипции → `voice_failed` event
- [x] orphan-файлы убираются sweep-ом
- [x] `/v1/me` отдаёт `voiceEnabled` корректно для разных `provider`
- [x] `POST /v1/chat` (текст) тоже эмитит `chat_message_created`
- [x] Telegram-голос работает как раньше — нет регрессий
- [x] прогнать полный `go test ./...` — зелёный
- [x] прогнать `go vet ./...` — без ворнингов
- [x] прогнать `go build -o bin/huskwoot ./cmd/huskwoot/` — собирается

### Task 15: End-to-end smoke-test

**Files:**
- Create: `internal/api/voice_e2e_test.go`

- [x] поднять `httptest.Server` со всеми компонентами на in-memory SQLite,
      мок-`Transcriber` (возвращает фикс-строку) и мок-`ChatService`
      (возвращает фикс-reply)
- [x] создать pairing → device + bearer
- [x] `POST /v1/chat/voice` с маленьким opus-файлом (или фейковым байтовым
      content-ом + `metadata`) → ожидать 202
- [x] параллельно подключиться к `/v1/events` (SSE), собрать события
- [x] прогнать Worker.Tick один раз (через test hook, если есть; иначе —
      через короткий `time.Sleep` и опросить `Recent`)
- [x] проверить: пришло 2 `chat_message_created` с `audioJobId`,
      первый user, второй agent; `History.Recent` отдаёт обе записи

### Task 16: [Final] Документация

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md` (если есть упоминания API)

- [x] обновить `CLAUDE.md`:
      - в секции `## Pipeline architecture` упомянуть, что DM-голос Telegram
        теперь проходит через `telegramVoiceConsumer`, а не `BuildVoiceMessage`
        напрямую из Worker-а
      - в секции `## SQLite stores` обновить описание `voice_queue` (новые
        поля `source_kind`, `payload`, `device_id`, `client_job_id`,
        partial unique indexes); упомянуть колонку
        `messages.message_id` (UUID v4)
      - в `## HTTP API` добавить `POST /v1/chat/voice` и новые event-kinds
      - в `### [transcriber]` добавить ключ `uploads_dir`
      - в `### [api]` добавить `voice_max_bytes`, `voice_rate_limit_per_minute`
- [x] переместить план: `mkdir -p docs/plans/completed && git mv
      docs/plans/20260505-client-voice-chat.md docs/plans/completed/`

## Post-Completion

*Действия вне репозитория, не покрываемые чек-боксами выше.*

**Native-клиенты** (отдельные репозитории, отдельные PR-ы):

- macOS: `AVAudioRecorder` → opus (через ffmpeg) или `m4a` напрямую,
  multipart-загрузка через `URLSession`, подписка на `/v1/events` (SSE)
  для отображения transcript-а и reply.
- iOS: `AVAudioRecorder` → m4a/AAC, аналогично.
- Android: `MediaRecorder` (`OutputFormat.OGG`+`AudioEncoder.OPUS` на
  API 29+, иначе fallback к m4a).

**Серверный деплой:**

- настроить persistent volume под `<configDir>/voice_uploads` —
  если контейнер ephemeral, файлы между рестартами теряются
  (Worker отдаст job-ам failure после 4 попыток `Download`).
- открыть `voice_uploads` каталог только под пользователем процесса
  (perms 0o700).
- выставить `transcriber.max_duration` и `api.voice_max_bytes` под реальный
  трафик; следить за дисковым ростом — теоретический верх =
  `voice_max_bytes × max_concurrent_uploads + voice_max_bytes ×
  voice_queue_pending`.

**Ручные проверки:**

- whisper-cli и yandex-stt на реальных голосовых из macOS/iOS/Android —
  проверить, что распознавание адекватное на коротких командах
  («создай задачу позвонить маме завтра до полудня»).
- стресс-тест rate-limit: 11+ запросов в минуту → 429.
- проверка идемпотентности: при перезапросе с тем же `Idempotency-Key` —
  тот же `jobId`, файл не задублирован.

**Будущее (вне scope этого плана):**

- `voice_status` event для прогресс-индикатора («расшифровываю…») —
  отдельный event-kind, отложено.
- унификация `/v1/chat` и Telegram-DM через единый `Pipeline` —
  отдельный рефакторинг.
- `GET /v1/chat/voice/{jobId}` для клиентов без SSE — добавим если
  понадобится.
- `chat_message_created` для Telegram-DM-канала — мульти-канальный
  таймлайн агента (требует отдельного дизайна по приватности).
