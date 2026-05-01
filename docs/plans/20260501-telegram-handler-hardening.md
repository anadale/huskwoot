# Telegram Handler Hardening

## Overview

Четыре связанных изменения в `internal/channel/telegram.go`, повышающих устойчивость и надёжность хендлера Telegram:

1. **(1.2)** Экспоненциальный бэкофф для `getUpdates` с учётом `Retry-After`. Сейчас при сетевой ошибке `Watch` уходит в tight loop без паузы.
2. **(1.3)** Зафиксировать at-least-once контракт курсора: один `saveCursor` на батч `getUpdates` после полного его прохода. Сейчас семантика расщеплена: курсор сохраняется в части веток `processRawUpdate` и не сохраняется на handler-ошибке, но `offset` всё равно инкрементирован — update теряется в текущем процессе.
3. **(1.4)** Блокирующая отправка `ForwardSignal` с тайм-аутом 5s; при тайм-ауте — `slog.Error` и пропуск сохранения курсора, чтобы триггер пришёл повторно после восстановления. Сейчас `default` ветка тихо роняет триггер и при этом сохраняет курсор.
4. **(1.1)** Перенести транскрипцию голоса в персистентную очередь: SQLite-таблица `voice_queue` + воркер `transcribe.Worker` по образцу `push.Dispatcher`. Сейчас Whisper/Yandex (до 60s) выполняется синхронно внутри `processRawUpdate` и блокирует поллинг для всех чатов.

Польза: бот не глохнет при сетевых проблемах, не залипает на голосовых, не теряет триггеры forward-batch и не дублирует обновления при рестарте.

## Context (from discovery)

- Файлы: `internal/channel/telegram.go`, `internal/channel/telegram_test.go`, `cmd/huskwoot/main.go`
- Новые файлы: миграция `011_voice_queue.sql`, `internal/storage/voice_queue.go`, `internal/transcribe/worker.go`
- Связанные паттерны: `internal/storage/forward_buffer.go` (SQLite store с tx-методами), `internal/push/dispatcher.go` и `internal/push/queue.go` (воркер + очередь с retry/backoff)
- Зависимости: `model.Transcriber`, `model.History`, `model.Channel`, `pipeline.Process` (handler), `model.ForwardDispatchSignal`
- Текущая миграция: `010_forward_buffer.sql` — следующая будет `011_*`

## Development Approach

- **testing approach**: TDD — по CLAUDE.md проекта тесты пишутся до реализации
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional — they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run `go test ./... && go vet ./...` after каждой задачи
- maintain backward compatibility (никаких миграций `down` без обратимости; конфиг — поля с дефолтами)

## Testing Strategy

- **unit tests**: обязательны в каждой задаче
- стиль — table-driven `[]struct{name, input, want}`, hand-written mocks (без testify/mock — см. CLAUDE.md)
- e2e тестов в проекте нет (Go-сервис без UI), достаточно `go test ./...`
- для миграций: проверить `goose up && goose down && goose up` локально
- для воркера: тесты с поддельным транскрайбером и проверкой retry/backoff

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

Каждая задача — отдельный коммит, между задачами — рабочее состояние. Порядок выбран от простого к сложному, каждая опирается на предыдущую:

- **Task 1 (1.2)** — изолированное изменение в `Watch` loop, не трогает остальной код. Делается первым, чтобы получить устойчивость к сетевым ошибкам ещё до рефакторинга курсора.
- **Task 2 (1.3)** — рефакторинг `processRawUpdate`, но контракт фиксируется заранее (godoc) и упрощает код для последующих задач (одно место для save вместо четырёх).
- **Task 3 (1.4)** — небольшое изменение в `handlePrivateDMForBuffer`, опирается на новый контракт курсора из Task 2 (теперь «не сохранить курсор» = вернуть `false` из метода).
- **Task 4 (1.1)** — отдельная подсистема: миграция + store + воркер + интеграция в `main.go`. Не пересекается с предыдущими задачами на уровне кода.

## Technical Details

### Task 1 — backoff в Watch

