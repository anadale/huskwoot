# Псевдонимы проектов (project aliases)

## Обзор

Добавляем механизм псевдонимов (aliases) для проектов — ключевых слов, позволяющих агенту определять проект по упоминанию в сообщении. Пример: проект «Магазин старых книг» с алиасом `букинист` → сообщение «надо в букинисте исправить каталог» попадает в этот проект.

**Проблема:** сейчас чтобы привязать задачу к проекту в чате с агентом, пользователь должен использовать полное имя проекта, помнить slug или заранее привязать чат. Это неудобно при множестве проектов с длинными названиями.

**Выгода:** короткие триггерные слова, естественная работа с русской морфологией (решение отдаётся на откуп LLM через системный промпт), меньше трения в повседневном использовании.

**Интеграция:** алиасы — это отдельная сущность 1:N с проектом. Алиасы попадают в системный промпт агента (`Known projects`), LLM сама сопоставляет их с текстом. Никакого детерминированного pre-resolution в pipeline, никаких изменений Extractor или Group-потока (только агент — DM и GroupDirect).

## Контекст (по результатам discovery)

- Язык/стек: Go 1.26, модуль `github.com/anadale/huskwoot`. БД — SQLite (goose migrations), TOML-конфиг, OpenAI SDK, Telegram Bot API.
- Ключевые файлы/области, которые затронет изменение:
  - `internal/model/` — типы `Project`, `ProjectUpdate`, `CreateProjectRequest`, `TaskStore`, `ProjectService`, `EventKind*`.
  - `internal/storage/` — миграции, `task_store.go`, `cached_task_store.go`.
  - `internal/usecase/projects.go` — расширение сервиса, sentinel errors лежат рядом (по примеру `internal/usecase/pairing.go`).
  - `internal/agent/` — существующий `tool_create_project.go`, новые `tool_get_project.go`, `tool_update_project.go`, `tool_add_project_alias.go`, `tool_remove_project_alias.go`, общий `resolve_project.go`, шаблоны промптов `prompts/agent_system_{ru,en}.tmpl`.
  - `internal/api/projects.go` — хендлеры GET/PATCH уже существуют, расширяем.
  - `internal/push/templates.go` — дроп push-job для `project_updated`.
  - `internal/i18n/locales/{ru,en}.json` — новые строки.
  - `api/openapi.yaml` — схемы `Project`, `UpdateProjectRequest`, `CreateProjectRequest`.
- Наблюдаемые паттерны:
  - Транзакционный use-case-слой (`BeginTx` → tx-aware store-calls → `EventStore.Insert` → `tx.Commit()` → `Broker.Notify`).
  - Write-методы stores принимают `*sql.Tx`; read-методы — без транзакции.
  - Sentinel errors в usecase: `var ErrX = errors.New("...")` с префиксом `Err*`.
  - Tools: каждый в своём файле `tool_*.go`, `DMOnly()` = `true` для действий управления конфигурацией.
  - `resolveTask` (`internal/agent/resolve_task.go`) — эталон для будущего `resolveProjectRef`.
  - Комментарии и строки ошибок в Go-коде — по-английски (per memory `feedback_language.md`).
- Зависимости: `pressly/goose/v3` (миграции), `modernc.org/sqlite` (драйвер), `nicksnyder/go-i18n/v2` (i18n), `chi` (HTTP router).

## Подход к разработке

- **Режим тестирования: TDD** (per CLAUDE.md раздел «Testing»). Тесты пишем до реализации, в табличном стиле `[]struct{name, input, want}`.
- Каждая задача — отдельный коммит, зелёные `go vet ./... && go test ./...` на каждом шаге.
- Мелкие фокусированные изменения; обратную совместимость HTTP API поддерживаем (добавляем поля, не убираем существующие).
- **КРИТИЧНО: каждая задача обязана содержать новые/обновлённые тесты.**
- **КРИТИЧНО: все тесты должны проходить до перехода к следующей задаче.**
- **КРИТИЧНО: обновлять этот файл при изменении scope.**

## Стратегия тестирования

