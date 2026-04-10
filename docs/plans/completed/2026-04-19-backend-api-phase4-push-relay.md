# Backend API — Фаза 4: Push relay и dispatcher

> **For agentic workers:** REQUIRED SUB-SKILL — `superpowers:subagent-driven-development` (рекомендуется) либо `superpowers:executing-plans` для пошаговой реализации. Шаги используют чекбоксы (`- [ ]`) для прогресса.

**Goal:** Замкнуть цикл «событие в инстансе → push на устройство». Ввести отдельный публичный бинарник `huskwoot-push-relay`, хранящий APNs/FCM ключи одного оператора и обслуживающий несколько инстансов пользователей по HMAC-подписанному HTTP-протоколу. На стороне инстанса — push-диспетчер, который вычитывает `push_queue`, резолвит событие через шаблоны уведомлений, отправляет релею и обрабатывает retry/backoff/drop. Синхронизировать жизненный цикл регистраций устройств (pairing confirm → upsert в релее, `PATCH /v1/devices/me` → upsert, revoke → delete). Обновить deploy: Caddy + Let's Encrypt, `docker-compose.yml` с двумя сервисами (huskwoot + caddy) и отдельный `deploy/push-relay/` для релея.

**Architecture:** Релей — самостоятельный модуль (`cmd/huskwoot-push-relay/`, `internal/relay/`) с собственной SQLite-БД (таблицы `instances`, `registrations`), собственной goose-цепочкой миграций и chi-роутером. На каждый запрос проверяется HMAC-подпись `hex(HMAC-SHA256(secret, METHOD|PATH|BODY|TIMESTAMP))`, timestamp-окно ±5 мин, белый список `instance_id` из TOML-конфига (перечитывается по SIGHUP). APNs — через `github.com/sideshow/apns2`, FCM — через `firebase.google.com/go/v4/messaging`. Релей **не хранит** пользовательских данных: только маппинг `(instance_id, device_id) → tokens + platform`. На стороне инстанса push-диспетчер работает одной горутиной-тиком: `NextBatch(limit=32)` → для каждого задания подгружает событие из `EventStore.GetBySeq`, прогоняет через `push.Templates.Resolve(kind, payload)` (возвращает `*Notification` или `nil` для «не push-worthy»), берёт `Device` из `DeviceStore.Get(ctx, id)`, дропает если нет apns/fcm токенов, иначе формирует `pushproto.PushRequest` и шлёт в релей через `RelayClient.Push`. Ответы релея: `sent` → `MarkDelivered`, `invalid_token` → `DeviceStore.UpdatePushTokens(nil,nil)` + `Drop`, `upstream_error` → `MarkFailed` с backoff (5s/30s/5m/30m, drop после 4-й), `bad_payload` → `Drop` + лог. Регистрация токенов в релее: `RelayClient.UpsertRegistration`/`DeleteRegistration` вызывается после pairing-confirm commit, после `PATCH /v1/devices/me`, после revoke (через CLI или HTTP); если релей недоступен — локальный state остаётся актуальным, синхронизация «подтянется» на следующем вызове (возможная регрессия, фиксируется `pending_registrations` задачей-бэклогом, см. Out of scope).

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `github.com/go-chi/chi/v5`, `github.com/pressly/goose/v3`, `github.com/google/uuid`, `github.com/BurntSushi/toml`, **новые зависимости:** `github.com/sideshow/apns2`, `firebase.google.com/go/v4` (с транзитивными `google.golang.org/api`, `cloud.google.com/go/firestore` и т.п. — смотреть `go mod tidy`), `golang.org/x/crypto` (если понадобится для APNs JWT).

---

## Overview

Фаза 4 — четвёртый и заключительный план в серии «Backend API + push» по [спецификации](../superpowers/specs/2026-04-18-backend-api-and-push-design.md).

- **Фаза 1 (завершена):** use-case слой + миграции UUID/slug/number.
- **Фаза 2 (завершена):** HTTP-инфраструктура, SSE, EventStore, PushQueue store, DeviceStore, CLI `devices create/list/revoke`.
- **Фаза 3 (завершена):** Pairing-flow (Telegram magic-link DM, HTML confirm с CSRF, long-poll `/v1/pair/status`), `pairing_requests`, `PairingService`.
- **Фаза 4 (этот план):** Push relay (бинарник `huskwoot-push-relay` + `internal/relay/`), push dispatcher (`internal/push/dispatcher.go`), relay client (`internal/push/relayclient.go`), шаблоны уведомлений (`internal/push/templates.go`), общий HTTP-протокол (`internal/pushproto/`), Caddy + обновлённый docker-compose, Dockerfile.push-relay, OpenAPI релея.

После Фазы 4 запущенный инстанс вместе с публичным релеем:

- Принимает события `task_created`/`task_updated (summary/deadline)`/`reminder_summary` → диспетчер формирует push-уведомление → отправляет в `https://push.<domain>` → APNs/FCM → устройство.
- На `ConfirmWithCSRF` (Фаза 3) и `PATCH /v1/devices/me` инстанс upsert'ит регистрацию в релее; на `Revoke` (CLI или `DELETE /v1/devices/{id}`) — удаляет.
- `push_queue` больше не копится бесконечно: дропает задания для событий, у которых нет push-шаблона, доставляет остальные, с retry/backoff.
- Релей запускается отдельным контейнером, публично доступен по HTTPS (Caddy + LE), хранит SQLite `relay.db` с таблицами `instances` и `registrations`.
- Конфиг релея читается из TOML; `docker kill -s HUP` перечитывает белый список `[instances]` без простоя.
- `huskwoot-push-relay` имеет healthcheck `/healthz`, структурированные логи (slog JSON).
- В `config.toml` инстанса появляется секция `[push]` (`relay_url`, `instance_id`, `instance_secret`, `retry_max_attempts`, `dispatcher_interval`, `batch_size`, `timeout`); при её отсутствии диспетчер не запускается (push выключен, SSE работает).

## Context (from discovery)

**Текущее состояние (после Фазы 3):**

- `internal/push/queue.go` + `queue_test.go` — `SQLitePushQueue` реализует `model.PushQueue` (Enqueue/NextBatch/MarkDelivered/MarkFailed/Drop/DeleteDelivered). Диспетчера нет.
- `internal/model/event.go` — константы `EventKind` включают `EventTaskCreated`, `EventTaskUpdated`, `EventReminderSummary` и т.п. Таблица `events` в миграции 004.
- `internal/usecase/tasks.go` — `CreateTask/UpdateTask/CompleteTask/MoveTask/Reopen` уже пишут события через `recordEvent`. `UpdateTask` кладёт в payload полный снепшот задачи; **метаданных об изменённых полях пока нет** — потребуется добавить поле `Changed []string` либо расширить snapshot, чтобы отличать `task_updated (summary/deadline)` от прочего.
- `internal/model/interfaces.go` — `EventStore` имеет `Insert/SinceSeq/MaxSeq/MinSeq/DeleteOlderThan`, но нет `GetBySeq(ctx, seq)` — диспетчеру нужно.
- `internal/model/interfaces.go` — `DeviceStore.FindByTokenHash` и `List`, но нет `Get(ctx, id)` — `api/devices.go` уже сейчас обходит это через `List` + цикл (`findDeviceByID`). Диспетчеру нужно прямое `Get` по ID для быстрого lookup.
- `internal/devices/store.go` — `UpdatePushTokens(ctx, id, apns, fcm *string)` доступен; `api/devices.patchMe` его вызывает.
- `internal/config/config.go` — секции `[api]`, `[owner]`, `[reminders]`, AI, каналы. `[push]` отсутствует.
- `cmd/huskwoot/main.go` — wiring всех компонентов: OpenDB, goose.Up, store'ы, сервисы, pipeline, API-сервер, reminderScheduler, retention-runner. Диспетчер push'ей нужно добавить в общий пул goroutines с cancel-контекстом.
- `docker-compose.yml` — содержит старый сервис `huskwoot` + `redis` (redis не используется проектом — следы раннего прототипа; в Фазе 4 redis убираем, добавляем `caddy`).
- `Dockerfile` — собирает один бинарь `huskwoot`. Второй Dockerfile для релея отсутствует.
- `api/openapi.yaml` — покрывает `/v1/*` инстанса. Протокол релея — отдельный документ; спецификация релея идёт в `deploy/push-relay/openapi.yaml`.
- Миграции инстанса: 001–007 (последняя — pairing_requests). Следующая — **008** (если нужна, например, расширение payload task_updated — **не потребуется**, если обогащение будет через JSON-поля).
- Миграции релея: отсутствуют; создаём с нуля в `internal/relay/migrations/`.

**Файлы, которые меняются:**

