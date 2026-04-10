# Task Struct Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Убрать вложенную структуру `Origin` из `Task`, перенести поля `Project`, `Topic`, `Details` (бывший `Context`) напрямую на `Task`; переименовать `OriginMessage` → `SourceMessage` в `Task` и `Command`.

**Architecture:** Сугубо механический рефакторинг без изменения логики. `model/types.go` — точка входа: после изменения компилятор укажет все затронутые места. Изменения в production-коде, затем в тестах, затем в документации.

**Tech Stack:** Go 1.26, `go build ./...` и `go test ./...` как проверочные команды.

---

## Карта файлов

| Файл | Что меняется |
|---|---|
| `internal/model/types.go` | Удалить `Origin`, обновить `Task` и `Command` |
| `internal/ai/extractor.go` | `Origin{...}` → плоские поля; `OriginMessage` → `SourceMessage` |
| `internal/ai/command_extractor.go` | `OriginMessage` → `SourceMessage` |
| `internal/handler/set_project.go` | `cmd.OriginMessage` → `cmd.SourceMessage` |
| `internal/pipeline/pipeline.go` | Убрать `Origin.Subject =`; `Origin.Account/Topic` → `Project/Topic` |
| `internal/sink/super_productivity.go` | `Origin.Account` → `Project`; `Origin.Context` → `Details` |
| `internal/sink/telegram_notifier.go` | `Origin.Subject/Account/Context` → `SourceMessage.Subject/Project/Details` |
| `internal/sink/obsidian.go` | Те же замены + `Origin.Topic` → `Topic` |
| `internal/ai/extractor_test.go` | `OriginMessage` → `SourceMessage`; `Origin.Account` → `Project` |
| `internal/ai/command_extractor_test.go` | `cmd.OriginMessage` → `cmd.SourceMessage` |
| `internal/handler/set_project_test.go` | `OriginMessage` → `SourceMessage` |
| `internal/pipeline/pipeline_test.go` | `mockExtractor` + все `Origin.*` → плоские поля |
| `internal/sink/super_productivity_test.go` | `Origin.{Account,Context}` → `Project/Details` |
| `internal/sink/telegram_notifier_test.go` | `OriginMessage` → `SourceMessage`; `Origin.*` → плоские поля |
| `internal/sink/obsidian_test.go` | `Origin.{Subject,Account,Topic,Context}` → плоские поля |
| `CLAUDE.md` | Обновить раздел «Обогащение контекста задач» |

---

### Task 1: model/types.go — удалить Origin, обновить Task и Command

**Files:**
- Modify: `internal/model/types.go`

- [ ] **Step 1: Убрать структуру Origin и обновить Task и Command**

Заменить весь блок начиная с комментария `// Origin описывает метаданные...` и далее:

```go
// Task описывает задачу, извлечённую из обещания пользователя.
type Task struct {
	// ID — уникальный идентификатор задачи.
	ID string
	// Summary — краткое описание того, что нужно сделать.
	Summary string
	// Details — контекст или детали задачи (заполняется экстрактором).
	Details string
	// Project — название проекта (из MetaStore или текста сообщения).
	Project string
	// Topic — тематическая группа задачи (например, «Деплой», «Клиент iOS»).
	// Заполняется экстрактором; очищается для групповых чатов в pipeline.
	Topic string
	// Deadline — срок выполнения задачи (nil если не указан).
	Deadline *time.Time
	// Confidence — уверенность модели в извлечённой задаче (0.0–1.0).
	Confidence float64
	// Source — технический идентификатор канала-источника.
	Source Source
	// SourceMessage — исходное сообщение с обещанием.
	// Содержит Subject (тема письма для IMAP) и коллбэки ReactFn/ReplyFn.
	SourceMessage Message
	// CreatedAt — время создания записи о задаче.
	CreatedAt time.Time
}
```

И структуру `Command` обновить:

