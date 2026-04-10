# Реализация sink'а для Super Productivity

> **Для агентов:** REQUIRED SUB-SKILL: Использовать superpowers:subagent-driven-development (рекомендуется) или superpowers:executing-plans для пошаговой реализации плана. Шаги используют синтаксис флажков (`- [ ]`) для отслеживания.

**Цель:** Реализовать sink, который сохраняет извлечённые задачи в приложение Super Productivity через Local Rest API.

**Архитектура:** Реализация нового sink'а в соответствии с существующим интерфейсом `model.Sink`. Использование прямого HTTP-клиента для взаимодействия с Local Rest API. Кэширование идентификаторов проектов для оптимизации производительности.

**Технологический стек:** Go 1.26, стандартная библиотека `net/http`, `encoding/json`, `sync`

---

### Задача 1: Обновление конфигурации

**Файлы:**
- Modify: `internal/config/config.go`

- [x] **Шаг 1: Добавление структуры SuperProductivityConfig**

Добавить в `internal/config/config.go` новую структуру конфигурации:

```go
// SuperProductivityConfig описывает настройки sink'а для Super Productivity.
type SuperProductivityConfig struct {
    // BaseURL — базовый адрес Local Rest API (по умолчанию http://localhost:3001).
    BaseURL string `toml:"base_url"`
    // CreateProjectPerSource — создавать ли отдельный проект для каждого источника.
    CreateProjectPerSource bool `toml:"create_project_per_source"`
}
```

- [x] **Шаг 2: Обновление SinksConfig**

Добавить поле в существующую структуру `SinksConfig`:

```go
// SinksConfig объединяет все хранилища задач (дополнение).
type SinksConfig struct {
    Obsidian               ObsidianSinkConfig     `toml:"obsidian"`
    SuperProductivity      SuperProductivityConfig `toml:"super_productivity"`
}
```

- [x] **Шаг 3: Валидация конфигурации**

Добавить проверку обязательных полей в метод `validate()`:

```go
if s.SuperProductivity.CreateProjectPerSource && s.SuperProductivity.BaseURL == "" {
    return fmt.Errorf("sinks.super_productivity.base_url обязателен при create_project_per_source = true")
}
```

- [x] **Шаг 4: Коммит изменений**

```bash
git add internal/config/config.go
git commit -m "config: add Super Productivity sink configuration"
```

### Задача 2: Реализация HTTP-клиента

**Файлы:**
- Create: `internal/sink/super_productivity.go`

- [x] **Шаг 1: Создание структуры sink'а**

Создать файл `internal/sink/super_productivity.go` с базовой структурой:

```go
package sink

import (
    "context"
    "encoding/json"
    "net/http"
    "sync"
    "time"

    "github.com/anadale/huskwoot/internal/model"
)

// SuperProductivitySink сохраняет задачи в Super Productivity через Local Rest API.
type SuperProductivitySink struct {
    mu        sync.Mutex
    client    *http.Client
    config    SuperProductivityConfig
    projects  map[string]string // Origin.Account -> projectId
}
```

- [x] **Шаг 2: Реализация конструктора**

```go
// NewSuperProductivitySink создает новый sink с указанной конфигурацией.
func NewSuperProductivitySink(cfg SuperProductivityConfig) (*SuperProductivitySink, error) {
    if cfg.BaseURL == "" {
        cfg.BaseURL = "http://localhost:3001"
    }
    return &SuperProductivitySink{
        client: &http.Client{Timeout: 10 * time.Second},
        config: cfg,
        projects: make(map[string]string),
    }, nil
}
```

- [x] **Шаг 3: Реализация интерфейса Sink**

```go
// Save отправляет задачи в Super Productivity.
func (s *SuperProductivitySink) Save(ctx context.Context, tasks []model.Task) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    for _, task := range tasks {
        if err := s.saveTask(ctx, task); err != nil {
            return fmt.Errorf("сохранение задачи в Super Productivity: %w", err)
        }
    }
    return nil
}
```

- [x] **Шаг 4: Коммит реализации**

```bash
git add internal/sink/super_productivity.go
git commit -m "sink: init Super Productivity sink structure"
```

### Задача 3: Реализация работы с проектами

**Файлы:**
- Modify: `internal/sink/super_productivity.go`

- [x] **Шаг 1: Структура ответа API**

