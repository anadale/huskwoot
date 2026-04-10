# Маршрутизация pipeline для поддержки DM-команд

## Overview

Реализовать механизм маршрутизации в pipeline, позволяющий обрабатывать сообщения из разных источников (групповые чаты, IMAP, DM) с использованием разных детекторов и экстракторов. Это заменяет жёстко зашитые `if`-проверки по `Source.Kind` на предикатную цепочку маршрутов (`Route`), каждый из которых определяет набор шагов обработки: нужна ли история, какой детектор и экстрактор использовать.

Конечная цель: дать пользователю возможность создавать задачи через DM-сообщения боту, выраженные натуральным языком — например, «Сегодня вечером опубликую новую версию бекенда приложения Помощь». Такие команды-обещания обрабатываются DM-специфичными промптами детектора и экстрактора, отличающимися от промптов для групповых чатов.

Решает проблему: текущий `pipeline.Process` содержит ad-hoc логику ветвления по типу источника. Добавление DM-режима с другими промптами усугубило бы это. Маршрутизация выносит конфигурацию обработки в декларативные структуры.

Интеграция: `Pipeline` получает `[]Route` при создании. `TelegramWatcher` учится распознавать DM-сообщения. `PromiseDetector` и `TaskExtractor` получают параметризуемые шаблоны промптов.

## Context (from discovery)

- **Ключевые файлы:**
  - `internal/pipeline/pipeline.go` — текущая логика Process с if-ветвлением
  - `internal/pipeline/pipeline_test.go` — 15 тестов, используют `New(detector, extractor, ...)`
  - `internal/ai/detector.go` — `PromiseDetector` с хардкод-шаблонами `detectorSystemTmpl`/`detectorUserTmpl`
  - `internal/ai/extractor.go` — `TaskExtractor` с хардкод-шаблонами `extractorSystemTmpl`/`extractorUserTmpl`
  - `internal/watcher/telegram.go` — `convertMessage` фильтрует по groups, не различает DM
  - `cmd/huskwoot/main.go` — создаёт один detector и один extractor, собирает pipeline
  - `internal/model/interfaces.go` — `Detector`, `Extractor` уже подходят без изменений

- **Паттерны:**
  - Моки пишутся вручную в тестах
  - Конструкторы `New*` возвращают `(*T, error)` при возможных ошибках инициализации
  - Table-driven тесты
  - TDD: тесты пишутся перед реализацией

- **Зависимости:** `model.Detector` и `model.Extractor` — интерфейсы, менять не нужно. Pipeline и тесты зависят от сигнатуры `pipeline.New`.

## Development Approach

- **Подход к тестированию**: TDD — тесты перед реализацией
- Каждая задача завершается полностью перед переходом к следующей
- **Каждая задача включает написание/обновление тестов**
- Все тесты должны проходить перед переходом к следующей задаче
- Обратная совместимость: дефолтные шаблоны сохраняются

## Testing Strategy

- **Unit-тесты**: для каждой задачи — новые функции и изменённое поведение
- Команда: `go test ./...`
- Линтер: `go vet ./...`

## Progress Tracking

- `[x]` — завершённые пункты
- ➕ — вновь обнаруженные задачи
- ⚠️ — блокеры

## Solution Overview

1. **`pipeline.Route`** — структура с предикатом `Match func(Message) bool`, флагом `UseHistory`, экземплярами `Detector` и `Extractor`
2. **`pipeline.Pipeline`** принимает `[]Route` вместо одиночных detector/extractor. `Process` проходит по маршрутам, первый совпавший предикат определяет поведение
3. **`ai.DetectorConfig`** и **`ai.ExtractorConfig`** получают опциональные поля `SystemTemplate`/`UserTemplate`. Пустое значение = дефолтный шаблон
4. **DM-шаблоны** — экспортированные константы в пакете `ai` для промптов команд-обещаний
5. **`TelegramWatcher.convertMessage`** — распознаёт DM по `m.Chat.Type == "private"` и `m.From.ID ∈ OwnerIDs`, выставляет `Source.ID = "dm"`
6. **`main.go`** — собирает маршруты DM → IMAP → Group с соответствующими экземплярами

