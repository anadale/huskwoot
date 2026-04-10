# Управление синками и нотификаторами — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить presence-based управление синками через конфиг, метод `Name()` в интерфейсы, устранить дублирование конфигурационных типов между пакетами `config` и `sink`.

**Architecture:** Pointer-типы в `SinksConfig` позволяют BurntSushi/toml оставить поле `nil` при отсутствии секции — это и есть механизм отключения. Конструкторы синков принимают типы из пакета `config` напрямую, устраняя дублирование. Метод `Name() string` добавляется в интерфейсы `model.Sink` и `model.Notifier`, реализуется на всех конкретных типах; `main.go` логирует активные компоненты при запуске.

**Tech Stack:** Go 1.26, `BurntSushi/toml`, `log/slog`, стандартные `go test ./...` и `go build`.

---

## Затронутые файлы

| Файл | Что меняется |
|------|-------------|
| `internal/model/interfaces.go` | Добавить `Name() string` в `Sink` и `Notifier` |
| `internal/pipeline/pipeline_test.go` | Добавить `Name()` в `mockSink` и `mockNotifier` |
| `internal/sink/obsidian.go` | Добавить `Name()`, изменить структуру и конструктор |
| `internal/sink/super_productivity.go` | Добавить `Name()`, изменить структуру и конструктор |
| `internal/sink/telegram_notifier.go` | Добавить `Name()` |
| `internal/sink/telegram_reaction.go` | Добавить `Name()` |
| `internal/sink/obsidian_test.go` | Обновить вызов `NewObsidianSink` |
| `internal/sink/super_productivity_test.go` | Обновить вызов `NewSuperProductivitySink` |
| `internal/config/config.go` | Pointer-типы в `SinksConfig`, обновить валидацию |
| `internal/config/config_test.go` | Новые тесты + обновить существующие |
| `cmd/huskwoot/main.go` | Presence-based инициализация, логирование активных компонентов |

---

## Задача 1: Добавить `Name()` в интерфейсы и обновить моки pipeline

**Файлы:**
- Изменить: `internal/model/interfaces.go`
- Изменить: `internal/pipeline/pipeline_test.go`

- [ ] **Шаг 1.1: Добавить `Name()` в интерфейсы**

В `internal/model/interfaces.go` обновить `Sink` и `Notifier`:

```go
// Sink сохраняет извлечённые задачи в хранилище.
type Sink interface {
	// Save сохраняет пакет задач. Возвращает ошибку при неудаче.
	Save(ctx context.Context, tasks []Task) error
	// Name возвращает человекочитаемое имя синка для логирования.
	Name() string
}

// Notifier отправляет пользователю уведомление о новых задачах.
type Notifier interface {
	// Notify отправляет уведомление о пакете задач.
	Notify(ctx context.Context, tasks []Task) error
	// Name возвращает человекочитаемое имя нотификатора для логирования.
	Name() string
}
```

- [ ] **Шаг 1.2: Убедиться, что компиляция ломается**

```bash
go build ./...
```

Ожидаемый результат: ошибки вида `does not implement model.Sink (missing method Name)` для `mockSink`, `mockNotifier` и всех конкретных типов в `sink`.

- [ ] **Шаг 1.3: Добавить `Name()` в моки pipeline**

В `internal/pipeline/pipeline_test.go` добавить методы после существующих методов `mockSink` и `mockNotifier`:

```go
func (m *mockSink) Name() string    { return "mock-sink" }
func (m *mockNotifier) Name() string { return "mock-notifier" }
```

- [ ] **Шаг 1.4: Убедиться, что pipeline-тесты проходят**

```bash
go test ./internal/pipeline/... -v
```

Ожидаемый результат: все тесты PASS (compile errors в `sink` пакете ещё есть, но pipeline изолирован).

```bash
go build ./internal/pipeline/...
```

Ожидаемый результат: SUCCESS.

- [ ] **Шаг 1.5: Коммит**

```bash
git add internal/model/interfaces.go internal/pipeline/pipeline_test.go
git commit -m "feat: добавить Name() в интерфейсы Sink и Notifier, обновить моки pipeline"
```

---

## Задача 2: Добавить `Name()` во все конкретные типы

**Файлы:**
- Изменить: `internal/sink/telegram_notifier.go`
- Изменить: `internal/sink/telegram_reaction.go`
- Изменить: `internal/sink/obsidian.go`
- Изменить: `internal/sink/super_productivity.go`

- [ ] **Шаг 2.1: Добавить `Name()` в `TelegramNotifier`**

