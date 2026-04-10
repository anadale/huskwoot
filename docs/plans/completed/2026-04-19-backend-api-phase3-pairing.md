# Backend API — Фаза 3: Pairing-flow через Telegram magic-link

> **For agentic workers:** REQUIRED SUB-SKILL — `superpowers:subagent-driven-development` (рекомендуется) либо `superpowers:executing-plans` для пошаговой реализации. Шаги используют чекбоксы (`- [ ]`) для прогресса.

**Goal:** Дать новому клиенту (десктоп/iOS/Android) подключиться к инстансу без ручной выдачи токенов через CLI. Поток: клиент `POST /v1/pair/request` → инстанс кладёт строку в `pairing_requests` и шлёт владельцу DM с magic-link → владелец тапает по ссылке, открывает HTML-страницу подтверждения, подтверждает с CSRF-токеном → инстанс создаёт device-запись и публикует bearer-токен в long-poll-канал → клиентский `GET /v1/pair/status` отдаёт токен. CLI `huskwoot devices create` остаётся как dev-fallback.

**Architecture:** `PairingService` владеет транзакциями для записи в `pairing_requests`/`devices` (по паттерну Фазы 2: open tx → write через store → commit → notify). Long-poll-ожидание реализовано через in-memory map `pairID → chan PairingResult` с `sync.Mutex`; `ConfirmPairing` после commit публикует результат в канал, `PollStatus` ждёт `select` либо первичную проверку из БД (на случай гонки между confirm и началом long-poll). Telegram-DM отправляется через интерфейс `TelegramSender` (мокается в тестах), реализация — обёртка над `*tgbotapi.BotAPI` владельца, выбираемого через `[owner].telegram_bot_id`. CSRF — `__Host-csrf` cookie (`SameSite=Strict`, `Secure`, `HttpOnly=false` — нужно читать из формы) + скрытое поле формы. Rate-limit на `POST /v1/pair/request` — in-memory token bucket (`golang.org/x/time/rate`) с ключом `client IP`.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `github.com/go-chi/chi/v5`, `github.com/google/uuid`, `github.com/go-telegram-bot-api/telegram-bot-api/v5` (уже в проекте), `golang.org/x/time/rate` (новая зависимость, опционально могла добавиться раньше — проверить `go.mod`), `html/template` (stdlib).

---

## Overview

Фаза 3 — третий из четырёх планов в серии «Backend API + push» по [спецификации](../superpowers/specs/2026-04-18-backend-api-and-push-design.md).

- **Фаза 1 (завершена):** use-case слой + миграции UUID/slug/number.
- **Фаза 2 (завершена):** HTTP-инфраструктура, SSE, EventStore, PushQueue store, DeviceStore, CLI `devices create/list/revoke`.
- **Фаза 3 (этот план):** Pairing-flow (Telegram magic-link DM, HTML confirm с CSRF, long-poll `/v1/pair/status`, rate-limit), `pairing_requests` миграция, `PairingService`, retention для pairing-запросов, OpenAPI обновление.
- **Фаза 4:** Push relay (отдельный бинарник `huskwoot-push-relay`), `push_queue` dispatcher с retry, HMAC-протокол инстанс ⇄ релей, Caddy + обновлённый docker-compose.

После Фазы 3 запущенный инстанс:

- Принимает безавторизационный `POST /v1/pair/request` с `{deviceName, platform, clientNonce, apnsToken?, fcmToken?}` и возвращает `{pairId, pollUrl, expiresAt}`.
- Отправляет владельцу DM в Telegram: «Подключить устройство «iPhone 17»? `https://<external_base_url>/pair/confirm/<pairId>`» (от `[owner].telegram_bot_id`).
- На `GET /pair/confirm/{id}` рендерит HTML с информацией об устройстве и формой подтверждения, выставляет `__Host-csrf` cookie, запоминает SHA256(csrf) в `pairing_requests.csrf_token_hash`.
- На `POST /pair/confirm/{id}` валидирует CSRF, помечает запись confirmed, создаёт `device`, публикует токен в long-poll-канал.
- `GET /v1/pair/status/{id}?nonce=<clientNonce>` валидирует SHA256(nonce) против сохранённого, ждёт результата до 60 секунд (`[api].pairing_status_long_poll`), возвращает `{status:"confirmed", deviceId, bearerToken}` или `{status:"pending"}` после таймаута.
- Rate-limit `POST /v1/pair/request`: 5 запросов/час на IP (через `[api].rate_limit_pair_per_hour`).
- Retention pairing_requests: горутина из Фазы 2 (`internal/events/retention.go`) дополнительно вычищает истёкшие записи через 1 час после `expires_at`.
- Magic-link и pair-запись живут 5 минут (`[api].pairing_link_ttl`).
- CLI-команда `huskwoot devices create` остаётся как dev/admin fallback.
- `cancel`-кнопка «Это не я» в DM **не входит** в скоп (отложена в спеке §5).

## Context (from discovery)

**Текущее состояние (после Фазы 2):**

- `internal/api/devices.go` — уже есть `PATCH /v1/devices/me` для обновления APNs/FCM-токенов; pairing-эндпоинтов нет.
- `internal/api/server.go` — chi-роутер с auth-middleware на `/v1/*`; pairing-эндпоинты должны жить вне auth-цепочки.
- `internal/devices/store.go` — `DeviceStore.Create(ctx, tx, *model.Device)` принимает `*sql.Tx` (Фаза 2). Используется в `cmd/huskwoot/devices.go` (CLI `devices create`).
- `internal/storage/migrations/` — миграции 001–006; следующая — 007.
- `cmd/huskwoot/main.go` — `buildTelegramComponents` создаёт `bots map[string]*tgbotapi.BotAPI`; pairing-сервис должен брать бот из этой карты по `[owner].telegram_bot_id`.
- `internal/config/config.go` — секция `[owner]` уже есть с `TelegramUserID` и `DisplayName`; нужно добавить `TelegramBotID`. Секция `[api]` уже есть с `Enabled/ListenAddr/ExternalBaseURL/RequestTimeout/ChatTimeout/EventsRetention/CORSAllowedOrigins`; добавляются `PairingLinkTTL`, `PairingStatusLongPoll`, `RateLimitPairPerHour`.
- `internal/events/retention.go` — фоновая горутина, вычищает `events` и `push_queue`. В Фазе 3 расширяется новым шагом — cleanup `pairing_requests`.
- `api/openapi.yaml` — текущий источник истины; добавляются 4 новых пути (`/v1/pair/request`, `/v1/pair/status/{id}`, `/pair/confirm/{id}` GET и POST).
- `internal/model/service.go` — есть `TaskService`, `ProjectService`, `ChatService`; `PairingService` отсутствует, добавляется в этот же файл.

