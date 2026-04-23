# Расширение инструментов агента для работы с задачами

## Overview

Добавить шесть новых инструментов агенту Huskwoot для ежедневной работы с задачами: `get_task`, `update_task`, `cancel_task`, `reopen_task`, `search_tasks`, `snooze_task`. Все инструменты опираются на уже существующий `TaskService` — новых методов в сервисном слое, миграций SQLite и изменений событий/пушей не требуется.

Проблема: агент сейчас умеет только создавать задачи, помечать выполненными, перемещать и привязывать чат к проекту. Нет способа посмотреть детали задачи, исправить опечатку в summary, отменить ошибочно созданную задачу, перенести дедлайн или найти задачу по подстроке — пользователь вынужден либо обращаться к мобильному клиенту, либо создавать новую задачу вместо правки.

Интеграция: новые инструменты регистрируются в `cmd/huskwoot/main.go` рядом с существующими. Поведение tool-calling-цикла (максимум 5 итераций) не меняется. Системный промпт правок не требует — список инструментов формируется из `Tool.Description()`.

## Context (from discovery)

**Файлы и области:**
- `internal/agent/` — существующие инструменты (`tool_complete_task.go`, `tool_move_task.go`, `tool_create_task.go`, `tool_list_projects.go`, `tool_list_tasks.go`, `tool_create_project.go`, `tool_set_project.go`) и тесты (`tools_test.go`, `tool_move_task_test.go`).
- `internal/model/service.go` — интерфейсы `TaskService`, `ProjectService`. Все нужные методы уже есть: `GetTask`, `GetTaskByRef`, `UpdateTask`, `ReopenTask`, `ListTasks`.
- `internal/model/types.go` — `Task`, `TaskUpdate`, `TaskFilter`.
- `internal/dateparse/` — парсер natural-language-дат, уже используется в `create_task`.
- `internal/i18n/locales/ru.json`, `en.json` — строки UI и ошибок.
- `cmd/huskwoot/main.go` — сборка списка инструментов агента.

**Найденные паттерны:**
- Инструмент = файл `tool_<name>.go` с конструктором `NewXxxTool(deps..., loc *goI18n.Localizer) Tool`, типом `type xxxTool struct{...}`, методами `Name()/Description()/Parameters()/DMOnly()/Execute(ctx, args)`.
- Описания и параметры идут через `huskwootI18n.Translate(loc, "key", nil)`.
- Tool-ответы сериализуются в JSON через `map[string]any` с **snake_case**-ключами (`display_id`, `project_id`, `task_id`). Это внутренняя шина LLM ↔ сервис, не HTTP API.
- Парсинг `slug#number`: утилита `parseTaskRef` в `tool_complete_task.go`; логика резолва ref → Task дублируется в `tool_complete_task.go` и `tool_move_task.go`.
- Тесты: табличные, ручные моки `mockTaskService`/`mockProjectService` в `tools_test.go`, пакет тестов `agent_test`.
- Параметр идентификатора задачи во всех существующих инструментах называется `task_id` (конвенция, зафиксированная commit `cbcfa6e`).

**Зависимости:**
- `TaskService.UpdateTask` уже публикует `task_updated` с корректными `changedFields` — cancel и snooze автоматически получают правильные события без дополнительной работы.
- `dateparse.Dateparser` принимает `now` из `ctx.Value(nowKey)` — конвенция заложена в `tool_create_task.go`.

## Development Approach

- **Testing approach**: **Regular** (код и тесты пишутся в рамках одной задачи; тесты могут идти сразу после реализации). Выбрано из соображения единообразия с существующими инструментами — в проекте нет строгого TDD, а `tool_move_task_test.go` и `tools_test.go` написаны рядом с реализацией.
- Complete each task fully before moving to the next.
- Make small, focused changes.
- **CRITICAL: every task MUST include new/updated tests** — тесты обязательны, не опциональны.
- **CRITICAL: all tests must pass before starting next task** — без исключений.
- **CRITICAL: update this plan file when scope changes during implementation**.
- Run `go test ./...` and `go vet ./...` after each task.
- Maintain backward compatibility — старые инструменты `complete_task`/`move_task` после рефакторинга ведут себя идентично (проверяется их существующими тестами).

## Testing Strategy

