# Channel + Classifier + Commands

## Overview

Рефакторинг архитектуры обработки сообщений в Huskwoot:
- **Watcher -> Channel** — переименование и расширение до двунаправленного канала (ReactFn/ReplyFn callbacks в Message)
- **Route -> map[MessageKind]** — маршруты убираются, pipeline выбирает компоненты по `msg.Kind`
- **Detector -> Classifier** — бинарный детектор заменяется трёхклассовым классификатором (Skip | Promise | Command)
- **Команды** — новая ветка обработки: CommandExtractor + CommandHandler
- **MetaStore** — key-value хранилище метаданных каналов (маппинг chatID -> projectName)
- **Обогащение Origin** — pipeline подставляет название проекта из MetaStore в `task.Origin`

Проблема: текущая архитектура не поддерживает конфигурационные команды от пользователя (например, "это группа проекта X").
Новая архитектура делает бота интерактивным и расширяемым для будущих типов команд.

## Context (from discovery)

Затронутые файлы и компоненты:
- `internal/model/types.go` — Message, Source, Task, Origin (добавление MessageKind, Classification, Command, callback-полей)
- `internal/model/interfaces.go` — Watcher->Channel, Detector->Classifier, новые: CommandExtractor, CommandHandler, MetaStore
- `internal/pipeline/pipeline.go` — Route удаляется, map[Kind] для компонентов, ветка Command
- `internal/watcher/` -> `internal/channel/` — переименование пакета, TelegramWatcher->TelegramChannel
- `internal/ai/detector.go` -> `internal/ai/classifier.go` — новый Classifier
- `internal/ai/extractor.go` — добавление CommandExtractor
- `internal/state/` — FileMetaStore
- `internal/sink/telegram_reaction.go` — упрощение через ReactFn
- `internal/handler/` — новый пакет для CommandHandler
- `cmd/huskwoot/main.go` — переработка инициализации
- `cmd/huskwoot/prompts.go` — новые prompt overrides для классификаторов

Обнаруженные паттерны:
- Моки в тестах — ручные, без фреймворков
- Промпты — Go text/template, загружаются из файлов с override
- Параллельный dispatch — sync.WaitGroup для sinks/notifiers
- Конструкторы — `New*(...) (*Type, error)` или `*Type`

## Development Approach

- **testing approach**: TDD — тесты пишутся перед реализацией
- Каждая задача завершается полностью перед переходом к следующей
- **CRITICAL: каждая задача ДОЛЖНА включать новые/обновлённые тесты**
- **CRITICAL: все тесты должны проходить перед началом следующей задачи**
- **CRITICAL: обновлять этот план при изменении скоупа во время реализации**
- Команда тестов: `go test ./...`
- Линтер: `go vet ./...`

## Testing Strategy

- **unit tests**: обязательны для каждой задачи (table-driven, httptest для AI-клиентов)
- Моки пишутся вручную в файле теста
- Тесты покрывают и success, и error сценарии

## Progress Tracking

- Выполненные пункты отмечать `[x]` сразу
- Новые обнаруженные задачи — ➕
- Блокеры — ⚠️
- Обновлять план при отклонении от скоупа

## Solution Overview

### Новый поток обработки сообщений

```
Channel.Watch() -> Message{Kind, ReactFn, ReplyFn}
    -> Pipeline.Process(msg)
        -> select Classifier by msg.Kind
        -> Classifier.Classify(msg) -> Classification
            Skip    -> (add to history if Group, done)
            Promise -> select Extractor by msg.Kind
                    -> Extractor.Extract(msg, history) -> []Task
                    -> enrich Origin (MetaStore project lookup)
                    -> dispatch to sinks/notifiers
            Command -> CommandExtractor.Extract(msg) -> Command
                    -> dispatch to commandHandlers
```

### Ключевые решения

1. **MessageKind** (DM/Batch/Group) — проставляется Channel при конвертации
2. **NeedsHistory** — выводится из Kind: `Kind == Group`
3. **Classifier** — один интерфейс, две реализации: simpleClassifier (DM/Batch: Promise|Skip), groupClassifier (Group: Promise|Command|Skip)
4. **Промпты** — отдельные для каждого Kind, загружаемые из файлов с override
5. **ReactFn/ReplyFn** — callback-поля в Message, замыкаемые на бота в TelegramChannel, nil для IMAP
6. **MetaStore** — новый key-value интерфейс, FileMetaStore реализация
7. **Pipeline обогащает Origin** — lookup проекта по chatID, fallback на Source.Name
8. **Конфиг-команды** — только от владельца, AI-детекция через groupClassifier
9. **Бот подтверждает команды** — command handler вызывает msg.ReplyFn