```go
backoff := time.Second
for {
    // ... ctx check, expirePending, build params
    resp, err := w.bot.MakeRequest("getUpdates", params)
    if err != nil {
        if ctx.Err() != nil { return ctx.Err() }
        wait := backoff
        var tgErr *tgbotapi.Error
        if errors.As(err, &tgErr) && tgErr.RetryAfter > 0 {
            wait = time.Duration(tgErr.RetryAfter) * time.Second
        }
        slog.ErrorContext(ctx, "fetching updates", "error", err, "wait", wait)
        select {
        case <-ctx.Done(): return ctx.Err()
        case <-time.After(wait):
        }
        if backoff < 30*time.Second { backoff *= 2 } else { backoff = 30*time.Second }
        continue
    }
    backoff = time.Second // reset on success
    // ... existing parse/process loop
}
```

Пауза `wait` инжектится через поле канала или через `clock`-абстракцию для тестируемости. Самый простой путь — поле `w.sleepFn func(time.Duration)` с дефолтом `time.Sleep` и переопределением в тестах; в продакшене всё равно используется `select { case <-ctx.Done(); case <-time.After }`, поэтому проще ввести `w.sleeper` или функцию-параметр.

### Task 2 — курсор: один save на батч

Текущая семантика хаотична: `saveCursor` вызывается в 6 точках `processRawUpdate` и `handlePrivateDMForBuffer`. Новая:

```go
// Watch loop, после for _, upd := range updates:
for _, upd := range updates {
    offset = upd.UpdateID + 1
    w.processRawUpdate(ctx, upd, handler)
}
if len(updates) > 0 {
    last := updates[len(updates)-1].UpdateID
    if err := w.saveUpdateCursor(ctx, last); err != nil {
        slog.Error("saving batch cursor", "error", err, "update_id", last)
    }
}
```

`processRawUpdate` теряет внутренний `saveCursor` closure и все его вызовы. Возвращаемый тип остаётся `void` — handler-ошибка только логируется (как сейчас). Семантика становится **at-least-once**: при крахе посреди батча после рестарта повторно обработаются 1-N обновлений того же батча. Идемпотентность пайплайна это переваривает (классификатор/экстрактор делает SELECT перед INSERT, реакции перезаписываются).

⚠️ Обновить godoc `Watch`: «Курсор фиксируется один раз после полной обработки каждого батча. Гарантия: at-least-once. Идемпотентность обеспечивается слоем pipeline.»

### Task 3 — блокирующий ForwardSignal

```go
const forwardSignalTimeout = 5 * time.Second
ctxSend, cancel := context.WithTimeout(ctx, forwardSignalTimeout)
defer cancel()
select {
case w.cfg.ForwardSignal <- sig:
    // курсор сохранится в Watch на уровне батча
    return true
case <-ctxSend.Done():
    slog.ErrorContext(ctx, "forward signal send timed out — trigger will be retried after restart",
        "chat_id", chatIDStr)
    return false // курсор НЕ сохраняется (Task 2 его сохраняет на батч; здесь возврат false означает «считать необработанным»)
}
```

⚠️ После Task 2 «не сохранить курсор» = «не выйти из батча на этом update». Поскольку курсор сохраняется в конце батча независимо от возврата `handlePrivateDMForBuffer`, чтобы триггер действительно ретраился, нужен другой механизм: либо вернуться из `Watch` с ошибкой (агрессивно — перезапустит весь поллинг), либо завести флаг «батч содержал нерасставленный update» и при флаге — не двигать курсор за пределы прошлого update_id.

После размышлений выбрал: **при тайм-ауте `processRawUpdate` возвращает false**, и в `Watch` после батча курсор сохраняется до **последнего update перед первым `false`**. Это сохраняет at-least-once, но требует мелкого изменения сигнатуры `processRawUpdate` → `bool`.

### Task 4 — voice_queue

**Миграция `011_voice_queue.sql`:**

```sql
-- +goose Up
CREATE TABLE voice_queue (
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
CREATE INDEX idx_voice_queue_pending ON voice_queue(next_attempt_at);

-- +goose Down
DROP INDEX IF EXISTS idx_voice_queue_pending;
DROP TABLE IF EXISTS voice_queue;
```

