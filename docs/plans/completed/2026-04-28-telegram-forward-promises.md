# Telegram Forward Promises — извлечение моих обещаний из форварднутых сообщений

## Overview

В DM с ботом приходят форварды переписки 1:1 с другим человеком. Бот накапливает их в персистентном буфере, по таймауту тишины или по триггер-сообщению извлекает обещания, которые **я** дал собеседнику, создаёт задачи и (опционально) удаляет форварды из DM, чтобы не зашумлять историю.

**Проблема:** в 1:1 чатах Telegram нельзя добавить бота, поэтому обещания, которые я даю собеседнику, теряются. Сейчас единственный путь — пересказывать их боту вручную.

**Решение:** позволить пересылать произвольную выборку сообщений из 1:1 чата в DM с ботом и автоматически их разбирать. Источник истины — сами форварды (с метаданными `forward_from`).

**Интеграция:** новый `MessageKindForwardBatch` идёт через Pipeline по отдельной ветке, переиспользуя существующие типы `Classifier`/`Extractor` с зеркальными промптами. Аккумулятор — SQLite-таблица `forward_buffer`, диспетчер и sweeper — две новые горутины.

## Context (from discovery)

- **Проект:** Huskwoot, Go 1.26, Cobra CLI + Telegram + SQLite (goose migrations).
- **Релевантные директории:**
  - `internal/channel/telegram.go` — `TelegramChannel.handleDirectMessage` (детекция forward, аккумулятор, исключение из history)
  - `internal/pipeline/pipeline.go` — добавление ветки `MessageKindForwardBatch`, диспетчер, sweeper
  - `internal/model/interfaces.go` — новые `ForwardBuffer`, `MessageKindForwardBatch`
  - `internal/storage/` — новая реализация `SQLiteForwardBuffer`
  - `internal/storage/migrations/` — следующая миграция `010_forward_buffer.sql` (текущий tip = 009)
  - `internal/ai/prompts/` — новые `classifier_forward_system_{ru,en}.tmpl`, `extractor_forward_system_{ru,en}.tmpl`. Загрузка через `loadPrompt(promptsFS, lang, name)`
  - `internal/ai/classifier.go` — паттерн `NewSimpleClassifier`/`NewGroupClassifier`, добавляем `NewForwardClassifier`
  - `internal/ai/extractor.go` — паттерн `NewExtractor`, добавляем `NewForwardExtractor` (или конструктор с явным prompt name)
  - `internal/config/` — новые поля в `[channels.telegram]`
  - `internal/i18n/locales/{ru,en}.json` — новые ключи подтверждений
  - `cmd/huskwoot/main.go` — wiring
- **Паттерны:**
  - Tx-aware writes: `*Tx`-методы внутри одной транзакции (`CreateTaskTx` + `EventStore.Insert` + `PushQueue.Enqueue` + новый `ForwardBuffer.DeleteBatchTx`)
  - `INSERT OR IGNORE` для идемпотентности
  - Mocks вручную, table-driven tests, TDD
  - Все public-методы первым аргументом принимают `context.Context`
  - Артефакты `go build` идут в `bin/`
- **Зависимости:** `go-telegram-bot-api/v5` (нужно убедиться, что есть `DeleteMessagesConfig`; иначе fallback на цикл `DeleteMessage`).

## Development Approach

- **Testing approach:** **TDD** (по конвенции проекта — `CLAUDE.md` явно требует «tests are written before the implementation»).
- Каждый таск делается полностью до перехода к следующему.
- Маленькие, сфокусированные изменения.
- **CRITICAL: каждый таск должен включать новые/обновлённые тесты** для всех изменённых функций. Тесты — обязательный deliverable, не опциональный.
- **CRITICAL: все тесты должны проходить перед началом следующего таска.** `go test ./...` + `go vet ./...`.
- Запускать тесты после каждого изменения.
- Backward compatibility: новые поля конфига имеют дефолты, фича выключена при `forward_batch_timeout = ""` или `"0s"`.

## Testing Strategy

- **Unit tests:** обязательны в каждом таске — таблично-управляемые `[]struct{name, input, want}`.
- **Integration tests:** Pipeline + моки `Classifier`/`Extractor`/`TaskService`/`ForwardBuffer` для проверки сценариев (happy path, skip, restart, delete failure, concurrency).
- **E2E tests:** в проекте нет UI-e2e — пропускаем.
- **Команды:**
  - `go test ./...` — полный прогон
  - `go vet ./...` — проверка
  - `go build -o bin/huskwoot ./cmd/huskwoot/` — сборка

