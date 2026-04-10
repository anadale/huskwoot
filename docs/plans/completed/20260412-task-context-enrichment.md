# Обогащение контекста задач

## Overview

Задачи, извлекаемые из сообщений, записываются в Obsidian без контекста происхождения.
Формулировка вроде "исправить ошибку" не позволяет понять, о какой ошибке речь.
Для meeting summaries из почты проблема аналогична.

Цель: каждая задача несёт достаточно контекста, чтобы пользователь понял её смысл
без обращения к оригинальному сообщению.

Три направления:
1. Экстрактор возвращает контекст и тему обещания (новые поля `context`, `topic`)
2. Метаданные происхождения (тема письма, имя аккаунта) передаются через `Origin`
3. Obsidian-файл организован по секциям, Telegram-уведомления содержат полную информацию

## Context (from discovery)

- Файлы/компоненты:
  - `internal/model/types.go` — Task, Message
  - `internal/model/interfaces.go` — Sink, Notifier
  - `internal/ai/extractor.go` — промпты, extractorModelResponse
  - `internal/pipeline/pipeline.go` — Process, dispatch
  - `internal/sink/obsidian.go` — запись в Obsidian
  - `internal/sink/telegram_notifier.go` — Telegram-уведомления
  - `internal/watcher/imap.go` — IMAP watcher
  - `internal/config/config.go` — IMAPConfig
  - `cmd/jeeves/main.go` — инициализация

- Паттерны проекта:
  - Интерфейсы в `internal/model/`, реализации в отдельных пакетах
  - Моки вручную, без фреймворков
  - Конструкторы `New*` возвращают `(*Type, error)` или `*Type`
  - Ошибки оборачиваются: `fmt.Errorf("операция: %w", err)`
  - Table-driven тесты

- Зависимости:
  - Изменение интерфейсов Sink/Notifier затрагивает pipeline, ObsidianSink, TelegramNotifier и main.go
  - IMAP watcher → config, main.go

## Development Approach

- **testing approach**: TDD (тесты перед реализацией)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change: `go test ./...`
- run линтер: `go vet ./...`
- maintain backward compatibility

## Testing Strategy

- **unit tests**: required for every task
- Table-driven тесты `[]struct{name, ...}` с `t.Run`
- Моки — вручную в файле теста
- `httptest` для HTTP-клиентов (AI, Telegram)

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with + prefix
- document issues/blockers with ! prefix
- update plan if implementation deviates from original scope

## Solution Overview

Добавляем struct `Origin` в `Task` для хранения метаданных происхождения
(`Subject`, `Account`, `Topic`, `Context`). Экстрактор возвращает `context` и `topic`
через расширенный JSON. Pipeline собирает `Origin` из данных сообщения и экстрактора,
обнуляет `Topic` для Telegram. Интерфейсы Sink и Notifier переходят на `[]Task`.
ObsidianSink парсит файл в секции и вставляет задачи в нужные места.
TelegramNotifier отправляет одно сообщение со списком задач.

## Technical Details

### Origin struct

```go
type Origin struct {
    Subject string // тема письма (IMAP), пустая для Telegram
    Account string // человекочитаемое имя аккаунта ("Рабочая почта")
    Topic   string // тематическая группа ("Сервис платежей")
    Context string // контекст обещания ("Иван сообщил о проблеме с OAuth")
}
```

### Формат Obsidian-файла

```markdown
## Команда Backend
- [ ] исправить ошибку авторизации
  - Иван сообщил о проблемах с OAuth-токенами
- [x] обновить конфиг деплоя
  - после миграции на новый кластер

## Конспект встречи 03.04.26 (Рабочая почта)

### Сервис платежей
- [ ] доработать механизм скидок 📅 2026-04-20
  - обсуждали на встрече с командой маркетинга

### Аутентификация
- [ ] проверить роли 📅 2026-04-10
  - срочный запрос от СБ
```

### Формат JSON-ответа экстрактора

```json
[{
  "summary": "исправить ошибку авторизации",
  "context": "Иван сообщил о проблемах с OAuth-токенами в мобильном приложении",
  "topic": "Аутентификация",
  "deadline": "2026-04-15",
  "confidence": 0.85
}]
```

### Формат Telegram-уведомления

```
✍️ Новые задачи записаны!

Источник: Команда Backend (telegram)

- Исправить ошибку авторизации
  Контекст: Иван сообщил о проблемах с OAuth

- Обновить конфиг деплоя 📅 15.04.2026
  Контекст: после миграции на новый кластер
```