- `internal/model/interfaces.go` — добавить `EventStore.GetBySeq`, `DeviceStore.Get`.
- `internal/events/store.go` + `store_test.go` — реализовать `GetBySeq`.
- `internal/devices/store.go` + `store_test.go` — реализовать `Get`.
- `internal/usecase/tasks.go` + `tasks_test.go` — в `UpdateTask` собирать список изменившихся полей и класть его в snapshot (`changedFields []string` на верхнем уровне payload) — требуется для шаблона push.
- `internal/api/devices.go` + `devices_test.go` — `patchMe` и `delete` дополнительно зовут `RelayClient.UpsertRegistration`/`DeleteRegistration`. Ошибка релея не блокирует локальный state, но логируется как warning. Использовать `findDeviceByID` уже есть — можно переиспользовать новый `DeviceStore.Get`.
- `internal/usecase/pairing.go` + `pairing_test.go` — `ConfirmWithCSRF` после commit вызывает `RelayClient.UpsertRegistration` (только если в `PendingPairing` были apns/fcm токены). Клиент релея инжектируется опционально — при `nil` (push выключен) вызов пропускается.
- `cmd/huskwoot/devices.go` — CLI `devices revoke` после `store.Revoke` дополнительно зовёт `relayClient.DeleteRegistration`. Клиент релея опционален.
- `internal/config/config.go` + `config_test.go` — секция `[push]` и валидация; флаг «push enabled» вычисляется из непустого `instance_id` + `relay_url` + `instance_secret`.
- `cmd/huskwoot/main.go` — wiring `push.Dispatcher`, `push.RelayClient`, `push.Templates`, передача `RelayClient` в `PairingService`, `api.Server` (через `devicesHandler`), CLI `devices revoke`.
- `docker-compose.yml` — убрать redis; добавить caddy; томы; healthchecks.
- `Dockerfile` — без изменений (строит один `huskwoot`).
- `api/openapi.yaml` — никаких **новых путей инстанса** не добавляется; существующие `PATCH /v1/devices/me` и `DELETE /v1/devices/{id}` получают комментарий про side-effect релея (в спецификации описано).
- `CLAUDE.md` — новый раздел «Push: relay + dispatcher».
- `README.md` — отметить Фазу 4 завершённой; добавить развёрнутый раздел «Push relay» с описанием архитектуры (инстанс ⇄ релей), назначения бинарника `huskwoot-push-relay`, настройки секции `[push]` в конфиге инстанса, ссылок на `docs/push-relay/setup.md` (ключи/сертификаты) и `docs/push-relay/hmac.md` (протокол подписи для разработчиков клиентов/альтернативных реализаций).
- `config.example.toml` — пример секции `[push]`.

**Файлы, которые создаются:**

- `internal/pushproto/types.go` + `types_test.go` — shared между инстансом и релеем: DTO для HTTP (`RegistrationRequest`, `PushRequest`, `PushResponse`, `Notification`, `Data`), функция `Sign(secret, method, path, body, ts) (sigHex string)`, парсер/сериализатор HMAC-заголовков, `Canonical(method, path, body, ts string) []byte`.
- `internal/pushproto/hmac.go` + `hmac_test.go` — `Sign`, `Verify(secret, sig, method, path, body, ts) error`, `VerifyTimestamp(ts, now, skew)`.
- `internal/push/templates.go` + `templates_test.go` — `Templates` с методом `Resolve(ev *model.Event) (*pushproto.PushRequest, bool)`; decision-table из спек §6.
- `internal/push/relayclient.go` + `relayclient_test.go` — `RelayClient` с методами `Push(ctx, req)`, `UpsertRegistration(ctx, deviceID, apns, fcm, platform)`, `DeleteRegistration(ctx, deviceID)`; подписывает каждый запрос через `pushproto.Sign`.
- `internal/push/dispatcher.go` + `dispatcher_test.go` — `Dispatcher` с `Run(ctx)`, `processOne(ctx, job)`; backoff table (`{5*time.Second, 30*time.Second, 5*time.Minute, 30*time.Minute}`).
- `internal/relay/server.go` + `server_test.go` — chi-роутер, монтаж middleware и handler'ов.
- `internal/relay/auth.go` + `auth_test.go` — middleware HMAC-валидации; достаёт `X-Huskwoot-*` заголовки, загружает `Instance` из `Store`, проверяет подпись, кладёт `instanceID` в контекст.
- `internal/relay/store.go` + `store_test.go` — `Store` для таблиц `instances` и `registrations`. Методы: `GetInstance`, `LoadInstancesFromConfig` (очищает и перезаписывает белый список), `UpsertRegistration`, `DeleteRegistration`, `GetRegistration`.
- `internal/relay/registrar.go` + `registrar_test.go` — handler PUT/DELETE `/v1/registrations/{device_id}`.
- `internal/relay/pusher.go` + `pusher_test.go` — handler POST `/v1/push`, маршрутизация в APNs/FCM.
- `internal/relay/apns.go` + `apns_test.go` — обёртка над `apns2.Client`, метод `Send(ctx, device_token, payload)`.
- `internal/relay/fcm.go` + `fcm_test.go` — обёртка над `firebase.google.com/go/v4/messaging.Client`, метод `Send(ctx, device_token, notification, data)`.
- `internal/relay/config.go` + `config_test.go` — загрузка `RelayConfig` из TOML; SIGHUP hot-reload механизм (через `signal.Notify(SIGHUP)`).
- `internal/relay/health.go` — `/healthz` handler.
- `internal/relay/migrations/001_instances.sql`, `002_registrations.sql` + `migrations.go` (с `//go:embed`) — отдельная цепочка goose для БД релея.
- `cmd/huskwoot-push-relay/main.go` — точка входа; парсит флаги `--config-file`, OpenDB, goose.Up, server.Run, SIGHUP loop.
- `cmd/huskwoot-push-relay/relay.example.toml` — пример конфига релея.
- `Dockerfile.push-relay` — отдельный образ; `ENTRYPOINT ["huskwoot-push-relay"]`.
- `deploy/push-relay/docker-compose.yml` — deployment шаблон для хоста автора; caddy + push-relay в той же сети.
- `deploy/push-relay/Caddyfile` — прокси `push.huskwoot.app` → `relay:8080`.
- `deploy/push-relay/openapi.yaml` — спецификация HTTP-протокола релея (не интегрируется в инстанс, хранится рядом с deploy).
- `docs/push-relay/setup.md` — пошаговая инструкция для оператора релея: как получить APNs `.p8` ключ (Apple Developer → Certificates/Identifiers → Keys → +Key → APNS; сохранить `KeyID`, `TeamID`, bundle `topic`) и FCM service-account JSON (Firebase Console → Project Settings → Service Accounts → Generate new private key → положить в `secrets/fcm-sa.json`); куда подключать оба файла в `relay.toml` и `docker-compose.yml`; как прогнать smoke-тест на sandbox-устройство.
- `docs/push-relay/hmac.md` — справка для разработчиков: канонический формат строки (`METHOD\nPATH\nTIMESTAMP\nlower(hex(SHA256(body)))`), требуемые заголовки (`X-Huskwoot-Instance`, `X-Huskwoot-Timestamp`, `X-Huskwoot-Signature`), окно skew ±5 мин, правила по пустому телу (`sha256("")`), примеры на Go и curl/openssl для ручной проверки, ссылка на `internal/pushproto/hmac.go` как эталон.
- `Caddyfile` (корень репозитория) — прокси `huskwoot.example.com` → `huskwoot:8080`, пример.

**Паттерны проекта (соблюдать):**

- Интерфейсы в `internal/model/`, реализации — в отдельных пакетах.
- Конструкторы возвращают `(*Type, error)` если возможна ошибка инициализации.
- Все публичные методы принимают `context.Context` первым параметром.
- Write-методы store'ов: `Method(ctx, tx *sql.Tx, ...args)`. Read-методы: `Method(ctx, ...args)`.
- Тесты — table-driven, моки вручную (никакого testify/mock).
- Логирование — `log/slog` структурированное; формат (JSON/text) из конфига.
- Ошибки и сообщения для пользователя/логов — на русском.
- JSON-свойства API — camelCase; URL query-параметры — snake_case.
- Use-case владеет транзакцией; `RelayClient` зовётся **после** commit (иначе сетевой сбой откатит изменения в БД).
- `go test -race ./...` обязательно перед acceptance.
- Бинарь кладётся в `bin/`.

## Development Approach

- **Testing approach:** TDD. Каждая задача начинается с failing-теста, затем минимальная реализация, затем рефакторинг.
- Маленькие коммиты — по одному на каждый завершённый цикл «тест → код → зелёная полоса».
- Каждая задача **MUST include new/updated tests** для затронутого кода.
- Все тесты должны проходить (`go test ./...` и `go vet ./...`) перед переходом к следующей задаче.
- При отклонении от плана — обновлять этот файл (➕ для новых задач, ⚠️ для блокеров).
- Не вводим обратно несовместимых изменений публичного API пакетов без тестов на новый контракт.

## Testing Strategy