## Progress Tracking

- Отметка `[x]` сразу при завершении пункта.
- Новые задачи добавляются с префиксом ➕.
- Блокеры — с префиксом ⚠️.
- При расхождении со scope обновляется этот файл.

## Solution Overview

### Высокоуровневая схема

```
TelegramChannel.handleDirectMessage(m)
  │
  ├─ if forward → ForwardBuffer.Append(...) + НЕ пишем в SQLiteHistory
  │                + сигнал НЕ шлём (диспетчер ждёт триггера или таймаута)
  │
  ├─ if обычное DM И буфер для chat_id не пуст:
  │     → диспетчер ← chat_id (текст этого DM = триггер)
  │     → НЕ пишем в SQLiteHistory (триггер — мета-команда)
  │     → НЕ передаём в ChatService
  │
  └─ иначе обычное DM → ChatService.HandleMessage  (без изменений)

ForwardBatchSweeper (Ticker 5s)
  → ListPendingChats(now - timeout)
  → диспетчер ← chat_id (без триггер-текста)

Диспетчер (single-goroutine consumer of `chan ForwardDispatchSignal`)
  → Pipeline.Process(MessageKindForwardBatch{forwards, triggerText, deleteFn})
```

### Pipeline.Process для ForwardBatch

1. Парсим `triggerText` через `parseTriggerProjectHint` → `*model.Project | Inbox`.
2. Форматируем буфер в текстовый блок с метками `[я, …]` / `[counterpart_name, …]`, отсортированный по `original_date`.
3. `ForwardClassifier.Classify(ctx, formatted)` → `promise` | `skip`.
   - `skip` → tx: `DeleteBatchTx`. Reply: «обещаний от тебя не нашёл, сообщения оставил». Удаление в Telegram **не выполняется**.
4. `promise` → `ForwardExtractor.Extract(ctx, formatted, counterpartName)` → `[]model.Task`.
5. tx: `CreateTaskTx` × N + `EventStore.Insert` × N + `PushQueue.Enqueue` × N + `ForwardBuffer.DeleteBatchTx`. Commit.
6. `Broker.Notify(events)` — после commit.
7. Если `cfg.DeleteForwardsAfterExtract && len(tasks) > 0` — вызываем `deleteFn(messageIDs + triggerID)`. Ошибка → warning-лог.
8. Reply: «извлёк N задач в проект «X»: …». Этот reply пишется в SQLiteHistory (через стандартный `recordBotReply`).

### Гарантии

- **Атомарность:** «задачи + очистка буфера» в одной tx.
- **Идемпотентность:** повторные Telegram update'ы → `INSERT OR IGNORE`.
- **Переживание рестарта:** буфер в SQLite; sweeper при первом тике обрабатывает зависшие батчи.
- **Race-free:** один диспетчер-горутина обрабатывает chat_id последовательно; sweeper и Telegram-канал шлют сигналы в общий канал.
- **Гигиена истории:** форварды и триггер-сообщения **не пишутся** в `SQLiteHistory`.

## Technical Details

### Таблица `forward_buffer`

```sql
CREATE TABLE forward_buffer (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id          TEXT    NOT NULL,
    message_id       TEXT    NOT NULL,
    forward_from_id  TEXT,
    forward_from_name TEXT,
    is_self_authored INTEGER NOT NULL,
    original_date    INTEGER NOT NULL,
    text             TEXT    NOT NULL DEFAULT '',
    received_at      INTEGER NOT NULL,
    UNIQUE(chat_id, message_id)
);
CREATE INDEX idx_forward_buffer_chat_received
    ON forward_buffer(chat_id, received_at);
```

### Интерфейс `ForwardBuffer`

```go
type ForwardEntry struct {
    ChatID, MessageID         string
    ForwardFromID, ForwardFromName string
    IsSelfAuthored            bool
    OriginalDate, ReceivedAt  time.Time
    Text                      string
}

type ForwardBuffer interface {
    Append(ctx context.Context, e ForwardEntry) error
    LastReceivedAt(ctx context.Context, chatID string) (time.Time, bool, error)
    LoadBatch(ctx context.Context, chatID string) ([]ForwardEntry, error)
    DeleteBatchTx(ctx context.Context, tx *sql.Tx, chatID string) error
    ListPendingChats(ctx context.Context, before time.Time) ([]string, error)
}
```