В `internal/sink/telegram_notifier.go` добавить после `NewTelegramNotifier`:

```go
// Name возвращает имя нотификатора для логирования.
func (n *TelegramNotifier) Name() string { return "telegram-dm" }
```

- [ ] **Шаг 2.2: Добавить `Name()` в `TelegramReactionNotifier`**

В `internal/sink/telegram_reaction.go` добавить после `NewTelegramReactionNotifier`:

```go
// Name возвращает имя нотификатора для логирования.
func (n *TelegramReactionNotifier) Name() string {
	return "telegram-reaction:" + n.watcherID
}
```

- [ ] **Шаг 2.3: Добавить `Name()` в `ObsidianSink`**

В `internal/sink/obsidian.go` добавить после `NewObsidianSink`:

```go
// Name возвращает имя синка для логирования.
func (s *ObsidianSink) Name() string { return "obsidian" }
```

- [ ] **Шаг 2.4: Добавить `Name()` в `SuperProductivitySink`**

В `internal/sink/super_productivity.go` добавить после `NewSuperProductivitySink`:

```go
// Name возвращает имя синка для логирования.
func (s *SuperProductivitySink) Name() string { return "super-productivity" }
```

- [ ] **Шаг 2.5: Проверить, что весь проект компилируется и тесты проходят**

```bash
go build ./...
go test ./...
```

Ожидаемый результат: SUCCESS, все тесты PASS.

- [ ] **Шаг 2.6: Коммит**

```bash
git add internal/sink/telegram_notifier.go internal/sink/telegram_reaction.go \
        internal/sink/obsidian.go internal/sink/super_productivity.go
git commit -m "feat: реализовать Name() на всех конкретных типах синков и нотификаторов"
```

---

## Задача 3: Pointer-типы в конфиге и обновление валидации

**Файлы:**
- Изменить: `internal/config/config.go`
- Изменить: `internal/config/config_test.go`

- [ ] **Шаг 3.1: Написать новые тесты (красная фаза)**

В конец `internal/config/config_test.go` добавить:

```go
func TestLoad_SinksObsidian_Optional(t *testing.T) {
	// Конфиг без секции [sinks.obsidian] должен загружаться без ошибки.
	content := `
[user]
user_name = "Имя"

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[[watchers.telegram]]
id = "main"
owner_id = "user123"
token = "token"
groups = [-100]
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"

[notify]
[notify.telegram]
chat_id = 1

[state]
dir = "/state"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("конфиг без sinks.obsidian должен загружаться без ошибки, получили: %v", err)
	}
	if cfg.Sinks.Obsidian != nil {
		t.Errorf("Sinks.Obsidian = %+v, ожидали nil", cfg.Sinks.Obsidian)
	}
}

func TestLoad_SinksObsidian_PresentButMissingFields(t *testing.T) {
	// Секция [sinks.obsidian] присутствует, но обязательные поля пустые — ошибка.
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "отсутствует vault_path",
			content: `
[user]
user_name = "Имя"

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[[watchers.telegram]]
id = "main"
owner_id = "user123"
token = "token"
groups = [-100]
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"

[sinks]
[sinks.obsidian]
target_file = "Tasks/todo.md"

[notify]
[notify.telegram]
chat_id = 1

[state]
dir = "/state"
`,
		},
		{
			name: "отсутствует target_file",
			content: `
[user]
user_name = "Имя"

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[[watchers.telegram]]
id = "main"
owner_id = "user123"
token = "token"
groups = [-100]
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"

[sinks]
[sinks.obsidian]
vault_path = "/vault"

[notify]
[notify.telegram]
chat_id = 1

[state]
dir = "/state"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			if err == nil {
				t.Fatalf("ожидали ошибку валидации (%s), получили nil", tt.name)
			}
		})
	}
}

func TestLoad_SuperProductivity_DefaultBaseURL(t *testing.T) {
	// Секция [sinks.super_productivity] без base_url → дефолт http://localhost:3001.
	content := validBase() + `
[sinks.super_productivity]
create_project_per_source = true
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("super_productivity без base_url должен загружаться без ошибки, получили: %v", err)
	}
	if cfg.Sinks.SuperProductivity == nil {
		t.Fatal("Sinks.SuperProductivity = nil, ожидали non-nil")
	}
	if cfg.Sinks.SuperProductivity.BaseURL != "http://localhost:3001" {
		t.Errorf("SuperProductivity.BaseURL = %q, ожидали %q",
			cfg.Sinks.SuperProductivity.BaseURL, "http://localhost:3001")
	}
	if !cfg.Sinks.SuperProductivity.CreateProjectPerSource {
		t.Error("SuperProductivity.CreateProjectPerSource = false, ожидали true")
	}
}