## Technical Details

### Новые типы в model/

```go
type MessageKind string
const (
    MessageKindDM    MessageKind = "dm"
    MessageKindBatch MessageKind = "batch"
    MessageKindGroup MessageKind = "group"
)

type Classification int
const (
    ClassSkip    Classification = iota
    ClassPromise
    ClassCommand
)

type Command struct {
    Type          string
    Payload       map[string]string
    Source        Source
    OriginMessage Message
}
```

### Расширение Message

```go
type Message struct {
    // ... существующие поля ...
    Kind    MessageKind
    ReactFn func(ctx context.Context, emoji string) error
    ReplyFn func(ctx context.Context, text string) error
}
```

### Новые интерфейсы

```go
type Classifier interface {
    Classify(ctx context.Context, msg Message) (Classification, error)
}

type CommandExtractor interface {
    Extract(ctx context.Context, msg Message) (Command, error)
}

type CommandHandler interface {
    Handle(ctx context.Context, cmd Command) error
    Name() string
}

type MetaStore interface {
    Get(ctx context.Context, key string) (string, error)
    Set(ctx context.Context, key string, value string) error
}
```

### Classifier промпты

**simpleClassifier** (DM/Batch) — двухклассовый, нужно доработать текущий промпт:
```
Ответь одним словом: promise или skip
```

**groupClassifier** (Group) — трёхклассовый, нужно доработать текущий промпт:
```
Ответь одним словом: promise, command или skip

command — пользователь даёт боту конфигурационную команду
(например: «это группа проекта X», «назови эту группу Y»)
```

### Pipeline.Process — новая логика

```go
func (p *Pipeline) Process(ctx context.Context, msg model.Message) error {
    // 1. История: добавить если Group
    if msg.Kind == model.MessageKindGroup && p.history != nil {
        p.history.Add(ctx, msg)
    }

    // 2. Проверка владельца (кроме Batch/IMAP)
    if msg.Kind != model.MessageKindBatch && !p.isOwner(msg.Author) {
        return nil
    }

    // 3. Классификация
    classifier := p.classifiers[msg.Kind]
    class, err := classifier.Classify(ctx, msg)

    switch class {
    case model.ClassSkip:
        return nil
    case model.ClassPromise:
        // существующая логика: extractor -> sinks/notifiers
        // + обогащение Origin из MetaStore
    case model.ClassCommand:
        cmd, err := p.commandExtractor.Extract(ctx, msg)
        // dispatch to commandHandlers
    }
}
```

## Implementation Steps

