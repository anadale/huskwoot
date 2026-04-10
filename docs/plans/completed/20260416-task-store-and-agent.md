# Внутренний таск-стор и агентский интерфейс

## Overview

Трансформация Huskwoot из «трекера обещаний с внешними синками» в «персонального ассистента с собственным таск-менеджером и агентским интерфейсом».

Ключевые изменения:
- **TaskStore** (SQLite) — единственное хранилище проектов и задач в `huskwoot.db`. Default project «Inbox» создаётся при инициализации
- **Agent** (`internal/agent/`) — заменяет DM-pipeline. Tool calling цикл на smart-модели через go-openai. Инструменты: create_project, list_projects, create_task, list_tasks, complete_task
- **Маршрутизация**: DM/GroupDirect → Agent, Group/Batch → Classifier → Extractor → TaskStore
- **MessageKindGroupDirect** — новый Kind для обращений к боту в групповом чате (mention/reply)
- **Синки удаляются** (Obsidian, SuperProductivity). TelegramNotifier остаётся
- **Scope доступа**: DM — полный доступ ко всем проектам; GroupDirect — только привязанный к чату проект

## Context (from discovery)

- **Текущий pipeline:** `Pipeline.Process` маршрутизирует по `msg.Kind` → Classifier → Extractor → Sink/Notifier
- **DM-обработка:** `MessageKindDM` → `dmClassifier` → `dmExtractor` → sinks (заменяется агентом)
- **Group-обработка:** `MessageKindGroup` → `groupClassifier` → `extractor` → sinks (сохраняется, только sink меняется на TaskStore)
- **AI-клиент:** `internal/ai/client.go` — `Client.Complete()` и `CompleteJSON[T]()`. Нет поддержки tool calling — нужно добавить
- **Telegram channel:** `internal/channel/telegram.go` — `convertMessage()` выставляет `MessageKindGroup`, нет определения mention/reply на бота
- **Конфиг:** `config.SinksConfig` содержит Obsidian и SuperProductivity — удаляются
- **MetaStore:** привязка проекта к чату через `"project:"+chatID` → хранит ID проекта (вместо названия). Используется агентом для scope и pipeline для привязки задач
- **Существующие хранилища:** `storage.OpenDB` создаёт таблицы cursors, channel_projects, messages

## Development Approach

- **Подход к тестированию:** TDD — тесты пишутся до реализации
- Каждая задача полностью завершается перед переходом к следующей
- Небольшие, сфокусированные изменения
- **CRITICAL: каждая задача ДОЛЖНА включать новые/обновлённые тесты**
- **CRITICAL: все тесты должны проходить перед началом следующей задачи**
- **CRITICAL: обновлять этот план при изменении скоупа во время реализации**
- Команды: `go test ./...`, `go vet ./...`

## Testing Strategy

- **Unit tests:** table-driven тесты, ручные моки без фреймворков (стиль проекта)
- SQLite-тесты используют `t.TempDir()` для изолированной БД
- AI-клиент и агент тестируются через `httptest` (mock OpenAI API)
- Покрытие: успешные случаи + ошибки + edge cases

## Progress Tracking

- Завершённые пункты помечаются `[x]` сразу
- Новые задачи добавляются с префиксом ➕
- Блокеры помечаются ⚠️
- План обновляется при отклонении от первоначального скоупа

## Solution Overview

### Архитектура

Агент — peer-компонент рядом с Pipeline. Pipeline маршрутизирует по `msg.Kind`:
- `DM` / `GroupDirect` → `Agent.Handle(ctx, msg) → string` (ответ пользователю через `msg.ReplyFn`)
- `Group` / `Batch` → существующий Classifier → Extractor → TaskStore (напрямую)

### Схема БД

Две новые таблицы в `huskwoot.db`:

```sql
CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id),
    summary     TEXT NOT NULL,
    details     TEXT NOT NULL DEFAULT '',
    topic       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open',
    deadline    DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    source_kind TEXT NOT NULL DEFAULT '',
    source_id   TEXT NOT NULL DEFAULT ''
);
```

### Tool calling цикл агента

