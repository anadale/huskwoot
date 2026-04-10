# Дизайн: управление синками и нотификаторами

**Дата:** 2026-04-13  
**Статус:** утверждён

## Цель

1. Добавить возможность отключать синки и нотификаторы через конфиг (без явного флага — через присутствие секции).
2. Выводить в лог при запуске список активных синков и нотификаторов.
3. Устранить дублирование конфигурационных типов между пакетами `config` и `sink`.
4. Убрать зависимость включения `SuperProductivitySink` от наличия `base_url`.

---

## Секция 1: Presence-based управление через pointer-типы

### Принцип

Наличие секции `[sinks.obsidian]` или `[sinks.super_productivity]` в `config.toml` означает включение соответствующего синка. Чтобы отключить — закомментировать секцию.

Реализуется через pointer-типы: BurntSushi/toml оставляет поле `nil`, если секция отсутствует в файле.

### Изменения в `internal/config/config.go`

```go
type SinksConfig struct {
    Obsidian          *ObsidianSinkConfig      `toml:"obsidian"`
    SuperProductivity *SuperProductivityConfig  `toml:"super_productivity"`
}
```

### Изменения валидации

- Проверки `vault_path` и `target_file` выполняются только если `cfg.Sinks.Obsidian != nil`.
- Для `cfg.Sinks.SuperProductivity != nil` применяется дефолт `base_url = "http://localhost:3001"` в `validate()`.
- Требование «хотя бы один синк должен быть включён» — не вводится (оставляем на усмотрение пользователя).

---

## Секция 2: Устранение дублирования конфигурационных типов

### Проблема

`sink.ObsidianConfig` и `config.ObsidianSinkConfig` — одинаковые структуры с одинаковыми полями.  
`sink.SuperProductivityConfig` и `config.SuperProductivityConfig` — аналогично.

`main.go` делает ручной field-by-field маппинг между ними.

### Решение

Конструкторы синков принимают типы из пакета `config`:

```go
// internal/sink/obsidian.go
func NewObsidianSink(cfg *config.ObsidianSinkConfig) (*ObsidianSink, error)

// internal/sink/super_productivity.go
func NewSuperProductivitySink(cfg *config.SuperProductivityConfig) *SuperProductivitySink
```

`sink.ObsidianConfig` и `sink.SuperProductivityConfig` удаляются.

`main.go` передаёт указатели напрямую:

```go
if cfg.Sinks.Obsidian != nil {
    obsidianSink, err := sink.NewObsidianSink(cfg.Sinks.Obsidian)
    // ...
}
if cfg.Sinks.SuperProductivity != nil {
    spSink := sink.NewSuperProductivitySink(cfg.Sinks.SuperProductivity)
    // ...
}
```

### Направление зависимости

`sink` → `config` допустимо: пакет `config` ничего из `sink` не импортирует.

---

## Секция 3: `Name() string` в интерфейсах и логирование при запуске

### Изменения интерфейсов в `internal/model/interfaces.go`

```go
type Sink interface {
    Save(ctx context.Context, tasks []Task) error
    Name() string
}

type Notifier interface {
    Notify(ctx context.Context, tasks []Task) error
    Name() string
}
```

### Реализации `Name()`

| Тип | Возвращаемое значение |
|-----|-----------------------|
| `ObsidianSink` | `"obsidian"` |
| `SuperProductivitySink` | `"super-productivity"` |
| `TelegramReactionNotifier` | `"telegram-reaction:<watcherID>"` |
| `TelegramNotifier` (DM) | `"telegram-dm"` |

### Логирование в `main.go`

После построения слайсов `sinks` и `notifiers`:

```go
for _, s := range sinks {
    logger.Info("синк активен", "name", s.Name())
}
for _, n := range notifiers {
    logger.Info("нотификатор активен", "name", n.Name())
}
```

---

## Затронутые файлы

| Файл | Изменение |
|------|-----------|
| `internal/config/config.go` | pointer-типы в `SinksConfig`, обновление валидации |
| `internal/model/interfaces.go` | добавить `Name() string` в `Sink` и `Notifier` |
| `internal/sink/obsidian.go` | удалить `ObsidianConfig`, конструктор принимает `*config.ObsidianSinkConfig` |
| `internal/sink/super_productivity.go` | удалить `SuperProductivityConfig`, конструктор принимает `*config.SuperProductivityConfig` |
| `internal/sink/telegram_notifier.go` | добавить `Name()` |
| `internal/sink/telegram_reaction.go` | добавить `Name()` |
| `cmd/huskwoot/main.go` | presence-based логика, логирование активных компонентов |
| `internal/sink/*_test.go` | обновить тесты под новые сигнатуры конструкторов |
| `internal/config/config_test.go` | обновить тесты валидации под pointer-типы |

---

## Тестирование

- Существующие тесты синков обновляются под новые сигнатуры конструкторов.
- Тесты валидации конфига проверяют: Obsidian nil → нет ошибки, Obsidian non-nil + пустой vault_path → ошибка.
- `Name()` — покрывается существующими тестами косвенно (или отдельными unit-тестами если сочтём нужным).
