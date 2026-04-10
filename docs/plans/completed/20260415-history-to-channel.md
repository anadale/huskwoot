# Перенос управления историей из Pipeline в TelegramChannel

## Overview

Перенести хранение и предоставление истории сообщений из `Pipeline` в `TelegramChannel`.
Канал сам знает, нужна ли ему история, и добавляет в сообщение замыкание `HistoryFn`,
которое возвращает историю именно для данного чата. Pipeline перестаёт знать про историю.

Это упрощает Pipeline (убирает зависимость `model.History` и два поля конфига),
даёт каждому каналу независимую конфигурацию `SilenceGap`/`FallbackLimit`, и облегчает
будущую замену in-memory истории на БД — нужно поменять реализацию `model.History` в одном месте.

## Context (from discovery)

- **Файлы, затронутые изменением:**
  - `internal/model/types.go` — добавить поле `HistoryFn` в `Message`
  - `internal/channel/telegram.go` — добавить `HistoryConfig`, зависимость `model.History`, логику в `Watch`
  - `internal/channel/telegram_test.go` — тесты для новой логики Watch
  - `internal/pipeline/pipeline.go` — убрать `history`, упростить `processPromise`
  - `internal/pipeline/pipeline_test.go` — убрать `mockHistory`, обновить тест-кейсы
  - `cmd/huskwoot/main.go` — передать `hist` в `buildTelegramComponents`, убрать из `pipeline.New`
- **Паттерн:** аналогично `ReactFn` и `ReplyFn` — канал замыкает поведение в callback-поле Message
- **Существующие зависимости:**
  - `pipeline.New` принимает `history model.History` и `Config{SilenceGap, HistoryFallbackLimit}` — оба уходят
  - `history.Add` и `history.RecentActivity` вызываются в `pipeline.go:100-103` и `pipeline.go:151-158`
  - `buildTelegramComponents` в `main.go` не получает `hist` сейчас — это меняется

## Development Approach

- **Подход к тестированию:** TDD — тесты пишутся до реализации
- Каждая задача полностью завершается перед переходом к следующей
- **CRITICAL: каждая задача должна включать новые/обновлённые тесты**
- **CRITICAL: все тесты должны проходить перед началом следующей задачи**
- Команды: `go test ./...`, `go vet ./...`

## Testing Strategy

- **Unit tests:** table-driven тесты, ручные моки без фреймворков
- Покрытие: успешные случаи + ошибки

## Progress Tracking

- Завершённые пункты помечаются `[x]` сразу
- Новые задачи добавляются с префиксом ➕
- Блокеры помечаются ⚠️

## Solution Overview

1. `model.Message` получает поле `HistoryFn func(ctx context.Context) ([]Message, error)`
2. `TelegramChannel` принимает `model.History` (может быть nil) и `HistoryConfig{SilenceGap, FallbackLimit}` с дефолтами 5 мин / 20
3. В `Watch`: для каждого группового сообщения — сначала `history.Add`, затем `msg.HistoryFn = замыкание(source, cfg)`
4. `Pipeline` вызывает `msg.HistoryFn(ctx)` если не nil — никакой логики про Kind и историю внутри не остаётся

## Technical Details

```go
// model/types.go
type Message struct {
    // ...существующие поля...
    HistoryFn func(ctx context.Context) ([]Message, error)
}

// channel/telegram.go
type HistoryConfig struct {
    SilenceGap    time.Duration // default: 5 * time.Minute
    FallbackLimit int           // default: 20
}

// newTelegramChannel — добавить параметры:
//   history model.History (nil = без истории)
//   historyCfg HistoryConfig

// Watch — после convertUpdate, перед handler():
if msg.Kind == model.MessageKindGroup && w.history != nil {
    _ = w.history.Add(ctx, msg)
    source := msg.Source.ID
    msg.HistoryFn = func(ctx context.Context) ([]model.Message, error) {
        return w.history.RecentActivity(ctx, source, w.historyCfg.SilenceGap, w.historyCfg.FallbackLimit)
    }
}

// pipeline.go — processPromise:
var history []model.Message
if msg.HistoryFn != nil {
    history, _ = msg.HistoryFn(ctx) // ошибка логируется
}
```

## Implementation Steps