### Task 1: Новые типы и интерфейсы в model/

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/interfaces.go`
- Modify: `internal/model/types_test.go`

- [x] Добавить `MessageKind` (DM/Batch/Group) и константы в `types.go`
- [x] Добавить `Classification` (Skip/Promise/Command) и константы в `types.go`
- [x] Добавить struct `Command` в `types.go`
- [x] Добавить поля `Kind MessageKind`, `ReactFn`, `ReplyFn` в struct `Message`
- [x] Добавить интерфейс `Classifier` в `interfaces.go`
- [x] Добавить интерфейс `CommandExtractor` в `interfaces.go`
- [x] Добавить интерфейс `CommandHandler` в `interfaces.go`
- [x] Добавить интерфейс `MetaStore` в `interfaces.go`
- [x] Написать тесты для `Classification.String()` и `MessageKind` валидации
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 2: FileMetaStore

**Files:**
- Create: `internal/state/meta.go`
- Create: `internal/state/meta_test.go`

- [x] Написать тесты для FileMetaStore: Get (существующий ключ, несуществующий ключ), Set (создание, перезапись), конкурентный доступ
- [x] Реализовать `FileMetaStore` с атомарной записью (tmp + rename) аналогично `FileStateStore`
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 3: Classifier — AI-компонент

**Files:**
- Create: `internal/ai/classifier.go`
- Create: `internal/ai/classifier_test.go`

- [x] Написать тесты: simpleClassifier возвращает Promise/Skip, никогда Command; groupClassifier возвращает все три; ошибки парсинга; таймаут
- [x] Определить `ClassifierConfig` (UserName, Aliases, SystemTemplate, UserTemplate) аналогично `DetectorConfig`
- [x] Реализовать `SimpleClassifier` (для DM/Batch) — промпт возвращает `promise` или `skip`
- [x] Реализовать `GroupClassifier` (для Group) — промпт возвращает `promise`, `command` или `skip`
- [x] Определить дефолтные промпты: `DefaultSimpleClassifierSystemTemplate`, `DMSimpleClassifierSystemTemplate`, `DefaultGroupClassifierSystemTemplate`
- [x] Реализовать `parseClassification(response string) (Classification, error)`
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 4: CommandExtractor — AI-компонент

**Files:**
- Create: `internal/ai/command_extractor.go`
- Create: `internal/ai/command_extractor_test.go`

- [x] Написать тесты: извлечение set_project_name, неизвестная команда, невалидный JSON, таймаут
- [x] Определить `CommandExtractorConfig` (SystemTemplate, UserTemplate)
- [x] Реализовать `AICommandExtractor` — промпт возвращает JSON `{"type": "...", "payload": {...}}`
- [x] Определить дефолтные промпты для экстракции команд
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 5: Переименование watcher -> channel

**Files:**
- Rename: `internal/watcher/` -> `internal/channel/`
- Modify: `internal/model/interfaces.go` (Watcher -> Channel)
- Modify: `cmd/huskwoot/main.go` (импорты)
- Modify: все файлы с import `internal/watcher`

- [x] Переименовать интерфейс `Watcher` -> `Channel` в `interfaces.go`, добавить метод `ID() string`
- [x] Переименовать пакет `internal/watcher` -> `internal/channel`
- [x] Переименовать `TelegramWatcher` -> `TelegramChannel`, `TelegramWatcherConfig` -> `TelegramChannelConfig`
- [x] Переименовать `IMAPWatcher` -> `IMAPChannel` (и аналогичные типы/конфиги)
- [x] Обновить все импорты (`cmd/huskwoot/main.go`, тесты)
- [x] Обновить существующие тесты — убедиться, что переименование не ломает логику
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 6: Message enrichment в Channel

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`
- Modify: `internal/channel/imap.go`
- Modify: `internal/channel/imap_test.go`

- [x] Написать тесты: TelegramChannel проставляет Kind=Group для групповых, Kind=DM для DM; IMAPChannel проставляет Kind=Batch
- [x] Написать тесты: TelegramChannel заполняет ReactFn и ReplyFn (вызов callback проверяется через мок botAPI)
- [x] Написать тесты: IMAPChannel оставляет ReactFn/ReplyFn = nil
- [x] Реализовать проставление `Kind` в `convertMessage` / `convertDMMessage` для Telegram
- [x] Реализовать замыкание `ReactFn` и `ReplyFn` на бот в TelegramChannel
- [x] Расширить интерфейс `botAPI` методами для отправки сообщений и реакций
- [x] Реализовать проставление `Kind = Batch` в IMAPChannel
- [x] Обновить существующие тесты Message-литералов (добавить Kind)
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 7: Рефакторинг Pipeline

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] Написать тесты для новой логики Pipeline:
  - ClassSkip -> сообщение пропускается (история добавляется для Group)
  - ClassPromise -> существующая цепочка Extractor -> sinks/notifiers
  - ClassCommand -> CommandExtractor -> commandHandlers
  - Выбор Classifier по msg.Kind
  - Выбор Extractor по msg.Kind
  - Обогащение Origin из MetaStore (project name найден / не найден / fallback)
  - Ошибки Classifier / CommandExtractor
- [x] Удалить struct `Route` и метод `matchRoute`
- [x] Заменить `routes []Route` на `classifiers map[model.MessageKind]model.Classifier`
- [x] Добавить `extractors map[model.MessageKind]model.Extractor`
- [x] Добавить `commandExtractor model.CommandExtractor`
- [x] Добавить `commandHandlers []model.CommandHandler`
- [x] Добавить `metaStore model.MetaStore`
- [x] Реализовать новый `Process`: classify -> switch -> extract/dispatch
- [x] Реализовать обогащение Origin: `metaStore.Get("project:" + chatID)`, fallback на `msg.Source.Name`
- [x] Определение NeedsHistory из `msg.Kind == MessageKindGroup`
- [x] Обновить конструктор `New(...)` с новыми параметрами
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 8: CommandHandler для set_project_name