```go
// Command описывает конфигурационную команду, извлечённую из сообщения.
type Command struct {
	// Type — тип команды (например, "set_project_name").
	Type string
	// Payload — параметры команды.
	Payload map[string]string
	// Source — источник, из которого получена команда.
	Source Source
	// SourceMessage — исходное сообщение с командой.
	SourceMessage Message
}
```

Структуру `Origin` и поля `OriginMessage` в старых `Task`/`Command` — удалить целиком.

- [ ] **Step 2: Убедиться, что компилятор указал все места**

```bash
go build ./... 2>&1 | head -40
```

Ожидаемый вывод: ошибки компиляции во всех пакетах, использующих `Origin` и `OriginMessage`. Если компиляция прошла успешно — значит старые поля остались: перечитать шаг 1.

---

### Task 2: ai/extractor.go — заполнять плоские поля

**Files:**
- Modify: `internal/ai/extractor.go`

- [ ] **Step 1: Обновить создание Task в методе Extract**

Найти блок (~строка 289):
```go
		task := model.Task{
			ID:         msg.Source.ID + "/" + msg.ID + "/" + strconv.Itoa(i),
			Summary:    resp.Summary,
			Confidence: resp.Confidence,
			Source:     msg.Source,
			Origin: model.Origin{
				Account: resp.Project,
				Context: resp.Context,
				Topic:   resp.Topic,
			},
			OriginMessage: msg,
			CreatedAt:     msgTime,
		}
```

Заменить на:
```go
		task := model.Task{
			ID:            msg.Source.ID + "/" + msg.ID + "/" + strconv.Itoa(i),
			Summary:       resp.Summary,
			Project:       resp.Project,
			Details:       resp.Context,
			Topic:         resp.Topic,
			Confidence:    resp.Confidence,
			Source:        msg.Source,
			SourceMessage: msg,
			CreatedAt:     msgTime,
		}
```

- [ ] **Step 2: Проверить компиляцию пакета**

```bash
go build ./internal/ai/ 2>&1
```

Ожидаемый вывод: пусто (нет ошибок).

---

### Task 3: ai/command_extractor.go — переименовать OriginMessage

**Files:**
- Modify: `internal/ai/command_extractor.go`

- [ ] **Step 1: Заменить OriginMessage на SourceMessage**

Найти строку (около строки 132):
```go
		OriginMessage: msg,
```

Заменить на:
```go
		SourceMessage: msg,
```

- [ ] **Step 2: Проверить компиляцию пакета**

```bash
go build ./internal/ai/ 2>&1
```

Ожидаемый вывод: пусто.

---

### Task 4: handler/set_project.go — переименовать OriginMessage

**Files:**
- Modify: `internal/handler/set_project.go`

- [ ] **Step 1: Заменить оба вхождения cmd.OriginMessage**

Найти (~строка 44):
```go
	if cmd.OriginMessage.ReplyFn != nil {
```
```go
		if err := cmd.OriginMessage.ReplyFn(ctx, reply); err != nil {
```

Заменить на:
```go
	if cmd.SourceMessage.ReplyFn != nil {
```
```go
		if err := cmd.SourceMessage.ReplyFn(ctx, reply); err != nil {
```

- [ ] **Step 2: Проверить компиляцию**

```bash
go build ./internal/handler/ 2>&1
```

Ожидаемый вывод: пусто.

---

### Task 5: pipeline/pipeline.go — убрать Origin.Subject, переименовать поля

**Files:**
- Modify: `internal/pipeline/pipeline.go`

- [ ] **Step 1: Обновить блок обогащения задач в processPromise**

Найти (~строка 162):
```go
	for i := range tasks {
		tasks[i].Origin.Subject = msg.Subject
		tasks[i].Origin.Account = projectName
		// Если экстрактор явно определил проект из текста — сохраняем его.
		// Иначе используем значение из MetaStore или Source.Name.
		if tasks[i].Origin.Account == "" {
			tasks[i].Origin.Account = projectName
		}
		// Topic очищается для групповых чатов; DM и Batch сохраняют тему от экстрактора.
		if msg.Kind == model.MessageKindGroup {
			tasks[i].Origin.Topic = ""
		}
		p.logger.InfoContext(ctx, "задача извлечена",
			"summary", tasks[i].Summary,
			"project", tasks[i].Origin.Account,
			"confidence", tasks[i].Confidence)
	}
```