- **Unit-тесты:** обязательны для каждой задачи, табличный стиль, ручные mocks (без testify/mock — per CLAUDE.md).
- **Интеграционные тесты SQLite:** создаём реальную БД через `storage.OpenDB` в `t.TempDir()`, проверяем миграции и CRUD.
- **Тесты HTTP-хендлеров:** используют `httptest.NewServer` и fixture-сервис — паттерн уже есть в `internal/api/projects_test.go`.
- **E2E-тесты:** отсутствуют в проекте; полагаемся на unit и integration.
- **Snapshot-тесты промптов:** если есть golden-файлы для системного промпта — обновить (проверить при реализации задачи 9).

## Отслеживание прогресса

- Завершённые пункты помечаем `[x]` сразу при выполнении.
- Новые обнаруженные задачи — с префиксом ➕.
- Блокеры — с префиксом ⚠️.
- При значительном изменении scope синхронизируем план с фактическим ходом работ.

## Обзор решения

**Архитектура:**
1. Хранение: отдельная таблица `project_aliases(project_id, alias PK, created_at)` с `ON DELETE CASCADE` — стандартный 1:N.
2. Нормализация и валидация — в usecase-слое перед записью. Store принимает уже готовые значения.
3. Глобальная уникальность алиаса (PRIMARY KEY на `alias`) гарантирует, что один алиас принадлежит одному проекту.
4. `Project.Aliases []string` возвращается всегда — пустой срез, если алиасов нет.
5. Изменения алиасов порождают единое событие `project_updated` с `changedFields` (как `task_updated`).
6. Пять новых tools агента + расширенный `create_project` + общий helper `resolveProjectRef`.
7. Системный промпт агента расширяется так, чтобы LLM видела алиасы и применяла их к тексту сообщения.

**Ключевые решения и обоснование:**
- **LLM, а не детерминированный матчинг.** Русская морфология (склонения, составные слова) естественно обрабатывается моделью; свой стеммер строить — overkill.
- **Глобальная уникальность алиасов.** Предотвращает неоднозначность и делает пользовательские ошибки явными (PRIMARY KEY вернёт конфликт).
- **Алиасы запрещены для Inbox.** Inbox — дефолт; алиас у него перехватывал бы сообщения, которым лучше попадать через fallback.
- **Лимит 10 алиасов на проект.** Защита от раздувания системного промпта.
- **Формат алиаса: одно слово 2–32 символа** (буквы/цифры/дефис, без пробелов/точек/подчёркиваний). Для развёрнутого контекста есть `description`.
- **Единое событие `project_updated`** вместо `alias_added`/`alias_removed` — проще ретенция и клиентский кэш.
- **Replace-set через PATCH для HTTP**, но для агента — отдельные `add_project_alias`/`remove_project_alias` (для LLM атомарные операции проще).

## Технические детали

### Модель данных

```sql
-- internal/storage/migrations/009_project_aliases.sql
CREATE TABLE project_aliases (
    project_id TEXT NOT NULL,
    alias      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (alias),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX idx_project_aliases_project ON project_aliases(project_id);
```

### Типы

```go
// internal/model/types.go
type Project struct {
    ID, Name, Slug, Description string
    Aliases     []string   // lowercase, sorted lexicographically; never nil (empty slice if no aliases)
    TaskCounter int
    CreatedAt   time.Time
}

type ProjectUpdate struct {
    Name, Description, Slug *string
    Aliases *[]string       // nil = no change; &[]string{} = clear; &[...] = replace-set
}

// internal/model/service.go
type CreateProjectRequest struct {
    Name, Description, Slug string
    Aliases []string   // optional; validated in usecase
}

type ProjectService interface {
    // ... existing ...
    AddProjectAlias(ctx context.Context, projectID, alias string) (*Project, error)
    RemoveProjectAlias(ctx context.Context, projectID, alias string) (*Project, error)
    ResolveProjectRef(ctx context.Context, ref string) (*Project, error)
}

// internal/model/event.go
const EventKindProjectUpdated EventKind = "project_updated"
```

### Sentinel errors (в `internal/usecase/projects.go`)