**`internal/storage/voice_queue.go`** — `SQLiteVoiceQueue`:
- `Enqueue(ctx, VoiceJob) error` — INSERT OR IGNORE по `(chat_id, message_id)` для идемпотентности.
- `Claim(ctx, now time.Time) (*VoiceJob, error)` — SELECT первой строки с `next_attempt_at <= now`, ORDER BY id LIMIT 1. Возвращает `nil, nil` если пусто.
- `Complete(ctx, id int64) error` — DELETE.
- `Reschedule(ctx, id int64, nextAt time.Time, lastErr string) error` — UPDATE attempts++ + next_attempt_at + last_error.

**`internal/transcribe/worker.go`** — `Worker`:
- Поля: `queue *SQLiteVoiceQueue`, `transcriber model.Transcriber`, `download func(ctx, fileID) ([]byte, error)`, `handler func(ctx, model.Message) error`, `buildMessage func(VoiceJob, text string) model.Message` (передаёт реконструкцию ReplyFn/ReactFn/HistoryFn в TelegramChannel), `interval time.Duration`, `maxAttempts int`, `localizer *goI18n.Localizer`, `replyVoiceI18n func(ctx, chatID int64, key string, data any)`.
- `Run(ctx)` — тикер, на каждый тик `Claim` → download → transcribe → `handler(buildMessage(...))` → `Complete`. На ошибке: `Reschedule` с бэкоффом 30s → 2m → 10m → drop after 4-й попытки (как push.Dispatcher).
- При drop отправляется `voice.error` пользователю и удаляется из очереди.

**`internal/channel/telegram.go`:**
- `convertDMMessage` теперь не вызывает `transcribeVoice` синхронно. Вместо этого `processRawUpdate` для voice/audio DM кладёт `VoiceJob` в очередь через инжектируемый `voiceEnqueueFn func(ctx, VoiceJob) error` и **возвращает true** (сохраняет курсор), не вызывая handler.
- Метод `BuildVoiceMessage(job VoiceJob, text string) model.Message` — публичный, используется воркером для сборки `model.Message` с правильными closures.

**`cmd/huskwoot/main.go`:**
- Создать `voiceQueue := storage.NewSQLiteVoiceQueue(db)`.
- Создать `voiceWorker := transcribe.NewWorker(...)` если транскрайбер сконфигурирован.
- Запустить `go voiceWorker.Run(ctx)`.
- Передать `voiceQueue.Enqueue` в `TelegramChannelConfig` (новое поле).

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): код, тесты, миграции, godoc-комментарии
- **Post-Completion** (no checkboxes): ручная проверка под нагрузкой voice-сообщений, проверка миграции `up/down` в проде, мониторинг `voice_queue.attempts` после релиза

## Implementation Steps

### Task 1: Backoff в Watch с учётом RetryAfter

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [ ] добавить поле `sleeper func(context.Context, time.Duration) error` в `TelegramChannel` с дефолтом, использующим `select { case <-ctx.Done(); case <-time.After }`
- [ ] в `Watch` ввести локальную переменную `backoff` и вызывать `w.sleeper(ctx, wait)` при ошибке `MakeRequest`
- [ ] парсить `*tgbotapi.Error` через `errors.As` и при `RetryAfter > 0` использовать его вместо backoff
- [ ] сбрасывать `backoff` в 1s при успешном `MakeRequest`
- [ ] добавить test `TestWatch_BackoffOnError`: mockBot возвращает 3 ошибки подряд, потом успех; зафиксировать timestamps вызовов через тестовый sleeper и проверить рост 1s→2s→4s
- [ ] добавить test `TestWatch_RetryAfterRespected`: mockBot возвращает `&tgbotapi.Error{RetryAfter: 7}`, проверить что sleeper получил 7s
- [ ] добавить test `TestWatch_BackoffResetsOnSuccess`: ошибка → backoff увеличился, успех, снова ошибка → backoff = 1s
- [ ] `go test ./internal/channel/ && go vet ./internal/channel/` — должны пройти