Заменить на:
```go
	for i := range tasks {
		// Если экстрактор явно определил проект из текста — сохраняем его.
		// Иначе используем значение из MetaStore или Source.Name.
		if tasks[i].Project == "" {
			tasks[i].Project = projectName
		}
		// Topic очищается для групповых чатов; DM и Batch сохраняют тему от экстрактора.
		if msg.Kind == model.MessageKindGroup {
			tasks[i].Topic = ""
		}
		p.logger.InfoContext(ctx, "задача извлечена",
			"summary", tasks[i].Summary,
			"project", tasks[i].Project,
			"confidence", tasks[i].Confidence)
	}
```

- [ ] **Step 2: Проверить компиляцию**

```bash
go build ./internal/pipeline/ 2>&1
```

Ожидаемый вывод: пусто.

---

### Task 6: sink/super_productivity.go — переименовать Origin-поля

**Files:**
- Modify: `internal/sink/super_productivity.go`

- [ ] **Step 1: Обновить saveTask — Origin.Context → Details**

Найти (~строка 104):
```go
	if task.Origin.Context != "" {
		t.Notes = task.Origin.Context
	}
```

Заменить на:
```go
	if task.Details != "" {
		t.Notes = task.Details
	}
```

- [ ] **Step 2: Обновить getProjectName — Origin.Account → Project**

Найти (~строка 128):
```go
	if s.createProjectPerSource {
		if task.Origin.Account != "" {
			return task.Origin.Account
		}
		return task.Source.Name
	}
```

Заменить на:
```go
	if s.createProjectPerSource {
		if task.Project != "" {
			return task.Project
		}
		return task.Source.Name
	}
```

- [ ] **Step 3: Проверить компиляцию**

```bash
go build ./internal/sink/ 2>&1
```

Ожидаемый вывод: пусто.

---

### Task 7: sink/telegram_notifier.go — переименовать Origin-поля

**Files:**
- Modify: `internal/sink/telegram_notifier.go`

- [ ] **Step 1: Обновить formatTaskMessage**

Найти (~строка 46):
```go
	if first.Origin.Subject != "" {
		if first.Origin.Account != "" {
			fmt.Fprintf(&sb, "Источник: %s (%s)\n", first.Origin.Subject, first.Origin.Account)
		} else {
			fmt.Fprintf(&sb, "Источник: %s\n", first.Origin.Subject)
		}
	} else if first.Origin.Account != "" {
		fmt.Fprintf(&sb, "Источник: %s\n", first.Origin.Account)
	} else {
		fmt.Fprintf(&sb, "Источник: %s\n", first.Source.Name)
	}
```

Заменить на:
```go
	if first.SourceMessage.Subject != "" {
		if first.Project != "" {
			fmt.Fprintf(&sb, "Источник: %s (%s)\n", first.SourceMessage.Subject, first.Project)
		} else {
			fmt.Fprintf(&sb, "Источник: %s\n", first.SourceMessage.Subject)
		}
	} else if first.Project != "" {
		fmt.Fprintf(&sb, "Источник: %s\n", first.Project)
	} else {
		fmt.Fprintf(&sb, "Источник: %s\n", first.Source.Name)
	}
```

Найти (~строка 69):
```go
		if task.Origin.Context != "" {
			ctx := strings.ReplaceAll(task.Origin.Context, "\n", " ")
```

Заменить на:
```go
		if task.Details != "" {
			ctx := strings.ReplaceAll(task.Details, "\n", " ")
```

- [ ] **Step 2: Проверить компиляцию**

```bash
go build ./internal/sink/ 2>&1
```

Ожидаемый вывод: пусто.

---

### Task 8: sink/obsidian.go — переименовать Origin-поля