**Files:**
- Create: `internal/handler/set_project.go`
- Create: `internal/handler/set_project_test.go`

- [x] Написать тесты: сохранение project name в MetaStore, вызов ReplyFn с подтверждением, ReplyFn = nil (не падает), ошибка MetaStore
- [x] Реализовать `SetProjectHandler` с зависимостями `MetaStore`
- [x] `Handle` сохраняет `"project:" + cmd.Source.ID` -> `cmd.Payload["name"]` в MetaStore
- [x] `Handle` вызывает `cmd.OriginMessage.ReplyFn` с подтверждением: `Запомнил: это группа проекта «{name}»`
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 9: Упрощение ReactionNotifier

**Files:**
- Modify: `internal/sink/telegram_reaction.go`
- Modify: `internal/sink/telegram_reaction_test.go`

- [x] Написать тесты для нового ReactionNotifier: вызов ReactFn на задачах с ReactFn != nil, пропуск задач с ReactFn == nil, дедупликация
- [x] Переименовать `TelegramReactionNotifier` -> `ReactionNotifier`
- [x] Убрать зависимость на `*tgbotapi.BotAPI` и `watcherID`
- [x] Реализовать через `task.OriginMessage.ReactFn` (если != nil)
- [x] Обновить конструктор
- [x] Запустить `go test ./...` — все тесты должны пройти

### Task 10: Обновление main.go и prompt overrides

**Files:**
- Modify: `cmd/huskwoot/main.go`
- Modify: `cmd/huskwoot/prompts.go`

- [x] Добавить prompt overrides для новых компонентов: `group-classifier-system`, `simple-classifier-system`, `dm-classifier-system`, `command-extractor-system`
- [x] Заменить `buildAIComponents` — создавать Classifier (simple для DM/Batch, group для Group) + CommandExtractor вместо Detector
- [x] Заменить `buildRoutes` на создание `map[MessageKind]Classifier` и `map[MessageKind]Extractor`
- [x] Инициализировать `FileMetaStore`
- [x] Инициализировать `SetProjectHandler` и передать в Pipeline
- [x] Заменить `NewTelegramReactionNotifier(bot, watcherID)` на `NewReactionNotifier()`
- [x] Обновить тип `model.Watcher` -> `model.Channel` в горутинах watcher-ов
- [x] Запустить `go test ./...` — все тесты должны пройти
- [x] Запустить `go vet ./...` — без предупреждений
- [x] Собрать `go build -o bin/huskwoot ./cmd/huskwoot` — успешно

### Task 11: Очистка и удаление старого кода

**Files:**
- Modify: `internal/model/interfaces.go`
- Delete: `internal/ai/detector.go`
- Delete: `internal/ai/detector_test.go`
- Modify: `internal/ai/extractor.go` (удаление DM-шаблонов если перенесены)

- [x] Удалить интерфейс `Detector` из `interfaces.go`
- [x] Удалить `internal/ai/detector.go` и `internal/ai/detector_test.go`
- [x] Удалить неиспользуемые промпт-константы (`DefaultDetectorSystemTemplate`, `DMDetectorSystemTemplate` и т.д.)
- [x] Проверить, что нигде не осталось ссылок на старые типы: `Detector`, `Route`, `Watcher`
- [x] Запустить `go test ./...` — все тесты должны пройти
- [x] Запустить `go vet ./...` — без предупреждений

### Task 12: Верификация

- [x] Проверить, что все требования из Overview реализованы
- [x] Проверить граничные случаи: IMAP-сообщение без ReactFn, неизвестный тип команды, MetaStore недоступен
- [x] Запустить `go test ./...`
- [x] Запустить `go vet ./...`
- [x] Собрать `go build -o bin/huskwoot ./cmd/huskwoot`

### Task 13: Обновление документации

- [x] Обновить `CLAUDE.md`: Watcher->Channel, Detector->Classifier, Route удалён, новые пакеты (handler), MetaStore
- [x] Переместить план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Протестировать с реальным Telegram-ботом: отправить "это группа проекта Тест" в групповой чат
- Убедиться, что бот ответил подтверждением
- Отправить обещание в ту же группу — проверить, что Origin содержит название проекта
- Проверить, что IMAP-обработка не сломалась

**Настройка промптов:**
- При необходимости скорректировать промпты классификаторов на реальных данных
- Проверить качество определения команд vs обещаний в групповом чате