1. System prompt с описанием роли + user message
2. Вызов AI с описанием tools (OpenAI function calling)
3. Если модель вернула tool calls → исполнение → результаты обратно модели
4. Повторение до текстового ответа (макс. 5 итераций)

### Scope доступа

- DM: полный доступ — все инструменты, все проекты
- GroupDirect: ограниченный — project_id фиксирован через MetaStore lookup (`"project:"+chatID`), list_projects и create_project недоступны

### Распознавание обещаний

System prompt агента инструктирует модель: если пользователь описывает намерение что-то сделать — вызвать `create_task`. Модель сама решает, когда это обещание, а когда — явная команда. Отдельного инструмента для «распознать обещание» нет.

## Technical Details

### Новые типы в `model/`

```go
type Project struct {
    ID          string
    Name        string
    Description string
    CreatedAt   time.Time
}

type TaskFilter struct {
    Status string  // "open", "done", "cancelled", "" (все)
}

type TaskUpdate struct {
    Status   *string
    Details  *string
    Deadline **time.Time
}
```

Тип `Task` (существующий) расширяется полями `Status string`, `UpdatedAt time.Time`, `ProjectID string`.
`TaskStore.CreateTask` принимает `*Task` — вызывающий заполняет Summary, Details, Topic, Deadline, ProjectID, Source. Хранилище генерирует ID, CreatedAt, UpdatedAt, Status="open".

### MetaStore — хранение project ID

MetaStore теперь хранит `"project:"+chatID → projectID` (UUID) вместо названия проекта.
`SetProjectHandler` обновляется: принимает имя проекта от пользователя → ищет/создаёт проект в TaskStore → сохраняет ID в MetaStore.
Pipeline в `lookupProjectName` заменяется на `lookupProjectID` — возвращает project ID для передачи в TaskStore.

### Расширение AI-клиента

Новый метод `Client.CompleteWithTools()` для tool calling цикла:
- Принимает `[]openai.Tool` и messages
- Возвращает `openai.ChatCompletionResponse` (вместо строки)
- Агент сам управляет циклом tool calls

### MessageKindGroupDirect

TelegramChannel определяет обращение к боту:
- Сообщение содержит mention бота (`@botusername`)
- Сообщение является reply на сообщение бота

В обоих случаях устанавливается `MessageKindGroupDirect`.

## Implementation Steps