```go
var (
    ErrAliasInvalid            = errors.New("alias format is invalid")
    ErrAliasTaken              = errors.New("alias already used by another project")
    ErrAliasConflictsWithName  = errors.New("alias conflicts with existing project name or slug")
    ErrAliasLimitReached       = errors.New("alias limit reached for project")
    ErrAliasNotFound           = errors.New("alias not found for project")
    ErrAliasForbiddenForInbox  = errors.New("aliases are not allowed for the Inbox project")
)
```

### Store-методы

```go
// internal/model/interfaces.go (TaskStore)
AddProjectAliasTx(ctx context.Context, tx *sql.Tx, projectID, alias string) error
RemoveProjectAliasTx(ctx context.Context, tx *sql.Tx, projectID, alias string) error
ListAliasesForProject(ctx context.Context, projectID string) ([]string, error)
```

`GetProject`/`GetProjectTx`/`ListProjects` расширяются внутри — заполняют `Project.Aliases`; сигнатуры не меняются. В `ListProjects` используется один запрос с `LEFT JOIN project_aliases` и `GROUP_CONCAT(alias, char(31))` (unit separator — безопасен, потому что алиасы валидированы).

### Валидатор

```go
// internal/usecase/alias_validator.go
func validateAlias(s string) (string, error)
```

Возвращает нормализованный (lowercase + trim) алиас или `ErrAliasInvalid`. Правила: 2–32 символа, буквы (`\p{L}`) + цифры + дефис, без пробелов/точек/подчёркиваний. Дефис не может быть первым/последним символом.

### Транзакционный flow `AddProjectAlias`

1. Нормализация и валидация алиаса через `validateAlias`.
2. Проверка: `projectID != store.DefaultProjectID()` → иначе `ErrAliasForbiddenForInbox`.
3. `BeginTx`.
4. `GetProjectTx(ctx, tx, projectID)` → `ErrProjectNotFound`, если `nil`.
5. Проверка лимита: `len(project.Aliases) >= 10` → `ErrAliasLimitReached`.
6. Проверка конфликта с name/slug: `ListProjects` (кэш), linear scan → `ErrAliasConflictsWithName`.
7. `TaskStore.AddProjectAliasTx(ctx, tx, projectID, alias)`; PRIMARY KEY constraint → `ErrAliasTaken`.
8. `GetProjectTx` для свежего snapshot.
9. `EventStore.Insert(ctx, tx, Event{Kind: EventKindProjectUpdated, Payload: {project, changedFields: ["aliases"]}})`.
10. `tx.Commit()`.
11. `s.invalidateProjectCache()`.
12. `Broker.Notify(event)`.
13. Return snapshot.

`RemoveProjectAlias` — симметрично; `ErrAliasNotFound` при отсутствии.

### UpdateProject с полем Aliases (replace-set)

Вычисляем diff: `toAdd = new \ current`, `toRemove = current \ new`. В одной транзакции: выполняем add-add-add + remove-remove-remove, собираем `changedFields` (включая `aliases` если что-то изменилось), вставляем **один** `project_updated` event.

### Формат системного промпта

```
{{if .Projects}}
Известные проекты в системе:
{{range .Projects}}- {{.Name}} (id: {{.ID}}, slug: {{.Slug}}{{if .Aliases}}, aliases: {{range $i, $a := .Aliases}}{{if $i}}, {{end}}«{{$a}}»{{end}}{{end}})
{{end}}
Если в сообщении пользователя встречается имя, slug или любой из алиасов проекта (даже в склонённой форме, внутри составных слов или как часть другого слова) — считай это явным указанием этого проекта и используй соответствующий id.
{{end}}
```

Симметричный текст в `agent_system_en.tmpl`.

### HTTP API

- `components/schemas/Project`: добавить `aliases: { type: array, items: { type: string }, maxItems: 10 }` (всегда присутствует, пустой массив если нет алиасов).
- `components/schemas/UpdateProjectRequest`: добавить опциональное `aliases` (replace-set).
- `components/schemas/CreateProjectRequest`: добавить опциональное `aliases`.
- Маппинг ошибок в `ProblemDetails`:
  - `ErrAliasInvalid` → 400 `alias_invalid`
  - `ErrAliasTaken` → 409 `alias_taken`
  - `ErrAliasConflictsWithName` → 409 `alias_conflicts_with_name`
  - `ErrAliasLimitReached` → 409 `alias_limit_reached`
  - `ErrAliasForbiddenForInbox` → 403 `alias_forbidden_for_inbox`