**Files:**
- Modify: `internal/sink/obsidian.go`

- [ ] **Step 1: Обновить комментарий к Save**

Найти:
```go
//   - imap: Origin.Subject (Origin.Account)
//
// Если Origin.Topic не пустой — задача идёт в подсекцию ### Topic.
// Если Origin.Topic пустой — задача вставляется перед первой ### (или в конец секции).
```

Заменить на:
```go
//   - imap: SourceMessage.Subject (Project)
//
// Если Topic не пустой — задача идёт в подсекцию ### Topic.
// Если Topic пустой — задача вставляется перед первой ### (или в конец секции).
```

- [ ] **Step 2: Обновить sectionHeading**

Найти (~строка 95):
```go
	if task.Source.Kind == "imap" {
		subject := strings.ReplaceAll(strings.TrimSpace(task.Origin.Subject), "\n", " ")
		account := strings.ReplaceAll(strings.TrimSpace(task.Origin.Account), "\n", " ")
```

Заменить на:
```go
	if task.Source.Kind == "imap" {
		subject := strings.ReplaceAll(strings.TrimSpace(task.SourceMessage.Subject), "\n", " ")
		account := strings.ReplaceAll(strings.TrimSpace(task.Project), "\n", " ")
```

- [ ] **Step 3: Обновить formatTaskLines**

Найти (~строка 121):
```go
	if task.Origin.Context != "" {
		ctx := strings.ReplaceAll(task.Origin.Context, "\n", " ")
```

Заменить на:
```go
	if task.Details != "" {
		ctx := strings.ReplaceAll(task.Details, "\n", " ")
```

- [ ] **Step 4: Обновить insertTaskIntoSection и комментарии**

Найти (~строка 129):
```go
// insertTaskIntoSection вставляет задачу в нужное место секции.
// При наличии Origin.Topic — в подсекцию ### Topic (создаётся при необходимости).
// При отсутствии Origin.Topic — перед первой ### (или в конец секции).
```

Заменить на:
```go
// insertTaskIntoSection вставляет задачу в нужное место секции.
// При наличии Topic — в подсекцию ### Topic (создаётся при необходимости).
// При отсутствии Topic — перед первой ### (или в конец секции).
```

Найти (~строка 134):
```go
	if task.Origin.Topic == "" {
```

Заменить на:
```go
	if task.Topic == "" {
```

Найти (~строка 156):
```go
	topicHeading := "### " + strings.ReplaceAll(task.Origin.Topic, "\n", " ")
```

Заменить на:
```go
	topicHeading := "### " + strings.ReplaceAll(task.Topic, "\n", " ")
```

- [ ] **Step 5: Проверить компиляцию всего проекта**

```bash
go build ./... 2>&1
```

Ожидаемый вывод: пусто. Если есть ошибки — значит пропущено вхождение: исправить по указанию компилятора.

---

### Task 9: Тесты — ai пакет

**Files:**
- Modify: `internal/ai/extractor_test.go`
- Modify: `internal/ai/command_extractor_test.go`

- [ ] **Step 1: extractor_test.go — OriginMessage → SourceMessage**

Найти (~строка 317):
```go
	if task.OriginMessage.ID != "msg99" {
		t.Errorf("OriginMessage.ID = %q, ожидали %q", task.OriginMessage.ID, "msg99")
```

Заменить на:
```go
	if task.SourceMessage.ID != "msg99" {
		t.Errorf("SourceMessage.ID = %q, ожидали %q", task.SourceMessage.ID, "msg99")
```

- [ ] **Step 2: extractor_test.go — Origin.Account → Project (новые тесты)**

Найти в `TestTaskExtractor_ExtractsProjectName`:
```go
	if tasks[0].Origin.Account != "Помощь" {
		t.Errorf("Origin.Account = %q, ожидали %q", tasks[0].Origin.Account, "Помощь")
```

Заменить на:
```go
	if tasks[0].Project != "Помощь" {
		t.Errorf("Project = %q, ожидали %q", tasks[0].Project, "Помощь")
```