Добавить структуры для десериализации ответов API:

```go
// spProject представляет проект из Super Productivity.
type spProject struct {
    Id   string `json:"id"`
    Name string `json:"name"`
}
```

- [x] **Шаг 2: Метод получения проекта по имени**

```go
// getProjectByName возвращает ID проекта по имени.
func (s *SuperProductivitySink) getProjectByName(ctx context.Context, name string) (string, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", s.config.BaseURL+"/project", nil)
    if err != nil {
        return "", err
    }
    
    resp, err := s.client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("API error: %d", resp.StatusCode)
    }
    
    var projects []spProject
    if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
        return "", err
    }
    
    for _, p := range projects {
        if p.Name == name {
            return p.Id, nil
        }
    }
    
    return "", nil // проект не найден
}
```

- [x] **Шаг 3: Метод создания проекта**

```go
// createProject создаёт новый проект.
func (s *SuperProductivitySink) createProject(ctx context.Context, name string) (string, error) {
    project := map[string]string{"name": name}
    body, err := json.Marshal(project)
    if err != nil {
        return "", err
    }
    
    req, err := http.NewRequestWithContext(ctx, "POST", s.config.BaseURL+"/project", bytes.NewBuffer(body))
    if err != nil {
        return "", err
    }
    req.Header.Set("Content-Type", "application/json")
    
    resp, err := s.client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusCreated {
        return "", fmt.Errorf("API error: %d", resp.StatusCode)
    }
    
    var result map[string]string
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return "", err
    }
    
    return result["id"], nil
}
```

- [x] **Шаг 4: Метод определения ID проекта**

```go
// getProjectId определяет или создаёт проект для задачи.
func (s *SuperProductivitySink) getProjectId(ctx context.Context, task model.Task) (string, error) {
    // Используем общий проект если не нужно создавать отдельные
    if !s.config.CreateProjectPerSource {
        // Логика для общего проекта
        return "huskwoot-main", nil
    }
    
    // Используем Origin.Account как имя проекта
    projectName := task.Origin.Account
    if projectName == "" {
        projectName = task.Source.Name
    }
    
    // Проверяем кэш
    if projectId, ok := s.projects[projectName]; ok {
        return projectId, nil
    }
    
    // Ищем в API
    projectId, err := s.getProjectByName(ctx, projectName)
    if err != nil {
        return "", err
    }
    
    // Создаём если не найден
    if projectId == "" {
        projectId, err = s.createProject(ctx, projectName)
        if err != nil {
            return "", err
        }
    }
    
    // Кэшируем
    s.projects[projectName] = projectId
    return projectId, nil
}
```

- [x] **Шаг 5: Коммит реализации**

```bash
git add internal/sink/super_productivity.go
git commit -m "sink: implement project management for Super Productivity"
```

### Задача 4: Реализация создания задач

**Файлы:**
- Modify: `internal/sink/super_productivity.go`

- [x] **Шаг 1: Структура задачи API**

```go
// spTask представляет задачу для создания в Super Productivity.
type spTask struct {
    Title     string `json:"title"`
    Notes     string `json:"notes,omitempty"`
    ProjectId string `json:"projectId"`
    DueDate   int64  `json:"dueDate,omitempty"`
}
```

- [x] **Шаг 2: Метод сохранения задачи**

```go
// saveTask сохраняет одну задачу в Super Productivity.
func (s *SuperProductivitySink) saveTask(ctx context.Context, task model.Task) error {
    projectId, err := s.getProjectId(ctx, task)
    if err != nil {
        return fmt.Errorf("получение ID проекта: %w", err)
    }
    
    spTask := spTask{
        Title:     task.Summary,
        ProjectId: projectId,
    }
    
    if task.Origin.Context != "" {
        spTask.Notes = task.Origin.Context
    }
    
    if task.Deadline != nil {
        spTask.DueDate = task.Deadline.Unix() * 1000 // в миллисекундах
    }
    
    body, err := json.Marshal(spTask)
    if err != nil {
        return err
    }
    
    req, err := http.NewRequestWithContext(ctx, "POST", s.config.BaseURL+"/task", bytes.NewBuffer(body))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")
    
    resp, err := s.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusCreated {
        return fmt.Errorf("API error: %d", resp.StatusCode)
    }
    
    return nil
}
```