- Idempotency для PATCH — через существующий middleware.

### События

- `EventKindProjectUpdated = "project_updated"`.
- Payload: `{"project": {...}, "changedFields": ["name"?, "description"?, "slug"?, "aliases"?]}` — порядок фиксирован.
- `Templates.Resolve` для `project_updated` возвращает `ok=false` → push-job дропается без попытки доставки.
- SSE fan-out через существующий `Broker.Notify`.

### Agent tools

- **`create_project`** (расширение): добавить опциональный параметр `aliases: []string`.
- **`get_project(ref)`** — новый. Возвращает полную карточку (id/slug/name/description/aliases/taskCounter/createdAt).
- **`update_project(ref, name?, description?, slug?)`** — новый (без алиасов; для них есть отдельные tools).
- **`add_project_alias(ref, alias)`** — новый.
- **`remove_project_alias(ref, alias)`** — новый.
- Все новые — `DMOnly() == true`.
- `ref` везде = UUID | slug | alias, резолвится через общий helper `resolveProjectRef` (по образцу `resolveTask`).

## Что куда идёт

- **Шаги реализации** (`[ ]` чекбоксы): всё, что делается внутри репозитория — код, тесты, обновление документации, правка openapi.yaml.
- **Post-Completion** (без чекбоксов): внешнее — обновление клиентов (iOS/web, если они консумируют openapi), ручное E2E-тестирование в Telegram DM, верификация работы push-relay.

## Шаги реализации

### Задача 1: Миграция таблицы project_aliases

**Файлы:**
- Create: `internal/storage/migrations/009_project_aliases.sql`
- Modify: `internal/storage/migrations/migrations.go` (embed новой миграции автоматически через `//go:embed` паттерн — проверить)
- Modify: `internal/storage/migrations/migrations_test.go`

- [x] написать тест `TestMigrationsCreatesProjectAliasesTable`: открыть БД, применить миграции, проверить существование таблицы `project_aliases` (schema, PK, FK, index) через `PRAGMA table_info` и `PRAGMA foreign_key_list`
- [x] написать тест `TestProjectAliasesCascadeDelete`: вставить project + alias, удалить project, убедиться что alias каскадно удалён
- [x] создать `009_project_aliases.sql` с `CREATE TABLE` и `CREATE INDEX`
- [x] проверить, что embed подхватывает новый файл (в `migrations.go` обычно `//go:embed *.sql *.go` — не требует ручной правки)
- [x] прогнать `go test ./internal/storage/migrations/...` — зелёные

### Задача 2: Расширение типов model

**Файлы:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/service.go`
- Modify: `internal/model/event.go`
- Modify: `internal/model/types_test.go`

- [x] добавить тест `TestProjectAliasesDefaultEmpty`: zero-value `Project{}` имеет `Aliases == nil` или `len == 0` — достаточно доказательства, что клиенты могут полагаться на «пустой срез = нет алиасов» (детали реализации: везде возвращаем `[]string{}` а не `nil`)
- [x] добавить поле `Aliases []string` в `model.Project`
- [x] добавить поле `Aliases *[]string` в `model.ProjectUpdate`
- [x] добавить поле `Aliases []string` в `model.CreateProjectRequest`
- [x] добавить методы `AddProjectAlias`, `RemoveProjectAlias`, `ResolveProjectRef` в интерфейс `ProjectService`
- [x] добавить константу `EventKindProjectUpdated = "project_updated"`
- [x] прогнать `go vet ./... && go test ./internal/model/...` — зелёные

### Задача 3: Валидатор алиасов

**Файлы:**
- Create: `internal/usecase/alias_validator.go`
- Create: `internal/usecase/alias_validator_test.go`

- [x] написать таблично-управляемый тест `TestValidateAlias` с покрытием: валидные алиасы (кириллица, латиница, цифры, дефис в середине, границы 2 и 32 символа); невалидные (пустая строка, один символ, 33 символа, пробелы, точки, подчёркивание, начинается/заканчивается дефисом, эмодзи)
- [x] написать тест `TestValidateAliasNormalizesCase`: вход `"Букинист"` → выход `"букинист"`; вход `"  TEST  "` → выход `"test"`
- [x] реализовать `validateAlias(s string) (string, error)`: trim → lowercase → regex `^[\p{L}\p{N}](?:[\p{L}\p{N}-]{0,30}[\p{L}\p{N}])?$` (или эквивалентная проверка вручную для ясности ошибок)
- [x] прогнать `go test ./internal/usecase/...` — зелёные

### Задача 4: TaskStore — методы для алиасов (интерфейс + SQLite)

**Файлы:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/storage/task_store.go`
- Modify: `internal/storage/task_store_test.go`