### Task 1: Типы и интерфейс TaskStore

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/interfaces.go`

- [x] Добавить `MessageKindGroupDirect` в `types.go`
- [x] Добавить тип `Project` в `types.go`
- [x] Добавить типы `TaskFilter`, `TaskUpdate` в `types.go`
- [x] Расширить существующий `model.Task` полями `Status string`, `UpdatedAt time.Time`, `ProjectID string`
- [x] Добавить интерфейс `TaskStore` в `interfaces.go` (CreateProject, GetProject, ListProjects, FindProjectByName, CreateTask, GetTask, ListTasks, UpdateTask, DefaultProjectID)
- [x] Убедиться что `go vet ./...` проходит

### Task 2: SQLiteTaskStore — реализация хранилища

**Files:**
- Create: `internal/storage/task_store.go`
- Create: `internal/storage/task_store_test.go`
- Modify: `internal/storage/db.go`

- [x] Написать тесты для `SQLiteTaskStore`: CreateProject/GetProject/ListProjects/FindProjectByName
- [x] Написать тесты для `SQLiteTaskStore`: CreateTask/GetTask/ListTasks/UpdateTask
- [x] Написать тесты для default project «Inbox» — создаётся автоматически при инициализации
- [x] Написать тесты для edge cases: дубликат имени проекта, несуществующий project_id, фильтрация по статусу
- [x] Добавить создание таблиц `projects` и `tasks` в `storage.OpenDB`
- [x] Реализовать `SQLiteTaskStore` с конструктором `NewSQLiteTaskStore(db *sql.DB) *SQLiteTaskStore`
- [x] Реализовать default project: при создании `SQLiteTaskStore` вставлять проект «Inbox» (INSERT OR IGNORE)
- [x] Метод `DefaultProjectID() string` для получения ID default-проекта
- [x] Запустить тесты — все должны проходить

### Task 3: Расширение AI-клиента для tool calling

**Files:**
- Modify: `internal/ai/client.go`
- Create: `internal/ai/client_tools_test.go`

- [x] Написать тесты для `Client.CreateChatCompletion` с tools через httptest (мок OpenAI API с tool calls в ответе)
- [x] Написать тесты для edge cases: пустой ответ, ответ без tool calls, несколько tool calls
- [x] Добавить метод `Client.CreateChatCompletion(ctx, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)` — тонкая обёртка над `inner.CreateChatCompletion`, подставляющая `Model` и `MaxCompletionTokens`
- [x] Запустить тесты — все должны проходить

### Task 4: Агент — ядро tool calling цикла

**Files:**
- Create: `internal/agent/agent.go`
- Create: `internal/agent/agent_test.go`

- [x] Написать тесты для `Agent.Handle`: сообщение без tool calls → текстовый ответ
- [x] Написать тесты для `Agent.Handle`: сообщение с tool call → исполнение → текстовый ответ
- [x] Написать тесты для `Agent.Handle`: ограничение на макс. 5 итераций
- [x] Написать тесты для scope: DM — полный набор tools, GroupDirect — ограниченный набор
- [x] Определить интерфейс `Tool` (Name, Description, Parameters, Execute)
- [x] Реализовать `Agent` struct с полями: client, tools, taskStore, metaStore, logger
- [x] Реализовать `Agent.Handle(ctx, msg) (string, error)` — цикл tool calling
- [x] Реализовать system prompt для агента (роль ассистента, распознавание обещаний)
- [x] Реализовать формирование scope (полный/ограниченный) по msg.Kind и MetaStore
- [x] Запустить тесты — все должны проходить

### Task 5: Инструменты агента

**Files:**
- Create: `internal/agent/tools.go`
- Create: `internal/agent/tools_test.go`

- [x] Написать тесты для `createProjectTool.Execute` (успех + ошибка дубликата)
- [x] Написать тесты для `listProjectsTool.Execute`
- [x] Написать тесты для `createTaskTool.Execute` (с проектом, без проекта → Inbox, с deadline)
- [x] Написать тесты для `listTasksTool.Execute` (все задачи, фильтр по статусу)
- [x] Написать тесты для `completeTaskTool.Execute` (успех + несуществующий ID)
- [x] Реализовать `createProjectTool`, `listProjectsTool`, `createTaskTool`, `listTasksTool`, `completeTaskTool`
- [x] Каждый инструмент реализует интерфейс `Tool` и работает с `TaskStore`
- [x] `createTaskTool`: если project не указан — использовать `DefaultProjectID()`
- [x] Запустить тесты — все должны проходить

### Task 6: MessageKindGroupDirect в TelegramChannel

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] Написать тесты для convertMessage: mention бота → `MessageKindGroupDirect`
- [x] Написать тесты для convertMessage: reply на сообщение бота → `MessageKindGroupDirect`
- [x] Написать тесты для convertMessage: обычное групповое сообщение → `MessageKindGroup` (без изменений)
- [x] Добавить поле `BotID string` в `TelegramChannelConfig` (user ID бота для определения mention/reply)
- [x] Реализовать определение mention бота в тексте сообщения (через `m.Entities` типа "mention")
- [x] Реализовать определение reply на сообщение бота (через `m.ReplyToMessage.From.ID`)
- [x] При совпадении устанавливать `MessageKindGroupDirect` вместо `MessageKindGroup`
- [x] Запустить тесты — все должны проходить

### Task 7: Интеграция в Pipeline

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`