- **Unit tests**: required for every task.
- **E2E tests**: в проекте нет Playwright/Cypress — полагаемся на `go test ./...`. HTTP API через `/v1/chat` не меняется; новые инструменты тестируются через их `Execute` с моками сервисного слоя.
- Тестовые сценарии по каждому инструменту зафиксированы в разделе Technical Details.

## Progress Tracking

- Mark completed items with `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix.
- Document issues/blockers with ⚠️ prefix.
- Update plan if implementation deviates from original scope.
- Keep plan in sync with actual work done.

## Solution Overview

Каждый новый инструмент — тонкая обёртка над уже существующим методом `TaskService`. Добавляется:

1. **Общий хелпер** `resolveTask` в `internal/agent/resolve_task.go` — принимает UUID либо `slug#number`, возвращает `*model.Task`. Дедуплицирует логику, которая сейчас живёт в `tool_complete_task.go` и частично в `tool_move_task.go`. Оба существующих инструмента переключаются на него.

2. **Шесть новых tool-файлов** — по одному на инструмент, однотипная структура.

3. **i18n-ключи** — единый коммит, все 25–30 строк сразу, чтобы последующие задачи могли ссылаться на готовые ключи.

4. **Регистрация** в `cmd/huskwoot/main.go` — шесть новых строк в сборке `agentTools`.

Ничего в `internal/usecase/`, `internal/model/`, `internal/storage/`, `api/openapi.yaml`, `internal/push/`, `internal/events/` не трогается. События публикуются текущим `TaskService.UpdateTask`/`ReopenTask`.

### Ключевые дизайн-решения

1. **Soft-delete вместо hard-delete.** `cancel_task` — это `UpdateTask(Status="cancelled")`. Причина: агент вероятностный; soft-delete обратим через `reopen_task`, hard-delete — нет. Дополнительно избегаем нового `EventKind`, push-шаблона, логики удаления на клиентах.

2. **Отдельный `snooze_task`** вместо «просто используй `update_task(deadline=…)`». Причина: LLM реже ошибается интентом при выделенном имени инструмента. Параметр `until` обязательный — в отличие от `deadline` в `update_task`, который можно сбросить пустой строкой.

3. **`search_tasks` без серверной фильтрации дат.** `TaskFilter` не поддерживает `due_before`/`due_after`, расширять его для одного потребителя избыточно. Фильтрация по датам выполняется в коде инструмента после `ListTasks(projectID="", filter)`. Если потом окажется, что поиск по датам нужен в HTTP API — расширим `TaskFilter` вместе с OpenAPI.

4. **Поле `Priority` не входит в этот план.** Требует миграции SQLite, расширения `TaskUpdate`, `taskSnapshot`, OpenAPI, push-шаблонов, клиентов. Отдельный брейншторм и план.

5. **Все шесть инструментов доступны и в DM, и в GroupDirect** (`DMOnly() == false`). Работа с задачами — это не конфигурационное действие, пользователь может её делать из группового чата реплаем на бота.

6. **`update_task` — только три поля**: `summary`, `details`, `deadline`. Topic не правим (не отображается в UI клиентов, не несёт пользы для пользователя). Status — только через выделенные `cancel_task`/`complete_task`/`reopen_task` (явный интент вместо угадывания).

## Technical Details

### Хелпер `resolveTask`

```go
// resolveTask resolves a task by its UUID or "<slug>#<number>" reference.
// Returns ErrTaskRefInvalid if the reference is malformed, ErrTaskNotFound if
// the task does not exist.
func resolveTask(ctx context.Context, svc model.TaskService, ref string) (*model.Task, error)
```

Правила:
- `ref == ""` → `ErrTaskIDRequired`.
- `strings.Contains(ref, "#")` → парсим через существующий `parseTaskRef` (оставляем его в `tool_complete_task.go` или переносим в `resolve_task.go` — решение во время реализации, выбираем перенос).
- Иначе — трактуем как UUID, вызываем `svc.GetTask(ctx, ref)`; `nil, nil` → `ErrTaskNotFound`.

Ошибки возвращаются как простые `error` с i18n-текстом (через `errors.New(huskwootI18n.Translate(...))`), как в существующих инструментах. Выделенных sentinel-ошибок не заводим — это внутренняя утилита.

### `get_task`