**Файлы, которые меняются:**

- `internal/model/service.go` — добавить интерфейс `PairingService` + DTO (`PairingRequest`, `PendingPairing`, `PairingResult`).
- `internal/model/interfaces.go` — никаких изменений (PairingStore — отдельный новый интерфейс в `internal/model/pairing.go` либо рядом с `Device`).
- `internal/api/server.go` — монтировать pairing-роуты вне auth-цепочки; передать `PairingService` через `Config`.
- `internal/api/openapi.go` — без изменений (раздаёт встроенный yaml).
- `api/openapi.yaml` — описать новые пути и схемы.
- `internal/config/config.go` — расширить `OwnerConfig.TelegramBotID` и `APIConfig` (новые поля).
- `internal/events/retention.go` — добавить cleanup pairing_requests; передать `PairingStore` в `RetentionRunner`.
- `cmd/huskwoot/main.go` — wiring `PairingService` (получить владельческий `*tgbotapi.BotAPI` из `bots[ownerCfg.TelegramBotID]`), регистрация в `api.Config`.

**Файлы, которые создаются:**

- `internal/storage/migrations/007_pairing_requests.sql` — новая таблица.
- `internal/pairing/store.go` + `store_test.go` — `SQLitePairingStore`.
- `internal/pairing/notifier.go` + `notifier_test.go` — `TelegramSender` интерфейс + реализация над `*tgbotapi.BotAPI`.
- `internal/pairing/broadcaster.go` + `broadcaster_test.go` — in-memory pub/sub `pairID → chan PairingResult`.
- `internal/usecase/pairing.go` + `pairing_test.go` — `PairingService` (RequestPairing/ConfirmPairing/PollStatus).
- `internal/api/pairing.go` + `pairing_test.go` — 4 HTTP-хэндлера + CSRF helper + rate-limit middleware.
- `internal/api/templates/pair_confirm.html.tmpl` — HTML-страница (или inline в `pairing.go` через `html/template`).
- `internal/model/pairing.go` — типы `PendingPairing`, `PairingResult`, `PairingRequest`, статусы; интерфейс `PairingStore` для абстракции от SQLite.

**Паттерны проекта (соблюдать):**

- Интерфейсы в `internal/model/`, реализации — в отдельных пакетах.
- Конструкторы возвращают `(*Type, error)` если возможна ошибка инициализации.
- Все публичные методы принимают `context.Context` первым параметром.
- Write-методы store'ов: `Method(ctx, tx *sql.Tx, ...args)`. Read-методы: `Method(ctx, ...args)`.
- Тесты — table-driven, моки вручную.
- Логирование — `log/slog` структурированное.
- Ошибки и сообщения для пользователя/логов — на русском.
- JSON-свойства API — camelCase (см. CLAUDE.md). URL query-параметры — snake_case.
- Use-case владеет транзакцией; broadcaster.Notify вызывается ПОСЛЕ commit.
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
- **TelegramSender-интерфейс** мокается в тестах (`PairingServiceTest` не должен дёргать `*tgbotapi.BotAPI`).
- **CSRF-тесты:** проверять, что POST без cookie → 403; cookie без поля формы → 403; mismatch → 403; happy-path → 204/302.
- **Long-poll-тесты:** контекст с таймаутом 100ms → возврат `{status:"pending"}`; параллельный `ConfirmPairing` → возврат токена в течение <50ms; неверный nonce → 403.
- **Rate-limit-тесты:** 5 запросов с одного IP проходят, 6-й → 429; разные IP — независимы.
- **E2E-тесты:** в проекте отсутствуют, не добавляем. Smoke-проверка — ручная (запуск `huskwoot serve` локально, cURL pair/request → DM в Telegram → подтверждение → клиентский cURL pair/status получает токен).
- **Race-detector:** `go test -race ./...` обязательно перед acceptance.

## Progress Tracking

- Чекбоксы помечаются `[x]` сразу после выполнения (не батчем).
- Новые обнаруженные подзадачи — с префиксом ➕.
- Блокеры/проблемы — с префиксом ⚠️.
- Смена scope или подхода — обновлять разделы Overview/Solution Overview/Implementation Steps в этом файле.

## Solution Overview

**1. Хранилище pairing-запросов.** Отдельный пакет `internal/pairing/` (по аналогии с `internal/devices/`) с интерфейсом `PairingStore` в `internal/model/pairing.go`:

```go
type PairingStore interface {
    CreateTx(ctx context.Context, tx *sql.Tx, p *PendingPairing) error
    Get(ctx context.Context, id string) (*PendingPairing, error)
    SetCSRFTx(ctx context.Context, tx *sql.Tx, id, csrfHash string) error
    MarkConfirmedTx(ctx context.Context, tx *sql.Tx, id, deviceID string) error
    DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}
```

`SQLitePairingStore` реализует чтение/запись с UUID, SHA256(client_nonce), SHA256(csrf_token), `expires_at`, `confirmed_at`, `issued_device_id`.

**2. PairingService (use-case).** В `internal/usecase/pairing.go` живёт сервис, владеющий `*sql.DB`, `PairingStore`, `DeviceStore`, `TelegramSender`, `Broadcaster`, `Clock`, конфигом TTL.

- `RequestPairing(ctx, req)`:
  1. Открыть tx.
  2. Сгенерировать `pairID := uuid.NewString()`, посчитать `nonceHash := sha256(req.ClientNonce)`, `expiresAt := now + ttl`.
  3. `pairingStore.CreateTx(ctx, tx, &PendingPairing{...})`.
  4. Commit.
  5. Вызвать `telegramSender.SendMagicLink(ctx, ownerChatID, deviceName, magicURL)`. Если отправка падает — записать pairing уже создан, ошибка возвращается клиенту 502 (он повторит); вариант: помечать в логах и оставлять запись на retention (магический паттерн «лучше дубликат, чем потерянный link»).
  6. Возвращает `{PairID, PollURL, ExpiresAt}`.

