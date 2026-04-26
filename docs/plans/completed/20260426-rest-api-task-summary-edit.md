# REST API: редактирование summary задачи

## Overview

Через `PATCH /v1/tasks/{id}` нельзя поменять название (`summary`) задачи, хотя на нижних слоях (`model.TaskUpdate.Summary`, `taskService.UpdateTask`, push-шаблоны с `changedFields`, агентский tool `update_task`) поле уже полностью поддерживается. Нужно «прокинуть» `summary` через REST API: добавить его в OpenAPI-схему `UpdateTaskRequest`, разрешить в обработчике, валидировать аналогично `CreateTask` (trim, пустая строка → 422). Это убирает текущий разрыв между API и бэкенд-функциональностью; пользовательские клиенты (iOS/macOS и др.) смогут переименовывать задачи без обхода через агента.

## Context (from discovery)

- `api/openapi.yaml:979` — схема `UpdateTaskRequest` сейчас содержит только `details`, `status`, `deadline`. В описании буквально: "Supported fields: details, status, deadline".
- `internal/api/tasks.go:398` — белый список `allowedTaskUpdateFields` (`details`, `status`, `deadline`).
- `internal/api/tasks.go:406` — `buildTaskUpdate` собирает `model.TaskUpdate` из raw JSON; `summary` не разбирается.
- `internal/api/tasks.go:212` — образец валидации в `create`: `strings.TrimSpace`, пустая строка → 422 с кодом `ErrorCodeUnprocessable` и сообщением `"summary field is required"`.
- `internal/usecase/tasks.go:100` — `taskService.UpdateTask` уже сравнивает `*upd.Summary != original.Summary` и добавляет `"summary"` в `changedFields`. Хранилище (`SQLiteTaskStore.UpdateTaskTx`) и push-шаблоны (`task_updated`) тоже уже понимают `summary` — никаких изменений ниже API-слоя не требуется.
- `internal/api/tasks_test.go:366` — стиль тестов для PATCH: `TestUpdateTaskPatchesFields`, табличные кейсы. Туда же добавятся новые.

## Development Approach

- **testing approach**: TDD (тесты до реализации)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility (новое поле `summary` опционально; отсутствие поля → значение не меняется, как у `details`)

## Testing Strategy

- **unit tests**: расширяем `internal/api/tasks_test.go` табличными кейсами для PATCH с `summary` (успех; пустая после trim → 422; не-строка → 400; неизвестное поле всё ещё отвергается). Mock-сервис в существующих тестах уже умеет `UpdateTask` — менять его не нужно.
- **e2e tests**: в проекте нет UI-уровневых e2e-тестов; достаточно покрытия на уровне HTTP-обработчика через `httptest`.
- ручная проверка: `curl -X PATCH /v1/tasks/{id}` с `{"summary":"новое название"}` — отдельный пункт в Post-Completion.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

Минимальный API-патч: расширяем единственный белый список и парсер. Логика самого update'а уже на месте — мы только перестаём её скрывать на REST-границе.

1. В OpenAPI добавляем `summary` в `UpdateTaskRequest` и обновляем описание схемы.
2. В `internal/api/tasks.go`:
   - `allowedTaskUpdateFields["summary"] = true`;
   - в `buildTaskUpdate` парсим `summary` как строку, делаем `strings.TrimSpace`, при пустой строке возвращаем ошибку, которую обработчик `update` вернёт как **422** (не 400 — чтобы поведение совпадало с `create`).
3. Тесты в `internal/api/tasks_test.go`: дополняем `TestUpdateTaskPatchesFields` (или родственный) кейсами для `summary`.

Поскольку текущая ветка `buildTaskUpdate` сваливает все ошибки в 400, для пустого `summary` нужно отделить семантическую ошибку (поле известно, но значение невалидно → 422) от синтаксической (не-строка → 400). Простейший способ: вернуть `model.TaskUpdate{}, sentinelError` и в `update` при `errors.Is(err, errSummaryEmpty)` отдать 422; альтернатива — оставить парсер «синтаксическим», а проверку пустоты сделать в `update` после `buildTaskUpdate`. Выбираем **второй вариант**: меньше связности, обработчик уже держит HTTP-семантику.

## Technical Details

### Изменение схемы (OpenAPI)

```yaml
UpdateTaskRequest:
  type: object
  description: |
    Supported fields: summary, details, status, deadline. summary is trimmed;
    an empty string after trim is rejected with 422. deadline=null clears the
    due date; an absent field leaves the value unchanged.
  properties:
    summary:
      type: string
    details:
      type: string
    status:
      type: string
      enum: [open, done, cancelled]
    deadline:
      type: string
      format: date-time
      nullable: true
```

### Изменение в `buildTaskUpdate`

Добавить ветку до уже существующих:

```go
if v, ok := raw["summary"]; ok {
    var s string
    if err := json.Unmarshal(v, &s); err != nil {
        return model.TaskUpdate{}, errors.New("summary must be a string")
    }
    s = strings.TrimSpace(s)
    upd.Summary = &s
}
```

И `allowedTaskUpdateFields["summary"] = true`.

### Изменение в `update`-обработчике

После `buildTaskUpdate` (до вызова сервиса):

```go
if upd.Summary != nil && *upd.Summary == "" {
    WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "summary field is required")
    return
}
```

### Поведение пуш-уведомлений

Не меняется: `taskService.UpdateTask` уже добавляет `"summary"` в `changedFields`, шаблон `task_updated` уже его обрабатывает. Никаких новых событийных полей.

## What Goes Where

- **Implementation Steps**: правки `api/openapi.yaml`, `internal/api/tasks.go`, тесты `internal/api/tasks_test.go`.
- **Post-Completion**: ручной `curl`-прогон, обновление клиентов (iOS/macOS), которые ранее не знали про возможность.

## Implementation Steps

### Task 1: Тесты PATCH summary (TDD, красная фаза)

**Files:**
- Modify: `internal/api/tasks_test.go`

- [x] добавить новый `TestUpdateTaskPatchesSummary` с подтестами:
  - `plain summary` → 200, summary в ответе равен переданному
  - `summary trimmed` (`"  hello  "`) → 200, в ответе `"hello"`
  - `summary with details` → оба поля обновляются
  - `empty summary rejected` (`"   "`) → 422
  - `non-string summary rejected` (`42`) → 400
- [x] обновить кейс «patch unknown field» в `TestTasksValidationErrors`: заменить `summary` на `garbage`
- [x] запустить `go test ./internal/api/...` — новые кейсы падают (красная фаза подтверждена)

### Task 2: Реализация в API-слое

**Files:**
- Modify: `internal/api/tasks.go`

- [x] добавить `"summary": true` в `allowedTaskUpdateFields`
- [x] в `buildTaskUpdate` распарсить `summary` (json-unmarshal в `string`, `strings.TrimSpace`, присвоить `upd.Summary = &s`)
- [x] в обработчике `update` после `buildTaskUpdate` проверить `upd.Summary != nil && *upd.Summary == ""` → `WriteError(422, ErrorCodeUnprocessable, "summary field is required")`
- [x] `go test ./internal/api/... -count=1` — все кейсы проходят
- [x] `go vet ./...` — чисто

### Task 3: Обновление OpenAPI

**Files:**
- Modify: `api/openapi.yaml`

- [x] добавить `summary: { type: string }` в `properties` схемы `UpdateTaskRequest`
- [x] обновить `description` схемы: упомянуть `summary`, trim и 422 для пустой строки
- [x] `go test ./... -count=1` — весь проект зелёный
- [x] `api/spec.go` — это `go:embed openapi.yaml`, никаких литеральных перечислений; обновлять не нужно

### Task 4: Verify acceptance criteria

- [x] PATCH `/v1/tasks/{id}` с `{"summary":"новое имя"}` меняет название (покрыто `TestUpdateTaskPatchesSummary/plain_summary`)
- [x] PATCH с `{"summary":"   "}` → 422 (`empty_summary_rejected`)
- [x] PATCH с `{"summary":42}` → 400 (`non-string_summary_rejected`)
- [x] PATCH с `{"summary":"x","details":"y"}` → оба обновляются (`summary_with_details`)
- [x] прежние сценарии (`details`/`status`/`deadline`) — `TestUpdateTaskPatchesFields` зелёный
- [x] `go test ./...` зелёный
- [x] `go vet ./...` чистый

### Task 5: [Final] Документация

**Files:**
- Modify: `CLAUDE.md` — пропущено (PATCH-поля в нём явно не перечислены; правда живёт в `api/openapi.yaml`)

- [x] проверить `CLAUDE.md` — упоминаний PATCH-полей нет, обновлять нечего
- [x] переместить план в `docs/plans/completed/`

## Post-Completion

**Manual verification**:

```sh
# Получить bearer-токен и id задачи, затем:
curl -X PATCH "$BASE/v1/tasks/$TASK_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"summary":"новое название"}'
# ожидаем 200 и обновлённый объект Task

curl -X PATCH "$BASE/v1/tasks/$TASK_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"summary":"   "}'
# ожидаем 422 с error.code = "unprocessable"
```

**External system updates**:

- iOS/macOS-клиенты (если потребляют OpenAPI и ограничивают редактируемые поля на UI-уровне) — добавить редактирование названия в UI и в client-side payload.
- SSE-подписчики получат событие `task_updated` с `changedFields: ["summary", ...]` без изменений в подписке — действий не требуется.