Найти в `TestTaskExtractor_EmptyProjectNotSetInAccount`:
```go
	if tasks[0].Origin.Account != "" {
		t.Errorf("Origin.Account = %q, ожидали пустую строку", tasks[0].Origin.Account)
```

Заменить на:
```go
	if tasks[0].Project != "" {
		t.Errorf("Project = %q, ожидали пустую строку", tasks[0].Project)
```

- [ ] **Step 3: command_extractor_test.go — OriginMessage → SourceMessage**

Найти (~строка 53):
```go
	if cmd.OriginMessage.ID != msg.ID {
		t.Errorf("cmd.OriginMessage.ID = %q, ожидали %q", cmd.OriginMessage.ID, msg.ID)
```

Заменить на:
```go
	if cmd.SourceMessage.ID != msg.ID {
		t.Errorf("cmd.SourceMessage.ID = %q, ожидали %q", cmd.SourceMessage.ID, msg.ID)
```

- [ ] **Step 4: Запустить тесты пакета ai**

```bash
go test ./internal/ai/ -v 2>&1 | tail -20
```

Ожидаемый вывод: все тесты PASS.

---

### Task 10: Тесты — handler пакет

**Files:**
- Modify: `internal/handler/set_project_test.go`

- [ ] **Step 1: Заменить все OriginMessage на SourceMessage**

В файле `internal/handler/set_project_test.go` 7 вхождений `OriginMessage`. Заменить все:

```bash
sed -i '' 's/OriginMessage/SourceMessage/g' internal/handler/set_project_test.go
```

- [ ] **Step 2: Запустить тесты пакета**

```bash
go test ./internal/handler/ -v 2>&1 | tail -10
```

Ожидаемый вывод: все тесты PASS.

---

### Task 11: Тесты — pipeline пакет

**Files:**
- Modify: `internal/pipeline/pipeline_test.go`

- [ ] **Step 1: Обновить mockExtractor — устанавливать SourceMessage**

Найти структуру mockExtractor и её метод Extract (~строка 30):
```go
func (m *mockExtractor) Extract(_ context.Context, _ model.Message, history []model.Message) ([]model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastHistory = history
	return m.tasks, m.err
}
```

Заменить на:
```go
func (m *mockExtractor) Extract(_ context.Context, msg model.Message, history []model.Message) ([]model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastHistory = history
	// Имитируем реальный экстрактор: устанавливаем SourceMessage из входящего сообщения.
	result := make([]model.Task, len(m.tasks))
	for i, t := range m.tasks {
		if t.SourceMessage.ID == "" {
			t.SourceMessage = msg
		}
		result[i] = t
	}
	return result, m.err
}
```

- [ ] **Step 2: Заменить Origin.Account → Project и Origin.Topic → Topic в тестовых задачах**

Найти (`TestProcess_OriginEnrichment_MetaStoreFound`, ~строка 678):
```go
	if sink.tasks[0].Origin.Account != "Бекенд" {
		t.Errorf("Origin.Account = %q, ожидалось %q", sink.tasks[0].Origin.Account, "Бекенд")
```
Заменить на:
```go
	if sink.tasks[0].Project != "Бекенд" {
		t.Errorf("Project = %q, ожидалось %q", sink.tasks[0].Project, "Бекенд")
```

Аналогично в остальных тестах обогащения — `TestProcess_OriginEnrichment_MetaStoreNotFound_FallbackToSourceName`, `TestProcess_OriginEnrichment_MetaStoreError_FallbackToSourceName`, `TestProcess_OriginEnrichment_NoMetaStore_FallbackToSourceName`:
```go
	// BEFORE:
	sink.tasks[0].Origin.Account != "Группа"
	t.Errorf("Origin.Account = %q, ожидалось %q", sink.tasks[0].Origin.Account, "Группа")
	// AFTER:
	sink.tasks[0].Project != "Группа"
	t.Errorf("Project = %q, ожидалось %q", sink.tasks[0].Project, "Группа")
```