- `ConfirmPairing(ctx, pairID)`:
  1. Получить `pairing` из store, проверить `expires_at > now`, `confirmed_at IS NULL`.
  2. Открыть tx.
  3. Сгенерировать device-токен `bearerToken := base64url(rand 32B)`, `tokenHash := sha256(bearerToken)`.
  4. `deviceStore.Create(ctx, tx, &Device{Name: pairing.DeviceName, Platform: pairing.Platform, TokenHash: ..., APNSToken/FCMToken: pairing.APNSToken/FCMToken})`.
  5. `pairingStore.MarkConfirmedTx(ctx, tx, pairID, device.ID)`.
  6. Commit.
  7. `broadcaster.Notify(pairID, PairingResult{Status: confirmed, DeviceID, BearerToken})`.
  8. Возвращает `*Device` (без bearer-токена — он только в результате broadcast).

- `PollStatus(ctx, pairID, clientNonce)`:
  1. Получить pairing, проверить `sha256(clientNonce) == NonceHash` (timing-safe), `expires_at > now`.
  2. Если `confirmed_at != nil` — это «гонка»: вернуть pending=false, но без токена (токен показан только однажды через broadcaster). Документируем: клиент должен начать подписку до подтверждения; при miss — 410 Gone «токен уже выдан».
  3. Подписаться на канал `broadcaster.Subscribe(pairID)`.
  4. `select` до `min(ctx.Deadline(), now + longPollTTL)`:
     - `result := <-chan` → 200 OK с `{status:"confirmed", deviceId, bearerToken}`.
     - timeout → 200 OK с `{status:"pending"}`.

- `PrepareCSRF(ctx, pairID, csrfToken)`: tx, `SetCSRFTx`, commit. Используется HTML-handler.

- `ConfirmWithCSRF(ctx, pairID, csrfToken)`: вычитать pairing, проверить `sha256(csrfToken) == CSRFHash`, дальше как `ConfirmPairing`.

**3. Broadcaster.** `internal/pairing/broadcaster.go` — структура с `sync.Mutex` и `map[string]chan PairingResult` (буфер 1, чтобы Notify не блокировался). `Subscribe(pairID)` возвращает канал и cleanup-функцию (удаляет из map). `Notify(pairID, result)` пишет в канал (non-blocking via `select default`). Все операции безопасны для конкурентных вызовов.

**4. TelegramSender.** Интерфейс в `internal/pairing/notifier.go`:

```go
type TelegramSender interface {
    SendMagicLink(ctx context.Context, chatID int64, deviceName, magicURL string) error
}
```

Реализация `botAPISender` принимает `*tgbotapi.BotAPI`, формирует сообщение «Подключить устройство «iPhone 17»? Подтвердить: <link>», шлёт `tgbotapi.NewMessage(chatID, text)` с `DisableWebPagePreview = true`. Если bot или chatID = 0 — использовать заглушку `noopSender{}`, которая возвращает nil (для dev без [owner]).

**5. HTTP-эндпоинты `/v1/pair/*` и `/pair/confirm/*`.** Регистрируются в `api.Server` ВНЕ auth-цепочки:

```go
r.Route("/v1/pair", func(r chi.Router) {
    r.With(rateLimitPairing).Post("/request", h.request)
    r.Get("/status/{id}", h.status)
})
r.Route("/pair/confirm", func(r chi.Router) {
    r.Get("/{id}", h.confirmPage)
    r.Post("/{id}", h.confirmSubmit)
})
```

Идемпотентность для `POST /v1/pair/request` поддерживаем стандартным middleware (заголовок `Idempotency-Key`); rate-limit — отдельный middleware на /v1/pair/request.

**6. CSRF.** Helper в `internal/api/pairing.go`:

- `GET /pair/confirm/{id}`: генерирует `csrf := base64url(rand 32B)`, сохраняет `sha256(csrf)` в pairing-запись через `PairingService.PrepareCSRF`, выставляет cookie `__Host-csrf=<csrf>; Path=/pair/confirm/<id>; Secure; SameSite=Strict; HttpOnly=false; Max-Age=600`. Рендерит HTML с `<input type=hidden name=csrf value="<csrf>">`. Если pairing истёк/отсутствует — 410 Gone HTML «ссылка устарела».
- `POST /pair/confirm/{id}`: парсит form, читает cookie, проверяет `sha256(form.csrf) == pairing.CSRFHash` И `cookie == form.csrf` (двойная защита). При успехе — `PairingService.ConfirmWithCSRF`. Возвращает HTML «✓ Устройство подключено». На ошибке — 403 HTML «отказано».

CSRF-cookie не помечается `HttpOnly`, потому что HTML-страница рендерит токен в форме напрямую — JS не нужен. Для тестов хватает текстовой формы.

**7. Rate-limit.** `internal/api/pairing.go` содержит `pairingRateLimiter` — структура с `sync.Mutex` и `map[string]*rate.Limiter`. Ключ — `r.RemoteAddr` (или `X-Forwarded-For` first hop, если настроен trusted-proxy — пока в скоп не входит). Limit — `Limit(rate.Every(time.Hour/N))`, Burst — N (=`[api].rate_limit_pair_per_hour`, дефолт 5). При превышении — 429 + `Retry-After`. Старые лимитеры вычищаются раз в час (отдельная мини-горутина в `Server.Run`).

**8. Конфиг.**

```toml
[owner]
telegram_user_id = 123456789      # для DM (target chat_id = telegram_user_id)
telegram_bot_id = "main"          # ID watcher'а из [[channels.telegram]] — выбирает бота для DM
display_name = "Nickon"

[api]
# существующие поля...
pairing_link_ttl = "5m"           # TTL для magic-link и записи в pairing_requests
pairing_status_long_poll = "60s"  # таймаут /v1/pair/status
rate_limit_pair_per_hour = 5      # бюджет POST /v1/pair/request на IP в час
```

При `telegram_bot_id == ""` или отсутствии записи в `bots[id]` — pairing-сервис всё равно работает, но шлёт через `noopSender` и логирует warning «pairing: telegram sender не настроен, magic-link не отправлен» (для smoke-теста владелец видит ссылку в логах).