### `MessageKindForwardBatch` и payload

```go
const MessageKindForwardBatch MessageKind = "forward_batch"

type ForwardBatchPayload struct {
    ChatID       string
    Forwards     []ForwardEntry
    TriggerText  string                      // "" если пришли по таймауту
    TriggerID    string                      // "" если пришли по таймауту
    DeleteFn     func(ctx context.Context, ids []string) error  // nil если фича удаления выключена
    ReplyFn      func(ctx context.Context, text string) error
    Counterpart  string                      // самый частый forward_from_name среди is_self_authored=false
}
```

`Message.Kind = MessageKindForwardBatch`, `Message.ForwardBatch *ForwardBatchPayload` (новое поле в `model.Message`).

### Конфигурация `[channels.telegram]`

```toml
forward_batch_timeout         = "10s"   # 0/empty → фича выключена
delete_forwards_after_extract = false
```

Валидация: `time.ParseDuration` для timeout; пустая строка эквивалентна `"0s"` и выключает фичу.

### `parseTriggerProjectHint`

```go
// Извлекает первый токен вида "#<alias_or_slug>" из текста.
// Возвращает hint и текст без хинта (пригодится для логов).
// "обработай #вася задачи" → ("вася", "обработай  задачи")
func parseTriggerProjectHint(text string) (hint, rest string)
```

### Форматирование входа для LLM

```go
// FormatForwardBatch возвращает текст вида:
//
//   Переписка между мной и Вася:
//
//   [я, 2026-04-25 14:30] Завтра пришлю слайды.
//   [Вася, 2026-04-25 14:32] Спасибо.
func FormatForwardBatch(forwards []ForwardEntry, counterpart string, loc *time.Location) string
```

Сортировка по `OriginalDate` восходящая. Метка автора: `я` если `IsSelfAuthored`, иначе `ForwardFromName` (или `"unknown"` если пусто). Для пустого `Text` — суффикс `[голосовое/вложение]`.

### Telegram delete

```go
// deleteMessages удаляет сообщения батчами по 100 через DeleteMessagesConfig.
// Если API не поддерживает батч (старая версия библиотеки) — fallback на цикл DeleteMessage.
func (w *TelegramChannel) deleteMessages(ctx context.Context, chatID int64, messageIDs []int) error
```

Окно 48ч у Telegram — обработка происходит за секунды, в окно укладываемся.

## What Goes Where

- **Implementation Steps** (`[ ]`): миграция, код, тесты, обновления промптов, конфига и i18n, документации.
- **Post-Completion** (без чекбоксов): ручное тестирование на живом Telegram-аккаунте, проверка с реальной LLM (классификатор/экстрактор) — не покрывается unit-тестами.

## Implementation Steps

### Task 1: Миграция SQLite для `forward_buffer`

**Files:**
- Create: `internal/storage/migrations/010_forward_buffer.sql`

- [x] создать `010_forward_buffer.sql` с `+goose Up` (CREATE TABLE + CREATE INDEX) и `+goose Down` (DROP INDEX + DROP TABLE)
- [x] убедиться, что миграция подхватывается через `embed.FS` в `migrations.go` (она использует glob — добавление файла достаточно)
- [x] запустить `go test ./internal/storage/...` — миграция должна применяться без ошибок (существующий `migrations_test.go` проверяет up/down)
- [x] `go test ./internal/storage/...` должен пройти перед следующим таском