- [ ] **Step 3: Обновить тесты Topic**

Найти `TestProcess_OriginTopic_ClearedForGroup` (~строка 745):
```go
		Origin: model.Origin{Topic: "Тема из экстрактора"},
```
Заменить на:
```go
		Topic: "Тема из экстрактора",
```

Найти проверку (~строка 763):
```go
	if sink.tasks[0].Origin.Topic != "" {
		t.Errorf("Origin.Topic должен быть пустым для Group, получен %q", sink.tasks[0].Origin.Topic)
```
Заменить на:
```go
	if sink.tasks[0].Topic != "" {
		t.Errorf("Topic должен быть пустым для Group, получен %q", sink.tasks[0].Topic)
```

Найти `TestProcess_OriginTopic_PreservedForDM` (~строка 768):
```go
		Origin: model.Origin{Topic: "Бекенд"},
```
Заменить на:
```go
		Topic: "Бекенд",
```

Найти проверку (~строка 786):
```go
	if sink.tasks[0].Origin.Topic != "Бекенд" {
		t.Errorf("Origin.Topic должен сохраняться для DM, получен %q", sink.tasks[0].Origin.Topic)
```
Заменить на:
```go
	if sink.tasks[0].Topic != "Бекенд" {
		t.Errorf("Topic должен сохраняться для DM, получен %q", sink.tasks[0].Topic)
```

- [ ] **Step 4: Обновить TestProcess_OriginSubject_FromBatchMessage**

Найти (~строка 814):
```go
	if got.Origin.Subject != "Встреча по Q1" {
		t.Errorf("Origin.Subject = %q, ожидалось %q", got.Origin.Subject, "Встреча по Q1")
	}
	if got.Origin.Account != "Рабочая почта" {
		t.Errorf("Origin.Account = %q, ожидалось %q", got.Origin.Account, "Рабочая почта")
	}
```

Заменить на (Subject теперь приходит из SourceMessage, установленного mockExtractor через Step 1):
```go
	if got.SourceMessage.Subject != "Встреча по Q1" {
		t.Errorf("SourceMessage.Subject = %q, ожидалось %q", got.SourceMessage.Subject, "Встреча по Q1")
	}
	if got.Project != "Рабочая почта" {
		t.Errorf("Project = %q, ожидалось %q", got.Project, "Рабочая почта")
	}
```

- [ ] **Step 5: Обновить тесты PreservesExtractorProjectName и FallsBackToMetaStore**

Найти `TestPipeline_PreservesExtractorProjectName` (~строка 1054):
```go
		Origin:     model.Origin{Account: "Помощь"},
```
Заменить на:
```go
		Project: "Помощь",
```

Найти проверку (~строка 1085):
```go
	if sink.tasks[0].Origin.Account != "Помощь" {
		t.Errorf("Origin.Account = %q, ожидали %q (не должен быть перезаписан на 'DM')",
			sink.tasks[0].Origin.Account, "Помощь")
```
Заменить на:
```go
	if sink.tasks[0].Project != "Помощь" {
		t.Errorf("Project = %q, ожидали %q (не должен быть перезаписан на 'DM')",
			sink.tasks[0].Project, "Помощь")
```

Найти в `TestPipeline_FallsBackToMetaStoreWhenNoExtractorProject` (~строка 1127):
```go
	if sink.tasks[0].Origin.Account != "МойПроект" {
		t.Errorf("Origin.Account = %q, ожидали %q", sink.tasks[0].Origin.Account, "МойПроект")
```
Заменить на:
```go
	if sink.tasks[0].Project != "МойПроект" {
		t.Errorf("Project = %q, ожидали %q", sink.tasks[0].Project, "МойПроект")
```

- [ ] **Step 6: Запустить тесты пакета**

```bash
go test ./internal/pipeline/ -v 2>&1 | tail -20
```

Ожидаемый вывод: все тесты PASS.

---

### Task 12: Тесты — sink пакет