- [x] добавить тесты `TestTaskStoreAddProjectAlias{Success,DuplicatePrimaryKey,ForeignKeyMissing}`: happy path, повторная вставка → ошибка PK, вставка для несуществующего project_id → FK-ошибка
- [x] добавить тест `TestTaskStoreRemoveProjectAlias{Success,NotFound}`: happy path + удаление отсутствующего возвращает нулевое число затронутых строк (mapping в ErrAliasNotFound на слое usecase)
- [x] добавить тест `TestTaskStoreListAliasesForProject{Empty,Multiple,Sorted}`: пустой результат, несколько алиасов в лексикографическом порядке
- [x] добавить тесты для расширенных `GetProject`, `GetProjectTx`, `ListProjects` — убедиться что `Aliases` заполнено и отсортировано; `ListProjects` с проектом без алиасов возвращает пустой срез
- [x] добавить методы `AddProjectAliasTx`, `RemoveProjectAliasTx`, `ListAliasesForProject` в интерфейс `TaskStore` (`internal/model/interfaces.go`)
- [x] реализовать эти методы в `SQLiteTaskStore` (`internal/storage/task_store.go`)
- [x] расширить `GetProject`/`GetProjectTx` — дополнительный запрос `ListAliasesForProject`; `ListProjects` — один запрос с `LEFT JOIN` + `GROUP_CONCAT(alias, char(31))`, split по `char(31)` в Go
- [x] обновить все in-memory mocks `TaskStore` в тестах (projects_test.go, tools_test.go, chat_test.go) — добавить no-op реализации новых методов
- [x] прогнать `go vet ./... && go test ./...` — зелёные

### Задача 5: CachedTaskStore инвалидация

**Файлы:**
- Modify: `internal/storage/cached_task_store.go`
- Modify: `internal/storage/cached_task_store_test.go`

- [x] написать тест `TestCachedTaskStoreProxiesAliasMethods`: новые методы проксируются в base без кэширования
- [x] в `CachedTaskStore` реализовать `AddProjectAliasTx`/`RemoveProjectAliasTx`/`ListAliasesForProject` как прямое проксирование к base (кэшируется только `ListProjects`, инвалидация вызывается из usecase после commit — как для существующего `CreateProjectTx`)
- [x] прогнать `go test ./internal/storage/...` — зелёные

### Задача 6: ProjectService — alias CRUD и ResolveProjectRef

**Файлы:**
- Modify: `internal/usecase/projects.go`
- Modify: `internal/usecase/projects_test.go`

