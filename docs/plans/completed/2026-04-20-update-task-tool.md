# Реализация инструмента update_task

> **Для агентских воркеров:** используйте `superpowers:executing-plans` для пошаговой реализации.
> Шаги используют синтаксис `- [ ]` для трекинга прогресса.

**Цель:** Добавить инструмент `update_task` в агент Huskwoot, позволяющий изменять поля задачи
(summary, details, deadline, status). Инструмент принимает только UUID (`task_id`), полученный
агентом через `list_projects` + `list_tasks`. Системный промпт обновляется с инструкцией,
как найти UUID задачи перед вызовом `update_task`.

**Спецификация:** `docs/superpowers/specs/2026-04-20-update-task-tool.md`

**Технологический стек:** Go 1.26; проверка: `go build ./...`, `go test ./...`

**Подход к разработке:** TDD — тесты пишутся перед реализацией или одновременно с ней.

---

## Карта файлов

| Файл | Изменения |
|---|---|
| `internal/agent/tool_update_task.go` | Создать — основная реализация |
| `internal/agent/tool_update_task_test.go` | Создать — 11 тест-кейсов |
| `internal/agent/tools_test.go` | Изменить — расширить `mockTaskService` |
| `internal/agent/prompts/agent_system.tmpl` | Изменить — добавить инструкцию по поиску UUID |
| `cmd/huskwoot/main.go` | Изменить — добавить `NewUpdateTaskTool` |

---

## Task 1: Расширить mockTaskService для поддержки UpdateTask

**Files:**
- Modify: `internal/agent/tools_test.go`

- [ ] Добавить поля в `mockTaskService`:
  ```go
  updateTaskResult *model.Task
  updateTaskErr    error
  lastUpdateID     string
  lastUpdateUpd    model.TaskUpdate
  ```
- [ ] Обновить метод `UpdateTask` мока:
  ```go
  func (m *mockTaskService) UpdateTask(_ context.Context, id string, upd model.TaskUpdate) (*model.Task, error) {
      m.lastUpdateID = id
      m.lastUpdateUpd = upd
      if m.updateTaskErr != nil {
          return nil, m.updateTaskErr
      }
      if m.updateTaskResult != nil {
          return m.updateTaskResult, nil
      }
      task := &model.Task{ID: id, Number: 1, ProjectSlug: "inbox", Status: "open"}
      if upd.Summary != nil { task.Summary = *upd.Summary }
      if upd.Status != nil  { task.Status = *upd.Status }
      return task, nil
  }
  ```
- [ ] Запустить `go test ./internal/agent/...` — все существующие тесты должны пройти.

---

## Task 2: Написать тесты для tool_update_task

**Files:**
- Create: `internal/agent/tool_update_task_test.go`

Тесты пишутся до реализации (TDD). Компилируются только после Task 3.

- [ ] Создать файл `tool_update_task_test.go` (пакет `agent_test`).

- [ ] **TestUpdateTaskTool_Execute_UpdateSummary** — обновление summary по UUID:
  ```go
  tool := agent.NewUpdateTaskTool(tasks, cfg)
  result, err := tool.Execute(ctx, `{"task_id":"uuid-1","summary":"новое название"}`)
  // err == nil
  // tasks.lastUpdateID == "uuid-1"
  // tasks.lastUpdateUpd.Summary != nil && *tasks.lastUpdateUpd.Summary == "новое название"
  ```

- [ ] **TestUpdateTaskTool_Execute_DeadlineRFC3339** — установка дедлайна RFC3339:
  ```go
  // args: {"task_id":"x","deadline":"2026-12-31T00:00:00Z"}
  // tasks.lastUpdateUpd.Deadline != nil && **tasks.lastUpdateUpd.Deadline != (zero time)
  ```

- [ ] **TestUpdateTaskTool_Execute_DeadlineNatural** — натуральный язык («завтра»):
  ```go
  // ctx с nowKey = time.Date(2026,4,15,14,0,0,0,time.UTC)
  // args: {"task_id":"x","deadline":"завтра"}
  // **tasks.lastUpdateUpd.Deadline == time.Date(2026,4,16,0,0,0,0,time.UTC)
  ```

- [ ] **TestUpdateTaskTool_Execute_DeadlineNone** — снятие дедлайна:
  ```go
  // args: {"task_id":"x","deadline":"none"}
  // tasks.lastUpdateUpd.Deadline != nil && *tasks.lastUpdateUpd.Deadline == nil
  ```

- [ ] **TestUpdateTaskTool_Execute_ClearDetails** — очистка details (пустая строка):
  ```go
  // args: {"task_id":"x","details":""}
  // tasks.lastUpdateUpd.Details != nil && *tasks.lastUpdateUpd.Details == ""
  ```

- [ ] **TestUpdateTaskTool_Execute_UpdateStatus** — изменение статуса:
  ```go
  // args: {"task_id":"x","status":"cancelled"}
  // *tasks.lastUpdateUpd.Status == "cancelled"
  ```