### Логика ObsidianSink: вставка в секции

1. Прочитать файл → разбить на строки
2. Распарсить в структуру: `[]section{heading, subsections, lines}`
3. Для каждой задачи:
   - Определить заголовок секции `##` по `Source.Kind`:
     - `telegram` → `Source.Name`
     - `imap` → `Origin.Subject (Origin.Account)`
   - Найти секцию или создать в конце файла
   - Если `Origin.Topic` не пустой — найти/создать `### Topic` внутри секции
   - Если `Origin.Topic` пустой — вставить перед первой `###` (или в конец секции)
4. Собрать строки обратно → записать файл

### Поток данных в Pipeline

```
Message (с Subject) → Detector → Extractor (возвращает context, topic)
  → Pipeline заполняет Origin:
      Origin.Subject = msg.Subject
      Origin.Account = msg.Source.Name
      Origin.Context = из экстрактора
      Origin.Topic = из экстрактора (обнуляется для telegram)
  → dispatch(ctx, []Task) → Sinks + Notifiers параллельно
```

## Implementation Steps

### Task 1: Расширение модели — Origin, Message.Subject, интерфейсы

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/interfaces.go`

- [x] добавить struct `Origin` с полями `Subject`, `Account`, `Topic`, `Context`
- [x] добавить поле `Origin Origin` в `Task`
- [x] добавить поле `Subject string` в `Message`
- [x] изменить интерфейс `Sink.Save`: `Save(ctx, task Task)` → `Save(ctx, tasks []Task)`
- [x] изменить интерфейс `Notifier.Notify`: `Notify(ctx, task Task)` → `Notify(ctx, tasks []Task)`
- [x] запустить `go vet ./...` — ожидаемы ошибки компиляции в реализациях (исправятся в следующих задачах)

### Task 2: IMAP watcher — Label и Subject

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/watcher/imap.go`
- Modify: `internal/watcher/imap_test.go`

- [x] добавить поле `Label string` в `config.IMAPConfig` с TOML-тегом `label`
- [x] добавить поле `Label string` в `watcher.IMAPWatcherConfig`
- [x] в `convertIMAPMessage`: `Source.Name` = `cfg.Label` (если пустой — fallback на `cfg.Folder`)
- [x] в `convertIMAPMessage`: `Message.Subject` = `msg.Envelope.Subject`
- [x] в `convertIMAPMessage`: `Message.Text` = только тело письма (без конкатенации subject)
- [x] передать `Label` из `config.IMAPConfig` в `watcher.IMAPWatcherConfig` в `cmd/jeeves/main.go`
- [x] написать тесты для `convertIMAPMessage` с Label (есть/нет, fallback)
- [x] написать тесты для `convertIMAPMessage` с разделением Subject/Text
- [x] запустить тесты — должны пройти

### Task 3: Экстрактор — context и topic в промпте и ответе