- [x] Написать тесты для маршрутизации: MessageKindDM → agent.Handle → ReplyFn
- [x] Написать тесты для маршрутизации: MessageKindGroupDirect → agent.Handle → ReplyFn
- [x] Написать тесты для: Group/Batch → Classifier → Extractor → TaskStore.CreateTask (напрямую)
- [x] Написать тесты для: agent.Handle возвращает ошибку → логируется, не прерывает
- [x] Написать тесты для: `lookupProjectID` возвращает project ID из MetaStore, fallback на default project
- [x] Добавить поля `agent` (интерфейс `AgentHandler`) и `taskStore` в Pipeline
- [x] Изменить `Process`: DM и GroupDirect → `agent.Handle` + `msg.ReplyFn`
- [x] Заменить `lookupProjectName` на `lookupProjectID` — возвращает project ID из MetaStore
- [x] В `processPromise`: после извлечения задач → `taskStore.CreateTask` напрямую (вместо dispatch в sinks)
- [x] Для DM/GroupDirect: после успешного ответа → reactDone, при ошибке → логирование
- [x] Убрать интерфейс `Sink` и поле `sinks` из Pipeline (dispatch только notifiers)
- [x] Запустить тесты — все должны проходить

### Task 8: Обновление SetProjectHandler

**Files:**
- Modify: `internal/handler/set_project.go`
- Modify: `internal/handler/set_project_test.go`

- [x] Написать тесты для: SetProjectHandler ищет проект по имени в TaskStore → сохраняет ID в MetaStore
- [x] Написать тесты для: проект не найден → создаёт новый проект в TaskStore → сохраняет ID
- [x] Обновить `SetProjectHandler`: принимает `TaskStore` в конструктор
- [x] Реализовать: FindProjectByName → если нет, CreateProject → MetaStore.Set(key, projectID)
- [x] Запустить тесты — все должны проходить

### Task 9: Удаление синков и обновление main.go

**Files:**
- Delete: `internal/sink/obsidian.go`
- Delete: `internal/sink/obsidian_test.go`
- Delete: `internal/sink/super_productivity.go`
- Delete: `internal/sink/super_productivity_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/huskwoot/main.go`

- [x] Удалить `internal/sink/obsidian.go` и `internal/sink/obsidian_test.go`
- [x] Удалить `internal/sink/super_productivity.go` и `internal/sink/super_productivity_test.go`
- [x] Удалить `ObsidianSinkConfig`, `SuperProductivityConfig`, `SinksConfig` из конфига
- [x] Убрать валидацию синков из `config.validate()`
- [x] Обновить `main.go`: убрать `buildSinks`, добавить инициализацию `TaskStore` и `Agent`
- [x] Передать `TaskStore` и `Agent` в pipeline
- [x] Передать `BotID` в `TelegramChannelConfig` (из `bot.Self.ID`)
- [x] Обновить тесты конфига — убрать проверки синков
- [x] Запустить `go test ./...` — все тесты должны проходить
- [x] Запустить `go vet ./...` — без ошибок

### Task 10: Верификация

- [x] Запустить `go test ./...` — все тесты проходят
- [x] Запустить `go vet ./...` — без ошибок
- [x] Проверить что `go build -o bin/huskwoot ./cmd/huskwoot` собирается без ошибок
- [x] Убедиться что нет неиспользуемых импортов и переменных

### Task 11: Обновление CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [x] Обновить раздел «Структура директорий» — добавить `internal/agent/`
- [x] Обновить раздел «Архитектура Pipeline» — описать агентский путь и MessageKindGroupDirect
- [x] Обновить раздел «SQLite-хранилище» — добавить описание TaskStore, таблиц projects/tasks
- [x] Удалить раздел «Добавление нового Sink» (или пометить как deprecated)
- [x] Добавить раздел «Агент и инструменты» — описание tool calling цикла, добавления новых инструментов
- [x] Переместить этот план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Отправить боту DM «создай проект Тест» → проверить что проект создан
- Отправить «нужно настроить CI для проекта Тест» → проверить что задача создана в проекте Тест
- Отправить «покажи задачи» → проверить список
- В группе с привязанным проектом: @бот покажи задачи → только задачи привязанного проекта
- В группе: обычное сообщение с обещанием → задача создаётся через pipeline (не агент)

**Будущие итерации:**
- REST API для внешних интеграций
- Экспортёры/синхронизаторы для Obsidian, SuperProductivity (через REST API или отдельные команды)
- Дополнительные инструменты агента (редактирование задач, поиск, напоминания)