### Task 2: At-least-once контракт курсора

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [ ] обновить godoc `Watch`: документировать at-least-once семантику и причину
- [ ] изменить сигнатуру `processRawUpdate` с `void` на `bool` (true = update полностью обработан, false = нужно остановить продвижение курсора на этом update)
- [ ] удалить `saveCursor` closure и все его вызовы из `processRawUpdate`
- [ ] удалить `saveUpdateCursor` вызовы из `handlePrivateDMForBuffer` (вернёт `bool`-результат вместо них)
- [ ] в `Watch` после `for _, upd := range updates` отслеживать индекс последнего update, для которого `processRawUpdate` вернул `true` подряд (без false посередине), сохранить его update_id через `saveUpdateCursor`
- [ ] обновить существующие тесты курсора: ожидание сместится с per-update save на per-batch save
- [ ] добавить test `TestWatch_BatchCursorSavedOnce`: батч из 5 updates, мок `state.SaveCursor` вызывается ровно 1 раз с update_id последнего
- [ ] добавить test `TestWatch_BatchCursorStopsAtFirstFalse`: 5 updates, третий возвращает false → `SaveCursor` вызван с update_id второго
- [ ] добавить test `TestWatch_HandlerErrorStillSavesCursor`: handler возвращает error, но курсор всё равно сохраняется (handler-ошибки не блокируют курсор по новой семантике — only false from processRawUpdate does)
- [ ] `go test ./... && go vet ./...` — все пакеты должны пройти

### Task 3: Блокирующая отправка ForwardSignal с тайм-аутом

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [ ] добавить константу `forwardSignalTimeout = 5 * time.Second` (или поле в `TelegramChannelConfig` с дефолтом)
- [ ] в `handlePrivateDMForBuffer` заменить `select { case ch <- sig: default: warn }` на блокирующий `select` с `time.After(forwardSignalTimeout)` и `ctx.Done()`
- [ ] при тайм-ауте — `slog.Error` и `return false` (триггер переобработается через at-least-once механизм Task 2)
- [ ] при `ctx.Done()` — `return false` без лога ошибки (нормальный shutdown)
- [ ] обновить test `TestForwardSignalDropOnFullChannel` (если есть) под новое поведение: вместо тихого drop ожидать тайм-аут и `return false`
- [ ] добавить test `TestForwardSignal_BlocksUntilCapacity`: канал размером 1 уже занят, после освобождения за 100ms — отправка проходит
- [ ] добавить test `TestForwardSignal_TimeoutLogsAndReturnsFalse`: канал заполнен, никто не читает; через `forwardSignalTimeout` метод возвращает false
- [ ] `go test ./internal/channel/ && go vet ./internal/channel/`

### Task 4: Персистентная очередь транскрипции голоса

**Files:**
- Create: `internal/storage/migrations/011_voice_queue.sql`
- Create: `internal/storage/voice_queue.go`
- Create: `internal/storage/voice_queue_test.go`
- Create: `internal/transcribe/worker.go`
- Create: `internal/transcribe/worker_test.go`
- Modify: `internal/model/interfaces.go` (добавить `VoiceQueue` interface и `VoiceJob` struct)
- Modify: `internal/channel/telegram.go` (заменить inline `transcribeVoice` на enqueue)
- Modify: `internal/channel/telegram_test.go`
- Modify: `cmd/huskwoot/main.go` (wire up worker)