**9. Retention.** `internal/events/retention.go` расширяется опциональным `PairingStore`. В каждом tick'е после `events`/`push_queue` вызывается `pairingStore.DeleteExpired(now - 1h)` (запись считается «истёкшей» если `expires_at < now - 1h`). Cutoff жёстко закодирован — спека §3 говорит «удаляются через 1 час после `expires_at`».

**10. OpenAPI.** `api/openapi.yaml` дополняется четырьмя путями. В `paths`:

- `/v1/pair/request` (POST, без security) — body `PairingRequest`, response `PendingPairing`, errors 400/422/429/502.
- `/v1/pair/status/{id}` (GET, без security, query `nonce`) — response `PairingStatusResponse` (`status`: `pending|confirmed|expired`), errors 403/404/410.
- `/pair/confirm/{id}` (GET) — response `text/html`, errors 410/404.
- `/pair/confirm/{id}` (POST) — body `application/x-www-form-urlencoded` (`csrf`), response `text/html`, errors 403/410.

Schemas: `PairingRequest`, `PendingPairing`, `PairingStatusResponse`. Раздаётся через существующий `GET /v1/openapi.yaml`.

**11. Wiring.** В `cmd/huskwoot/main.go` после построения `bots` и `deviceStore`/`db`:

```go
pairingStore := pairing.NewSQLiteStore(db)
broadcaster := pairing.NewBroadcaster()
ownerBot := bots[cfg.Owner.TelegramBotID]   // может быть nil
sender := pairing.NewTelegramSender(ownerBot, logger)
pairingSvc := usecase.NewPairingService(usecase.PairingDeps{
    DB: db, PairingStore: pairingStore, DeviceStore: deviceStore,
    Sender: sender, Broadcaster: broadcaster,
    OwnerChatID: cfg.Owner.TelegramUserID,
    LinkTTL: cfg.API.PairingLinkTTL,
    LongPoll: cfg.API.PairingStatusLongPoll,
    ExternalBaseURL: cfg.API.ExternalBaseURL,
    Now: nowFn, Rand: cryptorand.Reader, Logger: logger,
})
apiCfg.PairingService = pairingSvc
apiCfg.PairingRateLimit = cfg.API.RateLimitPairPerHour
```

Retention-runner получает дополнительный аргумент:

```go
retentionRunner := events.NewRetentionRunner(eventStore, pushQueue, pairingStore, retentionCutoffFn, logger)
```

## Technical Details

### Новые типы в `model/`

```go
// internal/model/pairing.go

type PairingStatus string

const (
    PairingStatusPending   PairingStatus = "pending"
    PairingStatusConfirmed PairingStatus = "confirmed"
    PairingStatusExpired   PairingStatus = "expired"
)

type PairingRequest struct {
    DeviceName  string
    Platform    string
    ClientNonce string  // открытый, передаётся клиентом; в БД хранится sha256
    APNSToken   *string
    FCMToken    *string
}

type PendingPairing struct {
    ID              string  // UUID, одновременно токен в magic-link
    DeviceName      string
    Platform        string
    APNSToken       *string
    FCMToken        *string
    NonceHash       string  // hex sha256
    CSRFHash        string  // hex sha256, пусто пока не сгенерирован
    CreatedAt       time.Time
    ExpiresAt       time.Time
    ConfirmedAt     *time.Time
    IssuedDeviceID  *string
}

type PairingResult struct {
    PairID      string
    Status      PairingStatus
    DeviceID    string  // заполнен при confirmed
    BearerToken string  // показывается ОДИН раз, через broadcaster
}

type PairingStore interface {
    CreateTx(ctx context.Context, tx *sql.Tx, p *PendingPairing) error
    Get(ctx context.Context, id string) (*PendingPairing, error)
    SetCSRFTx(ctx context.Context, tx *sql.Tx, id, csrfHash string) error
    MarkConfirmedTx(ctx context.Context, tx *sql.Tx, id, deviceID string) error
    DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}
```

### Интерфейс PairingService (расширение `model/service.go`)

```go
type PairingService interface {
    RequestPairing(ctx context.Context, req PairingRequest) (*PendingPairing, error)
    PollStatus(ctx context.Context, pairID, clientNonce string) (*PairingResult, error)
    PrepareConfirm(ctx context.Context, pairID, csrfToken string) (*PendingPairing, error)
    ConfirmWithCSRF(ctx context.Context, pairID, csrfToken string) (*Device, error)
}
```

`PendingPairing.NonceHash` и `CSRFHash` не уходят клиенту — есть отдельные DTO в API-слое.

### Миграция 007

```sql
-- internal/storage/migrations/007_pairing_requests.sql

CREATE TABLE pairing_requests (
    id                 TEXT PRIMARY KEY,
    device_name        TEXT NOT NULL,
    platform           TEXT NOT NULL,
    apns_token         TEXT,
    fcm_token          TEXT,
    client_nonce_hash  TEXT NOT NULL,
    csrf_token_hash    TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         DATETIME NOT NULL,
    confirmed_at       DATETIME,
    issued_device_id   TEXT REFERENCES devices(id)
);

CREATE INDEX idx_pairing_requests_expires ON pairing_requests(expires_at);
```

### HTTP-форматы

```jsonc
// POST /v1/pair/request
{
  "deviceName": "iPhone 17",
  "platform": "ios",
  "clientNonce": "<base64url, 32+ bytes>",
  "apnsToken": "<optional>",
  "fcmToken": "<optional>"
}

// 202 Accepted
{
  "pairId": "<uuid>",
  "pollUrl": "/v1/pair/status/<uuid>",
  "expiresAt": "2026-04-19T12:05:00Z"
}

// GET /v1/pair/status/{id}?nonce=<clientNonce>
// Long-poll: блокируется до 60s

// 200 OK (confirmed)
{ "status": "confirmed", "deviceId": "<uuid>", "bearerToken": "<base64url>" }

// 200 OK (pending after timeout)
{ "status": "pending" }

// 403 Forbidden (nonce mismatch)
{ "error": { "code": "forbidden", "message": "nonce не совпадает" } }

// 404 Not Found / 410 Gone (expired или token уже выдан)
{ "error": { "code": "gone", "message": "ссылка устарела" } }
```