- Параметры: `task_id` (обязательно, UUID или `slug#number`).
- Исполнение: `resolveTask` → ответ JSON.
- Ответ (snake_case, опциональные поля опускаются если `nil`):
  ```json
  {
    "id": "uuid",
    "display_id": "inbox#42",
    "project_id": "uuid",
    "project_slug": "inbox",
    "summary": "...",
    "details": "...",
    "topic": "...",
    "status": "open",
    "deadline": "2026-04-25T09:00:00Z",
    "created_at": "2026-04-20T08:11:00Z",
    "updated_at": "2026-04-22T15:02:00Z",
    "closed_at": null
  }
  ```
- Не включать: `confidence`, `source`, `source_message`, `number` — шумят контекст LLM.

### `update_task`

- Параметры: `task_id` (обязательно), `summary` (опц.), `details` (опц.), `deadline` (опц.).
- Парсинг: `json.RawMessage` для каждого опционального поля, чтобы различать «не прислано» и «прислали пустую строку».
- Семантика:
  - `summary`: если прислано — новое значение; пустая строка запрещена (возвращаем ошибку).
  - `details`: если прислано — новое значение (включая пустую строку — очистить details).
  - `deadline`: `""` → сбросить (передать `TaskUpdate.Deadline = **nilTime`); непустое → парсить через `dateparse.Dateparser` с `now` из `ctx.Value(nowKey)`; не прислано → не менять.
- Если все три поля отсутствуют → ошибка «no fields to update».
- Исполнение: `resolveTask` → собрать `TaskUpdate` → `TaskService.UpdateTask` → ответ `{id, display_id, summary, deadline, note: "updated"}`.

### `cancel_task`

- Параметры: `task_id` (обязательно).
- Исполнение: `resolveTask` → `status := "cancelled"`; `UpdateTask(TaskUpdate{Status: &status})` → ответ `{id, display_id, status: "cancelled"}`.
- Идемпотентность: повторный вызов для уже cancelled возвращает успех без изменений.

### `reopen_task`

- Параметры: `task_id` (обязательно).
- Исполнение: `resolveTask` → `TaskService.ReopenTask(ctx, task.ID)` → ответ `{id, display_id, status: "open"}`.
- Работает для done и cancelled; для open — идемпотентный успех.

### `snooze_task`

- Параметры: `task_id` (обязательно), `until` (обязательно, natural language — `"tomorrow"`, `"next monday"`, `"in 3 days"`).
- Исполнение: `resolveTask` → парсинг `until` через `dateparse.Dateparser`; `nil` результат → ошибка «could not parse deadline»; иначе `UpdateTask(TaskUpdate{Deadline: &newDeadline})` → ответ `{id, display_id, deadline}`.

### `search_tasks`

- Параметры: все опциональные, кроме ограничений:
  - `query` (string) → `TaskFilter.Query`.
  - `status` (string, дефолт `"open"`): значение `"all"` маппится в `""`; прочие — как есть.
  - `project` (string, UUID или имя). UUID определяется попыткой `GetProject` → если найден, используем ID; иначе `ProjectService.FindProjectByName` (это же паттерн и в `create_task`, `move_task`). Пустое — все проекты.
  - `due_before`, `due_after` (string, natural language) — парсим в `time.Time`.
  - `limit` (int, дефолт 20, максимум 50).
- Исполнение:
  1. Резолвим `projectID` (пустое → `""`).
  2. Вызываем `ListTasks(projectID, TaskFilter{Query, Status, Limit: limit * 2})` — берём с запасом на случай отсева по датам.
  3. В коде фильтруем: `due_before` → оставляем задачи с `Deadline != nil && *Deadline < due_before`; `due_after` → `*Deadline > due_after`.
  4. Если результат больше `limit` — урезаем.
- Ответ: массив `{id, display_id, project_slug, summary, status, deadline}` без `details` (чтобы не раздувать контекст LLM при большой выдаче).

### i18n ключи (полный список)

`internal/i18n/locales/ru.json` и `en.json`:

```
tool_get_task_desc
tool_get_task_param_task_id
tool_update_task_desc
tool_update_task_param_task_id
tool_update_task_param_summary
tool_update_task_param_details
tool_update_task_param_deadline
tool_cancel_task_desc
tool_cancel_task_param_task_id
tool_reopen_task_desc
tool_reopen_task_param_task_id
tool_snooze_task_desc
tool_snooze_task_param_task_id
tool_snooze_task_param_until
tool_search_tasks_desc
tool_search_tasks_param_query
tool_search_tasks_param_status
tool_search_tasks_param_project
tool_search_tasks_param_due_before
tool_search_tasks_param_due_after
tool_search_tasks_param_limit

agent_no_fields_to_update
agent_deadline_parse_failed
agent_snooze_until_required
agent_summary_empty
agent_search_limit_exceeded
```

`agent_task_not_found`, `agent_task_id_or_ref_required`, `agent_invalid_ref_format` уже существуют — переиспользуем.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): код, тесты, локализации, регистрация в main.go, обновление CLAUDE.md.
- **Post-Completion** (без чекбоксов): ручная проверка в Telegram-DM и Telegram-группе после деплоя (что LLM реально находит новые инструменты и корректно их применяет).

## Implementation Steps

### Task 1: Общий хелпер resolveTask и рефакторинг существующих инструментов

**Files:**
- Create: `internal/agent/resolve_task.go`
- Create: `internal/agent/resolve_task_test.go`
- Modify: `internal/agent/tool_complete_task.go`
- Modify: `internal/agent/tool_move_task.go`

- [x] Создать `internal/agent/resolve_task.go` с функцией `resolveTask(ctx, svc model.TaskService, loc *goI18n.Localizer, ref string) (*model.Task, error)`. Вынести `parseTaskRef` из `tool_complete_task.go` в этот же файл.
- [x] Рефакторить `tool_complete_task.go`: заменить `t.resolveRef` + inline UUID-ветку на вызов `resolveTask`. Удалить приватный `resolveRef`.
- [x] Рефакторить `tool_move_task.go`: заменить inline `parseTaskRef`/`GetTaskByRef`/UUID-проверку на вызов `resolveTask`.
- [x] Написать `resolve_task_test.go` (табличный тест в пакете `agent_test` через `export_test.go` либо в пакете `agent`): UUID / `slug#42` / `slug#` (malformed: число <=0 или отсутствует) / `#42` без slug / пустая строка / несуществующий UUID (мок возвращает `nil, nil`) / несуществующий ref (мок `GetTaskByRef` возвращает `nil, nil`).
- [x] Запустить `go test ./internal/agent/...` — существующий `tool_move_task_test.go` должен пройти без правок, новый `resolve_task_test.go` зелёный.
- [x] Запустить `go vet ./...`.

### Task 2: i18n-ключи для всех новых инструментов

**Files:**
- Modify: `internal/i18n/locales/ru.json`
- Modify: `internal/i18n/locales/en.json`

- [x] Добавить все 25+ ключей из раздела Technical Details в `ru.json`. Русские формулировки — описательные и краткие, соответствующие стилю существующих (пример: `"tool_complete_task_desc": "Отметить задачу как выполненную..."`).
- [x] Добавить те же ключи в `en.json` с английскими формулировками.
- [x] Проверить, что существующие `agent_task_not_found`, `agent_task_id_or_ref_required`, `agent_invalid_ref_format` остались неизменными.
- [x] Написать или расширить тест на загрузку локалей (если есть — в `internal/i18n/`), убедиться, что ключи читаются. Если такого теста нет — пропустить; мы проверим через компиляцию пакета `internal/agent/`.
- [x] Запустить `go test ./internal/i18n/...`.

### Task 3: Простые инструменты — get_task, cancel_task, reopen_task

**Files:**
- Create: `internal/agent/tool_get_task.go`
- Create: `internal/agent/tool_cancel_task.go`
- Create: `internal/agent/tool_reopen_task.go`
- Modify: `internal/agent/tools_test.go`