## Technical Details

### Route

```go
type Route struct {
    Name       string
    Match      func(model.Message) bool
    UseHistory bool
    Detector   model.Detector
    Extractor  model.Extractor
}
```

### Pipeline.Process (упрощённо)

```go
func (p *Pipeline) Process(ctx context.Context, msg model.Message) error {
    route := p.matchRoute(msg)
    if route == nil {
        return nil
    }
    if route.UseHistory {
        p.history.Add(ctx, msg)
    }
    if !p.isOwner(msg.Author) && msg.Source.Kind != "imap" {
        return nil
    }
    isPromise, _ := route.Detector.IsPromise(ctx, msg)
    var history []model.Message
    if route.UseHistory {
        history, _ = p.history.RecentActivity(...)
    }
    tasks, _ := route.Extractor.Extract(ctx, msg, history)
    p.dispatch(ctx, tasks)
    return nil
}
```

### Порядок маршрутов

DM → IMAP → Group. DM проверяется первым, чтобы group-предикат (`Kind == "telegram"`) не перехватил DM-сообщения.

### TelegramWatcher: распознавание DM

`convertMessage` получает доступ к `OwnerIDs` (через `TelegramWatcherConfig`). Если `m.Chat.Type == "private"` и `m.From.ID` совпадает с одним из `OwnerIDs`, формируется `Source{Kind: "telegram", ID: "dm", Name: "DM"}`.

## Implementation Steps

### Task 1: Параметризация шаблонов в DetectorConfig

**Files:**
- Modify: `internal/ai/detector.go`
- Modify: `internal/ai/detector_test.go`

- [x] Написать тест: `NewPromiseDetector` с пустыми `SystemTemplate`/`UserTemplate` использует дефолтные шаблоны (существующее поведение)
- [x] Написать тест: `NewPromiseDetector` с кастомным `SystemTemplate` использует переданный шаблон
- [x] Добавить поля `SystemTemplate` и `UserTemplate` в `DetectorConfig`
- [x] Экспортировать дефолтные шаблоны: `DefaultDetectorSystemTemplate`, `DefaultDetectorUserTemplate`
- [x] В `NewPromiseDetector` использовать шаблон из конфига, если задан; иначе дефолтный
- [x] Убедиться, что все существующие тесты проходят: `go test ./internal/ai/...`

### Task 2: Параметризация шаблонов в ExtractorConfig

**Files:**
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/extractor_test.go`

- [x] Написать тест: `NewTaskExtractor` с пустыми шаблонами использует дефолтные
- [x] Написать тест: `NewTaskExtractor` с кастомным `SystemTemplate` использует переданный шаблон
- [x] Добавить поля `SystemTemplate` и `UserTemplate` в `ExtractorConfig`
- [x] Экспортировать дефолтные шаблоны: `DefaultExtractorSystemTemplate`, `DefaultExtractorUserTemplate`
- [x] В `NewTaskExtractor` использовать шаблон из конфига, если задан; иначе дефолтный
- [x] Убедиться, что все существующие тесты проходят: `go test ./internal/ai/...`

### Task 3: Структура Route и рефакторинг Pipeline

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] Написать тест: `Process` с одним маршрутом (group) — happy path аналог текущего `TestProcess_HappyPath`
- [x] Написать тест: `Process` с маршрутом `UseHistory: false` — `history.Add` и `history.RecentActivity` не вызываются
- [x] Написать тест: `matchRoute` возвращает nil если ни один предикат не совпал — сообщение пропускается
- [x] Написать тест: `matchRoute` с несколькими маршрутами — выбирается первый совпавший
- [x] Определить `Route` с полями `Name`, `Match`, `UseHistory`, `Detector`, `Extractor`
- [x] Изменить `Pipeline`: заменить `detector`/`extractor` на `routes []Route`
- [x] Добавить метод `matchRoute(msg) *Route`
- [x] Переписать `Process` с использованием `matchRoute` и `Route`
- [x] Изменить конструктор `New` на приём `[]Route` вместо отдельных detector/extractor
- [x] Адаптировать все существующие тесты к новой сигнатуре `New` (каждый тест создаёт `[]Route` с одним маршрутом)
- [x] Проверить, что все тесты проходят: `go test ./internal/pipeline/...`

### Task 4: DM-промпты для детектора и экстрактора

**Files:**
- Modify: `internal/ai/detector.go`
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/detector_test.go`
- Modify: `internal/ai/extractor_test.go`