- [ ] создать миграцию `011_voice_queue.sql` с таблицей `voice_queue` и индексом `idx_voice_queue_pending`
- [ ] добавить `VoiceJob` в `internal/model/interfaces.go` (поля: ID, ChatID, MessageID, FileID, MimeType, Duration, ReceivedAt, Attempts)
- [ ] добавить `VoiceQueue` interface в `internal/model/interfaces.go` с методами `Enqueue`, `Claim`, `Complete`, `Reschedule`
- [ ] написать тесты для `SQLiteVoiceQueue` (table-driven): Enqueue идемпотентность; Claim возвращает первую готовую и nil если пусто; Reschedule увеличивает attempts; Complete удаляет
- [ ] реализовать `internal/storage/voice_queue.go` с `SQLiteVoiceQueue`
- [ ] прогнать `go test ./internal/storage/`
- [ ] написать тесты для `transcribe.Worker`: успешная транскрипция вызывает handler с правильным `model.Message`; ошибка транскрипции вызывает Reschedule с правильным backoff (30s → 2m → 10m); drop после 4-й попытки отправляет `voice.error` и Complete; tick без работы — no-op
- [ ] реализовать `internal/transcribe/worker.go` с `Worker.Run(ctx)` (тикер + Claim/process/Reschedule)
- [ ] прогнать `go test ./internal/transcribe/`
- [ ] добавить публичный метод `(*TelegramChannel).BuildVoiceMessage(job VoiceJob, text string) model.Message` собирающий `model.Message{Kind: DM, ReplyFn: w.buildReplyFn(...), ...}`
- [ ] добавить поле `VoiceEnqueueFn func(ctx, VoiceJob) error` в `TelegramChannelConfig`; nil = старое инлайн-поведение (фоллбэк, удалить позже)
- [ ] изменить `processRawUpdate` для DM voice/audio: вместо `transcribeVoice` вызвать `cfg.VoiceEnqueueFn` и вернуть `true` (курсор сохранится)
- [ ] удалить старый `transcribeVoice` или оставить только проверки `MaxVoiceDuration` и `Transcriber == nil` перед enqueue (отказы остаются синхронными — пользователь сразу видит «слишком длинное»/«отключено»)
- [ ] обновить тесты telegram: вместо проверки inline-вызова Transcriber проверить, что `VoiceEnqueueFn` получил правильный `VoiceJob`
- [ ] в `cmd/huskwoot/main.go` создать `voiceQueue` и `voiceWorker`, запустить `go voiceWorker.Run(ctx)` если транскрайбер сконфигурирован
- [ ] прокинуть `voiceQueue.Enqueue` в `TelegramChannelConfig.VoiceEnqueueFn`
- [ ] прогнать `go test ./... && go vet ./...` — должны пройти все пакеты
- [ ] вручную проверить миграцию: `goose up`, отправить голосовое в DM, убедиться что появляется строка в `voice_queue`, после транскрипции — исчезает; `goose down` — таблица удалена без ошибок

### Task 5: Verify acceptance criteria

- [ ] verify backoff не превышает 30s даже при долгом сбое
- [ ] verify RetryAfter из Telegram учитывается
- [ ] verify курсор сохраняется один раз на батч и останавливается на первом false
- [ ] verify ForwardSignal тайм-аут логирует Error и не теряет триггер
- [ ] verify voice queue выживает рестарт: kill процесса между Claim и Complete → после рестарта job в `voice_queue.Claim` отдаётся снова
- [ ] verify voice transcription больше не блокирует поллинг: пока идёт транскрипция, новые сообщения в других чатах обрабатываются
- [ ] run full test suite: `go test ./... && go vet ./...`

### Task 6: Update documentation and finalize

- [ ] обновить `CLAUDE.md`: задокументировать `voice_queue` в разделе SQLite stores, обновить описание pipeline для DM голоса (теперь асинхронно), добавить заметку о at-least-once и backoff в секции Telegram
- [ ] обновить `cmd/huskwoot/relay.example.toml` если потребуется (вряд ли — это для relay)
- [ ] обновить `config.toml`-пример в README/CLAUDE для voice (никаких новых полей не добавляли — настройки те же `[transcriber]`)
- [ ] переместить план в `docs/plans/completed/`

## Post-Completion

**Manual verification:**
- Отправить ~50 голосовых подряд в DM, убедиться что бот не глохнет на 30+ секундных запросах транскрипции
- Симулировать сетевой сбой (отключить интернет на 30s), убедиться что после восстановления `Watch` продолжает работать без рестарта процесса
- Проверить под Telegram rate limit (генерация ~30 RPS reply'ев) что `RetryAfter` корректно соблюдается
- При высокой нагрузке forward-batch (10+ батчей подряд) проверить что триггер не теряется

**External system updates:**
- Никаких — нет внешних потребителей, изменения локальные для huskwoot-инстанса
- Push relay не затрагивается
- Конфиг-файл совместим: новых обязательных полей нет

**Monitoring after release:**
- Логировать `voice_queue` глубину раз в час (опционально в следующей итерации)
- Метрика количества `Reschedule` для voice — индикатор проблем с транскрайбером