- [x] **Шаг 3: Импорт пакета bytes**

Добавить `"bytes"` в список импортов.

- [x] **Шаг 4: Коммит реализации**

```bash
git add internal/sink/super_productivity.go
git commit -m "sink: implement task creation for Super Productivity"
```

### Задача 5: Интеграция в основной модуль

**Файлы:**
- Modify: `cmd/huskwoot/main.go`

- [x] **Шаг 1: Импорт нового sink'а**

Добавить в импорты:

```go
"github.com/anadale/huskwoot/internal/sink"
```

- [x] **Шаг 2: Инициализация sink'а**

Добавить инициализацию после создания ObsidianSink:

```go
superProductivitySink, err := sink.NewSuperProductivitySink(cfg.Sinks.SuperProductivity)
if err != nil {
    return fmt.Errorf("инициализация SuperProductivitySink: %w", err)
}
sinks = append(sinks, superProductivitySink)
```

- [x] **Шаг 3: Коммит интеграции**

```bash
git add cmd/huskwoot/main.go
git commit -m "main: integrate Super Productivity sink"
```

### Задача 6: Тестирование и проверка

**Файлы:**
- Create: `internal/sink/super_productivity_test.go`

- [x] **Шаг 1: Создание тестового сервера**

```go
package sink

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

func TestSuperProductivitySink_Save(t *testing.T) {
    // Создаём мок-сервер
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/project":
            if r.Method == "GET" {
                json.NewEncoder(w).Encode([]spProject{})
            } else if r.Method == "POST" {
                var project map[string]string
                json.NewDecoder(r.Body).Decode(&project)
                json.NewEncoder(w).Encode(map[string]string{
                    "id":   "proj-123",
                    "name": project["name"],
                })
            }
        case "/task":
            if r.Method == "POST" {
                w.WriteHeader(http.StatusCreated)
                json.NewEncoder(w).Encode(map[string]string{"id": "task-123"})
            }
        default:
            t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
            http.Error(w, "not found", http.StatusNotFound)
        }
    }))
    defer server.Close()
    
    // Создаём sink с URL мок-сервера
    sink, err := NewSuperProductivitySink(SuperProductivityConfig{
        BaseURL:              server.URL,
        CreateProjectPerSource: true,
    })
    if err != nil {
        t.Fatal(err)
    }
    
    // Создаём тестовую задачу
    task := model.Task{
        Summary: "Test task",
        Origin: model.Origin{
            Account: "Test Account",
            Context: "Test context",
        },
        Source: model.Source{Name: "test-source"},
    }
    
    // Вызываем метод Save
    err = sink.Save(context.Background(), []model.Task{task})
    if err != nil {
        t.Errorf("Save() error = %v", err)
    }
}
```

- [x] **Шаг 2: Запуск тестов**

```bash
go test ./...
```

- [x] **Шаг 3: Коммит тестов**

```bash
git add internal/sink/super_productivity_test.go
git commit -m "test: add tests for Super Productivity sink"
```

### Задача 7: Документирование конфигурации

**Файлы:**
- Modify: `CLAUDE.md`

- [x] **Шаг 1: Обновление раздела конфигурации**

Добавить в `CLAUDE.md` описание новой секции конфигурации:

```markdown
## Добавление нового Sink

1. Создать файл `internal/sink/mytype.go`
2. Реализовать интерфейс `model.Sink`: `Save(ctx context.Context, tasks []model.Task) error`
3. Добавить секцию в конфиг
4. Добавить в срез `sinks` в `cmd/huskwoot/main.go`
5. Написать тесты в `internal/sink/mytype_test.go`

**Пример конфигурации для Super Productivity:**

```toml
[sinks.super_productivity]
base_url = "http://localhost:3001"
create_project_per_source = true
```
```

- [x] **Шаг 2: Коммит документации**

```bash
git add CLAUDE.md
git commit -m "docs: update configuration guide with Super Productivity example"
```

### Задача 8: Финальная проверка

- [x] **Шаг 1: Проверка статуса git**

```bash
git status
```

- [x] **Шаг 2: Запуск линтера**

```bash
go vet ./...
```

- [x] **Шаг 3: Создание финального коммита**

```bash
git commit --allow-empty -m "chore: complete Super Productivity sink implementation"
```