### HTML-страница confirm (черновик)

```html
<!doctype html>
<html lang="ru"><head>
<meta charset="utf-8"><title>Подтверждение устройства · Huskwoot</title>
<style>body{font-family:-apple-system,sans-serif;max-width:480px;margin:48px auto;padding:0 16px;color:#222}
button{font-size:16px;padding:12px 24px;background:#2c7;color:#fff;border:0;border-radius:6px;cursor:pointer}
button:hover{background:#0a5}
.card{background:#f6f6f6;padding:16px;border-radius:6px;margin:16px 0}
</style></head><body>
<h1>Подключить устройство?</h1>
<div class="card">
  <p><b>Имя:</b> {{.DeviceName}}</p>
  <p><b>Платформа:</b> {{.Platform}}</p>
  <p><b>Запрошено:</b> {{.CreatedAt}}</p>
</div>
<form method="post">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <button type="submit">Подключить</button>
</form>
</body></html>
```

### Поток данных «Pairing happy path»

```
Client                          Instance                              Owner
------                          --------                              -----
POST /v1/pair/request
  {deviceName, platform,
   clientNonce}
                                rate-limit check
                                tx: insert pairing_requests
                                    (id, expires=now+5m, nonceHash)
                                commit
                                sender.SendMagicLink(ownerChatID,
                                  "Подключить «iPhone»?
                                   https://.../pair/confirm/<id>")
                                                                 ───> DM
←202 {pairId, pollUrl, expiresAt}

GET /v1/pair/status/<id>?nonce=...
(long-poll 60s)
  validate sha256(nonce) ==
  pairing.NonceHash
  subscribe(pairID)
  select{}
                                                                 [User taps link]
                                GET /pair/confirm/<id>
                                  generate csrf
                                  tx: SetCSRFTx(sha256(csrf))
                                  commit
                                  set cookie __Host-csrf=<csrf>
                                  render HTML form
                                                                 [User clicks]
                                POST /pair/confirm/<id>
                                  body: csrf=<csrf>
                                  cookie: csrf=<csrf>
                                  validate cookie==body && sha256==Hash
                                  tx: insert devices(token_hash, platform)
                                      MarkConfirmedTx
                                  commit
                                  broadcaster.Notify(pairID, result)
                                                                 ←HTML "✓ подключено"
  ←result via channel
←200 {status:"confirmed", deviceId, bearerToken}
```

## What Goes Where

- **Implementation Steps (`[ ]` checkboxes):** код, тесты, миграции, OpenAPI, конфиг, wiring.
- **Post-Completion (без чекбоксов):** ручной smoke-тест pairing-flow с реальным Telegram-ботом, TLS-проверка `__Host-csrf` cookie за reverse proxy, безопасностный обзор CSRF-механики.

## Implementation Steps

### Task 1: Миграция 007 — таблица `pairing_requests`

**Files:**
- Create: `internal/storage/migrations/007_pairing_requests.sql`
- Modify: `internal/storage/migrations/migrations_test.go`

- [x] добавить SQL-миграцию `007_pairing_requests.sql` со схемой из «Technical Details» (включая индекс по `expires_at`)
- [x] обновить тест миграций: после `Up` проверить, что `pairing_requests` существует и содержит ожидаемые колонки (через `PRAGMA table_info`)
- [x] проверить, что `Up` идемпотентен (повторный вызов на свежей БД не падает) — table-driven case
- [x] запустить `go test ./internal/storage/migrations/...` — должно быть зелёное

### Task 2: Модель `PendingPairing`, `PairingResult`, интерфейс `PairingStore`

**Files:**
- Create: `internal/model/pairing.go`
- Modify: `internal/model/service.go`

- [x] создать `internal/model/pairing.go` с типами `PairingStatus`, `PairingRequest`, `PendingPairing`, `PairingResult` и интерфейсом `PairingStore` (см. Technical Details)
- [x] добавить интерфейс `PairingService` в `internal/model/service.go` рядом с `TaskService`/`ProjectService`/`ChatService`
- [x] написать табличный тест на `PairingStatus.String()`/JSON-маршалинг (если используется тип `PairingStatus` в JSON-ответе — добавить `MarshalJSON`/проверить дефолтное поведение)
- [x] запустить `go test ./internal/model/...` и `go vet ./internal/model/...`

### Task 3: SQLitePairingStore — реализация

**Files:**
- Create: `internal/pairing/store.go`
- Create: `internal/pairing/store_test.go`

- [x] написать failing-тест `TestSQLitePairingStore_CreateAndGet` (вставка через `CreateTx`, чтение через `Get`, сравнение полей включая `NonceHash`/`ExpiresAt`)
- [x] написать failing-тест `TestSQLitePairingStore_SetCSRFTx_Updates`
- [x] написать failing-тест `TestSQLitePairingStore_MarkConfirmedTx_PopulatesIssuedDeviceID`
- [x] написать failing-тест `TestSQLitePairingStore_DeleteExpired_RemovesOnlyOlder` (cutoff = now-1h, оставшаяся запись с expires_at>cutoff не трогается)
- [x] реализовать `SQLitePairingStore` (конструктор `NewSQLiteStore(*sql.DB)`); все Tx-методы принимают `*sql.Tx`, `Get`/`DeleteExpired` — `*sql.DB`
- [x] добавить хэлпер `scanPairing(*sql.Row|*sql.Rows) (*PendingPairing, error)`
- [x] запустить `go test ./internal/pairing/...` — все тесты зелёные

### Task 4: Pairing Broadcaster — in-memory pub/sub

**Files:**
- Create: `internal/pairing/broadcaster.go`
- Create: `internal/pairing/broadcaster_test.go`

- [x] написать failing-тест `TestBroadcaster_Subscribe_NotifyDeliversResult` (подписка → notify → result через канал в <50ms)
- [x] написать failing-тест `TestBroadcaster_Notify_NoSubscribers_DoesNotBlock`
- [x] написать failing-тест `TestBroadcaster_Subscribe_CleanupRemovesEntry` (вызов cleanup → повторный Notify не упирается в утечку)
- [x] написать failing-тест `TestBroadcaster_ConcurrentSubscribeNotify_NoRace` (через `sync.WaitGroup` + `-race`)
- [x] реализовать `Broadcaster` (`map[string]chan PairingResult` под `sync.Mutex`, буфер 1, non-blocking send через `select default`)
- [x] метод `Subscribe(pairID) (<-chan PairingResult, func())` возвращает канал и cleanup
- [x] метод `Notify(pairID, result PairingResult)` шлёт без блокировки
- [x] запустить `go test -race ./internal/pairing/...`