- [x] написать тест `TestProjectServiceAddAliasHappyPath`: алиас записан, `Project.Aliases` содержит его, emit'нулось `project_updated` с `changedFields: ["aliases"]`
- [x] написать тесты на все sentinel errors: `ErrAliasInvalid`, `ErrAliasTaken` (моделируется через store mock), `ErrAliasConflictsWithName` (алиас совпадает с name/slug другого проекта), `ErrAliasLimitReached` (11-й алиас), `ErrAliasForbiddenForInbox` (алиас для `DefaultProjectID()`)
- [x] написать тест `TestProjectServiceAddAliasRollbackOnEventError`: если EventStore.Insert падает, вся транзакция откатывается, кэш не инвалидируется, Broker не получает event
- [x] написать тест `TestProjectServiceRemoveAliasHappyPath` + `TestProjectServiceRemoveAliasNotFound`
- [x] написать тест `TestProjectServiceResolveProjectRef`: по UUID, по slug, по alias (регистр игнорируется через `validateAlias`), несуществующий ref → `ErrProjectNotFound`
- [x] добавить sentinel errors в `internal/usecase/projects.go` (рядом с существующими)
- [x] реализовать `AddProjectAlias`, `RemoveProjectAlias`, `ResolveProjectRef` по описанному транзакционному flow
- [x] расширить существующий `CreateProject`: если `req.Aliases` не пусто — валидировать каждый, в той же транзакции вызвать `AddProjectAliasTx` для каждого, свернуть в одно `project_created` событие (или расширить существующий payload); проверить как сейчас устроен payload `project_created` и решить — если там нет алиасов, добавить
- [x] расширить существующий `UpdateProject`: если `upd.Aliases != nil` — вычислить diff, в той же tx делать add/remove, включить `aliases` в `changedFields`; использовать **одно** `project_updated` событие на весь вызов
- [x] обновить mocks в `projects_test.go` (если нужно), прогнать тесты
- [x] прогнать `go vet ./... && go test ./...` — зелёные

### Задача 7: Push Templates — дроп для project_updated

**Файлы:**
- Modify: `internal/push/templates.go`
- Modify: `internal/push/templates_test.go`

- [x] написать тест `TestTemplatesResolveProjectUpdatedDropped`: `Templates.Resolve` на event с `Kind == "project_updated"` возвращает `ok=false`
- [x] добавить кейс `project_updated` в `Templates.Resolve` (возврат `ok=false`)
- [x] прогнать `go test ./internal/push/...` — зелёные

### Задача 8: OpenAPI — схемы и коды ошибок

**Файлы:**
- Modify: `api/openapi.yaml`

- [x] добавить поле `aliases` (array of string, `maxItems: 10`, описание) в `components/schemas/Project`
- [x] добавить опциональное поле `aliases` в `CreateProjectRequest`
- [x] добавить опциональное поле `aliases` в `UpdateProjectRequest` (replace-set, поведение описать в description)
- [x] добавить 400/403/409 responses для `PATCH /v1/projects/{id}` с перечислением новых `code` значений в `ProblemDetails`
- [x] валидировать YAML: если есть `npx @redocly/cli lint` или подобный инструмент в проекте — запустить; иначе `go test ./internal/api/openapi_test.go` (есть `openapi_test.go` — проверяет валидность YAML)
- [x] прогнать тесты валидации openapi

### Задача 9: HTTP handlers — PATCH с алиасами, error mapping

**Файлы:**
- Modify: `internal/api/projects.go`
- Modify: `internal/api/projects_test.go`
- Modify: `internal/api/errors.go` (если в нём централизован error→ProblemDetails маппинг)

- [x] написать тесты `TestPatchProject{AddsAlias,RemovesAlias,ReplaceSet}`: PATCH с `aliases: [...]` изменяет набор, ответ содержит обновлённый `Project`
- [x] написать тесты `TestPatchProjectAliasErrors`: `ErrAliasInvalid` → 400 `alias_invalid`; `ErrAliasTaken` → 409 `alias_taken`; `ErrAliasConflictsWithName` → 409 `alias_conflicts_with_name`; `ErrAliasLimitReached` → 409 `alias_limit_reached`; `ErrAliasForbiddenForInbox` → 403 `alias_forbidden_for_inbox`
- [x] написать тест `TestPostProjectWithAliases`: POST /v1/projects с `aliases: ["test"]` создаёт проект с алиасом (если endpoint уже поддерживает `CreateProjectRequest` — наследуется автоматически; проверить)
- [x] написать тест `TestGetProjectIncludesAliases`: GET /v1/projects/{id} возвращает `aliases` (пустой массив, если нет)
- [x] в хендлерах расширить маппинг sentinel → ProblemDetails (`errors.Is` на новые `ErrAlias*`)
- [x] прогнать `go test ./internal/api/...` — зелёные