### Task 2: Интерфейс `ForwardBuffer` и `ForwardEntry` в model

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/model/message.go` (или где определён `MessageKind`)

- [x] добавить тип `ForwardEntry` с полями `ChatID, MessageID, ForwardFromID, ForwardFromName, IsSelfAuthored, OriginalDate, ReceivedAt, Text`
- [x] добавить интерфейс `ForwardBuffer` с методами `Append`, `LastReceivedAt`, `LoadBatch`, `DeleteBatchTx`, `ListPendingChats`
- [x] добавить константу `MessageKindForwardBatch MessageKind = "forward_batch"`
- [x] добавить тип `ForwardBatchPayload` (chat_id, forwards, trigger text/id, delete fn, reply fn, counterpart)
- [x] добавить поле `ForwardBatch *ForwardBatchPayload` в `model.Message`
- [x] компилируемость проверить через `go build ./...`
- [x] `go vet ./...` должен пройти перед следующим таском

### Task 3: Реализация `SQLiteForwardBuffer`

**Files:**
- Create: `internal/storage/forward_buffer.go`
- Create: `internal/storage/forward_buffer_test.go`

- [x] реализовать `SQLiteForwardBuffer` с конструктором `NewSQLiteForwardBuffer(db *sql.DB) *SQLiteForwardBuffer`
- [x] `Append`: `INSERT OR IGNORE INTO forward_buffer (...) VALUES (...)`, time → unix-секунды
- [x] `LastReceivedAt`: `SELECT MAX(received_at) FROM forward_buffer WHERE chat_id = ?`, обработать `sql.ErrNoRows` через `(zero, false, nil)`
- [x] `LoadBatch`: `SELECT ... FROM forward_buffer WHERE chat_id = ? ORDER BY original_date ASC, id ASC`
- [x] `DeleteBatchTx`: `DELETE FROM forward_buffer WHERE chat_id = ?` через переданный `*sql.Tx`
- [x] `ListPendingChats`: `SELECT DISTINCT chat_id FROM forward_buffer GROUP BY chat_id HAVING MAX(received_at) < ?`
- [x] написать тест с in-memory SQLite + миграциями: Append идемпотентность (повторный Append с тем же chat_id+message_id не дублирует строки)
- [x] тест LastReceivedAt: пустой буфер → false; после Append → корректный max
- [x] тест LoadBatch: правильный порядок по `original_date`, корректный преобраз `is_self_authored` (0/1 ↔ bool)
- [x] тест DeleteBatchTx: rollback не удаляет; commit удаляет; чужой chat_id не затрагивается
- [x] тест ListPendingChats: только чаты с MAX(received_at) < threshold; пустой результат при отсутствии записей
- [x] `go test ./internal/storage/...` должен пройти

### Task 4: Helper `parseTriggerProjectHint`

**Files:**
- Create: `internal/pipeline/trigger_hint.go` (или в `internal/agent/` рядом с другими helpers — выбрать по итогам обзора структуры)
- Create: `internal/pipeline/trigger_hint_test.go`

- [x] реализовать `parseTriggerProjectHint(text string) (hint, rest string)` — регулярка `#([a-zA-Z0-9_\-]+)`, берётся первое совпадение
- [x] таблично-управляемый тест: `"#вася"`, `"обработай #вася"`, `"#alpha #beta"` (берётся `alpha`), `""` → пустые, `"привет"` → пустые, `"# "` → пустые (граница), `"#проект-1"` (поддержка кириллицы и дефиса)
- [x] обработать unicode (`\p{L}` через Go regex `\pL`): таблица должна включать кириллический slug
- [x] `go test ./internal/pipeline/...` должен пройти

### Task 5: Helper `FormatForwardBatch`

**Files:**
- Create: `internal/pipeline/forward_format.go`
- Create: `internal/pipeline/forward_format_test.go`

- [x] реализовать `FormatForwardBatch(forwards []model.ForwardEntry, counterpart string, loc *time.Location) string`
- [x] сортировка по `OriginalDate` ASC; стабильная вторичная сортировка по `ReceivedAt`
- [x] метки автора: `[я, YYYY-MM-DD HH:MM]` или `[<name>, …]`; пустой `Text` → суффикс ` [голосовое/вложение]`
- [x] заголовок: `Переписка между мной и {counterpart}:` (или `Переписка с неизвестным контактом:` если `counterpart == ""`)
- [x] таблично-управляемые тесты: один self-форвард, один counterpart-форвард, смешанная переписка, hidden privacy (counterpart=""), пустой text
- [x] `go test ./internal/pipeline/...` должен пройти

### Task 6: Промпт-шаблоны для forward classifier и extractor

**Files:**
- Create: `internal/ai/prompts/classifier_forward_system_ru.tmpl`
- Create: `internal/ai/prompts/classifier_forward_system_en.tmpl`
- Create: `internal/ai/prompts/extractor_forward_system_ru.tmpl`
- Create: `internal/ai/prompts/extractor_forward_system_en.tmpl`

- [x] `classifier_forward_system_ru.tmpl`: задать роль; чёткий критерий «promise = я (автор `[я, …]`) явно обещаю сделать что-то конкретное собеседнику»; явные не-обещания (вопросы, размышления вслух, обещания собеседника); формат ответа `promise` или `skip`
- [x] `classifier_forward_system_en.tmpl`: эквивалент на английском
- [x] `extractor_forward_system_ru.tmpl`: извлекать только обещания от меня; формат JSON со списком задач (summary, details включает `Обещано: <counterpart>` + ключевую цитату, deadline natural-language)
- [x] `extractor_forward_system_en.tmpl`: эквивалент на английском
- [x] существующий `prompts_test.go` использует glob по `*.tmpl` — добавление файлов автоматически попадёт в тест проверки парсинга
- [x] `go test ./internal/ai/...` должен пройти