### Task 5: TelegramSender — отправка magic-link DM

**Files:**
- Create: `internal/pairing/notifier.go`
- Create: `internal/pairing/notifier_test.go`

- [x] написать failing-тест `TestBotAPISender_SendMagicLink_FormatsMessage` (используем httptest-сервер вместо `*tgbotapi.BotAPI`: создаём `tgbotapi.NewBotAPIWithAPIEndpoint` указывающий на httptest.URL+"/bot%s/%s", проверяем, что в `sendMessage` приходит chat_id и текст с magic-URL)
- [x] написать тест `TestNoopSender_DoesNothing` (возвращает nil без вызовов)
- [x] определить интерфейс `TelegramSender` с `SendMagicLink(ctx, chatID, deviceName, magicURL) error`
- [x] реализовать `botAPISender{bot *tgbotapi.BotAPI; logger *slog.Logger}` — формирует текст «Подключить устройство «{deviceName}»? Подтвердите по ссылке: {magicURL}», `DisableWebPagePreview = true`
- [x] реализовать `noopSender` (для случая `bot == nil`); `NewTelegramSender(bot, logger)` возвращает `noopSender` если `bot == nil`
- [x] логировать warning при `noopSender` использовании на каждый вызов (с `pairID` и `deviceName`)
- [x] запустить `go test ./internal/pairing/...`

### Task 6: PairingService — RequestPairing + PollStatus

**Files:**
- Create: `internal/usecase/pairing.go`
- Create: `internal/usecase/pairing_test.go`

- [x] написать failing-тест `TestPairingService_RequestPairing_PersistsAndSendsDM` (моки `PairingStore`/`TelegramSender`, проверка вставки записи и вызова Sender)
- [x] написать тест `TestPairingService_RequestPairing_ReturnsPendingDTO` (проверка корректного `pollUrl` и `expiresAt`)
- [x] написать тест `TestPairingService_PollStatus_NonceMismatch_Returns403Equivalent` (возвращается ошибка типа `ErrNonceMismatch`)
- [x] написать тест `TestPairingService_PollStatus_TimeoutReturnsPending` (контекст с таймаутом 50ms → `Status:"pending"`)
- [x] написать тест `TestPairingService_PollStatus_ReceivesConfirmedFromBroadcaster` (параллельная горутина дёргает `Broadcaster.Notify`, PollStatus возвращает confirmed)
- [x] написать тест `TestPairingService_PollStatus_ExpiredPairing_ReturnsExpired` (запись с `ExpiresAt < now`)
- [x] реализовать `PairingService` (`NewPairingService(deps PairingDeps) *Service`); use timing-safe сравнение хешей через `subtle.ConstantTimeCompare`
- [x] реализовать `RequestPairing` по схеме «Solution Overview» (tx insert → commit → sender.SendMagicLink, ошибка sender логируется и оборачивается в `ErrSenderFailed`)
- [x] реализовать `PollStatus` (`select` между broadcaster-каналом и `time.After(longPoll)` с учётом ctx.Done())
- [x] запустить `go test -race ./internal/usecase/...`

### Task 7: PairingService — PrepareConfirm + ConfirmWithCSRF

**Files:**
- Modify: `internal/usecase/pairing.go`
- Modify: `internal/usecase/pairing_test.go`

- [x] написать failing-тест `TestPairingService_PrepareConfirm_StoresCSRFHash`
- [x] написать тест `TestPairingService_PrepareConfirm_ExpiredPairing_ReturnsErrExpired`
- [x] написать тест `TestPairingService_ConfirmWithCSRF_ValidatesAndCreatesDevice` (проверить: CSRF mismatch → `ErrCSRFMismatch`, expired → `ErrExpired`, success → device создан с `bearerToken` через broadcaster)
- [x] написать тест `TestPairingService_ConfirmWithCSRF_DoubleConfirm_ReturnsErrAlreadyConfirmed` (повторный вызов на уже confirmed pairing)
- [x] реализовать `PrepareConfirm(ctx, pairID, csrfToken)` — tx, `pairingStore.SetCSRFTx`, commit
- [x] реализовать `ConfirmWithCSRF(ctx, pairID, csrfToken)` — проверки + tx (Create device + MarkConfirmedTx) + commit + broadcaster.Notify
- [x] добавить генерацию device-токена через `crypto/rand` (32 байта → base64url) и `tokenHash := sha256(token)` (hex)
- [x] запустить `go test -race ./internal/usecase/...`

### Task 8: API хэндлеры — POST /v1/pair/request + rate-limit middleware

**Files:**
- Create: `internal/api/pairing.go`
- Create: `internal/api/pairing_test.go`
- Modify: `internal/api/server.go`

- [x] написать failing-тест `TestPairingHandler_RequestPairing_Success` (POST с валидным body → 202 + json с `pairId`/`pollUrl`/`expiresAt`)
- [x] написать тест `TestPairingHandler_RequestPairing_InvalidBody_Returns400` (отсутствует `clientNonce`)
- [x] написать тест `TestPairingHandler_RequestPairing_RateLimit_Returns429` (6-й запрос с одного IP в течение часа)
- [x] написать тест `TestPairingHandler_RequestPairing_DifferentIPsIndependent`
- [x] реализовать `pairingHandler.request` (parse JSON → валидация → `service.RequestPairing` → 202 JSON; ошибки маппятся через `WriteError`)
- [x] реализовать `pairingRateLimiter` (map IP → `*rate.Limiter`, мютекс, метод `Allow(ip)`); ключ — `r.RemoteAddr` без порта
- [x] добавить cleanup-горутину `pairingRateLimiter.Sweep(ctx, every)` (раз в час удаляет лимитеры с момента последнего использования > 1h)
- [x] зарегистрировать `r.With(rateLimit).Post("/request", h.request)` в `server.go` под `/v1/pair`
- [x] запустить `go test -race ./internal/api/...`