**Files:**
- Modify: `internal/sink/super_productivity_test.go`
- Modify: `internal/sink/telegram_notifier_test.go`
- Modify: `internal/sink/obsidian_test.go`

- [ ] **Step 1: super_productivity_test.go — все Origin → плоские поля**

Все 12 вхождений `Origin` в файле — это `model.Origin{Account: "..."}` и `model.Origin{Account: "...", Context: "..."}`. Заменить каждое:

```go
// BEFORE:
Origin: model.Origin{Account: "Test Account", Context: "Test context"},
// AFTER:
Project: "Test Account", Details: "Test context",

// BEFORE:
Origin: model.Origin{Account: "Same Account"},
// AFTER:
Project: "Same Account",

// BEFORE:
Origin: model.Origin{Account: ""},
// AFTER:
// (строку убрать — нулевое значение и так пустое)

// BEFORE:
Origin: model.Origin{Account: existingProject.Title},
// AFTER:
Project: existingProject.Title,

// BEFORE:
Origin: model.Origin{Account: "Рабочий проект"},
// AFTER:
Project: "Рабочий проект",

// BEFORE:
Origin: model.Origin{Account: existingTag.Title},
// AFTER:
Project: existingTag.Title,

// BEFORE:
{Summary: "Задача 1", Origin: model.Origin{Account: "Проект A"}},
{Summary: "Задача 2", Origin: model.Origin{Account: "Проект A"}},
// AFTER:
{Summary: "Задача 1", Project: "Проект A"},
{Summary: "Задача 2", Project: "Проект A"},

// BEFORE:
Origin: model.Origin{Account: "Найденный проект"},
// AFTER:
Project: "Найденный проект",
```

Строки с `Origin: model.Origin{Account: "Any Account"}` (~строка 162) заменить на `Project: "Any Account"`.

- [ ] **Step 2: telegram_notifier_test.go — OriginMessage + Origin → SourceMessage + плоские поля**

Найти (~строка 76):
```go
		OriginMessage: model.Message{
			ID:   "42",
			Text: "Можешь подготовить отчёт?",
		},
```
Заменить на:
```go
		SourceMessage: model.Message{
			ID:   "42",
			Text: "Можешь подготовить отчёт?",
		},
```

Найти в `TestFormatTaskMessage_IMAPSource` (~строка 253):
```go
	task.Origin.Subject = "Встреча по проекту"
	task.Origin.Account = "Рабочая почта"
	task.Origin.Context = "обсуждали дорожную карту"
```
Заменить на:
```go
	task.SourceMessage.Subject = "Встреча по проекту"
	task.Project = "Рабочая почта"
	task.Details = "обсуждали дорожную карту"
```

Найти в `TestFormatTaskMessage_IMAPEmptySubject` (~строка 273):
```go
	task.Origin.Subject = ""
	task.Origin.Account = "Рабочая почта"
```
Заменить на:
```go
	task.SourceMessage.Subject = ""
	task.Project = "Рабочая почта"
```

Найти в `TestFormatTaskMessage_IMAPEmptyAccount` (~строка 290):
```go
	task.Origin.Subject = "Встреча по проекту"
	task.Origin.Account = ""
```
Заменить на:
```go
	task.SourceMessage.Subject = "Встреча по проекту"
	task.Project = ""
```

Найти в `TestFormatTaskMessage_IMAPFallbackToSourceName` (~строка 307):
```go
	task.Origin.Subject = ""
	task.Origin.Account = ""
```
Заменить на:
```go
	task.SourceMessage.Subject = ""
	task.Project = ""
```

Найти все `task.Origin.Context` (строки ~165, 192, 195, 222, 235):
```go
// BEFORE:
task.Origin.Context = "обсуждали на встрече"
task1.Origin.Context = "по результатам ревью"
task2.Origin.Context = "обновили API"
task.Origin.Context = "по запросу команды"
task.Origin.Context = ""
// AFTER:
task.Details = "обсуждали на встрече"
task1.Details = "по результатам ревью"
task2.Details = "обновили API"
task.Details = "по запросу команды"
task.Details = ""
```