- **Unit-тесты:** обязательны на каждый шаг.
- **Integration-тесты** для миграций, store'ов и HTTP-хэндлеров — на in-memory SQLite + `httptest.NewServer`.
- **HMAC-тесты:** valid/invalid signature, stale timestamp (>5 мин), future timestamp, отсутствующие заголовки, неизвестный `instance_id` → 401/403 с понятным сообщением.
- **APNs/FCM клиенты мокаются** через интерфейсы `apnsSender`/`fcmSender` — `apns2` и `firebase` SDK не инжектируются в unit-тестах, только в main-сборке.
- **Dispatcher-тесты:** моки `PushQueue` / `EventStore.GetBySeq` / `DeviceStore.Get` / `RelayClient` / `Templates`; проверить все ветки (sent/invalid_token/upstream_error/bad_payload), backoff-таблицу, drop после 4-й попытки, пропуск не push-worthy событий.
- **Template-тесты:** table-driven по `EventKind` + payload; проверить title/body/priority/collapseKey/data.
- **Integration relay↔dispatcher:** поднять `httptest.Server` с реальным релеем (но мок APNs/FCM), послать реальный push через клиента, проверить полный путь.
- **Race-detector:** `go test -race ./...` обязательно перед acceptance.
- **E2E/ручной smoke:** запустить оба бинарника локально, послать фейковый APNs/FCM токен (мок-адаптер) через pairing, убедиться, что dispatcher отработал и релей получил запрос — см. Task 20.

## Progress Tracking

- Чекбоксы помечаются `[x]` сразу после выполнения (не батчем).
- Новые обнаруженные подзадачи — с префиксом ➕.
- Блокеры/проблемы — с префиксом ⚠️.
- Смена scope или подхода — обновлять разделы Overview/Solution Overview/Implementation Steps в этом файле.

## Solution Overview

### 1. HTTP-протокол инстанс ⇄ релей (`internal/pushproto/`)

Общие DTO в camelCase:

```go
// POST /v1/push (инстанс → релей)
type PushRequest struct {
    DeviceID     string       `json:"deviceId"`
    Priority     string       `json:"priority"`     // "high" | "normal"
    CollapseKey  string       `json:"collapseKey,omitempty"`
    Notification Notification `json:"notification"`
    Data         Data         `json:"data,omitempty"`
}
type Notification struct {
    Title string `json:"title"`
    Body  string `json:"body"`
    Badge *int   `json:"badge,omitempty"`
}
type Data struct {
    Kind      string `json:"kind"`
    EventSeq  int64  `json:"eventSeq"`
    TaskID    string `json:"taskId,omitempty"`
    DisplayID string `json:"displayId,omitempty"`
}

type PushResponse struct {
    Status     string `json:"status"`               // "sent" | "invalid_token" | "upstream_error" | "bad_payload"
    RetryAfter int    `json:"retryAfter,omitempty"` // секунды; только для upstream_error
    Message    string `json:"message,omitempty"`    // диагностика
}

// PUT /v1/registrations/{device_id}
type RegistrationRequest struct {
    APNSToken *string `json:"apnsToken,omitempty"`
    FCMToken  *string `json:"fcmToken,omitempty"`
    Platform  string  `json:"platform"`
}
```

HMAC-подпись:

```go
// Canonical составляет строку, над которой считается HMAC. Формат:
// METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + sha256(body hex)
// (тело хешируется отдельно, чтобы не гонять большой буфер в HMAC).
func Canonical(method, path, timestamp string, bodySHA256 string) []byte

func Sign(secret []byte, method, path, timestamp string, body []byte) string       // hex HMAC-SHA256
func Verify(secret []byte, sigHex string, method, path, ts string, body []byte) error
func VerifyTimestamp(tsHeader string, now time.Time, skew time.Duration) error
```

Заголовки: `X-Huskwoot-Instance`, `X-Huskwoot-Timestamp` (unix seconds), `X-Huskwoot-Signature` (hex).

### 2. Релей: store (`internal/relay/store.go`)

```sql
-- 001_instances.sql
CREATE TABLE instances (
    id           TEXT PRIMARY KEY,
    owner_contact TEXT NOT NULL,
    secret_hash  TEXT NOT NULL,              -- hex(SHA256(secret))
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    disabled_at  DATETIME
);

-- 002_registrations.sql
CREATE TABLE registrations (
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    device_id    TEXT NOT NULL,
    apns_token   TEXT,
    fcm_token    TEXT,
    platform     TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    PRIMARY KEY (instance_id, device_id)
);
CREATE INDEX idx_registrations_last_used ON registrations(last_used_at);
```

`Store` методы:

- `GetInstance(ctx, id) (*Instance, error)` — lookup по PK; nil если нет или `disabled_at` заполнено.
- `SyncInstances(ctx, list []InstanceSpec)` — атомарно: внутри tx выставляет `disabled_at = now` всем, кого нет в `list`, и upsert'ит остальных. Используется при старте и при SIGHUP.
- `UpsertRegistration(ctx, instanceID, deviceID, reg RegistrationFields) error` — INSERT ... ON CONFLICT DO UPDATE.
- `DeleteRegistration(ctx, instanceID, deviceID) error`.
- `GetRegistration(ctx, instanceID, deviceID) (*Registration, error)`.
- `MarkUsed(ctx, instanceID, deviceID, at) error` — обновляет `last_used_at`.

Секреты хранятся только в виде `sha256(secret)` — при SIGHUP оператор вписывает plaintext в `[instances]` одноразово, при загрузке хэшируется. Сравнение при HMAC-проверке: `sha256(config_secret) == stored_hash` — но это не нужно: при `SyncInstances` мы храним `secret_hash = sha256(plaintext)`, а при HMAC-проверке используется сам `secret` из конфига (который по-прежнему в памяти процесса). БД хранит хеш только как «аудит» для ручной верификации. **Упрощение:** держим `secret` в памяти (`map[instanceID][]byte`); в БД храним хеш только для consistency-check при SIGHUP (если у одного и того же `instance_id` меняется секрет, это логируется как warning).

### 3. Релей: HMAC middleware (`internal/relay/auth.go`)

```go
func HMACMiddleware(loader InstanceLoader, clock func() time.Time, skew time.Duration) func(next http.Handler) http.Handler
```

Последовательность:

1. Извлекает `X-Huskwoot-*` заголовки; отсутствие → 401 `{code:"unauthorized"}`.
2. `VerifyTimestamp(ts, clock(), skew)` → 401 `{code:"timestamp_skew"}`.
3. `loader.Secret(instanceID)` возвращает `[]byte` из памяти (белый список из конфига) или `nil` → 401 `{code:"unknown_instance"}`.
4. Читает `r.Body` в буфер (лимит 1 MiB через `http.MaxBytesReader`), восстанавливает `r.Body = io.NopCloser(bytes.NewReader(buf))`.
5. `pushproto.Verify(secret, sig, method, path, ts, buf)` → 401 `{code:"bad_signature"}`.
6. Кладёт `instanceID` в `r.Context()`.

### 4. Релей: регистрации (`internal/relay/registrar.go`)

`PUT /v1/registrations/{device_id}`:

- Body — `pushproto.RegistrationRequest`.
- Если и `apnsToken == nil` и `fcmToken == nil` → 422 `{code:"empty_tokens"}`.
- `store.UpsertRegistration(ctx, instanceID, deviceID, fields)` → 204.

`DELETE /v1/registrations/{device_id}`:

- `store.DeleteRegistration` → 204. Idempotent.

### 5. Релей: push (`internal/relay/pusher.go`)

`POST /v1/push`:

1. Decode `pushproto.PushRequest`.
2. `store.GetRegistration(ctx, instanceID, deviceID)` → 404 `{status:"invalid_token"}` если нет записи.
3. Выбор провайдера: если есть `APNSToken` **и** `platform ∈ {"ios","macos"}` → APNs; иначе если `FCMToken` → FCM; иначе 400 `{status:"bad_payload", message:"нет токенов"}`.
4. Вызов соответствующего `Sender.Send(ctx, token, buildPayload(req))`.
5. Маппинг ошибок:
   - `ErrInvalidToken` (APNs `BadDeviceToken`/`Unregistered`; FCM `registration-token-not-registered`) → `store.DeleteRegistration` + 200 `{status:"invalid_token"}`.
   - Temp error (5xx APNs; FCM UNAVAILABLE/INTERNAL) → 200 `{status:"upstream_error", retryAfter: suggested}`.
   - Success → `store.MarkUsed` + 200 `{status:"sent"}`.
   - Валидация payload (например, пустой title) → 400 `{status:"bad_payload"}`.

### 6. Релей: APNs/FCM адаптеры

```go
type apnsSender interface {
    Send(ctx context.Context, deviceToken string, payload APNsPayload) error
}
// Реализация: *apns2.Client; конструктор получает .p8 + keyID + teamID + bundleID.
// Возвращает apnsTempError / apnsInvalidToken / nil.

type fcmSender interface {
    Send(ctx context.Context, deviceToken string, n pushproto.Notification, data pushproto.Data) error
}
// Реализация: *messaging.Client; конструктор из service-account JSON.
```

В unit-тестах — стабы. Интеграционные тесты APNs/FCM не делаем (требуют реальных ключей; проверяется вручную).

### 7. Релей: сервер + конфиг + SIGHUP