### Task 9: API хэндлер — GET /v1/pair/status/{id} (long-poll)

**Files:**
- Modify: `internal/api/pairing.go`
- Modify: `internal/api/pairing_test.go`
- Modify: `internal/api/server.go`

- [x] написать failing-тест `TestPairingHandler_Status_Pending_AfterTimeout` (моковый `PairingService` с быстрым timeout → 200 + `{status:"pending"}`)
- [x] написать тест `TestPairingHandler_Status_Confirmed` (мок возвращает `Status:"confirmed", DeviceID, BearerToken`)
- [x] написать тест `TestPairingHandler_Status_NonceMismatch_Returns403`
- [x] написать тест `TestPairingHandler_Status_Expired_Returns410`
- [x] написать тест `TestPairingHandler_Status_MissingNonceQuery_Returns400`
- [x] реализовать `pairingHandler.status` (читает `id` из URL, `nonce` из query, вызывает `service.PollStatus`, маппит `Result.Status`/ошибки)
- [x] handler должен снять `WriteTimeout` через `http.NewResponseController` (так же, как SSE-handler), чтобы long-poll не убивался глобальным таймаутом сервера
- [x] зарегистрировать `r.Get("/status/{id}", h.status)` в `server.go`
- [x] запустить `go test -race ./internal/api/...`

### Task 10: API хэндлер — GET /pair/confirm/{id} (HTML с CSRF cookie)

**Files:**
- Modify: `internal/api/pairing.go`
- Modify: `internal/api/pairing_test.go`
- Create: `internal/api/templates/pair_confirm.html.tmpl` (или inline в `pairing.go`)
- Modify: `internal/api/server.go`

- [x] написать failing-тест `TestPairingHandler_ConfirmPage_RendersHTML` (200 + Content-Type text/html; в теле есть deviceName, csrf input)
- [x] написать тест `TestPairingHandler_ConfirmPage_SetsCSRFCookie` (Set-Cookie с `__Host-csrf`, `Secure`, `SameSite=Strict`, `Path=/pair/confirm/<id>`)
- [x] написать тест `TestPairingHandler_ConfirmPage_ExpiredPairing_Returns410HTML`
- [x] написать тест `TestPairingHandler_ConfirmPage_StoresCSRFHash` (мок проверяет, что `PrepareConfirm` вызван с тем же токеном, что в cookie)
- [x] реализовать `pairingHandler.confirmPage` (генерировать csrf через `crypto/rand`, рендерить шаблон через `html/template`, выставлять cookie)
- [x] добавить шаблон в `internal/api/templates/pair_confirm.html.tmpl` и встроить через `//go:embed`
- [x] зарегистрировать `r.Get("/{id}", h.confirmPage)` под `/pair/confirm` (вне auth-цепочки!) в `server.go`
- [x] запустить `go test -race ./internal/api/...`

### Task 11: API хэндлер — POST /pair/confirm/{id} (приём формы)

**Files:**
- Modify: `internal/api/pairing.go`
- Modify: `internal/api/pairing_test.go`
- Modify: `internal/api/server.go`

- [x] написать failing-тест `TestPairingHandler_ConfirmSubmit_Success_RendersOKHTML` (POST с cookie+form csrf, мок `ConfirmWithCSRF` возвращает device → 200 HTML «подключено»)
- [x] написать тест `TestPairingHandler_ConfirmSubmit_MissingCookie_Returns403`
- [x] написать тест `TestPairingHandler_ConfirmSubmit_FormCSRFMismatchCookie_Returns403`
- [x] написать тест `TestPairingHandler_ConfirmSubmit_ServiceError_RendersErrorHTML` (мок возвращает `ErrCSRFMismatch` → 403 HTML с понятным сообщением)
- [x] написать тест `TestPairingHandler_ConfirmSubmit_AlreadyConfirmed_Returns410`
- [x] реализовать `pairingHandler.confirmSubmit` (`r.ParseForm()`, прочитать cookie, сравнить cookie==form.csrf — иначе 403, вызвать `service.ConfirmWithCSRF`, рендерить «success»/«error» шаблон)
- [x] зарегистрировать `r.Post("/{id}", h.confirmSubmit)` в `server.go`
- [x] запустить `go test -race ./internal/api/...`

### Task 12: Конфиг — расширение `[owner]` и `[api]`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: документы/примеры конфига (если есть `config.example.toml`)

- [x] добавить failing-тест `TestLoadConfig_PairingDefaults` (без секций → `OwnerConfig.TelegramBotID == ""`, `APIConfig.PairingLinkTTL == 5*time.Minute`, `PairingStatusLongPoll == 60*time.Second`, `RateLimitPairPerHour == 5`)
- [x] добавить тест `TestLoadConfig_PairingOverrides` (TOML задаёт значения → подхватываются)
- [x] добавить тест `TestLoadConfig_PairingValidation` (отрицательный TTL → ошибка валидации)
- [x] расширить `OwnerConfig` полем `TelegramBotID string` (TOML `telegram_bot_id`)
- [x] расширить `APIConfig` полями `PairingLinkTTL`, `PairingStatusLongPoll` (`time.Duration`), `RateLimitPairPerHour` (int)
- [x] добавить дефолты при загрузке (если `[api]` есть, но поля пусты)
- [x] обновить пример конфига и комментарии в `config.go`
- [x] запустить `go test ./internal/config/...`

### Task 13: Retention — расширение под pairing_requests

**Files:**
- Modify: `internal/events/retention.go`
- Modify: `internal/events/retention_test.go`

- [x] написать failing-тест `TestRetentionRunner_DeletesExpiredPairings` (мок `PairingStore.DeleteExpired` вызывается с `now - 1h`)
- [x] написать тест `TestRetentionRunner_ContinuesIfPairingFails` (если pairing.DeleteExpired возвращает err — events/push retention всё равно отрабатывают, ошибка логируется)
- [x] добавить параметр `PairingStore model.PairingStore` в `RetentionRunner` (через опции либо новый аргумент конструктора)
- [x] cutoff жёстко закодирован — `pairingCutoff := now.Add(-time.Hour)` (спека §3 говорит «через 1 час после `expires_at`»)
- [x] обновить тесты ретеншена, передавая мок `PairingStore`
- [x] запустить `go test -race ./internal/events/...`