### Task 7: `NewForwardClassifier` и `NewForwardExtractor`

**Files:**
- Modify: `internal/ai/classifier.go`
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/classifier_test.go`
- Modify: `internal/ai/extractor_test.go`

- [x] добавить `NewForwardClassifier(client OpenAIClient, lang string) *Classifier` — внутри `loadPrompt(promptsFS, lang, "classifier_forward_system")` + переиспользовать существующий `classifier_user`
- [x] добавить `NewForwardExtractor(client OpenAIClient, lang string) *Extractor` — `loadPrompt(promptsFS, lang, "extractor_forward_system")` + переиспользовать `extractor_user`
- [x] тесты с замокированным `OpenAIClient`: forward classifier возвращает `promise` для self-обещания, `skip` для пустого блока
- [x] тесты для forward extractor: один self-форвард с дедлайном → одна задача с `Обещано: <counterpart>` в `details`; counterpart-форвард → пустой массив
- [x] `go test ./internal/ai/...` должен пройти

### Task 8: Расширение конфига `[channels.telegram]`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/huskwoot/config.example.toml` (если существует)

- [x] добавить поля `ForwardBatchTimeout time.Duration` и `DeleteForwardsAfterExtract bool` в `TelegramChannelConfig`
- [x] парсить `forward_batch_timeout` из строки через `time.ParseDuration`; пустая строка → `0`
- [x] валидация: отрицательное значение → ошибка; `0` → допустимо (фича выключена)
- [x] таблично-управляемые тесты: `"10s"` → 10s, `""` → 0, `"0s"` → 0, `"-1s"` → ошибка, `"abc"` → ошибка
- [x] `delete_forwards_after_extract` — стандартный bool с дефолтом `false`
- [x] обновить пример конфига (если есть)
- [x] `go test ./internal/config/...` должен пройти

### Task 9: i18n-ключи для подтверждений и предупреждений

**Files:**
- Modify: `internal/i18n/locales/ru.json`
- Modify: `internal/i18n/locales/en.json`

- [x] добавить ключ `forward.extracted_tasks` (template с `{{.Count}}` и `{{.Project}}` и `{{.Tasks}}`): «извлёк N задач в проект «X»: …»
- [x] `forward.no_promises_found`: «обещаний от тебя в этой переписке не нашёл, сообщения оставил»
- [x] `forward.project_not_found`: «проект «{{.Hint}}» не найден, положил задачи в Inbox»
- [x] `forward.delete_failed_warning` (только для логов — i18n не нужен; этот ключ можно пропустить, в плане его нет)
- [x] добавить тест парсинга/локализации, если такой существует в проекте
- [x] `go test ./internal/i18n/...` должен пройти

### Task 10: Детекция форвардов и аккумулятор в `TelegramChannel`

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] в `handleDirectMessage`: проверять `m.ForwardFrom != nil || m.ForwardOrigin != nil`. Если форвард из user (не из chat/channel) — формировать `ForwardEntry` и вызывать `forwardBuffer.Append(...)`. **Не** писать в `SQLiteHistory`. **Не** возвращать `model.Message` в pipeline (return zero, false).
- [x] определять `IsSelfAuthored`: `m.ForwardFrom.ID == cfg.User.TelegramUserID` (или эквивалент через ForwardOrigin для новых версий API)
- [x] определять `ForwardFromID`/`ForwardFromName` (с учётом privacy hidden — id может быть пуст, имя берётся из `m.ForwardSenderName`)
- [x] игнорировать форварды из group/channel (`forward_origin.type != user`) — обрабатывать как обычный DM
- [x] если пришло обычное (не-форвард) DM-сообщение и `forwardBuffer.LastReceivedAt(chatID)` вернул запись — отправить в канал диспетчера сигнал `{ChatID, TriggerText: m.Text, TriggerID: strconv(m.MessageID), Source: "user-trigger"}` и **не** возвращать сообщение в pipeline (триггер не идёт в ChatService и не пишется в history). Сборку батча и Pipeline.Process делает диспетчер.
- [x] добавить поле `forwardBuffer model.ForwardBuffer` и `forwardSignal chan<- ForwardDispatchSignal` в `TelegramChannel`
- [x] обновить конструктор `NewTelegramChannel`
- [x] добавить метод `BuildDeleteFn(chatID int64) func(ctx, []string) error` для передачи в payload (использует `bot.Request(DeleteMessagesConfig{...})` с fallback на цикл `DeleteMessage`)
- [x] таблично-управляемые тесты: forward от меня → IsSelfAuthored=true, в буфер; forward от собеседника → IsSelfAuthored=false; forward из канала → обычный DM (НЕ в буфер); обычный DM при пустом буфере → нормальный flow; обычный DM при непустом буфере → сигнал диспетчеру + не записывается в history
- [x] `go test ./internal/channel/...` должен пройти