**Files:**
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/extractor_test.go`

- [x] добавить поля `Context` и `Topic` в `extractorModelResponse`
- [x] обновить `extractorSystemTmpl`: добавить правила формирования `context` и `topic`
  - `context` — краткая справка, помогающая понять обещание без оригинала
  - `topic` — тематическая группа, заполняется всегда
- [x] обновить формат JSON в промпте: `{"summary", "context", "topic", "deadline", "confidence"}`
- [x] в `Extract`: копировать `Context` и `Topic` из ответа модели в `Task.Origin`
- [x] написать тесты: ответ модели с context и topic корректно парсится
- [x] написать тесты: ответ модели без context/topic (пустые строки) не ломает парсинг
- [x] написать тесты: confidence ниже порога по-прежнему фильтрует задачу
- [x] запустить тесты — должны пройти

### Task 4: Pipeline — заполнение Origin и батчевый dispatch

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] в `Process`: после `extractor.Extract` заполнить `Origin.Subject` = `msg.Subject`, `Origin.Account` = `msg.Source.Name` для каждой задачи
- [x] в `Process`: для `msg.Source.Kind == "telegram"` — обнулить `Origin.Topic`
- [x] изменить `dispatch`: принимает `[]Task` вместо одной задачи
- [x] в `dispatch`: вызывать `s.Save(ctx, tasks)` и `n.Notify(ctx, tasks)` с полным срезом
- [x] в `Process`: вызвать `dispatch(ctx, tasks)` один раз вместо цикла
- [x] обновить моки `mockSink` и `mockNotifier` на сигнатуры `[]Task`
- [x] написать тесты: Origin заполняется корректно для Telegram-источника
- [x] написать тесты: Origin заполняется корректно для IMAP-источника (Subject, Account)
- [x] написать тесты: Topic обнуляется для Telegram
- [x] написать тесты: dispatch вызывает Sink и Notifier с полным срезом задач
- [x] запустить тесты — должны пройти (pipeline: ok; cmd/jeeves: ожидаемые ошибки компиляции, исправятся в Task 5–7)

### Task 5: ObsidianSink — секционная вставка

**Files:**
- Modify: `internal/sink/obsidian.go`
- Modify: `internal/sink/obsidian_test.go`

- [x] изменить сигнатуру `Save` на `Save(ctx context.Context, tasks []model.Task) error`
- [x] реализовать парсинг markdown-файла в структуру секций (## / ### / строки)
- [x] реализовать определение заголовка секции по `Source.Kind`:
  - `telegram` → `task.Source.Name`
  - `imap` → `task.Origin.Subject (task.Origin.Account)`
- [x] реализовать поиск/создание секции `##` по заголовку
- [x] реализовать поиск/создание подсекции `### Topic` (если `Origin.Topic` не пустой)
- [x] реализовать вставку задачи без topic перед первой `###` (или в конец секции)
- [x] реализовать форматирование строки задачи: `- [ ] summary 📅 дата\n  - context`
- [x] при парсинге сохранять существующие строки, включая `- [x] ...`
- [x] записать файл обратно целиком
- [x] написать тесты: вставка в пустой файл создаёт секцию
- [x] написать тесты: вставка в существующую секцию добавляет задачу
- [x] написать тесты: задача с topic попадает в подсекцию ###
- [x] написать тесты: задача без topic попадает перед первой ###
- [x] написать тесты: существующие задачи `- [x]` сохраняются
- [x] написать тесты: несколько задач из разных источников — разные секции
- [x] написать тесты: формат с дедлайном и контекстом корректен
- [x] запустить тесты — должны пройти

### Task 6: TelegramNotifier — групповое уведомление

**Files:**
- Modify: `internal/sink/telegram_notifier.go`
- Modify: `internal/sink/telegram_notifier_test.go`

- [x] изменить сигнатуру `Notify` на `Notify(ctx context.Context, tasks []model.Task) error`
- [x] обновить `formatTaskMessage`: принимает `[]model.Task`, формирует одно сообщение
  - Заголовок: "✍️ Новые задачи записаны!"
  - Источник: `Source.Name (Source.Kind)`, для IMAP — `Origin.Subject (Origin.Account)`
  - Список задач с контекстом
- [x] написать тесты: одна задача — корректное форматирование
- [x] написать тесты: несколько задач — все в одном сообщении
- [x] написать тесты: задача с дедлайном и контекстом
- [x] написать тесты: задача без дедлайна и без контекста
- [x] запустить тесты — должны пройти

### Task 7: Интеграция в main.go

**Files:**
- Modify: `cmd/jeeves/main.go`

- [x] передать `Label` из `config.IMAPConfig` в `watcher.IMAPWatcherConfig`
- [x] убедиться, что все компоненты компилируются с новыми интерфейсами
- [x] запустить `go build -o bin/jeeves ./cmd/jeeves/`
- [x] запустить `go vet ./...`

### Task 8: Верификация

- [x] запустить полный набор тестов: `go test ./...`
- [x] запустить линтер: `go vet ./...`
- [x] проверить, что все требования из Overview реализованы:
  - Origin struct с Subject, Account, Topic, Context
  - Экстрактор возвращает context и topic
  - Pipeline заполняет Origin, обнуляет Topic для Telegram
  - ObsidianSink вставляет задачи в секции
  - TelegramNotifier отправляет групповое уведомление
  - IMAP: Source.Name = Label, Message.Subject отдельно

### Task 9: Документация

- [x] обновить CLAUDE.md при обнаружении новых паттернов
- [x] переместить план: `mv docs/plans/20260412-task-context-enrichment.md docs/plans/completed/`

## Post-Completion

**Ручная верификация:**
- Проверить с реальным TOML-конфигом: добавить `label = "Рабочая почта"` в секцию `[[watchers.imap]]`
- Проверить формирование Obsidian-файла на реальных данных
- Проверить Telegram-уведомление на реальном боте
- Оценить качество context и topic от LLM на реальных диалогах — возможно потребуется итерация промптов