func TestLoad_SuperProductivity_Absent(t *testing.T) {
	// Без секции [sinks.super_productivity] — nil.
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("ожидали успех: %v", err)
	}
	if cfg.Sinks.SuperProductivity != nil {
		t.Errorf("Sinks.SuperProductivity = %+v, ожидали nil", cfg.Sinks.SuperProductivity)
	}
}
```

- [ ] **Шаг 3.2: Запустить тесты — убедиться, что новые падают**

```bash
go test ./internal/config/... -run "TestLoad_Sinks|TestLoad_SuperProductivity" -v
```

Ожидаемый результат: новые тесты FAIL (валидация ещё требует vault_path).

- [ ] **Шаг 3.3: Обновить `SinksConfig` на pointer-типы**

В `internal/config/config.go` изменить структуру `SinksConfig`:

```go
// SinksConfig объединяет все хранилища задач.
type SinksConfig struct {
	Obsidian          *ObsidianSinkConfig      `toml:"obsidian"`
	SuperProductivity *SuperProductivityConfig `toml:"super_productivity"`
}
```

- [ ] **Шаг 3.4: Обновить метод `validate()`**

Найти в `validate()` блок с проверками Obsidian и SuperProductivity и заменить:

```go
// Было:
if c.Sinks.Obsidian.VaultPath == "" {
    return fmt.Errorf("sinks.obsidian.vault_path обязателен")
}
if c.Sinks.Obsidian.TargetFile == "" {
    return fmt.Errorf("sinks.obsidian.target_file обязателен")
}
```

```go
// Стало:
if c.Sinks.Obsidian != nil {
    if c.Sinks.Obsidian.VaultPath == "" {
        return fmt.Errorf("sinks.obsidian.vault_path обязателен")
    }
    if c.Sinks.Obsidian.TargetFile == "" {
        return fmt.Errorf("sinks.obsidian.target_file обязателен")
    }
}
if c.Sinks.SuperProductivity != nil && c.Sinks.SuperProductivity.BaseURL == "" {
    c.Sinks.SuperProductivity.BaseURL = "http://localhost:3001"
}
```

- [ ] **Шаг 3.5: Запустить все тесты конфига**

```bash
go test ./internal/config/... -v
```

Ожидаемый результат: все тесты PASS, включая новые.

- [ ] **Шаг 3.6: Убедиться, что компиляция проходит**

```bash
go build ./...
```

Ожидаемый результат: SUCCESS (main.go обратится к nil Obsidian — но это обнаружится на следующем шаге).

- [ ] **Шаг 3.7: Коммит**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: presence-based синки через pointer-типы в SinksConfig, default base_url для SuperProductivity"
```

---

## Задача 4: Обновить `ObsidianSink` — новый конструктор и структура

**Файлы:**
- Изменить: `internal/sink/obsidian.go`
- Изменить: `internal/sink/obsidian_test.go`

- [ ] **Шаг 4.1: Обновить тест-хелпер `newSink` (красная фаза)**

В `internal/sink/obsidian_test.go` обновить импорты и хелпер:

```go
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/sink"
)

func newSink(t *testing.T, dir, file string) *sink.ObsidianSink {
	t.Helper()
	s, err := sink.NewObsidianSink(&config.ObsidianSinkConfig{VaultPath: dir, TargetFile: file})
	if err != nil {
		t.Fatalf("NewObsidianSink: %v", err)
	}
	return s
}
```

- [ ] **Шаг 4.2: Запустить тесты — убедиться, что компиляция ломается**

```bash
go test ./internal/sink/... 2>&1 | head -20
```

Ожидаемый результат: `cannot use &config.ObsidianSinkConfig{...} as type sink.ObsidianConfig`.

- [ ] **Шаг 4.3: Обновить `ObsidianSink` в `obsidian.go`**

Заменить `ObsidianConfig` и структуру `ObsidianSink`. Добавить импорт `config`. Итоговый вид начала файла:

```go
package sink

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)

// ObsidianSink сохраняет задачи в Obsidian-файл с секционной организацией.
// Задачи вставляются в секции ## (по имени источника) и подсекции ### (по теме).
type ObsidianSink struct {
	mu         sync.Mutex
	vaultPath  string
	targetFile string
}

// NewObsidianSink создаёт новый ObsidianSink с указанной конфигурацией.
func NewObsidianSink(cfg *config.ObsidianSinkConfig) (*ObsidianSink, error) {
	if cfg.VaultPath == "" {
		return nil, fmt.Errorf("vault_path обязателен")
	}
	if cfg.TargetFile == "" {
		return nil, fmt.Errorf("target_file обязателен")
	}
	return &ObsidianSink{vaultPath: cfg.VaultPath, targetFile: cfg.TargetFile}, nil
}

// Name возвращает имя синка для логирования.
func (s *ObsidianSink) Name() string { return "obsidian" }
```

- [ ] **Шаг 4.4: Обновить метод `Save` — заменить обращения к старому полю `config`**

В методе `Save` заменить:
- `s.config.VaultPath` → `s.vaultPath`
- `s.config.TargetFile` → `s.targetFile`

Итоговый метод Save (только изменённые строки):

```go
func (s *ObsidianSink) Save(_ context.Context, tasks []model.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.vaultPath, s.targetFile)
	// ... остальное без изменений ...
```

- [ ] **Шаг 4.5: Запустить тесты**

```bash
go test ./internal/sink/... -run "TestObsidian" -v
go test ./internal/config/... -v
go build ./...
```

Ожидаемый результат: все тесты PASS, компиляция SUCCESS.

- [ ] **Шаг 4.6: Коммит**

```bash
git add internal/sink/obsidian.go internal/sink/obsidian_test.go
git commit -m "refactor: ObsidianSink принимает *config.ObsidianSinkConfig, удалить дублирующий sink.ObsidianConfig"
```

---

## Задача 5: Обновить `SuperProductivitySink` — новый конструктор и структура

**Файлы:**
- Изменить: `internal/sink/super_productivity.go`
- Изменить: `internal/sink/super_productivity_test.go`

- [ ] **Шаг 5.1: Обновить тест — использовать `*config.SuperProductivityConfig` (красная фаза)**

В `internal/sink/super_productivity_test.go` добавить импорт `config` и заменить все вызовы `NewSuperProductivitySink`:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)
```

Каждый `NewSuperProductivitySink(SuperProductivityConfig{...})` заменить на `NewSuperProductivitySink(&config.SuperProductivityConfig{...})`. Например, первое вхождение:

```go
s := NewSuperProductivitySink(&config.SuperProductivityConfig{
    BaseURL:                server.URL,
    CreateProjectPerSource: true,
})
```

- [ ] **Шаг 5.2: Запустить тесты — убедиться, что компиляция ломается**

```bash
go test ./internal/sink/... 2>&1 | head -20
```

Ожидаемый результат: `cannot use &config.SuperProductivityConfig{...} as type sink.SuperProductivityConfig`.

- [ ] **Шаг 5.3: Обновить `SuperProductivitySink` в `super_productivity.go`**

Удалить тип `SuperProductivityConfig`. Обновить структуру и конструктор. Добавить импорт `config`. Итоговый вид структуры и конструктора:

```go
import (
	// ... существующие импорты ...
	"github.com/anadale/huskwoot/internal/config"
)

// SuperProductivitySink сохраняет задачи в Super Productivity через Local Rest API.
type SuperProductivitySink struct {
	mu                     sync.Mutex
	client                 *http.Client
	baseURL                string // нормализованный URL (без trailing slash)
	createProjectPerSource bool
	projects               map[string]string // project name -> projectId
}

// NewSuperProductivitySink создает новый sink с указанной конфигурацией.
func NewSuperProductivitySink(cfg *config.SuperProductivityConfig) *SuperProductivitySink {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:3001"
	}
	return &SuperProductivitySink{
		client:                 &http.Client{Timeout: 10 * time.Second},
		baseURL:                strings.TrimRight(baseURL, "/"),
		createProjectPerSource: cfg.CreateProjectPerSource,
		projects:               make(map[string]string),
	}
}