### Task 11: Диспетчер форвард-батчей

**Files:**
- Create: `internal/pipeline/forward_dispatcher.go`
- Create: `internal/pipeline/forward_dispatcher_test.go`

- [x] реализовать `ForwardDispatcher` со структурой:
  ```go
  type ForwardDispatcher struct {
      buffer   model.ForwardBuffer
      signal   chan ForwardDispatchSignal
      pipeline *Pipeline
      logger   *slog.Logger
  }
  ```
- [x] метод `Run(ctx context.Context)` — читает из `signal`, для каждого сигнала вызывает `processChat(ctx, sig)`. Один потребитель → race-free.
- [x] `processChat` загружает батч через `LoadBatch`, формирует `Message{Kind: MessageKindForwardBatch, ForwardBatch: payload}`, передаёт в `pipeline.Process`. Если буфер пуст — пропустить (sweeper мог отправить устаревший сигнал).
- [x] метод `Submit(sig ForwardDispatchSignal)` — non-blocking send с warning при переполнении канала
- [x] тип `ForwardDispatchSignal` со всеми нужными полями (chat_id, trigger text/id, reply fn, delete fn)
- [x] тест: два сигнала для одного chat_id обрабатываются последовательно (моки буфера и пайплайна с lock-проверкой)
- [x] тест: пустой буфер → pipeline.Process не вызывается
- [x] тест: ctx.Done → корректный выход из Run
- [x] `go test ./internal/pipeline/...` должен пройти

### Task 12: Sweeper для зависших батчей

**Files:**
- Create: `internal/pipeline/forward_sweeper.go`
- Create: `internal/pipeline/forward_sweeper_test.go`

- [x] реализовать `ForwardBatchSweeper`:
  ```go
  type ForwardBatchSweeper struct {
      buffer     model.ForwardBuffer
      dispatcher *ForwardDispatcher
      timeout    time.Duration
      tickEvery  time.Duration   // дефолт 5s
      now        func() time.Time // для тестов
      logger     *slog.Logger
  }
  ```
- [x] метод `Run(ctx)` — `time.Ticker(tickEvery)`, на каждом тике `ListPendingChats(now() - timeout)` → `dispatcher.Submit(...)` для каждого chat_id (без trigger text/id, delete fn ставится отдельно — берётся через TelegramChannel.BuildDeleteFn по chat_id)
- [x] но: sweeper не знает, как построить delete/reply fn для произвольного chat_id. Решение: sweeper отправляет сигнал в формате `{ChatID, Source: "sweeper"}`, диспетчер сам обогащает сигнал через коллбэк `enrichFn(chatID) (deleteFn, replyFn, error)` — этот коллбэк передаётся при создании диспетчера и реализуется в `TelegramChannel`. Документировать этот контракт.
- [x] тест: tick → dispatcher.Submit вызывается для каждого chat_id из ListPendingChats
- [x] тест: ctx.Done корректно завершает Run
- [x] тест: нулевой `timeout` → sweeper не запускается (early return в Run)
- [x] `go test ./internal/pipeline/...` должен пройти

### Task 13: Ветка `MessageKindForwardBatch` в Pipeline

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Create: `internal/pipeline/pipeline_forward_test.go`