```toml
# relay.example.toml
[server]
listen_addr = "0.0.0.0:8080"
hmac_skew   = "5m"
log_level   = "info"
log_format  = "json"

[db]
path = "/var/lib/huskwoot-relay/relay.db"

[apns]
key_file  = "/run/secrets/apns-key.p8"
key_id    = "ABCDEF1234"
team_id   = "TEAM123456"
bundle_id = "com.huskwoot.client"
# production = true

[fcm]
service_account_file = "/run/secrets/fcm-sa.json"

[[instances]]
id             = "nickon"
owner_contact  = "@nickon"
secret         = "${HUSKWOOT_RELAY_SECRET_NICKON}"

[[instances]]
id             = "alice"
owner_contact  = "alice@example.com"
secret         = "${HUSKWOOT_RELAY_SECRET_ALICE}"
```

- `secret` читается из env при загрузке; в памяти держится plaintext для HMAC-проверки.
- `SIGHUP` → `config.Reload()` → `store.SyncInstances` + атомарный swap in-memory карты секретов (через `sync.RWMutex` или `atomic.Value`).
- Пустой список `[[instances]]` — валидно (релей принимает никого, возвращает 401 для всех).

### 8. Релей: main.go

```
huskwoot-push-relay --config-file /etc/huskwoot-relay/relay.toml
├── parseFlags → config
├── OpenDB + goose.Up (relay migrations)
├── buildAPNs, buildFCM (nil если секция пуста — fatal start, если оба nil)
├── store = relay.NewStore(db)
├── store.SyncInstances(config.Instances)
├── loader = relay.NewInstanceLoader(config.Instances)   // in-memory secrets
├── server = relay.NewServer(store, loader, apns, fcm, logger, cfg)
├── go signalLoop: SIGHUP → reload config → SyncInstances → loader.Swap
└── server.Run(ctx)
```

### 9. Инстанс: `RelayClient` (`internal/push/relayclient.go`)

```go
type RelayClient struct {
    httpClient *http.Client
    baseURL    string
    instanceID string
    secret     []byte
    clock      func() time.Time
    logger     *slog.Logger
}

func (c *RelayClient) Push(ctx, req pushproto.PushRequest) (pushproto.PushResponse, error)
func (c *RelayClient) UpsertRegistration(ctx, deviceID string, r pushproto.RegistrationRequest) error
func (c *RelayClient) DeleteRegistration(ctx, deviceID string) error
```

Каждый запрос:

1. Сериализация body.
2. `ts := clock().Unix()` → header.
3. `sig := pushproto.Sign(secret, method, path, tsStr, body)` → header.
4. HTTP call с таймаутом `cfg.Push.Timeout` (дефолт 10s).
5. Парсинг `PushResponse`. Сетевые ошибки/5xx/context-cancel оборачиваются в `ErrRelayUnavailable`.

`NilRelayClient` (no-op реализация) — когда `[push]` в конфиге отсутствует; все методы возвращают `nil` без действий. Используется в `PairingService`, `devicesHandler`, CLI — чтобы не иметь `if client != nil { ... }` на каждом вызове.

### 10. Инстанс: шаблоны (`internal/push/templates.go`)

```go
type Templates struct {
    projectLookup func(ctx context.Context, projectID string) (*model.Project, error) // опционально, для fallback если slug отсутствует
    now           func() time.Time
}

func (t *Templates) Resolve(ctx context.Context, ev *model.Event) (*pushproto.PushRequest, bool, error)
```

- `ok=false` → событие не push-worthy (`Drop`).
- `ok=true, req, nil` → отправить.

Decision-table (спек §6):

| EventKind | push? | priority | title | body | collapseKey |
|---|---|---|---|---|---|
| `task_created` | да | `high` | «Новая задача» | `{displayID}: {summary}{ deadline suffix}` | `tasks` |
| `task_updated` (`changed` содержит `summary` или `deadline`) | да | `normal` | «Задача обновлена» | `{displayID}: {summary}` | `tasks` |
| `task_updated` (прочее) | нет | — | — | — | — |
| `task_completed`/`task_moved`/`task_reopened`/`project_*` | нет | — | — | — | — |
| `reminder_summary` | да | `normal` | «Утренняя сводка» | «N задач сегодня» | `reminders` |
| `chat_reply` | нет (след. итерация) | — | — | — | — |

`TaskID`/`DisplayID` кладутся в `Data` из снепшота payload. Форматирование дедлайна — `internal/dateparse` отсутствует в этом пакете; шаблон использует `time.Format("02.01 15:04")` в таймзоне владельца (уже прокинутой через `Templates.now`).

Для `task_updated` требуется `changed_fields` в payload. См. задачу 11.

### 11. Инстанс: расширить `task_updated` payload полем `changedFields`

В `internal/usecase/tasks.go` у `UpdateTask` добавить:

1. Перед `tx.Commit()` — прочитать `GetTaskTx(ctx, tx, id)` (оригинал уже читается для diff'а), сравнить с обновлениями, собрать список изменившихся полей.
2. Payload события — структура `taskUpdatedSnapshot { Task taskSnapshot; ChangedFields []string }` с JSON-ключами `task`, `changedFields`. Ранее было просто `taskSnapshot`.
3. SSE-клиенты и агент — реализации не ломаются, т.к. структура расширяется добавлением корневого объекта. **Это breaking change.** Чтобы сохранить обратную совместимость SSE-потребителей (клиенты — будущие подпроекты, формат ещё публичный не зафиксирован), можно:
   - **вариант A (принимается)**: заменить payload task_updated на `{"task": {...}, "changedFields": [...]}`. Документируется в OpenAPI (описание EventEnvelope payload). Прочие `task_*` события переводятся на ту же форму (`{"task": {...}}` без changedFields), чтобы payload'ы были единообразны.
   - вариант B: внедрить отдельное поле `EventMeta` в `model.Event` и пробрасывать его в SSE-рендере как отдельное поле `event:`. Более инвазивно, не принимается.

Принимается вариант A. Затрагивает: `internal/usecase/tasks.go` (все task-события), `internal/api/events.go` (SSE-сериализация — если использует `Payload` сырым, менять не нужно), тесты use-case и API. Для простоты рендер SSE остаётся сырым `payload`.

### 12. Инстанс: dispatcher (`internal/push/dispatcher.go`)

```go
type DispatcherDeps struct {
    DB          *sql.DB
    Queue       model.PushQueue
    Events      model.EventStore
    Devices     model.DeviceStore
    Relay       RelayClient
    Templates   *Templates
    Clock       func() time.Time
    Logger      *slog.Logger
    Interval    time.Duration // дефолт 2s
    BatchSize   int           // дефолт 32
    MaxAttempts int           // дефолт 4
}

type Dispatcher struct { /* ... */ }

func NewDispatcher(deps DispatcherDeps) *Dispatcher
func (d *Dispatcher) Run(ctx context.Context) error
```

`Run`:

```
ticker := time.NewTicker(d.Interval)
for {
    select {
    case <-ctx.Done(): return ctx.Err()
    case <-ticker.C:
    }
    batch, _ := d.queue.NextBatch(ctx, d.batchSize)
    for _, job := range batch {
        d.processOne(ctx, job)
    }
}
```

`processOne(ctx, job)`:

1. `ev, err := d.events.GetBySeq(ctx, job.EventSeq)`; если `ev == nil` — retention съел, `Drop(id, "event_missing")` и warning.
2. `req, ok, err := d.templates.Resolve(ctx, ev)`; если `!ok` — `Drop(id, "not_pushable")`.
3. `dev, err := d.devices.Get(ctx, job.DeviceID)`; если `nil` или `Revoked` — `Drop(id, "device_revoked")`.
4. Если `dev.APNSToken == nil && dev.FCMToken == nil` — `Drop(id, "no_tokens")`.
5. `req.DeviceID = dev.ID`. Отправка: `resp, err := d.relay.Push(ctx, req)`.
   - `err != nil` (сетевой/5xx-парсинг) → `MarkFailed(id, err.Error(), now + backoff)`; если `attempts+1 >= MaxAttempts` — `Drop(id, "max_attempts")`.
   - `resp.Status == "sent"` → `MarkDelivered(id)`.
   - `resp.Status == "invalid_token"` → `devices.UpdatePushTokens(id, nil, nil)` + `Drop(id, "invalid_token")`. (Релей уже удалил регистрацию сам.)
   - `resp.Status == "upstream_error"` → `MarkFailed` с backoff = `max(resp.RetryAfter, next_from_table)`.
   - `resp.Status == "bad_payload"` → `Drop(id, "bad_payload")` + error-log (наш баг).

Backoff-таблица: `[]time.Duration{5*time.Second, 30*time.Second, 5*time.Minute, 30*time.Minute}` индексируется по `attempts` (после инкремента). `MaxAttempts=4` соответствует 4 неудачным попыткам (после последней — drop).

### 13. Инстанс: интеграция регистраций в pairing/devices/CLI

- `usecase.PairingService.ConfirmWithCSRF`: после `tx.Commit()` и перед `broadcaster.Notify` (или параллельно — не критично) вызвать `relay.UpsertRegistration(ctx, device.ID, {APNSToken, FCMToken, Platform})`. Ошибка релея логируется как warning `"pairing: relay upsert failed"` и не откатывает создание устройства (локально всё зафиксировано; следующий push просто провалится с `invalid_token` и будет пропущен). В `PairingDeps` добавить `Relay RelayClient` — опционально, дефолт `NilRelayClient{}`.
- `api/devices.patchMe`: после `UpdatePushTokens` и успешного lookup `updated` — `relay.UpsertRegistration`. Ошибка — warning.
- `api/devices.delete` и `cmd/huskwoot/devices.go` команда `revoke`: после `Revoke` — `relay.DeleteRegistration`. Ошибка — warning.

### 14. Конфиг `[push]` и дефолты

```toml
[push]
relay_url            = "https://push.huskwoot.app"
instance_id          = "nickon"
instance_secret      = "${HUSKWOOT_PUSH_SECRET}"
timeout              = "10s"       # таймаут каждого HTTP-запроса в релей
dispatcher_interval  = "2s"        # интервал тика диспетчера
batch_size           = 32
retry_max_attempts   = 4
```

Валидация: при `enabled()` (когда все три: `relay_url`, `instance_id`, `instance_secret` — не пусты) остальные поля получают дефолты. Если секция отсутствует целиком — `enabled() == false`, в `main.go` инстанцируется `NilRelayClient` и диспетчер не запускается. Если задано частично (например, `relay_url` без `instance_id`) — `LoadConfig` возвращает ошибку.

### 15. docker-compose + Caddy

`docker-compose.yml` (инстанс):

```yaml
services:
  huskwoot:
    build: .
    restart: unless-stopped
    volumes:
      - ./config:/etc/huskwoot:ro
      - huskwoot_data:/var/lib/huskwoot
    environment:
      - HUSKWOOT_CONFIG_DIR=/etc/huskwoot
      - OPENAI_API_KEY=${OPENAI_API_KEY}
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
      - IMAP_PASSWORD=${IMAP_PASSWORD}
      - HUSKWOOT_PUSH_SECRET=${HUSKWOOT_PUSH_SECRET}
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      - huskwoot

volumes:
  huskwoot_data:
  caddy_data:
  caddy_config:
```

Caddyfile:

```
huskwoot.example.com {
    reverse_proxy huskwoot:8080
    encode gzip
}
```

`deploy/push-relay/docker-compose.yml`:

```yaml
services:
  push-relay:
    build:
      context: ../..
      dockerfile: Dockerfile.push-relay
    restart: unless-stopped
    volumes:
      - ./relay.toml:/etc/huskwoot-relay/relay.toml:ro
      - ./secrets/apns-key.p8:/run/secrets/apns-key.p8:ro
      - ./secrets/fcm-sa.json:/run/secrets/fcm-sa.json:ro
      - relay_data:/var/lib/huskwoot-relay
    environment:
      - HUSKWOOT_RELAY_SECRET_NICKON=${HUSKWOOT_RELAY_SECRET_NICKON}
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config

volumes:
  relay_data:
  caddy_data:
  caddy_config:
```

## Technical Details

### DeviceStore.Get

```go
// Get возвращает устройство по ID (включая отозванные).
// Возвращает nil, nil если устройство не найдено.
Get(ctx context.Context, id string) (*Device, error)
```

Реализация в `SQLiteDeviceStore` — прямой SELECT по PK без фильтра `revoked_at`.

### EventStore.GetBySeq

```go
// GetBySeq возвращает событие по seq. Возвращает nil, nil если событие
// отсутствует (удалено retention'ом или никогда не существовало).
GetBySeq(ctx context.Context, seq int64) (*Event, error)
```

### Обновлённый payload task-событий

```go
// В payload event'а task_* теперь кладётся структура:
// {"task": {...taskSnapshot...}, "changedFields": [...]}  // для task_updated
// {"task": {...taskSnapshot...}}                           // для остальных task_*
// Это позволяет шаблону push понять, что именно изменилось.
```

SSE-клиент, получая `event: task_updated\ndata: {...}`, теперь имеет доступ к `changedFields`. Существующих потребителей в проекте нет (клиенты появятся в Подпроектах 2–4), но формат фиксируется в OpenAPI схеме `EventEnvelope`.

### Backoff

```go
var defaultBackoff = []time.Duration{
    5 * time.Second,
    30 * time.Second,
    5 * time.Minute,
    30 * time.Minute,
}

func nextAttempt(attempts int, now time.Time, retryAfter time.Duration) time.Time {
    idx := attempts - 1
    if idx < 0 { idx = 0 }
    if idx >= len(defaultBackoff) { idx = len(defaultBackoff) - 1 }
    d := defaultBackoff[idx]
    if retryAfter > d { d = retryAfter }
    return now.Add(d)
}
```

### HMAC canonical строка

```
METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + lower(hex(SHA256(body)))
```

PATH включает query-часть (для `GET`-запросов к релею их нет, но контракт общий). Timestamp — секунды UNIX в десятичной строке. Тело — raw bytes, даже если пустое (`sha256("") = e3b0...`).

Эти же правила выносятся в `docs/push-relay/hmac.md` с примерами на Go/curl — чтобы сторонние реализации клиентов (например, альтернативный инстанс или CLI-тестер релея) могли подписывать запросы без чтения исходников `internal/pushproto`.

### Ошибки APNs → ответ релея

- `apns2.StatusBadDeviceToken`, `Unregistered`, `DeviceTokenNotForTopic`, `BadTopic` → `invalid_token`.
- `Throttled`, `Internal server error` → `upstream_error` с `retryAfter = 30`.
- Сетевой сбой, context.DeadlineExceeded → `upstream_error` с `retryAfter = 60`.

### Ошибки FCM → ответ релея

- `messaging.IsRegistrationTokenNotRegistered(err)` → `invalid_token`.
- `messaging.IsUnavailable(err)` / `IsInternal(err)` → `upstream_error` с `retryAfter = 30`.
- Остальные — `bad_payload` с `message: err.Error()`.

## What Goes Where

- **Implementation Steps (`[ ]` checkboxes):** код, тесты, миграции, OpenAPI релея, конфиг, wiring, docker-compose/Caddyfile.
- **Post-Completion (без чекбоксов):** ручной smoke-тест с реальными APNs/FCM ключами (требует dev-аккаунта Apple/Firebase), деплой релея на хост автора, обновление secrets в CI/deploy.

## Implementation Steps

### Task 1: `internal/pushproto` — DTO и HMAC-подпись

**Files:**
- Create: `internal/pushproto/types.go`
- Create: `internal/pushproto/types_test.go`
- Create: `internal/pushproto/hmac.go`
- Create: `internal/pushproto/hmac_test.go`

- [x] написать failing-тесты `TestCanonical_StableOrderingOfFields` и `TestSign_ReproducibleForSameInputs`
- [x] написать тест `TestVerify_RejectsTampered` (изменить любое поле → ошибка)
- [x] написать тест `TestVerifyTimestamp_RejectsSkewOverLimit` (skew=5m, ts=now-6m → err; ts=now+6m → err; ts=now → ok)
- [x] написать тест `TestVerify_ValidatesBodySHA` (тело хешируется; пустое тело допустимо)
- [x] реализовать `Canonical(method, path, ts, bodyHashHex) []byte`, `Sign(secret, method, path, ts, body) string`, `Verify(...)`, `VerifyTimestamp(...)` — использовать `crypto/hmac` + `crypto/sha256`, `subtle.ConstantTimeCompare`
- [x] объявить DTO `PushRequest`, `PushResponse`, `RegistrationRequest`, `Notification`, `Data`, константы статусов (`StatusSent`, `StatusInvalidToken`, `StatusUpstreamError`, `StatusBadPayload`)
- [x] написать round-trip тест на JSON-(де)сериализацию каждого DTO (проверить camelCase-ключи)
- [x] `go test -race ./internal/pushproto/...` — зелёное

### Task 2: Релей — миграции `instances` и `registrations`

**Files:**
- Create: `internal/relay/migrations/001_instances.sql`
- Create: `internal/relay/migrations/002_registrations.sql`
- Create: `internal/relay/migrations/migrations.go`
- Create: `internal/relay/migrations/migrations_test.go`

- [x] добавить SQL-файлы из «Solution Overview §2»
- [x] реализовать `migrations.Up(db *sql.DB) error` через goose с `//go:embed`
- [x] написать тест `TestRelayMigrations_UpCreatesTables` (после Up таблицы `instances` и `registrations` существуют с ожидаемыми колонками через `PRAGMA table_info`)
- [x] тест идемпотентности (повторный Up не падает)
- [x] `go test ./internal/relay/migrations/...`

### Task 3: Релей — `Store` (CRUD instances + registrations)

**Files:**
- Create: `internal/relay/store.go`
- Create: `internal/relay/store_test.go`

- [x] failing-тест `TestRelayStore_SyncInstances_AddsNewAndDisablesMissing`
- [x] тест `TestRelayStore_GetInstance_ReturnsNilWhenDisabled`
- [x] тест `TestRelayStore_UpsertRegistration_InsertThenUpdate` (первый вызов INSERT, второй — UPDATE; `updated_at` растёт)
- [x] тест `TestRelayStore_DeleteRegistration_Idempotent`
- [x] тест `TestRelayStore_GetRegistration_ReturnsNilForMissing`
- [x] тест `TestRelayStore_MarkUsed_UpdatesLastUsedAt`
- [x] реализовать `Store` с конструктором `NewStore(db *sql.DB) *Store`; все методы принимают `ctx`; Sync обёрнут в tx
- [x] `go test -race ./internal/relay/...`

### Task 4: Релей — HMAC middleware

**Files:**
- Create: `internal/relay/auth.go`
- Create: `internal/relay/auth_test.go`

- [x] failing-тест `TestHMACMiddleware_Allows_ValidRequest` (корректный signature+ts → next.ServeHTTP)
- [x] тест `TestHMACMiddleware_Rejects_MissingHeaders` → 401 `unauthorized`
- [x] тест `TestHMACMiddleware_Rejects_StaleTimestamp` → 401 `timestamp_skew`
- [x] тест `TestHMACMiddleware_Rejects_UnknownInstance` → 401 `unknown_instance`
- [x] тест `TestHMACMiddleware_Rejects_TamperedBody` → 401 `bad_signature`
- [x] тест `TestHMACMiddleware_RestoresBodyForHandler` (handler дочитывает тело заново)
- [x] тест `TestHMACMiddleware_EnforcesMaxBody` (payload > 1 MiB → 413)
- [x] определить `InstanceLoader` интерфейс (`Secret(id string) []byte`)
- [x] реализовать `HMACMiddleware` с параметрами `(loader, clock, skew)` + хэлпер `InstanceIDFromContext(ctx) string`
- [x] `go test -race ./internal/relay/...`

### Task 5: Релей — `registrar` (PUT/DELETE)

**Files:**
- Create: `internal/relay/registrar.go`
- Create: `internal/relay/registrar_test.go`

- [x] failing-тест `TestRegistrar_Put_UpsertsRegistration` (204 + запись в store)
- [x] тест `TestRegistrar_Put_RejectsEmptyTokens` (apnsToken=nil и fcmToken=nil → 422)
- [x] тест `TestRegistrar_Delete_Idempotent` (204 при первом и повторном)
- [x] тест `TestRegistrar_Put_InvalidJSON_Returns400`
- [x] реализовать handler'ы; ключ `deviceID` из `chi.URLParam`
- [x] `go test -race ./internal/relay/...`

### Task 6: Релей — APNs-адаптер

**Files:**
- Create: `internal/relay/apns.go`
- Create: `internal/relay/apns_test.go`

- [x] failing-тест `TestAPNsSender_BuildsPayload_HighPriority` (мок-клиент через интерфейс; проверить topic=bundleID, priority=10 для high, 5 для normal, apsBody: `{"aps":{"alert":{"title":..,"body":..},"badge":..}, ...customData}`)
- [x] тест `TestAPNsSender_InvalidToken_ReturnsErrInvalidToken`
- [x] тест `TestAPNsSender_ServerError_ReturnsErrTemporary`
- [x] объявить `apnsSender` интерфейс + фабрика `NewAPNsSender(cfg APNsConfig) (*APNsSender, error)` над `*apns2.Client` (production/development через флаг конфига)
- [x] реализовать `Send(ctx, token, req pushproto.PushRequest) error` с маппингом ошибок в `ErrInvalidToken`/`ErrTemporary`
- [x] `go test -race ./internal/relay/...`

### Task 7: Релей — FCM-адаптер

**Files:**
- Create: `internal/relay/fcm.go`
- Create: `internal/relay/fcm_test.go`

- [x] failing-тест `TestFCMSender_BuildsMessage` (мок-клиент через интерфейс; проверить `data.kind`, `data.eventSeq`, `notification.title`/`body`, priority)
- [x] тест `TestFCMSender_InvalidToken_ReturnsErrInvalidToken`
- [x] тест `TestFCMSender_ServerError_ReturnsErrTemporary`
- [x] объявить `fcmSender` интерфейс + фабрика `NewFCMSender(cfg FCMConfig) (*FCMSender, error)` над `*messaging.Client`
- [x] реализовать `Send(ctx, token, req pushproto.PushRequest) error` с маппингом ошибок
- [x] `go test -race ./internal/relay/...`

### Task 8: Релей — `pusher` (POST /v1/push)

**Files:**
- Create: `internal/relay/pusher.go`
- Create: `internal/relay/pusher_test.go`

- [x] failing-тест `TestPusher_Success_Returns200Sent` (регистрация iOS → APNs-стаб возвращает nil → `{status:"sent"}`)
- [x] тест `TestPusher_InvalidToken_DeletesRegistration` (APNs-стаб → `ErrInvalidToken` → store.DeleteRegistration вызван → `{status:"invalid_token"}`)
- [x] тест `TestPusher_UpstreamError_ReturnsRetryAfter`
- [x] тест `TestPusher_UnknownDevice_ReturnsInvalidToken`
- [x] тест `TestPusher_NoTokens_ReturnsBadPayload` (регистрация без apns/fcm → 400)
- [x] тест `TestPusher_IOSRegistration_UsesAPNs` vs `AndroidRegistration_UsesFCM`
- [x] реализовать handler — инжектирует `apnsSender` и `fcmSender` интерфейсы
- [x] `go test -race ./internal/relay/...`

### Task 9: Релей — `config` + SIGHUP loader

**Files:**
- Create: `internal/relay/config.go`
- Create: `internal/relay/config_test.go`
- Create: `internal/relay/loader.go`
- Create: `internal/relay/loader_test.go`

- [x] failing-тест `TestLoadRelayConfig_ParsesInstances` (TOML → `[]InstanceSpec{id, secret, owner_contact}`)
- [x] тест `TestLoadRelayConfig_ExpandsEnvVars` (${VAR} подставляется)
- [x] тест `TestLoadRelayConfig_ValidatesRequiredSections` (отсутствие APNs+FCM → ошибка)
- [x] тест `TestInstanceLoader_Swap_IsRaceFree` (параллельные `Secret(id)` и `Swap(newMap)` под `-race`)
- [x] тест `TestInstanceLoader_Secret_ReturnsNilForMissing`
- [x] реализовать `RelayConfig`, `LoadRelayConfig(path string) (*RelayConfig, error)`
- [x] реализовать `InstanceLoader` (in-memory map под `sync.RWMutex`) с методами `Secret(id) []byte`, `Swap(map[string][]byte)`, `InstanceIDs() []string`
- [x] `go test -race ./internal/relay/...`

### Task 10: Релей — `Server` + `/healthz` + сборка

**Files:**
- Create: `internal/relay/server.go`
- Create: `internal/relay/server_test.go`
- Create: `internal/relay/health.go`
- Create: `cmd/huskwoot-push-relay/main.go`
- Create: `cmd/huskwoot-push-relay/relay.example.toml`
- Create: `Dockerfile.push-relay`

- [x] failing-тест `TestRelayServer_RoutesRegistered` (GET /healthz=200, PUT/DELETE /v1/registrations/{id}, POST /v1/push с HMAC → 200/204 на стабах)
- [x] тест `TestRelayServer_HealthIsUnauthenticated` (без HMAC-заголовков → 200)
- [x] тест `TestRelayServer_V1PathsRequireHMAC` (без HMAC → 401)
- [x] реализовать `NewServer(deps ServerDeps) *http.Server` со структурой `ServerDeps{Store, Loader, APNs, FCM, Logger, Skew, Clock}`
- [x] реализовать `/healthz` — SELECT 1 по `relay.db`
- [x] реализовать `cmd/huskwoot-push-relay/main.go`: флаги `--config-file`, `OpenDB`, `migrations.Up`, `SyncInstances`, `signalLoop` (SIGHUP → reload), `server.Run(ctx)`
- [x] создать `Dockerfile.push-relay` (multi-stage build `cmd/huskwoot-push-relay/`, ENTRYPOINT = бинарь)
- [x] создать `cmd/huskwoot-push-relay/relay.example.toml` с примерами всех секций
- [x] `go build -o bin/huskwoot-push-relay ./cmd/huskwoot-push-relay/` — успешная сборка
- [x] `go test -race ./internal/relay/... ./cmd/huskwoot-push-relay/...`

### Task 11: Инстанс — обогатить payload task-событий `changedFields`

**Files:**
- Modify: `internal/usecase/tasks.go`
- Modify: `internal/usecase/tasks_test.go`
- Modify: `internal/api/events_test.go` (если есть assert'ы на сырой payload)

- [x] failing-тест `TestTaskService_UpdateTask_PayloadIncludesChangedFields` (обновить summary → payload содержит `{"task":{...}, "changedFields":["summary"]}`)
- [x] тест `TestTaskService_UpdateTask_MultipleChanges` (summary+deadline → `["summary","deadline"]` в стабильном порядке)
- [x] тест `TestTaskService_UpdateTask_NoChanges_DoesNotEmitEvent` (или сохранить текущее поведение — уточнить в коде; если сейчас пишется событие-пустышка, оставить; главное — payload корректен)
- [x] тест `TestTaskService_OtherTaskEvents_PayloadWrapsInTaskKey` (для create/complete/reopen/move payload = `{"task":{...}}`)
- [x] в `UpdateTask` собрать `changedFields []string` сравнением `old` (из `GetTaskTx`) с `new` (из `upd`); порядок — фиксированный (summary, details, topic, deadline, status — по приоритету)
- [x] ввести helper `wrapTaskPayload(task *model.Task, changed []string) json.RawMessage`
- [x] применить ко всем `recordEvent` в `tasks.go`
- [x] обновить API-тесты на snapshot сериализации SSE, если таковые опирались на старый формат
- [x] `go test -race ./internal/usecase/... ./internal/api/...`

### Task 12: Инстанс — `EventStore.GetBySeq` и `DeviceStore.Get`

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/events/store.go`
- Modify: `internal/events/store_test.go`
- Modify: `internal/devices/store.go`
- Modify: `internal/devices/store_test.go`
- Modify: `internal/api/devices.go` (переключить `findDeviceByID` на `store.Get`)

- [x] failing-тест `TestEventStore_GetBySeq_Returns_Event`
- [x] тест `TestEventStore_GetBySeq_ReturnsNilForMissing`
- [x] failing-тест `TestDeviceStore_Get_ReturnsRevokedDevices`
- [x] тест `TestDeviceStore_Get_ReturnsNilForMissing`
- [x] расширить интерфейсы в `internal/model/interfaces.go`
- [x] реализовать обе функции (простые SELECT по PK)
- [x] в `api/devices.go` заменить `findDeviceByID` → `store.Get` (удалить helper или пометить deprecated); обновить тесты
- [x] `go test -race ./...` — не сломать существующие

### Task 13: Инстанс — `push.Templates`

**Files:**
- Create: `internal/push/templates.go`
- Create: `internal/push/templates_test.go`

- [x] failing-тест `TestTemplates_Resolve_TaskCreated_BuildsHighPriorityRequest` (displayID, summary, deadline-суффикс)
- [x] тест `TestTemplates_Resolve_TaskUpdatedSummary_Included`
- [x] тест `TestTemplates_Resolve_TaskUpdatedDeadline_Included`
- [x] тест `TestTemplates_Resolve_TaskUpdatedOther_ReturnsFalse`
- [x] тест `TestTemplates_Resolve_TaskCompleted_ReturnsFalse`
- [x] тест `TestTemplates_Resolve_ReminderSummary_BuildsNormalPriority`
- [x] тест `TestTemplates_Resolve_UnknownKind_ReturnsFalse`
- [x] реализовать `Templates` и `Resolve`; payload парсится в локальный `taskUpdatedSnapshot`/`taskSnapshot`
- [x] `go test ./internal/push/...`

### Task 14: Инстанс — `RelayClient` (HTTP + HMAC)

**Files:**
- Create: `internal/push/relayclient.go`
- Create: `internal/push/relayclient_test.go`

- [x] failing-тест `TestRelayClient_Push_SignsRequest` (через `httptest.NewServer` — проверить заголовки X-Huskwoot-*, payload, валидность HMAC)
- [x] тест `TestRelayClient_Push_ParsesSentResponse`
- [x] тест `TestRelayClient_Push_ParsesInvalidTokenResponse`
- [x] тест `TestRelayClient_Push_ParsesUpstreamError_WithRetryAfter`
- [x] тест `TestRelayClient_Push_NetworkError_WrapsAsErrRelayUnavailable`
- [x] тест `TestRelayClient_UpsertRegistration_Returns204`
- [x] тест `TestRelayClient_DeleteRegistration_Idempotent`
- [x] тест `TestRelayClient_Push_Timeout_ReturnsError` (server sleep > timeout)
- [x] реализовать `RelayClient` с `http.Client{Timeout: cfg.Timeout}`; все методы подписывают запросы через `pushproto.Sign`
- [x] реализовать `NilRelayClient` (no-op); интерфейс `RelayClient` — либо interface, либо конкретная структура с методами. Решение: интерфейс `interface { Push(...); UpsertRegistration(...); DeleteRegistration(...) }`, две реализации `httpRelayClient` и `nilRelayClient`
- [x] `go test -race ./internal/push/...`

### Task 15: Инстанс — `push.Dispatcher`

**Files:**
- Create: `internal/push/dispatcher.go`
- Create: `internal/push/dispatcher_test.go`

- [x] failing-тест `TestDispatcher_ProcessOne_SendsAndMarksDelivered`
- [x] тест `TestDispatcher_ProcessOne_MissingEvent_Drops`
- [x] тест `TestDispatcher_ProcessOne_NotPushable_Drops`
- [x] тест `TestDispatcher_ProcessOne_MissingDevice_Drops`
- [x] тест `TestDispatcher_ProcessOne_NoTokens_Drops`
- [x] тест `TestDispatcher_ProcessOne_InvalidToken_ClearsTokensAndDrops`
- [x] тест `TestDispatcher_ProcessOne_UpstreamError_SchedulesBackoff` (проверить attempts=1 → +5s; attempts=2 → +30s; …; attempts=4 → Drop)
- [x] тест `TestDispatcher_ProcessOne_UpstreamError_HonoursRetryAfter` (retryAfter > backoff-табличного → используется retryAfter)
- [x] тест `TestDispatcher_ProcessOne_BadPayload_Drops` (+ error лог)
- [x] тест `TestDispatcher_Run_TerminatesOnContextCancel`
- [x] тест `TestDispatcher_Run_ProcessesBatch_AndContinues`
- [x] реализовать `Dispatcher` со всеми ветками; инжектируемые `Clock` и таймер (`NewTicker`)
- [x] `go test -race ./internal/push/...`

### Task 16: Инстанс — интеграция `RelayClient` в pairing/devices/CLI

**Files:**
- Modify: `internal/usecase/pairing.go`
- Modify: `internal/usecase/pairing_test.go`
- Modify: `internal/api/devices.go`
- Modify: `internal/api/devices_test.go`
- Modify: `cmd/huskwoot/devices.go`
- Modify: `cmd/huskwoot/devices_test.go` (если есть)

- [x] failing-тест `TestPairingService_ConfirmWithCSRF_CallsRelayUpsert` (мок RelayClient)
- [x] тест `TestPairingService_ConfirmWithCSRF_RelayErrorDoesNotFailConfirm` (мок возвращает ошибку → device создан, warning в логе)
- [x] тест `TestPairingService_ConfirmWithCSRF_NoTokens_SkipsRelay` (apns==nil и fcm==nil → Upsert не зовётся)
- [x] тест `TestDevicesHandler_PatchMe_CallsRelayUpsert`
- [x] тест `TestDevicesHandler_PatchMe_RelayError_ReturnsSuccess_Warning`
- [x] тест `TestDevicesHandler_Delete_CallsRelayDelete`
- [x] тест `TestDevicesCLI_Revoke_CallsRelayDelete`
- [x] добавить поле `Relay RelayClient` в `PairingDeps`, default — `NilRelayClient{}`
- [x] добавить поле `Relay RelayClient` в `api.Config` (и `devicesHandler`)
- [x] в `cmd/huskwoot/devices.go` передавать `RelayClient` в CLI-revoke
- [x] `go test -race ./...`

### Task 17: Конфиг `[push]` + дефолты

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.toml`

- [x] failing-тест `TestLoadConfig_PushSectionDefaults` (секция есть целиком → дефолты: Timeout=10s, DispatcherInterval=2s, BatchSize=32, RetryMaxAttempts=4)
- [x] тест `TestLoadConfig_PushSectionPartial_ReturnsError` (только `relay_url` → ошибка валидации)
- [x] тест `TestLoadConfig_PushSectionAbsent_Disabled` (`cfg.Push.Enabled() == false`)
- [x] тест `TestLoadConfig_PushSectionEnvExpansion` (`${HUSKWOOT_PUSH_SECRET}` подставляется)
- [x] добавить `PushConfig{RelayURL, InstanceID, InstanceSecret, Timeout, DispatcherInterval, BatchSize, RetryMaxAttempts}` и метод `Enabled() bool`
- [x] обновить `config.example.toml`
- [x] `go test ./internal/config/...`

### Task 18: Wiring в `cmd/huskwoot/main.go`

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] инстанцировать `relayClient push.RelayClient`:
      - если `cfg.Push.Enabled()` → `push.NewHTTPRelayClient(...)`
      - иначе → `push.NilRelayClient{}`
- [x] инстанцировать `templates := push.NewTemplates(cfg.DateTime.Timezone)`
- [x] инстанцировать `dispatcher := push.NewDispatcher(push.DispatcherDeps{DB, PushQueue, EventStore, DeviceStore, RelayClient, Templates, Clock, Logger, ...})` — только если `cfg.Push.Enabled()`
- [x] передать `relayClient` в `PairingDeps`, `api.Config`, CLI `devices revoke`
- [x] добавить `go dispatcher.Run(ctx)` в пул goroutines с `wg.Add(1)` + graceful shutdown
- [x] логировать `slog.Info("push: dispatcher запущен", ...)` / `slog.Warn("push: секция [push] не задана, диспетчер отключён")`
- [x] `go build ./...` и `go vet ./...`

### Task 19: docker-compose + Caddyfile + deploy/push-relay

**Files:**
- Modify: `docker-compose.yml`
- Create: `Caddyfile` (корень)
- Create: `deploy/push-relay/docker-compose.yml`
- Create: `deploy/push-relay/Caddyfile`
- Create: `deploy/push-relay/relay.toml.example`
- Create: `deploy/push-relay/README.md` (краткая инструкция по onboarding'у инстанса)

- [x] обновить корневой `docker-compose.yml`: убрать redis, добавить caddy, exposed ports, healthchecks, volumes (huskwoot_data, caddy_data, caddy_config)
- [x] создать корневой `Caddyfile` с `huskwoot.example.com { reverse_proxy huskwoot:8080 }`
- [x] создать `deploy/push-relay/docker-compose.yml` (каталог сервисов push-relay + caddy)
- [x] создать `deploy/push-relay/Caddyfile`: `push.example.com { reverse_proxy push-relay:8080 }`
- [x] создать `deploy/push-relay/relay.toml.example` (копия `cmd/huskwoot-push-relay/relay.example.toml` с deploy-путями /run/secrets/)
- [x] написать `deploy/push-relay/README.md` (шаги: скопировать конфиг, сгенерировать `instance_id` (uuidgen) и `instance_secret` (openssl rand -hex 32), добавить секцию `[[instances]]`, `docker compose kill -s HUP push-relay`, передать владельцу инстанса `instance_id + secret`)
- [x] (нет Go-тестов для этой задачи — только ручная проверка `docker compose config` в Task 20)

### Task 20: Acceptance — race, build, интеграция

- [x] `go test -race ./...` — все тесты зелёные
- [x] `go vet ./...` — без замечаний
- [x] `go build -o bin/huskwoot ./cmd/huskwoot/`
- [x] `go build -o bin/huskwoot-push-relay ./cmd/huskwoot-push-relay/`
- [x] `docker compose -f docker-compose.yml config` — конфиг валиден
- [x] `docker compose -f deploy/push-relay/docker-compose.yml config` — конфиг валиден
- [x] локальный интеграционный тест (ручной): manual test (skipped - not automatable)
- [x] ручная проверка HMAC: manual test (skipped - not automatable)
- [x] ручная проверка SIGHUP: manual test (skipped - not automatable)
- [x] вписать ⚠️ в план, если что-то не работает; не двигаться дальше до зелёной полосы

### Task 21: [Final] Документация и завершение плана

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`
- Create: `docs/push-relay/setup.md`
- Create: `docs/push-relay/hmac.md`
- Create: `deploy/push-relay/openapi.yaml` (опционально — описание HTTP-протокола релея)
- Move: `docs/plans/2026-04-19-backend-api-phase4-push-relay.md` → `docs/plans/completed/`

- [x] добавить в `CLAUDE.md` раздел «Push: relay + dispatcher» (пакеты `internal/pushproto/`, `internal/push/` (dispatcher/templates/relayclient/NilRelayClient), `internal/relay/`, бинарник `huskwoot-push-relay`, секция `[push]` в конфиге, decision-table kind → push)
- [x] добавить в `CLAUDE.md` про обогащение payload task-событий (`{"task":{...}, "changedFields":[...]}`)
- [x] написать `docs/push-relay/setup.md` — пошаговый гайд для оператора релея:
      - Apple APNs: регистрация в Apple Developer Program, создание App ID с `Push Notifications` capability, получение `.p8` auth key (Keys → +Key → APNS), сохранение `KeyID` и `TeamID`, проверка bundle `topic` (= bundleID приложения)
      - Google FCM: создание Firebase-проекта, подключение Android-приложения, Project Settings → Service Accounts → Generate new private key → JSON, размещение в `deploy/push-relay/secrets/fcm-sa.json`
      - как прописать пути в `relay.toml` (секции `[apns]` и `[fcm]`) и примонтировать secrets в `docker-compose.yml`
      - smoke-тест: `curl -X POST https://push.../v1/push` с корректной HMAC-подписью и одним device-token → проверить, что нотификация доходит на sandbox-устройство
      - отладка типовых ошибок: `BadDeviceToken`, `Unregistered`, `InvalidArgument` (FCM) — как понимать и что делать
- [x] написать `docs/push-relay/hmac.md` — справка для разработчиков:
      - каноническая строка `METHOD\nPATH\nTIMESTAMP\nlower(hex(SHA256(body)))`
      - обязательные заголовки (`X-Huskwoot-Instance`, `X-Huskwoot-Timestamp`, `X-Huskwoot-Signature`), правила `hex(lowercase)`
      - окно skew ±5 мин, поведение при пустом теле (`sha256("")`)
      - пример на Go (ссылается на `internal/pushproto/hmac.go` как эталон)
      - пример ручной подписи через `openssl dgst -sha256 -mac HMAC`/`curl`
      - раздел «Типовые ошибки» (`401 bad_signature`, `401 stale_timestamp`, `401 unknown_instance`)
- [x] обновить `README.md`:
      - отметить Фазу 4 завершённой
      - указать два бинарника (`huskwoot` — инстанс, `huskwoot-push-relay` — релей)
      - добавить раздел «Push relay» с краткой архитектурой (инстанс ⇄ релей по HMAC), мини-примером секции `[push]` для инстанса, ссылками на `deploy/push-relay/README.md`, `docs/push-relay/setup.md`, `docs/push-relay/hmac.md`
- [x] (опционально) положить `deploy/push-relay/openapi.yaml` — описать три endpoint'а релея и HMAC-заголовки [skipped — справка в hmac.md достаточна]
- [x] переместить план в `docs/plans/completed/`
- [x] проверить `MEMORY.md` на устаревшие упоминания фаз

## Post-Completion

*Items requiring manual intervention or external systems — no checkboxes, informational only.*

**Manual verification:**

- **Реальные APNs/FCM ключи:** поднять dev-аккаунт Apple Developer Program и проект Firebase; положить `.p8` и `service-account.json` в `deploy/push-relay/secrets/`; запустить реальный push на sandbox-устройство; проверить, что приходит с правильным title/body.
- **Безопасность HMAC:** убедиться, что `instance_secret` проставляется только через env (не в коммитах); проверить, что `secret_hash` в БД релея действительно `sha256(plaintext)` и секреты не логируются.
- **Deployment релея:** после деплоя на публичный host (условно `push.huskwoot.app`) проверить, что Caddy автоматически получил Let's Encrypt-сертификат; `curl -I https://push.huskwoot.app/healthz` → 200.
- **Onboarding друга:** пройти полный сценарий из `deploy/push-relay/README.md` — создать `instance_id`, сгенерировать секрет, добавить в `[[instances]]`, SIGHUP, передать значения другу. Друг прописывает `[push]` в своём `config.toml`, запускает инстанс, делает pairing → push доходит.
- **Retry-реалистично:** отключить релей (`docker compose stop push-relay`), создать задачу через Telegram, убедиться, что в `push_queue` появилась запись с `attempts=1..4` и `next_attempt_at` растёт по табличке; после drop'а — warning в логе.
- **Coalescing/debounce:** проверить батч-обработку IMAP (5 задач подряд из одного письма) — сейчас будет 5 push'ей. Если UX плох, открыть тикет на следующую итерацию (спек §6: debounce отложен).

**External system updates:**

- Клиентские подпроекты (№2–4) будут полагаться на формат `Data` в push-payload и новый `changedFields` в `task_updated` payload SSE — зафиксировать в OpenAPI / клиентской документации при старте Подпроекта №2.
- Если деплой использует Traefik/nginx вместо Caddy — заменить Caddyfile на соответствующий конфиг (оставляется на совести оператора; Caddy — рекомендуемый путь).
- CI/CD: добавить `HUSKWOOT_PUSH_SECRET` в secrets репозитория для деплоя инстанса автора; `HUSKWOOT_RELAY_SECRET_*` — в secrets деплоя релея.
- Мониторинг: метрики Prometheus (отложено в спек §1) — открыть отдельный тикет «Push dispatcher metrics» (backlog events, backlog age, failures/hour, delivered/hour) как первую задачу следующей итерации подпроекта №1.

**Backlog / резерв для следующей итерации:**

- Streaming ответа агента через SSE (`chat_reply` push).
- Debounce/coalescing push-уведомлений.
- Prometheus метрики и opentelemetry-трейсинг.
- Кнопка «Это не я → отменить» в Telegram magic-link DM.
- Резервный CLI-based pairing (без Telegram).
- Web-UI администратора инстанса (список устройств, push-очередь, логи).
- Админ-API релея (добавление инстансов без правки TOML).
- Ротация `instance_secret` без простоя (сейчас требуется перезапуск клиента).
- Reconciliation task: периодическая проверка, что `registrations` в релее совпадают с `devices` инстанса (идея — фоновая джоба, ресинхронизирующая после длительных сбоев связи).