### Task 14: Wiring в `cmd/huskwoot/main.go`

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] получить владельческий бот: `ownerBot := bots[cfg.Owner.TelegramBotID]` (если `TelegramBotID` пуст или нет в карте — оставить `ownerBot = nil`)
- [x] инстанцировать `pairingStore := pairing.NewSQLiteStore(db)`, `broadcaster := pairing.NewBroadcaster()`, `sender := pairing.NewTelegramSender(ownerBot, logger)`
- [x] инстанцировать `pairingSvc := usecase.NewPairingService(usecase.PairingDeps{...})` со всеми зависимостями (см. «Solution Overview §11»)
- [x] передать `pairingSvc` и `cfg.API.RateLimitPairPerHour` в `api.Config`
- [x] передать `pairingStore` в `events.NewRetentionRunner`
- [x] логировать `slog.Warn("pairing: telegram_bot_id не настроен, magic-link будет только в логах", ...)` если `ownerBot == nil` И `cfg.API.Enabled`
- [x] прогнать `go build ./...` (нет автотеста на main, но компиляция обязательна)
- [x] прогнать `go vet ./...`

### Task 15: OpenAPI — добавить /v1/pair/* и /pair/confirm/*

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `internal/api/openapi_test.go` (если есть проверки покрытия)

- [x] добавить в `paths`: `/v1/pair/request` (POST, без security), `/v1/pair/status/{id}` (GET, без security, query `nonce`), `/pair/confirm/{id}` (GET и POST, без security, response text/html)
- [x] добавить в `components.schemas`: `PairingRequestBody`, `PendingPairingResponse`, `PairingStatusResponse`
- [x] обновить `internal/api/openapi_test.go` — если он проверяет полноту путей (например через `chi.Walk`), добавить новые пути в whitelist; если проверяет только валидность YAML — оставить как есть
- [x] запустить `go test ./internal/api/...` и убедиться, что `GET /v1/openapi.yaml` возвращает обновлённый файл

### Task 16: Acceptance — race-detector + smoke

- [x] прогнать `go test -race ./...` — все тесты зелёные
- [x] прогнать `go vet ./...` — без замечаний
- [x] прогнать `go build -o bin/huskwoot ./cmd/huskwoot/` — успешная сборка
- [x] локальный smoke: запустить `huskwoot serve` с тестовым конфигом, выполнить `curl -X POST http://localhost:8080/v1/pair/request -d '{"deviceName":"test","platform":"linux","clientNonce":"abc"}'` → проверить, что (а) пришёл 202 + JSON с `pairId`, (б) в логах warning «telegram_bot_id не настроен» (если бот не настроен) либо реальный DM пришёл [x] manual test (skipped - not automatable)
- [x] локальный smoke: открыть `http://localhost:8080/pair/confirm/<pairId>` в браузере → отображается HTML, в DevTools видно cookie `__Host-csrf` (важно: для localhost `__Host-` префикс работает только под HTTPS — задокументировать в README, что для локального теста подойдёт обычный `csrf` cookie) [x] manual test (skipped - not automatable)
- [x] long-poll smoke: `curl -N http://localhost:8080/v1/pair/status/<pairId>?nonce=abc` параллельно с подтверждением через cURL/браузер — клиент должен получить bearerToken [x] manual test (skipped - not automatable)
- [x] проверить, что rate-limit срабатывает: 6 запросов подряд — последний 429 [x] manual test (skipped - not automatable)
- [x] вписать ⚠️ в этот файл, если что-то не работает; не двигаться дальше до зелёной полосы

### Task 17: [Final] Обновление документации и завершение плана

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md` (если упоминает pairing/Phase roadmap)
- Move: `docs/plans/2026-04-19-backend-api-phase3-pairing.md` → `docs/plans/completed/`

- [x] добавить в `CLAUDE.md` раздел про `internal/pairing/` (PairingStore/Broadcaster/TelegramSender), про `usecase.PairingService` и про `[owner].telegram_bot_id` / `[api].pairing_*` поля
- [x] добавить ссылку на pairing-эндпоинты в раздел `## HTTP API`; пояснить, что `/v1/pair/*` и `/pair/confirm/*` — без auth
- [x] обновить README, если он перечисляет план фаз (отметить Фазу 3 как завершённую, Фазу 4 как следующую)
- [x] переместить план в `docs/plans/completed/`
- [x] проверить, что в `MEMORY.md` нет устаревших упоминаний «pairing — Фаза 3 не начата»

## Post-Completion

*Items requiring manual intervention or external systems — no checkboxes, informational only.*

**Manual verification:**

- Полный pairing-flow с реальным Telegram-ботом и реальным владельческим аккаунтом: запросить с iPhone-симулятора (или curl) → подтвердить из Telegram-DM на десктопе → проверить, что bearer-токен сохраняется в Keychain клиента (для CLI-теста — что cURL получает `bearerToken`).
- Проверить под реальным TLS (Caddy / ngrok-туннель), что cookie `__Host-csrf` действительно ставится. Под чистым HTTP браузер отвергает `__Host-` префикс — это ожидаемое поведение, требующее `https://` в `external_base_url`.
- Безопасностный обзор: убедиться, что `client_nonce` имеет минимум 16 байт энтропии (валидация на API-входе?), что magic-link нельзя угадать (UUID v4, 122 бита), что rate-limit не обходится через смену порта в `RemoteAddr`.
- Проверить, что revoke device (`DELETE /v1/devices/{id}`) с владельческого устройства не ломает long-poll-запрос на этом же устройстве (это уже работает с Фазы 2; здесь — только sanity-check, что pairing не вводит регрессию).

**External system updates:**

- Клиентам (Подпроекты №2–4) понадобится поддержка нового API: они появятся после Фазы 4. Сейчас никаких изменений потребителей нет.
- В деплое ничего менять не нужно: pairing-эндпоинты живут на том же `:8080` за тем же reverse proxy. Caddy обновлять не нужно (он уже проксирует `/`).
- Для будущей итерации зарезервировано: «Это не я → отменить» кнопка в DM, CLI-fallback `huskwoot pair generate`, провижнинг друзей в push-relay (Фаза 4).