- [x] добавить в `Pipeline.Process` switch-ветку `case model.MessageKindForwardBatch: return p.processForwardBatch(ctx, msg)`
- [x] метод `processForwardBatch`:
  1. Парсит `triggerText` через `parseTriggerProjectHint` → resolve project через `projectService.ResolveProjectRef`. Если hint есть, но не найден — лог warning, fallback Inbox + reply «проект `X` не найден, положил в Inbox» (с локализацией)
  2. Формирует `formatted := FormatForwardBatch(payload.Forwards, payload.Counterpart, loc)`
  3. `forwardClassifier.Classify(ctx, formatted)`. `skip` → tx.Begin; `DeleteBatchTx`; commit; reply «обещаний не нашёл»; return
  4. `forwardExtractor.Extract(ctx, formatted, payload.Counterpart)` → `[]model.Task`
  5. `tx.Begin()` → `CreateTaskTx` × N + `EventStore.Insert` × N + `PushQueue.Enqueue` × N + `ForwardBuffer.DeleteBatchTx`. `tx.Commit()`
  6. `Broker.Notify(events)` после commit
  7. Если `len(tasks) > 0 && payload.DeleteFn != nil` — собрать список message IDs (форварды + триггер_id если ≠ ""); вызвать `payload.DeleteFn(ctx, ids)`; ошибка → `slog.Warn`, не возвращать
  8. Reply со списком задач
- [x] добавить зависимости в `Pipeline`: `forwardClassifier`, `forwardExtractor`, `forwardBuffer`, `db *sql.DB`, `eventStore`, `pushQueue`, `broker`, `taskStore`, `projectService`, `localizer`, `deviceStore`
- [x] таблично-управляемые тесты:
  - happy path: 3 forward → classifier=promise → extractor=2 tasks → 2 задачи в БД, буфер очищен, deleteFn вызван с 3+1=4 IDs
  - skip path: classifier=skip → буфер очищен, 0 задач, deleteFn НЕ вызван
  - project hint path: triggerText="#вася" → задачи в проекте «вася»
  - project not found: triggerText="#нет" → fallback Inbox + reply содержит warning
  - delete failure: deleteFn возвращает ошибку → 0 ошибок наверх, warning в логе, задачи в БД
  - tx rollback на CreateTaskTx → буфер не очищен (важно для retry в следующий раз через sweeper)
- [x] `go test ./internal/pipeline/...` должен пройти

### Task 14: Wiring в `main.go`

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] создать `forwardBuffer := storage.NewSQLiteForwardBuffer(db)`
- [x] если `cfg.Channels.Telegram.ForwardBatchTimeout > 0`: создать каналы и компоненты:
  - `forwardSignal := make(chan ForwardDispatchSignal, 64)`
  - `forwardClassifier := ai.NewForwardClassifier(aiClient, cfg.User.Language)`
  - `forwardExtractor := ai.NewForwardExtractor(aiClient, cfg.User.Language)`
  - `dispatcher := pipeline.NewForwardDispatcher(forwardBuffer, forwardSignal, pipelineInstance, enrichFn, logger)`
  - `sweeper := pipeline.NewForwardBatchSweeper(forwardBuffer, dispatcher, timeout, 5*time.Second, time.Now, logger)`
  - запустить `go dispatcher.Run(ctx)` и `go sweeper.Run(ctx)`
- [x] передать `forwardBuffer` и `forwardSignal` в `TelegramChannel`
- [x] передать `forwardBuffer`, `forwardClassifier`, `forwardExtractor` в `Pipeline`
- [x] если `forward_batch_timeout = 0` — не создавать диспетчер/sweeper, форвард-логика в TelegramChannel должна быть no-op (форварды записываются как обычные DM)
- [x] graceful shutdown: ctx cancel → горутины завершаются
- [x] компиляция `go build -o bin/huskwoot ./cmd/huskwoot/` должна пройти
- [x] добавить smoke-тест wiring (если такой существует) — проверить, что `--config-dir` с включённой фичей не падает на старте
- [x] `go test ./cmd/...` (если есть) должен пройти

### Task 15: Verify acceptance criteria

- [x] verify все требования из Overview реализованы:
  - детекция форвардов в DM ✓
  - персистентный буфер ✓
  - обработка по таймауту ИЛИ триггеру ✓
  - извлечение МОИХ обещаний ✓
  - проект из hint или Inbox ✓
  - опциональное удаление при success ✓
  - гигиена истории ✓
  - переживание рестарта ✓
- [x] verify edge cases:
  - hidden privacy → классификатор возвращает skip
  - mixed counterparts → counterpart_display_name = самый частый
  - voice/photo → text="" с маркером, классификатор обычно skip
  - forward из канала/группы → не попадает в форвард-flow
  - skip → буфер очищен, форварды в Telegram остались
  - delete failure → задачи созданы, warning
  - process crash между commit и Telegram delete → задачи созданы, форварды в DM остались (приемлемо)