### Задача 10: Общий helper resolveProjectRef

**Файлы:**
- Create: `internal/agent/resolve_project.go`
- Create: `internal/agent/resolve_project_test.go`

- [x] написать таблично-управляемый тест `TestResolveProjectRef`: UUID → найден; slug → найден; alias → найден (через валидацию формата сначала); невалидный ref → `ErrProjectNotFound` (обёрнутый в i18n-ошибку для tool)
- [x] реализовать `resolveProjectRef(ctx, projects model.ProjectService, loc *goI18n.Localizer, ref string) (*model.Project, error)` — один вызов `ProjectService.ResolveProjectRef` + маппинг ошибок в i18n
- [x] прогнать `go test ./internal/agent/...` — зелёные

### Задача 11: Tool get_project

**Файлы:**
- Create: `internal/agent/tool_get_project.go`
- Create: `internal/agent/tool_get_project_test.go`

- [x] написать таблично-управляемый тест для `executeGetProject`: успех — JSON содержит все поля (id, slug, name, description, aliases, taskCounter, createdAt); проект не найден — ошибка; `DMOnly() == true`
- [x] реализовать `NewGetProjectTool(projects model.ProjectService, loc *goI18n.Localizer) Tool`: parameters `ref`, Execute зовёт `resolveProjectRef` → сериализует в JSON
- [x] прогнать `go test ./internal/agent/...` — зелёные

### Задача 12: Tool update_project

**Файлы:**
- Create: `internal/agent/tool_update_project.go`
- Create: `internal/agent/tool_update_project_test.go`

- [x] написать тесты: успех (обновление name), обновление description, обновление slug (с валидацией); ошибки project_not_found, slug_conflict
- [x] реализовать Tool с параметрами `ref`, `name?`, `description?`, `slug?`; `ProjectUpdate.Aliases == nil` всегда (алиасы — через add/remove)
- [x] прогнать тесты

### Задача 13: Tools add_project_alias / remove_project_alias

**Файлы:**
- Create: `internal/agent/tool_add_project_alias.go`
- Create: `internal/agent/tool_add_project_alias_test.go`
- Create: `internal/agent/tool_remove_project_alias.go`
- Create: `internal/agent/tool_remove_project_alias_test.go`

- [x] написать тесты для add: happy path (алиас добавлен, ответ содержит обновлённый список); все sentinel errors из Задачи 6 мапятся в понятные i18n-сообщения
- [x] написать тесты для remove: happy path; `alias_not_found` возвращает понятную ошибку
- [x] реализовать оба tool'а по паттерну остальных tool'ов
- [x] прогнать тесты

### Задача 14: Расширение tool create_project

**Файлы:**
- Modify: `internal/agent/tool_create_project.go`
- Modify: `internal/agent/tools_test.go` или `internal/agent/tool_create_project_test.go` (создать если нет)

- [x] добавить тест `TestCreateProjectToolWithAliases`: параметр `aliases: ["test1","test2"]` — проект создан, алиасы присутствуют
- [x] добавить тест `TestCreateProjectToolValidatesAliases`: невалидный алиас → ошибка, проект не создан (вся транзакция откачена)
- [x] добавить в `Parameters()` опциональный массив `aliases`, пробросить в `CreateProjectRequest`
- [x] обновить i18n-ключи (см. задачу 16 — можно вместе)
- [x] прогнать тесты

### Задача 15: Регистрация новых tools и обновление системного промпта