- [x] Создать `tool_get_task.go`: конструктор `NewGetTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool`; `Execute` — парсит `task_id`, вызывает `resolveTask`, формирует JSON-ответ со всеми полями из раздела Technical Details (опциональные поля с `omitempty`-семантикой: не включать в `map[string]any`, если nil/пусто).
- [x] Создать `tool_cancel_task.go`: конструктор `NewCancelTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool`; `Execute` — `resolveTask` → `UpdateTask(Status="cancelled")` → JSON `{id, display_id, status}`.
- [x] Создать `tool_reopen_task.go`: конструктор `NewReopenTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool`; `Execute` — `resolveTask` → `ReopenTask` → JSON `{id, display_id, status}`.
- [x] Расширить `mockTaskService` в `tools_test.go`: убедиться, что есть `GetTask`, `UpdateTask`, `ReopenTask` (обычно уже есть; при необходимости добавить поля для фиксации аргументов и возвращаемого значения).
- [x] Тесты `get_task`: найдена (все поля) / не найдена / `deadline==nil` опущен из JSON / `closed_at==nil` опущен / `slug#number` (проверяем, что `resolveTask` подхвачен).
- [x] Тесты `cancel_task`: обычная отмена / уже cancelled (идемпотентный ответ) / не найдена.
- [x] Тесты `reopen_task`: из done / из cancelled / из open / не найдена.
- [x] Запустить `go test ./internal/agent/...` и `go vet ./...`.

### Task 4: update_task с трёхсостоянной семантикой полей

**Files:**
- Create: `internal/agent/tool_update_task.go`
- Modify: `internal/agent/tools_test.go`

- [x] Создать `tool_update_task.go`: конструктор `NewUpdateTaskTool(tasks model.TaskService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool`.
- [x] Парсинг параметров через `map[string]json.RawMessage` либо структуру с `*string` + `json.Decoder` с `DisallowUnknownFields=false`. Выбор — `map[string]json.RawMessage` (проще отличить «ключа нет» от «ключ с пустой строкой»).
- [x] Реализовать семантику:
  - `summary` прислан и пустой → ошибка `agent_summary_empty`.
  - `summary` прислан и непустой → `upd.Summary = &s`.
  - `details` прислан → `upd.Details = &s` (пустая строка разрешена — очистить).
  - `deadline` прислан и `""` → `upd.Deadline = new(*time.Time)` (указатель на nil-указатель — семантика сброса в `TaskUpdate`).
  - `deadline` прислан и непустой → парсить `dp.Parse(s, now)`; nil результат → ошибка; иначе `upd.Deadline = &parsed` (указатель на указатель-на-время).
  - Ни одно поле не прислано → ошибка `agent_no_fields_to_update`.
- [x] `resolveTask` → `UpdateTask(task.ID, upd)` → JSON `{id, display_id, summary, deadline, note: "updated"}`.
- [x] Тесты: summary only / details only (непустое) / details = "" (очистить) / deadline сброс (`""`) / deadline natural language / summary + details + deadline одновременно / пустой patch → ошибка / пустой summary → ошибка / невалидный deadline → ошибка / task не найден / `slug#number` ref.
- [x] Запустить `go test ./internal/agent/...` и `go vet ./...`.

### Task 5: snooze_task

**Files:**
- Create: `internal/agent/tool_snooze_task.go`
- Modify: `internal/agent/tools_test.go`

- [x] Создать `tool_snooze_task.go`: конструктор `NewSnoozeTaskTool(tasks model.TaskService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool`.
- [x] `Execute`: парсит `task_id` и `until` (оба обязательные). `until==""` → ошибка `agent_snooze_until_required`. Парсит `until` через `dp.Parse(s, now)`; nil → ошибка `agent_deadline_parse_failed`.
- [x] `resolveTask` → `UpdateTask(TaskUpdate{Deadline: &newDeadline})` → JSON `{id, display_id, deadline}` (RFC3339).
- [x] Тесты: обычный snooze (корректный natural language) / без `until` → ошибка / невалидный `until` → ошибка / task не найден / `slug#number` ref.
- [x] Запустить `go test ./internal/agent/...` и `go vet ./...`.

### Task 6: search_tasks

**Files:**
- Create: `internal/agent/tool_search_tasks.go`
- Modify: `internal/agent/tools_test.go`

- [x] Создать `tool_search_tasks.go`: конструктор `NewSearchTasksTool(tasks model.TaskService, projects model.ProjectService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool`.
- [x] `Execute`:
  - Парсит все параметры (все опциональные).
  - `status`: дефолт `"open"`; `"all"` → `""`.
  - `project`: если непустое — сначала пытаемся `projects.FindProjectByName`; если `nil` — пробуем как UUID через... в интерфейсе `ProjectService` нет `GetProject`, только `ListProjects`. Тогда логика: `FindProjectByName` сначала. Если не найден — попробуем считать, что это уже UUID, и передадим в `ListTasks(projectID=...)` напрямую. Если задач нет — вернём пустой массив.
  - `limit`: дефолт 20, `>50` → `50`, `<=0` → 20.
  - `due_before`, `due_after`: если непустые — парсим через `dp.Parse(s, now)`; nil результат → ошибка.