- [x] `go test ./...` — full test suite должен пройти
- [x] `go vet ./...` — без замечаний
- [x] `go build -o bin/huskwoot ./cmd/huskwoot/` — собирается
- [x] verify test coverage: новые файлы покрыты тестами; запустить `go test -cover ./internal/storage/... ./internal/pipeline/... ./internal/ai/... ./internal/channel/...`

### Task 16: Update documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [x] **CLAUDE.md:** обновить раздел `## Pipeline architecture > MessageKind` — добавить `MessageKindForwardBatch`
- [x] **CLAUDE.md:** добавить новый подраздел `### Forward batch flow (Telegram 1:1 promises)` под Pipeline architecture с описанием schema/dispatcher/sweeper/transactional invariant
- [x] **CLAUDE.md:** обновить раздел `## SQLite stores` — добавить `SQLiteForwardBuffer` и таблицу `forward_buffer`
- [x] **CLAUDE.md:** обновить раздел `## Configuration > [channels.telegram]` — добавить `forward_batch_timeout` и `delete_forwards_after_extract`
- [x] **CLAUDE.md:** добавить упоминание гигиены истории (форварды и триггер не пишутся в SQLiteHistory)
- [x] **README.md:** добавить пункт в список «что умеет» / возможностей бота: «извлекает обещания, которые ты дал в 1:1 переписке Telegram — пересылаешь сообщения в DM с ботом, бот сам разбирает их и заводит задачи»
- [x] **README.md:** добавить отдельный раздел `## Извлечение обещаний из форварднутых сообщений` (или аналогичное название в стилистике README) со следующим содержанием:
  - **Зачем:** проблема потерянных обещаний в 1:1 чатах, где нельзя добавить бота
  - **Как пользоваться:** пересылаешь N сообщений из 1:1 чата в DM с ботом → ждёшь паузу либо пишешь любое сообщение-триггер → бот извлекает твои обещания и создаёт задачи
  - **Триггер-фраза:** любое не-форвард сообщение в DM, отправленное после форвардов, запускает обработку. Если в тексте триггера указать `#<slug-или-alias-проекта>` (например, `обработай #вася` или просто `#вася`) — задачи попадут в указанный проект; иначе — в Inbox
  - **Что распознаётся:** только обещания, данные **тобой** (не собеседником). Распознавание основано на метаданных Telegram-форварда (`forward_from.id`)
  - **Удаление форвардов:** при `delete_forwards_after_extract = true` бот удаляет форварды и сообщение-триггер из DM после успешного извлечения (чтобы не зашумлять историю); подтверждение бота остаётся
  - **Конфигурация:**
    ```toml
    [channels.telegram]
    forward_batch_timeout         = "10s"   # пауза тишины для авто-обработки; 0/empty — фича выключена
    delete_forwards_after_extract = false   # удалять форварды + триггер из DM при success
    ```
  - **Ограничения:** форварды из групп и каналов не обрабатываются (только 1:1 переписки); голосовые/медиа без подписи распознаются как пустые сообщения и обычно классифицируются как skip; при включённой privacy у собеседника определение «кто сказал» может быть неточным
- [x] переместить план в `docs/plans/completed/`: `mkdir -p docs/plans/completed && mv docs/plans/2026-04-28-telegram-forward-promises.md docs/plans/completed/`

## Post-Completion

*Пункты, требующие ручного действия — без чекбоксов.*

**Manual verification:**

- Проверить на живом Telegram-аккаунте: переслать 3-4 сообщения из 1:1 чата с другом, написать триггер `обработай`. Убедиться, что задачи появились в Inbox, форварды и триггер исчезли (при включённом флаге), бот ответил квитанцией.
- Проверить таймаут: переслать сообщения и подождать 10 с — обработка должна сработать без триггера.
- Проверить рестарт: переслать сообщения, остановить сервер до таймаута, запустить снова — sweeper должен подхватить батч на первом тике.
- Проверить project hint: создать проект `личное`, переслать сообщения, написать `#личное` — задачи должны попасть в этот проект.
- Проверить privacy hidden: переслать сообщение от пользователя со скрытыми пересылками — должно вернуться «обещаний не нашёл» либо корректно классифицироваться как skip.
- Проверить корректность LLM-извлечения с реальной моделью (не моками) — sanity-check на 5-10 реальных переписках.

**External system updates:**

- Не применимо — фича целиком внутренняя для huskwoot, без внешних консьюмеров API.