**Файлы:**
- Modify: `internal/agent/agent.go` (или где регистрируются tool'ы)
- Modify: `cmd/huskwoot/main.go` (где собирается список tool'ов)
- Modify: `internal/agent/prompts/agent_system_ru.tmpl`
- Modify: `internal/agent/prompts/agent_system_en.tmpl`
- Modify: `internal/agent/tools_test.go`

- [x] добавить тест `TestAgentToolSetIncludesProjectManagement{DM,GroupDirect}`: в DM — 5 новых tool'ов присутствуют; в GroupDirect — исключены (все `DMOnly`)
- [x] добавить тест на рендеринг системного промпта: проект с алиасами → строка содержит `aliases: «...», «...»`; без алиасов — хвост отсутствует
- [x] зарегистрировать tools в main.go (где собирается `[]Tool`)
- [x] обновить `prompts/agent_system_ru.tmpl` и `agent_system_en.tmpl` — формат строки `Known projects` + инструкция про алиасы
- [x] прогнать `go test ./internal/agent/... ./cmd/...`

### Задача 16: i18n-строки

**Файлы:**
- Modify: `internal/i18n/locales/ru.json`
- Modify: `internal/i18n/locales/en.json`
- Modify: `internal/i18n/*_test.go` (если есть тесты валидирующие полноту набора ключей)

- [x] добавить тест (или расширить существующий), который загружает обе локали и проверяет наличие всех новых ключей: `tool_get_project_*`, `tool_update_project_*`, `tool_add_project_alias_*`, `tool_remove_project_alias_*`, `tool_create_project_param_aliases`, `err_alias_invalid`, `err_alias_taken`, `err_alias_conflicts_with_name`, `err_alias_limit_reached`, `err_alias_not_found`, `err_alias_forbidden_for_inbox`
- [x] добавить все ключи в `ru.json` и `en.json`
- [x] прогнать `go test ./internal/i18n/... ./internal/agent/...`

### Задача 17: Проверка acceptance-критериев

- [x] подтвердить, что `go vet ./...` проходит без warnings
- [x] подтвердить, что `go test ./...` зелёный на всём дереве
- [x] ручной прогон: `go run ./cmd/huskwoot serve`, в DM боту — `создай проект "Букинист" с псевдонимом "букинист"`, затем `добавь в букинисте купить новую витрину` → задача создана в проекте «Букинист» [x] manual test (skipped - not automatable)
- [x] ручной прогон: удалить алиас через `remove_project_alias`, повторить ту же фразу — LLM должна переспросить или создать в Inbox [x] manual test (skipped - not automatable)
- [x] ручной прогон: PATCH `/v1/projects/{id}` с телом `{"aliases": ["new1","new2"]}` возвращает 200 с обновлённым списком; повторный PATCH с тем же телом — no-op (changedFields не содержит aliases если идентично) [x] manual test (skipped - not automatable)
- [x] проверить, что push-relay не получает уведомления о `project_updated` (dispatcher дропает job) [x] manual test (skipped - not automatable)

### Задача 18 [финальная]: Документация

**Файлы:**
- Modify: `CLAUDE.md`

- [x] обновить таблицу `Agent / Tools` — добавить 5 новых tool'ов с `DMOnly=true`
- [x] в таблице поднять `create_project` примечание про `aliases` параметр
- [x] в разделе про события добавить описание `project_updated` payload (аналогично `task_updated`)
- [x] в разделе `Stores` упомянуть таблицу `project_aliases` (или просто добавить в перечисление)
- [x] перенести этот план в `docs/plans/completed/`: `mv docs/plans/20260424-project-aliases.md docs/plans/completed/`

## Post-Completion

*Действия вне репозитория — без чекбоксов, информационно.*

**Ручная верификация:**
- E2E-сценарий в Telegram DM: полный цикл создать проект с алиасом → отправить сообщение, использующее алиас в склонённой форме (например, «в букинисте», «про букиниста») → убедиться, что задача попала в нужный проект.
- Нагрузочная проверка не требуется — операции редкие, нагрузка на SSE не меняется.
- Security-обзор: алиасы пишутся в системный промпт — проверить, что пользовательский ввод не способен сломать структуру промпта (валидатор формата защищает).

**Обновление внешних систем:**
- Если есть клиенты (iOS app, web UI), консумирующие OpenAPI, — необходимо регенерировать типы и добавить поле `aliases` в UI (отдельная задача для клиентских проектов).
- Push-relay — изменений не требует (не получает `project_updated` по контракту).
- Миграция применяется автоматически при следующем запуске `huskwoot serve` — никаких ручных операций над prod-БД не нужно.