- [ ] **Step 3: obsidian_test.go — Origin → плоские поля**

Найти функцию `imapTask` (~строка 27):
```go
func imapTask(summary, subject, account, topic string) model.Task {
	return model.Task{
		ID:      "task-2",
		Summary: summary,
		Source:  model.Source{Kind: "imap", ID: "inbox", Name: "inbox"},
		Origin:  model.Origin{Subject: subject, Account: account, Topic: topic},
	}
}
```
Заменить на:
```go
func imapTask(summary, subject, account, topic string) model.Task {
	return model.Task{
		ID:            "task-2",
		Summary:       summary,
		Source:        model.Source{Kind: "imap", ID: "inbox", Name: "inbox"},
		SourceMessage: model.Message{Subject: subject},
		Project:       account,
		Topic:         topic,
	}
}
```

Найти (~строка 294):
```go
			Origin:  model.Origin{Subject: "Встреча", Account: "Рабочая почта"},
```
Заменить на:
```go
			SourceMessage: model.Message{Subject: "Встреча"},
			Project:       "Рабочая почта",
```

Найти (~строка 327):
```go
		Origin:   model.Origin{Context: "Иван попросил к пятнице"},
```
Заменить на:
```go
		Details: "Иван попросил к пятнице",
```

- [ ] **Step 4: Запустить тесты пакета sink**

```bash
go test ./internal/sink/ -v 2>&1 | tail -20
```

Ожидаемый вывод: все тесты PASS.

---

### Task 13: Полная верификация + CLAUDE.md + коммит

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Полный прогон тестов**

```bash
go test ./... 2>&1
```

Ожидаемый вывод: все пакеты `ok`, нет `FAIL`.

- [ ] **Step 2: Линтер**

```bash
go vet ./... 2>&1
```

Ожидаемый вывод: пусто.

- [ ] **Step 3: Обновить CLAUDE.md — раздел «Обогащение контекста задач»**

Найти раздел:
```markdown
## Обогащение контекста задач

Структура `Task` содержит поле `Origin Origin` с метаданными происхождения:
- `Subject` — тема письма (IMAP), пустая для Telegram
- `Account` — название проекта из MetaStore (по chatID) для Telegram; для IMAP — человекочитаемое имя аккаунта (из `config.IMAPConfig.Label`, fallback на имя папки). Заполняется pipeline после экстракции через `lookupProjectName`.
- `Topic` — тематическая группа, заполняется экстрактором (обнуляется для Telegram в pipeline)
- `Context` — краткая справка для понимания обещания без оригинала, заполняется экстрактором

`Message` содержит поле `Subject string` (тема письма) и `Text` (только тело письма).

Pipeline заполняет `Origin.Subject` из `msg.Subject` и `Origin.Account` через `lookupProjectName(msg)` после экстракции.
```

Заменить на:
```markdown
## Обогащение контекста задач

Структура `Task` содержит плоские поля с метаданными задачи:
- `Project` — название проекта. Для Telegram: из текста сообщения (AI) или MetaStore (по chatID). Для IMAP: из `config.IMAPConfig.Label` (fallback на имя папки). Заполняется экстрактором (если AI нашёл проект в тексте) или pipeline через `lookupProjectName`.
- `Topic` — тематическая группа, заполняется экстрактором (очищается для групповых чатов в pipeline).
- `Details` — краткая справка для понимания обещания без оригинала, заполняется экстрактором.
- `SourceMessage` — исходное сообщение. `SourceMessage.Subject` содержит тему письма (IMAP). Также хранит коллбэки ReactFn/ReplyFn.

`Message` содержит поле `Subject string` (тема письма) и `Text` (только тело письма).

Pipeline устанавливает `Project` через `lookupProjectName(msg)` только если экстрактор не нашёл проект в тексте (`task.Project == ""`).
```

- [ ] **Step 4: Коммит**

```bash
git add -p
git commit -m "refactor: упростить Task — убрать Origin, плоские поля Project/Topic/Details"
```
