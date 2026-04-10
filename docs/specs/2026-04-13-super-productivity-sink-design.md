# Спецификация: Sink для Super Productivity

## Цель
Реализовать sink, который сохраняет извлечённые задачи в приложение Super Productivity через Local Rest API.

## Требования
1. Использовать Local Rest API Super Productivity (http://localhost:3001)
2. Поддерживать создание отдельного проекта для каждого источника
3. Хранить контекст задачи в поле notes
4. Возможность переопределить базовый URL в конфигурации
5. Надёжная обработка ошибок и повторные попытки

## Детали реализации

### Конфигурация

Добавляем новую секцию в `internal/config/config.go`:

```go
// SuperProductivityConfig описывает настройки sink'а для Super Productivity.
type SuperProductivityConfig struct {
    // BaseURL — базовый адрес Local Rest API (по умолчанию http://localhost:3001).
    BaseURL string `toml:"base_url"`
    // CreateProjectPerSource — создавать ли отдельный проект для каждого источника.
    CreateProjectPerSource bool `toml:"create_project_per_source"`
}

// SinksConfig объединяет все хранилища задач (дополнение).
type SinksConfig struct {
    Obsidian               ObsidianSinkConfig     `toml:"obsidian"`
    SuperProductivity      SuperProductivityConfig `toml:"super_productivity"`
}
```

### Модель данных

Используем существующую структуру `model.Task`:
- `Summary` → заголовок задачи в Super Productivity
- `Origin.Context` → поле notes задачи
- `Origin.Account` → имя проекта при `CreateProjectPerSource=true`
- `Deadline` → дедлайн задачи

### Реализация sink'а

Создаём `internal/sink/super_productivity.go` с реализацией интерфейса `model.Sink`:

```go
// SuperProductivitySink сохраняет задачи в Super Productivity через Local Rest API.
type SuperProductivitySink struct {
    mu        sync.Mutex
    client    *http.Client
    config    SuperProductivityConfig
    projects  map[string]string // Origin.Account -> projectId
}

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

### HTTP API взаимодействие

Используем следующие эндпоинты Local Rest API:
- `GET /project` — получение списка проектов
- `POST /project` — создание нового проекта
- `POST /task` — создание задачи

### Логика работы

1. Для каждой задачи определяем целевой проект:
   - При `CreateProjectPerSource=true`: используем `Origin.Account` как имя проекта
   - При `CreateProjectPerSource=false`: используем общий проект "Huskwoot"

2. Проверяем наличие проекта:
   - Ищем в кэше `s.projects`
   - При отсутствии делаем запрос к `/project`
   - При отсутствии в API создаём новый проект

3. Сохраняем ID проекта в кэше

4. Создаём задачу через `/task` с полями:
   - `title`: `task.Summary`
   - `notes`: `task.Origin.Context`
   - `projectId`: найденный/созданный проект
   - `dueDate`: `task.Deadline.Unix() * 1000` (в миллисекундах)

### Обработка ошибок

- При временных ошибках соединения — 3 попытки с экспоненциальной задержкой
- Логирование всех ошибок через `slog`
- При ошибке одной задачи — продолжаем обработку остальных
- Контекстные ошибки (отмена) — немедленное прерывание

### Интеграция

1. Добавляем sink в `cmd/huskwoot/main.go`:

```go
superProductivitySink, err := sink.NewSuperProductivitySink(cfg.Sinks.SuperProductivity)
if err != nil {
    return fmt.Errorf("инициализация SuperProductivitySink: %w", err)
}
sinks = append(sinks, superProductivitySink)
```

2. Обновляем `.toml` конфигурацию:

```toml
[sinks.super_productivity]
base_url = "http://localhost:3001"
create_project_per_source = true
```

## Тестирование

1. Покрытие unit-тестами:
   - Создание sink'а с валидной/невалидной конфигурацией
   - Логика определения проекта
   - Обработка различных сценариев API (успех, ошибка, повторная попытка)

2. Использование `httptest.Server` для мокирования API

3. Проверка потокобезопасности через `go test -race`

## Безопасность

- Таймауты на все HTTP-запросы
- Ограничение размера ответов
- Валидация входных данных
- Защита от DoS через ограничение частоты запросов

## Зависимости

- Стандартная библиотека Go (`net/http`, `encoding/json`, `sync`, `context`)
- Новых внешних зависимостей не требуется

## Ограничения

- Требуется запущенное приложение Super Productivity
- API должно быть включено в настройках Super Productivity
- Работает только с локальной установкой (localhost)

## Дальнейшее развитие

- Поддержка аутентификации (если будет добавлена в Local API)
- Синхронизация статуса задач в обратную сторону
- Поддержка тегов и субзадач
- Интеграция с облачной версией Super Productivity