- [x] `tasks.ListTasks(projectID, TaskFilter{Query, Status, Limit: limit*2})`. Пост-фильтрация по датам в коде. Усечение до `limit`.
- [x] Ответ: массив объектов `{id, display_id, project_slug, summary, status, deadline}`.
- [x] Тесты: по query / по status (open/done/cancelled/all) / по project (имя) / по project (UUID) / по несуществующему project (пустой результат) / due_before / due_after / due_before + due_after одновременно / limit обрезает результат / limit > 50 → clamp до 50 / невалидный due_before → ошибка.
- [x] Запустить `go test ./internal/agent/...` и `go vet ./...`.

### Task 7: Регистрация инструментов в main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] Найти место сборки `agentTools` (рядом с `agent.NewCompleteTaskTool`, `agent.NewMoveTaskTool`).
- [x] Добавить шесть новых вызовов конструкторов в список:
  - `agent.NewGetTaskTool(taskService, loc)`
  - `agent.NewUpdateTaskTool(taskService, dateparser, loc)`
  - `agent.NewCancelTaskTool(taskService, loc)`
  - `agent.NewReopenTaskTool(taskService, loc)`
  - `agent.NewSnoozeTaskTool(taskService, dateparser, loc)`
  - `agent.NewSearchTasksTool(taskService, projectService, dateparser, loc)`
- [x] Убедиться, что `dateparser` и `projectService` уже в скоупе (они нужны для `create_task`/`move_task`, значит есть).
- [x] Запустить `go build -o bin/huskwoot ./cmd/huskwoot/` — должен собраться.
- [x] Запустить `go test ./...` — полный прогон.
- [x] Запустить `go vet ./...`.

### Task 8: Verify acceptance criteria

- [x] Проверить, что все шесть инструментов присутствуют в выводе агента (manual test - skipped, not automatable; инструменты зарегистрированы в main.go, сборка проходит).
- [x] Verify edge cases: `resolveTask` с некорректным `slug#abc`, с несуществующим UUID, с пустой строкой — покрыто в `resolve_task_test.go`.
- [x] Run full test suite: `go test ./...` — все пакеты OK.
- [x] Run: `go vet ./...` — чисто.
- [x] Verify test coverage — все сценарии из Technical Details покрыты в `tools_test.go` и `resolve_task_test.go`.

### Task 9: Update documentation and move plan

**Files:**
- Modify: `CLAUDE.md`

- [x] Обновить таблицу инструментов в разделе «Agent» файла `CLAUDE.md`: добавить шесть новых строк с `DMOnly` и кратким описанием.
- [x] Переместить этот план: `mkdir -p docs/plans/completed && git mv docs/plans/2026-04-23-agent-task-tools.md docs/plans/completed/`.

## Post-Completion

*Items requiring manual intervention or external systems — no checkboxes, informational only.*

**Manual verification:**
- В Telegram-DM: проверить, что `get_task`, `update_task`, `cancel_task`, `reopen_task`, `snooze_task`, `search_tasks` реально применяются LLM в естественных диалогах («покажи задачу inbox#3», «отложи эту задачу до понедельника», «отмени задачу про отчёт»). LLM должна сама выбирать нужный инструмент без явных подсказок в промпте.
- В Telegram-группе (реплай на бота): убедиться, что инструменты доступны (все они `DMOnly()==false`). Проверить, что поиск и правка работают для задач группы.
- Проверить push-уведомления на мобильном клиенте: `cancel_task` должен прийти как `task_updated` с `changedFields: ["status"]`; `snooze_task` — с `changedFields: ["deadline"]`; `update_task` — с соответствующим списком полей.

**External system updates:**
- Ни один клиент iOS/Android/Web правок не требует — события и снапшоты tasks не меняются. Если позднее появится UI-функциональность «показать только cancelled задачи», она не заблокирована этим планом.