// Name возвращает имя синка для логирования.
func (s *SuperProductivitySink) Name() string { return "super-productivity" }
```

- [ ] **Шаг 5.4: Обновить внутренние методы — заменить `s.config.*` на поля структуры**

Найти все вхождения `s.config.BaseURL` и `s.config.CreateProjectPerSource` и заменить:
- `s.config.BaseURL` → `s.baseURL`
- `s.config.CreateProjectPerSource` → `s.createProjectPerSource`

Затронутые методы: `getProjectName`, `getProjectByName`, `createProject`, `doWithRetry`, `makeRequest`.

Затронутые вхождения:
```
s.config.BaseURL               → s.baseURL                (в getProjectByName, createProject, makeRequest)
s.config.CreateProjectPerSource → s.createProjectPerSource (в getProjectName)
```

- [ ] **Шаг 5.5: Запустить тесты**

```bash
go test ./internal/sink/... -v
go build ./...
```

Ожидаемый результат: все тесты PASS, компиляция SUCCESS.

- [ ] **Шаг 5.6: Коммит**

```bash
git add internal/sink/super_productivity.go internal/sink/super_productivity_test.go
git commit -m "refactor: SuperProductivitySink принимает *config.SuperProductivityConfig, удалить дублирующий sink.SuperProductivityConfig"
```

---

## Задача 6: Обновить `main.go` — presence-based инициализация и логирование

**Файлы:**
- Изменить: `cmd/huskwoot/main.go`

- [ ] **Шаг 6.1: Заменить блок инициализации синков**

Найти в `run()` текущий блок (примерно строки 132–148):

```go
obsidianSink, err := sink.NewObsidianSink(sink.ObsidianConfig{
    VaultPath:  cfg.Sinks.Obsidian.VaultPath,
    TargetFile: cfg.Sinks.Obsidian.TargetFile,
})
if err != nil {
    return fmt.Errorf("инициализация ObsidianSink: %w", err)
}
sinks := []model.Sink{obsidianSink}

// Инициализируем SuperProductivitySink, если конфигурация присутствует
if cfg.Sinks.SuperProductivity.BaseURL != "" {
    superProductivitySink := sink.NewSuperProductivitySink(sink.SuperProductivityConfig{
        BaseURL:                cfg.Sinks.SuperProductivity.BaseURL,
        CreateProjectPerSource: cfg.Sinks.SuperProductivity.CreateProjectPerSource,
    })
    sinks = append(sinks, superProductivitySink)
}
```

Заменить на:

```go
var sinks []model.Sink

if cfg.Sinks.Obsidian != nil {
    obsidianSink, err := sink.NewObsidianSink(cfg.Sinks.Obsidian)
    if err != nil {
        return fmt.Errorf("инициализация ObsidianSink: %w", err)
    }
    sinks = append(sinks, obsidianSink)
}

if cfg.Sinks.SuperProductivity != nil {
    sinks = append(sinks, sink.NewSuperProductivitySink(cfg.Sinks.SuperProductivity))
}
```

- [ ] **Шаг 6.2: Добавить логирование активных синков и нотификаторов**

Найти строку `logger.Info("Huskwoot запущен, ожидание сообщений")` и добавить блок логирования перед ней:

```go
for _, s := range sinks {
    logger.Info("синк активен", "name", s.Name())
}
for _, n := range notifiers {
    logger.Info("нотификатор активен", "name", n.Name())
}

logger.Info("Huskwoot запущен, ожидание сообщений")
```

- [ ] **Шаг 6.3: Проверить компиляцию**

```bash
go build ./cmd/huskwoot/
```

Ожидаемый результат: SUCCESS.

- [ ] **Шаг 6.4: Запустить все тесты проекта**

```bash
go test ./...
```

Ожидаемый результат: все тесты PASS.

- [ ] **Шаг 6.5: Запустить линтер**

```bash
go vet ./...
```

Ожидаемый результат: нет предупреждений.

- [ ] **Шаг 6.6: Коммит**

```bash
git add cmd/huskwoot/main.go
git commit -m "feat: presence-based инициализация синков, логирование активных компонентов при запуске"
```

---

## Самопроверка плана

**Покрытие спека:**
- ✅ Presence-based отключение через pointer-типы (Задача 3)
- ✅ SuperProductivity не зависит от base_url, имеет дефолт (Задачи 3, 5)
- ✅ `Name()` в интерфейсах (Задача 1)
- ✅ `Name()` реализован на всех 4 типах (Задача 2)
- ✅ Устранение дублирования ObsidianConfig (Задача 4)
- ✅ Устранение дублирования SuperProductivityConfig (Задача 5)
- ✅ Логирование активных компонентов (Задача 6)

**Зависимости задач:**
- Задача 1 → независима
- Задача 2 → зависит от Задачи 1 (интерфейс должен требовать Name())
- Задача 3 → независима
- Задача 4 → зависит от Задач 2 и 3
- Задача 5 → зависит от Задач 2 и 3
- Задача 6 → зависит от Задач 3, 4 и 5