- [ ] **TestUpdateTaskTool_Execute_EmptyFieldsNoChange** — пустые поля не трогают TaskUpdate:
  ```go
  // args: {"task_id":"x","summary":"","status":""}
  // tasks.lastUpdateUpd.Summary == nil
  // tasks.lastUpdateUpd.Status == nil
  // tasks.lastUpdateUpd.Deadline == nil  (не передан)
  // tasks.lastUpdateUpd.Details == nil   (не передан)
  ```

- [ ] **TestUpdateTaskTool_Execute_TaskNotFound** — задача не найдена:
  ```go
  // tasks.getTaskErr = errors.New("не найдена")
  // _, err = tool.Execute(ctx, `{"task_id":"x"}`)
  // err != nil
  ```

- [ ] **TestUpdateTaskTool_Execute_InvalidJSON**:
  ```go
  // _, err = tool.Execute(ctx, `{not valid}`)
  // err != nil
  ```

- [ ] **TestUpdateTaskTool_Execute_MissingTaskID** — нет task_id:
  ```go
  // _, err = tool.Execute(ctx, `{"summary":"что-то"}`)
  // err != nil
  ```

- [ ] **TestUpdateTaskTool_Metadata**:
  ```go
  // tool.Name() == "update_task"
  // tool.DMOnly() == false
  ```

---

## Task 3: Реализовать tool_update_task.go

**Files:**
- Create: `internal/agent/tool_update_task.go`

- [ ] Структура `updateTaskTool{tasks model.TaskService; cfg dateparse.Config}`.

- [ ] `NewUpdateTaskTool(tasks model.TaskService, cfg dateparse.Config) Tool`.

- [ ] `Name()`, `Description()`, `DMOnly()`.

- [ ] `Parameters()` — JSON-схема: `task_id` (required), `summary`, `details`, `deadline`, `status`.

- [ ] `Execute(ctx, args)`:
  - Анмаршалить JSON
  - `task_id` пуст → ошибка
  - `GetTask(ctx, task_id)` → не найдена → ошибка
  - `buildUpdate(params, now)`:
    - `summary`: `ptr(val)` если `val != ""`; иначе `nil`
    - `details`: JSON-присутствие через `json.RawMessage`; если передан — `ptr(val)`; нет → `nil`
    - `deadline`: `"none"`/`""` → `ptr((*time.Time)(nil))`; иначе `dateparse.Parse(val, now, cfg)`; не передан → `nil`
    - `status`: `ptr(val)` если `val != ""`; иначе `nil`
  - `now` из `ctx.Value(nowKey).(time.Time)` (как в `tool_create_task.go`)
  - `TaskService.UpdateTask(ctx, task_id, update)`
  - JSON-ответ: `id`, `display_id`, `summary`, `details`, `deadline` (RFC3339 или ""), `status`

- [ ] `go build ./internal/agent/...` — OK.
- [ ] `go test ./internal/agent/...` — все 11 тестов зелёные.

---

## Task 4: Обновить системный промпт агента

**Files:**
- Modify: `internal/agent/prompts/agent_system.tmpl`

Добавить раздел после существующего абзаца про `list_tasks` (строка ~40):

- [ ] Добавить абзац об `update_task` в промпт:

  ```
  Изменение задачи (update_task):
  - update_task принимает только task_id — UUID из поля "id" в результате list_tasks.
  - Если пользователь называет задачу по части названия — используй list_tasks с параметром query для поиска нужного task_id.
  - Если пользователь называет задачу по номеру (например «inbox#5» или «задача #5 в Работе»):
    1. Если проект неизвестен — вызови list_projects и найди нужный проект.
    2. Вызови list_tasks с project_id этого проекта; найди задачу с нужным display_id в результате.
  - Если найдена ровно одна задача — вызывай update_task с её id.
  - Если найдено несколько — выведи первые 3 в виде пронумерованного списка (#display_id: summary) и спроси: «Уточни: 1, 2 или 3?». После ответа вызови update_task с нужным id.
  - Если задача не найдена — сообщи пользователю и не вызывай update_task.
  ```

- [ ] `go build ./...` — OK (промпт встраивается через embed).

---

## Task 5: Интегрировать в main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [ ] Добавить `agent.NewUpdateTaskTool(taskSvc, dateTimeCfg)` в `agentTools`
  после `agent.NewMoveTaskTool(taskSvc, projectSvc)`.

- [ ] `go build ./...` — OK.
- [ ] `go test ./...` — все тесты зелёные.

---

## Task 6: Финальная проверка

- [ ] `go test ./internal/agent/... -v -run TestUpdateTaskTool` — 11 тестов зелёные.
- [ ] `go test ./...` — полный прогон.
- [ ] `go vet ./...` — чисто.
- [ ] Переместить план в `docs/superpowers/plans/completed/`.

---

## Post-Completion

**Ручная проверка:**
- DM: «Поставь дедлайн на завтра задаче inbox#5» → агент вызывает `list_tasks` с `project_id` inbox, находит задачу, вызывает `update_task` с её UUID
- DM: «Измени задачу про отчёт в Работе» → агент вызывает `list_tasks` с query, при нескольких — предлагает 3 варианта
- Убедиться, что `update_task` без `task_id` сразу возвращает ошибку