- [x] Написать тест: DM-детектор с кастомным шаблоном корректно рендерит промпт для команды-обещания
- [x] Написать тест: DM-экстрактор с кастомным шаблоном корректно рендерит промпт для извлечения задач
- [x] Написать DM-промпт детектора: `DMDetectorSystemTemplate` — акцент на команды-обещания от первого лица ("сегодня вечером опубликую новую версию")
- [x] Написать DM-промпт экстрактора: `DMExtractorSystemTemplate` — извлечение задач из прямых команд пользователя
- [x] Проверить: `go test ./internal/ai/...`

### Task 5: Распознавание DM в TelegramWatcher

**Files:**
- Modify: `internal/watcher/telegram.go`
- Modify: `internal/watcher/telegram_test.go`

- [x] Написать тест: `convertMessage` для private-чата с OwnerID → `Source.ID == "dm"`, `Source.Name == "DM"`
- [x] Написать тест: `convertMessage` для private-чата с неизвестным From.ID → сообщение отфильтровано (false)
- [x] Написать тест: `convertMessage` для group-чата — поведение не изменилось
- [x] Добавить `OwnerIDs []string` в `TelegramWatcherConfig`
- [x] В `convertMessage`: если `m.Chat.Type == "private"` и `m.From.ID ∈ OwnerIDs` → `Source{Kind: "telegram", ID: "dm", Name: "DM"}`
- [x] В `convertMessage`: если `m.Chat.Type == "private"` и `m.From.ID ∉ OwnerIDs` → return false
- [x] Проверить: `go test ./internal/watcher/...`

### Task 6: Сборка маршрутов в main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] Создать DM-экземпляры: `dmDetector` и `dmExtractor` с DM-шаблонами
- [x] Собрать `[]pipeline.Route` в порядке: DM → IMAP → Group
- [x] Передать `OwnerIDs` (срез OwnerID из всех Telegram-конфигов) в `TelegramWatcherConfig`
- [x] Обновить вызов `pipeline.New` с передачей маршрутов
- [x] Проверить сборку: `go build -o bin/huskwoot ./cmd/huskwoot`
- [x] Проверить все тесты: `go test ./...`
- [x] Проверить линтер: `go vet ./...`

### Task 7: Верификация критериев приёмки

- [x] Проверить, что все требования из Overview реализованы
- [x] Проверить граничные случаи: сообщение не совпадающее ни с одним маршрутом
- [x] Запустить полный набор тестов: `go test ./...`
- [x] Запустить линтер: `go vet ./...`

### Task 8: [Final] Обновление документации

- [x] Обновить `CLAUDE.md` если обнаружены новые паттерны
- [x] Обновить спецификацию `docs/superpowers/specs/2026-04-13-dm-commands-spec.md` — отразить изменение подхода (Route вместо CommandRouter)
- [x] Переместить этот план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Запустить приложение с реальным Telegram-ботом и отправить DM-команду
- Убедиться, что групповые сообщения обрабатываются как раньше
- Проверить, что DM от постороннего пользователя отфильтровывается