### Task 1: Добавить HistoryFn в model.Message

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/types_test.go`

- [x] Добавить поле `HistoryFn func(ctx context.Context) ([]Message, error)` в структуру `Message` с комментарием
- [x] Написать тест: `Message` с установленным `HistoryFn` возвращает ожидаемые сообщения при вызове
- [x] Написать тест: `Message` с `HistoryFn == nil` — проверка что поле zero-value
- [x] Запустить `go test ./internal/model/...` — должно пройти

### Task 2: Добавить HistoryConfig и зависимость History в TelegramChannel

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] Объявить структуру `HistoryConfig` с полями `SilenceGap time.Duration` и `FallbackLimit int`
- [x] Добавить поля `history model.History` и `historyCfg HistoryConfig` в структуру `TelegramChannel`
- [x] Обновить `newTelegramChannel`: добавить параметры `history model.History, historyCfg HistoryConfig`; применить дефолты (SilenceGap=5min, FallbackLimit=20) если значения нулевые
- [x] Обновить публичный `NewTelegramChannel` аналогично
- [x] Написать тест: конструктор применяет дефолты при нулевых значениях `HistoryConfig`
- [x] Написать тест: конструктор сохраняет явно заданные значения `HistoryConfig`
- [x] Запустить `go test ./internal/channel/...` — должно пройти

### Task 3: Добавить логику истории в Watch

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] В `Watch`, после успешного `convertUpdate` и до вызова `handler`:
  - если `msg.Kind == MessageKindGroup` и `w.history != nil` — вызвать `w.history.Add(ctx, msg)` (ошибка логируется через `slog.WarnContext`)
  - если условие выполнено — установить `msg.HistoryFn` как замыкание на `msg.Source.ID` и `w.historyCfg`
- [x] Написать table-driven тест: группового сообщение → `history.Add` вызван, `msg.HistoryFn != nil`
- [x] Написать тест: `HistoryFn` при вызове делегирует в `history.RecentActivity` с правильными параметрами
- [x] Написать тест: DM-сообщение → `history.Add` не вызван, `msg.HistoryFn == nil`
- [x] Написать тест: `history == nil` → `HistoryFn == nil` для любого типа сообщения
- [x] Написать тест: ошибка `history.Add` логируется, обработка продолжается (handler вызван)
- [x] Запустить `go test ./internal/channel/...` — должно пройти

### Task 4: Упростить Pipeline — убрать зависимость History

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] Убрать поле `history model.History` из структуры `Pipeline`
- [x] Убрать параметр `history model.History` из `pipeline.New`
- [x] Убрать поля `SilenceGap` и `HistoryFallbackLimit` из `pipeline.Config`
- [x] Убрать блок `history.Add` из `Process` (строки 100-103)
- [x] В `processPromise`: заменить вызов `p.history.RecentActivity(...)` на `msg.HistoryFn(ctx)` (с проверкой != nil и логированием ошибки)
- [x] Убрать `mockHistory` из `pipeline_test.go`
- [x] Обновить все тест-кейсы, которые передавали `mockHistory` в конструктор
- [x] Добавить тест: при `msg.HistoryFn != nil` — `HistoryFn` вызван, результат передан в `extractor.Extract`
- [x] Добавить тест: при `msg.HistoryFn == nil` — `extractor.Extract` вызван с пустой историей
- [x] Запустить `go test ./internal/pipeline/...` — должно пройти

### Task 5: Обновить wiring в main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] Передать `hist` в `buildTelegramComponents` (добавить параметр `history model.History`)
- [x] Внутри `buildTelegramComponents` передавать `hist` в каждый `NewTelegramChannel` с `channel.HistoryConfig{}`
- [x] Убрать `hist` из `pipeline.New`
- [x] Убрать `SilenceGap` и `HistoryFallbackLimit` из `pipeline.Config`
- [x] Запустить `go build ./...` — должно компилироваться без ошибок
- [x] Запустить `go vet ./...` — без предупреждений

### Task 6: Финальная проверка

- [x] Запустить `go test ./...` — все тесты проходят
- [x] Запустить `go vet ./...` — чисто
- [x] Убедиться, что `model.History` интерфейс не изменился (обратная совместимость для будущей замены на БД)
- [x] Переместить план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Запустить приложение в dev-режиме, отправить несколько сообщений в группу — убедиться что история накапливается и контекст передаётся в AI-экстрактор
- Проверить что DM и IMAP работают без изменений (HistoryFn == nil)
