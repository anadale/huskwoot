# Регулярные сводки о незавершённых задачах

## Overview

Бот должен сам напоминать владельцу о его открытых задачах — не только молча хранить их. Сейчас задачи живут в `TaskStore`, но без активного уведомления пользователь теряет из виду просроченные обещания, забывает про сегодняшние приоритеты и не видит ближайшие планы.

Цель: раз-три раза в день бот собирает сводку по открытым задачам всех проектов и отправляет её в Telegram DM владельца. Сводка состоит из четырёх секций:

- **Пропущенные** — задачи, чей дедлайн уже прошёл (`Deadline < at`, где `at` — момент срабатывания).
- **Нужно выполнить** — задачи с дедлайном в текущем дне (`[startOfDay, endOfDay)` в таймзоне пользователя).
- **Планы** — задачи с дедлайном в пределах настраиваемого горизонта (default `7d`).
- **Без срока** — задачи без дедлайна; лимит настраивается (default `0`, т.е. по умолчанию скрыты).

Расписание — именованные слоты `morning` / `afternoon` / `evening` в `[reminders.schedule]`. Отсутствие секции полностью выключает фичу. Сводки отправляются только в рабочие дни (`[datetime].weekdays`). При старте в середине дня пропущенные слоты не «догоняются» — стандартное cron-поведение.

Email-доставка и другие каналы — задел на будущее; реализуется общий интерфейс `model.SummaryDeliverer`, но единственная реализация сейчас — Telegram.

## Context (from discovery)

**Файлы/компоненты, которые уже есть:**

- [internal/model/types.go](../../internal/model/types.go), [internal/model/interfaces.go](../../internal/model/interfaces.go) — `Task` с `Deadline *time.Time`/`Status`, `TaskStore` с `ListTasks(projectID=0, TaskFilter{Status:"open"})`, `DefaultProjectID()` для Inbox.
- [internal/config/config.go](../../internal/config/config.go) — `DateTimeConfig`, `[notify.telegram]` с `WatcherID`/`ChatID`, `[datetime].weekdays`.
- [cmd/huskwoot/main.go](../../cmd/huskwoot/main.go) — `resolveTimezone`, `parseWeekdays`, сборка `bots map[string]*tgbotapi.BotAPI` ([main.go:356-380](../../cmd/huskwoot/main.go#L356-L380)), `nowFn := func() time.Time { return time.Now().In(loc) }` ([main.go:86](../../cmd/huskwoot/main.go#L86)).
- [internal/sink/telegram_notifier.go](../../internal/sink/telegram_notifier.go) и [internal/sink/telegram_notifier_test.go](../../internal/sink/telegram_notifier_test.go) — эталон реализации Telegram-доставки и httptest-паттерна для тестов (`newTGTestServer`, `newTestBot`).
- [CLAUDE.md](../../CLAUDE.md) — проектные соглашения (TDD, интерфейсы в `model`, моки вручную в тестах, русскоязычные ошибки и коммиты).

**Выбранные решения (подтверждены в brainstorm):**

- Секции и границы: `Deadline < at` для Overdue (точное время), `[startOfDay, endOfDay)` для Today, `[endOfDay, startOfDay + horizon)` для Upcoming, `Deadline == nil` с лимитом для Undated; за горизонтом — отбрасываем.
- Группировка по проектам внутри секций (`ProjectGroup`); Inbox — первым в каждой секции; остальные — по имени проекта asc; внутри группы — по `Deadline` asc (для Undated — по `CreatedAt` asc, старейшие сверху).
- Scheduler — свой, без cron-библиотеки: чистая `nextSlot(from)`, `sleepUntil(ctx, t)` через `time.NewTimer` + `select` с `ctx.Done`.
- Конфиг: отсутствие `[reminders.schedule]` = фича выключена; `morning` обязателен при наличии секции (на него ссылается `send_when_empty="morning"`); `afternoon`/`evening` пустые = слот выключен.
- Ошибка одного `SummaryDeliverer` не блокирует остальных (только лог).
- Telegram-формат: plain text (упрощает обрезку по 4096 символов), emoji-заголовок `🌅`/`☀️`/`🌙`, обрезка с хвостом `… и ещё N задач`.

**Архитектурные особенности:**

- Все новые типы (`Summary`, `ProjectGroup`, `SummaryDeliverer`) живут в `internal/model/` — реализации зависят только от интерфейса, нет импортных циклов.
- `reminder.SummaryBuilder` — локальный интерфейс пакета `reminder`, объявленный в `scheduler.go` (чтобы тесты scheduler могли использовать мок без выноса интерфейса в `model`).
- `now func() time.Time` — та же функция, что передаётся в агент и экстрактор (пользовательская таймзона из `[datetime].timezone`).
- SQLite-миграций нет: вся выборка через `TaskStore.ListTasks` + `ListProjects`, бакетизация в памяти (десятки-сотни задач — оптимизация излишня).

## Development Approach

- **testing approach**: TDD (проектный дефолт по CLAUDE.md — тесты пишутся перед реализацией; табличные тесты Go-стиля; моки вручную).
- каждый task завершается зелёным `go test ./...` и `go vet ./...`
- без расширения API существующих интерфейсов (не трогаем `model.Notifier` и `TaskStore`)
- при расхождении реального поведения и плана — обновлять этот файл сразу (`➕` для новых задач, `⚠️` для блокеров)
- сборка артефактов — строго в `bin/`

## Testing Strategy

- **unit-тесты** Go — единственный уровень автоматических тестов в проекте
- фокусы:
  - `reminder.Scheduler.nextSlot` — чистая функция, все ветки (до/между/после слотов, пятница→понедельник, только `morning`, catch-up)
  - `reminder.Scheduler.shouldSendEmpty` — матрица 3×3 (`always`/`never`/`morning` × `morning`/`afternoon`/`evening`)
  - `reminder.Scheduler.Run` — fake clock + моки builder/deliverer: одиночный `fire`, ошибка одного deliverer не блокирует других, cancel ctx
  - `reminder.Builder.Build` — распределение по бакетам, сортировка, лимит Undated, `UndatedTotal`, `IsEmpty`
  - `sink.TelegramSummaryDeliverer.Deliver` — через `httptest` (по образцу `telegram_notifier_test.go`): форматирование секций, пустая сводка, обрезка до 4096 символов
  - `config.Load` — отсутствие `[reminders]`, обязательность `morning`, невалидные `HH:MM`, `plans_horizon`, `send_when_empty`, fallback `[reminders.telegram]` → `[notify.telegram]`
- ручная проверка — по сценариям из Post-Completion после merge

## Progress Tracking

- `[x]` — сделано; `➕` — добавлено по ходу; `⚠️` — блокер
- обновлять сразу после каждого task

## Solution Overview

### Поток сводки

```
time → Scheduler.Run loop
  nextSlot(now)  → (t, "morning"|"afternoon"|"evening")
  sleepUntil(ctx, t)
  fire(ctx, slot, at):
    summary := Builder.Build(ctx, slot, at)
      ListTasks(status=open)  → бакеты Overdue/Today/Upcoming/Undated
      ListProjects           → группировка по ProjectGroup
      сортировка: Inbox first, далее by ProjectName asc; внутри по Deadline/CreatedAt asc
      IsEmpty := all sections empty
    if summary.IsEmpty && !shouldSendEmpty(slot): log skip; continue
    for each deliverer in []SummaryDeliverer:
        deliverer.Deliver(ctx, summary)  // ошибка одного не блокирует других
```

### Границы пакетов

```
internal/model/
  +Summary, +ProjectGroup           (types.go)
  +SummaryDeliverer                 (interfaces.go)

internal/reminder/      НОВЫЙ
  types.go              Config, BuilderConfig, локальный SummaryBuilder-интерфейс
  builder.go            Builder (TaskStore → model.Summary)
  builder_test.go
  scheduler.go          Scheduler (loop + nextSlot + sleepUntil + fire)
  scheduler_test.go

internal/sink/
  telegram_summary.go        TelegramSummaryDeliverer
  telegram_summary_test.go

internal/config/
  config.go             +RemindersConfig, +ReminderSchedule, +ReminderTelegramConfig + валидация
  config_test.go        +новые кейсы

cmd/huskwoot/main.go    инициализация reminder-а (только если Schedule != nil)

CLAUDE.md               новый раздел «Reminder — сводки задач» (соглашения)
```

## Technical Details

### Доменные типы (`internal/model/types.go`)

```go
// Summary — содержимое регулярной сводки, сгруппированное по секциям.
type Summary struct {
    GeneratedAt  time.Time
    Slot         string           // "morning" | "afternoon" | "evening"
    Overdue      []ProjectGroup
    Today        []ProjectGroup
    Upcoming     []ProjectGroup
    Undated      []ProjectGroup   // задачи без дедлайна, уже подрезанные UndatedLimit
    UndatedTotal int              // общее количество задач без дедлайна (для «показано N из M»)
    IsEmpty      bool             // true если все секции пусты
}

// ProjectGroup — задачи одного проекта внутри секции.
type ProjectGroup struct {
    ProjectID   int64
    ProjectName string
    Tasks       []Task
}
```

### Интерфейс доставки (`internal/model/interfaces.go`)

```go
// SummaryDeliverer доставляет сводку в один канал (Telegram, email, ...).
type SummaryDeliverer interface {
    Deliver(ctx context.Context, summary Summary) error
    Name() string
}
```

### Конфиг (`internal/config/config.go`)

```go
type RemindersConfig struct {
    PlansHorizonRaw string                  `toml:"plans_horizon"`
    PlansHorizon    time.Duration           `toml:"-"`
    UndatedLimit    int                     `toml:"undated_limit"`
    SendWhenEmpty   string                  `toml:"send_when_empty"`
    Schedule        *ReminderSchedule       `toml:"schedule"`
    Telegram        *ReminderTelegramConfig `toml:"telegram"`
}

type ReminderSchedule struct {
    Morning   string `toml:"morning"`
    Afternoon string `toml:"afternoon"`
    Evening   string `toml:"evening"`
}

type ReminderTelegramConfig struct {
    WatcherID string `toml:"watcher_id"`
    ChatID    int64  `toml:"chat_id"`
}
```

Поле в `Config`:

```go
type Config struct {
    ...
    Reminders RemindersConfig `toml:"reminders"`
}
```

**Валидация** (в `config.validate`, только если `Reminders.Schedule != nil`):

- `Schedule.Morning` непуст, формат `HH:MM` (часы 0–23, минуты 0–59).
- `Schedule.Afternoon`/`Evening` — если непусто, валидный `HH:MM`.
- `PlansHorizonRaw` пуст → `PlansHorizon = 7*24h`; иначе `time.ParseDuration`; отрицательное или ноль → ошибка.
- `UndatedLimit < 0` → ошибка; ноль допустим.
- `SendWhenEmpty` пуст → `"morning"`; иначе должно быть одно из `always`/`never`/`morning`.
- Для Telegram: если `Reminders.Telegram == nil`, используем `Notify.Telegram.WatcherID` и `Notify.Telegram.ChatID`; если оба отсутствуют — ошибка.
- `WatcherID` (после fallback) должен совпадать с одним из `Channels.Telegrams[i].ID`, если их больше одного; `ChatID != 0`.

Хелпер `parseHHMM(s string) (hour, minute int, err error)` вынести в `config.go` — нужен как для валидации, так и для `reminder.Config` (см. ниже).

### `reminder.Config` и `BuilderConfig`

```go
// Config передаётся в reminder.New из main.go.
type Config struct {
    Slots         []Slot      // включённые слоты в естественном порядке morning→afternoon→evening
    SendWhenEmpty string      // "always" | "never" | "morning"
}

// Slot — один активный слот расписания.
type Slot struct {
    Name   string // "morning" | "afternoon" | "evening"
    Hour   int
    Minute int
}

// BuilderConfig настраивает Builder.
type BuilderConfig struct {
    PlansHorizon time.Duration
    UndatedLimit int
}
```

### `reminder.Scheduler.nextSlot`

```go
// nextSlot находит ближайшее будущее срабатывание строго после from.
// Учитывает workdays (только рабочие дни).
// Безопасность: не более 14 итераций по дням (достаточно для любой комбинации weekdays).
func (s *Scheduler) nextSlot(from time.Time) (time.Time, string) {
    for i := 0; i < 14; i++ {
        day := from.AddDate(0, 0, i)
        if !isWorkday(day.Weekday(), s.workdays) { continue }
        for _, slot := range s.cfg.Slots { // отсортированы по времени
            candidate := time.Date(day.Year(), day.Month(), day.Day(), slot.Hour, slot.Minute, 0, 0, s.loc)
            if candidate.After(from) {
                return candidate, slot.Name
            }
        }
    }
    // недостижимо при непустых workdays и slots — валидируется в конфиге
    return time.Time{}, ""
}
```

### `reminder.Scheduler.shouldSendEmpty`

```go
func (s *Scheduler) shouldSendEmpty(slot string) bool {
    switch s.cfg.SendWhenEmpty {
    case "always":
        return true
    case "never":
        return false
    case "morning":
        return slot == "morning"
    }
    return false
}
```

### `reminder.Builder.Build`

```go
func (b *Builder) Build(ctx context.Context, slot string, at time.Time) (model.Summary, error) {
    tasks, err := b.taskStore.ListTasks(ctx, 0, model.TaskFilter{Status: "open"})
    if err != nil { return model.Summary{}, fmt.Errorf("выборка открытых задач: %w", err) }

    projects, err := b.taskStore.ListProjects(ctx)
    if err != nil { return model.Summary{}, fmt.Errorf("выборка проектов: %w", err) }
    projectByID := make(map[int64]model.Project, len(projects))
    for _, p := range projects { projectByID[p.ID] = p }

    loc := at.Location()
    startOfDay := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
    endOfDay   := startOfDay.AddDate(0, 0, 1)
    planLimit  := startOfDay.Add(b.cfg.PlansHorizon)

    var overdue, today, upcoming []model.Task
    var undatedAll []model.Task

    for _, t := range tasks {
        if t.Deadline == nil {
            undatedAll = append(undatedAll, t)
            continue
        }
        d := *t.Deadline
        switch {
        case d.Before(at):       overdue  = append(overdue,  t)
        case d.Before(endOfDay): today    = append(today,    t)
        case d.Before(planLimit): upcoming = append(upcoming, t)
        // else: за горизонтом — отбрасываем
        }
    }

    // Undated: стабильная сортировка по CreatedAt asc, потом подрезаем
    sort.SliceStable(undatedAll, func(i, j int) bool {
        return undatedAll[i].CreatedAt.Before(undatedAll[j].CreatedAt)
    })
    undatedTotal := len(undatedAll)
    undated := undatedAll
    if b.cfg.UndatedLimit >= 0 && len(undated) > b.cfg.UndatedLimit {
        undated = undated[:b.cfg.UndatedLimit]
    }

    summary := model.Summary{
        GeneratedAt:  at,
        Slot:         slot,
        Overdue:      b.groupByProject(overdue,  projectByID, sortByDeadlineAsc),
        Today:        b.groupByProject(today,    projectByID, sortByDeadlineAsc),
        Upcoming:     b.groupByProject(upcoming, projectByID, sortByDeadlineAsc),
        Undated:      b.groupByProject(undated,  projectByID, sortByCreatedAtAsc),
        UndatedTotal: undatedTotal,
    }
    summary.IsEmpty = len(summary.Overdue) == 0 && len(summary.Today) == 0 &&
                      len(summary.Upcoming) == 0 && len(summary.Undated) == 0
    return summary, nil
}
```

`groupByProject` собирает `map[int64][]Task`, сортирует каждую группу заданным компаратором, группы выводит отсортированно: `ProjectID == taskStore.DefaultProjectID()` → первой, остальные — по `ProjectName` asc.

### Telegram-формат (`sink/telegram_summary.go`)

```
🌅 Утренняя сводка — 17.04.2026

🔴 Пропущенные
  [work]
    — подготовить отчёт 📅 просрочено с 16.04 · #отчётность
  [inbox]
    — ответить Ане 📅 просрочено с 15.04

📋 Нужно выполнить
  [work]
    — созвон с клиентом 📅 17:00 · #клиент-iOS

🗓 Планы
  [work]
    — релиз 1.4 📅 21.04

📦 Без срока (показано 3 из 12)
  [inbox]
    — уточнить требования по проекту X
```

- Заголовок: `{emoji} {Утренняя|Дневная|Вечерняя} сводка — DD.MM.YYYY`; emoji по слоту: `🌅` / `☀️` / `🌙`.
- Пустая сводка: одно сообщение `{emoji} {заголовок слота} — DD.MM.YYYY\n\nВсё чисто 👌 задач нет`.
- Обрезка: если итог длиннее 4096 символов, удалять строки задач с конца, пока не влезет заголовок + все секции + строка `… и ещё N задач` (N — сколько строк задач удалено). Гарантия: результат ≤ 4096.
- Parse mode не указывается (plain text) — экранировать Markdown/HTML не надо.

## What Goes Where

- **Implementation Steps** (`[ ]` чекбоксы): всё внутри репозитория — типы, пакет `reminder/`, Telegram-деливерер, конфиг + валидация, интеграция в `main.go`, тесты, документация.
- **Post-Completion** (без чекбоксов): ручная проверка с реальным Telegram-ботом; правки `config.toml` пользователя; наблюдение за логами первых суток работы.

## Implementation Steps

### Task 1: Доменные типы Summary/ProjectGroup/SummaryDeliverer

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/interfaces.go`

- [x] добавить в `internal/model/types.go` структуры `Summary` и `ProjectGroup` с документирующими комментариями (поля `GeneratedAt`, `Slot`, `Overdue`, `Today`, `Upcoming`, `Undated`, `UndatedTotal`, `IsEmpty`; `ProjectID`, `ProjectName`, `Tasks`)
- [x] добавить в `internal/model/interfaces.go` интерфейс `SummaryDeliverer` с методами `Deliver(ctx context.Context, summary Summary) error` и `Name() string`
- [x] запустить `go build ./...` — пакет должен собираться (тестов на сами типы не нужно: это value-объекты без поведения)
- [x] запустить `go vet ./...` — без предупреждений

### Task 2: Конфиг `[reminders]` и валидация

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] добавить в `config.go` структуры `RemindersConfig`, `ReminderSchedule`, `ReminderTelegramConfig`; поле `Reminders RemindersConfig` в `Config`
- [x] добавить хелпер `parseHHMM(s string) (int, int, error)` — 2 цифры + `:` + 2 цифры, часы 0–23, минуты 0–59
- [x] расширить `Config.validate`: если `Reminders.Schedule == nil` — пропустить (фича выключена); иначе валидировать `Morning` (обязателен, валидный `HH:MM`), `Afternoon`/`Evening` (валидный или пусто), `PlansHorizonRaw` (default 7*24h, отрицательное или ноль → ошибка), `UndatedLimit >= 0`, `SendWhenEmpty` ∈ `{"","always","never","morning"}` (пустое → `"morning"`), Telegram-таргет (fallback на `Notify.Telegram`, проверить `ChatID != 0` и совпадение `WatcherID`)
- [x] написать тесты в `config_test.go` (table-driven): секции нет → `Reminders.Schedule == nil`, ОК; `[reminders.schedule]` есть, `morning=""` → ошибка; некорректный `HH:MM` (`"25:00"`, `"9:0"`, `"abc"`) → ошибка; `plans_horizon="-1h"` или `"0"` → ошибка; `undated_limit=-1` → ошибка; `send_when_empty="bogus"` → ошибка; `send_when_empty=""` → нормализация в `"morning"`; `[reminders.telegram]` отсутствует → fallback берёт значения из `[notify.telegram]`
- [x] тесты на разобранный `PlansHorizon`: `"7d"` не парсится `time.ParseDuration` — уточнить формат (`"168h"`). Проверить в тесте, что `plans_horizon = "168h"` → `7*24h`, и актуализировать документ, если нужно поддержать `"d"` (см. «⚠️-заметка» ниже)
- [x] `go test ./internal/config/... && go vet ./internal/config/...`

⚠️-заметка для Task 2: `time.ParseDuration` **не поддерживает суффикс `d`**. План выше обещает default `7d` — это значение во внутреннем представлении, но в TOML пользователь задаёт `plans_horizon = "168h"`. В документации (`CLAUDE.md`, Task 7) зафиксировать формат явно.

### Task 3: `reminder/types.go` и `reminder.Builder`

**Files:**
- Create: `internal/reminder/types.go`
- Create: `internal/reminder/builder.go`
- Create: `internal/reminder/builder_test.go`

- [x] создать `internal/reminder/types.go` — объявить `Config`, `Slot`, `BuilderConfig` и локальный интерфейс `SummaryBuilder interface { Build(ctx context.Context, slot string, at time.Time) (model.Summary, error) }`
- [x] создать `internal/reminder/builder.go` — `Builder` с `NewBuilder(store model.TaskStore, cfg BuilderConfig, now func() time.Time) *Builder` и методом `Build`; реализовать распределение по бакетам, сортировки, группировку по проектам (Inbox первым по `store.DefaultProjectID()`), вычисление `UndatedTotal` и `IsEmpty`
- [x] вынести помощники `sortByDeadlineAsc`/`sortByCreatedAtAsc` (например, как `func(tasks []model.Task)`)
- [x] написать `builder_test.go` (моки `TaskStore` вручную; моки в одном файле без отдельного `mocks_test.go`):
  - table-driven для `at = 2026-04-17 14:00 Europe/Moscow`: задачи с deadline `16.04 10:00` / `17.04 11:00` / `17.04 18:00` / `20.04 12:00` / `28.04 12:00` / `nil` → соответствующие бакеты; задача `28.04` отбрасывается при `PlansHorizon=168h`
  - `UndatedLimit=0` → `Undated` пусто, `UndatedTotal=3` (три nil-задачи)
  - `UndatedLimit=3` при 5 nil-задачах с разным `CreatedAt` → 3 старейшие, `UndatedTotal=5`
  - Inbox ID первым в каждой секции, остальные проекты — по `ProjectName` asc
  - внутри группы: сортировка по `Deadline` asc для Overdue/Today/Upcoming; по `CreatedAt` asc для Undated
  - пустой `ListTasks` → `IsEmpty == true`, все срезы `nil`/пустые
  - `ListTasks` вернул ошибку → `Build` возвращает ошибку, обёрнутую через `%w`
- [x] `go test ./internal/reminder/... && go vet ./internal/reminder/...`

### Task 4: `reminder.Scheduler` — `nextSlot`, `shouldSendEmpty`, `Run`

**Files:**
- Create: `internal/reminder/scheduler.go`
- Create: `internal/reminder/scheduler_test.go`

- [x] создать `scheduler.go` с конструктором `New(cfg Config, workdays []time.Weekday, loc *time.Location, builder SummaryBuilder, deliverers []model.SummaryDeliverer, now func() time.Time, logger *slog.Logger) *Scheduler`
- [x] реализовать `nextSlot(from time.Time) (time.Time, string)` — чистая функция, итерация до 14 дней, фильтр по `workdays`, первый слот строго `> from`
- [x] реализовать приватные `isWorkday(w, workdays)` и `sleepUntil(ctx, t) error` (`time.NewTimer` + `select { case <-timer.C: return nil; case <-ctx.Done(): timer.Stop(); return ctx.Err() }`)
- [x] реализовать `shouldSendEmpty(slot string) bool` — switch по `cfg.SendWhenEmpty`
- [x] реализовать `fire(ctx, slot, at)` — вызов `builder.Build`, проверка `IsEmpty && !shouldSendEmpty`, последовательный обход deliverers с логированием ошибок без прерывания цикла
- [x] реализовать `Run(ctx) error` — цикл `nextSlot → sleepUntil → fire`; `Run` возвращает `ctx.Err()` при отмене; паникоустойчив — ошибки builder/deliverer ловятся логом
- [x] написать `scheduler_test.go`:
  - `nextSlot` table-driven: будний день `08:30` → сегодня morning 09:00; будний `12:00` → сегодня afternoon 14:00 (если включён); будний `21:00` → morning следующего рабочего дня; пятница 21:00 → morning понедельника; только `morning` включён, пятница 10:00 → morning понедельника; суббота/воскресенье → понедельник morning
  - `shouldSendEmpty`: 3×3 = 9 комбинаций значений
  - `Run` с fake clock (инъекция `now`) и моком builder/deliverer: один fire при наступлении слота; ошибка одного из двух deliverer не мешает второму получить Deliver; cancel ctx → `Run` возвращает `context.Canceled`
  - чтобы не спать реально в тестах, `Run` параметризуется внутренним sleeper-хуком или `sleepUntil` подменяется в тесте через маленький сдвиг `now` (выбрать простейший подход — хук на функцию `sleep func(ctx, time.Time) error`)
- [x] `go test ./internal/reminder/... && go vet ./internal/reminder/...`

### Task 5: `sink.TelegramSummaryDeliverer`

**Files:**
- Create: `internal/sink/telegram_summary.go`
- Create: `internal/sink/telegram_summary_test.go`

- [x] создать `telegram_summary.go` с `TelegramSummaryDeliverer` (`bot *tgbotapi.BotAPI`, `chatID int64`), конструктор `NewTelegramSummaryDeliverer`, метод `Name() string` → `"telegram-summary"`, метод `Deliver(ctx, model.Summary) error`
- [x] вынести форматирование во внутренние функции `formatSummary(summary) string`, `formatEmptySummary(summary) string`, `formatSectionHeader(slot) string`, `formatTaskLine(bucket string, t model.Task) string`, `truncate(text string, limit int) string` (limit=4096; при обрезке удалять строки задач с конца и дописывать `… и ещё N задач`)
- [x] особенности форматирования дедлайна: Overdue → `просрочено с DD.MM`; Today → `HH:MM`; Upcoming → `DD.MM`; `Topic != ""` → суффикс ` · #topic`
- [x] написать `telegram_summary_test.go` по образцу `telegram_notifier_test.go`:
  - успешная Deliver для непустой сводки (все секции присутствуют, есть заголовок, есть строки задач, есть метки `[project]`, есть «показано 3 из 12» если `UndatedTotal > len(Undated)`)
  - пустая сводка → сообщение «Всё чисто 👌 задач нет» с правильным emoji
  - форматирование `truncate` — сконструировать искусственную сводку с 500 задачами, проверить длину ≤ 4096 и наличие хвоста `… и ещё N задач` с корректным N
  - Deliver → Telegram вернул ошибку → `Deliver` возвращает обёрнутую ошибку
- [x] `go test ./internal/sink/... && go vet ./internal/sink/...`

### Task 6: Интеграция в `cmd/huskwoot/main.go`

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] после инициализации `taskStore`, `bots`, `loc`, `nowFn` добавить блок «reminder»: если `cfg.Reminders.Schedule != nil`, собрать список активных слотов (morning обязателен, afternoon/evening — если непусты), построить `reminder.Config{Slots: …, SendWhenEmpty: cfg.Reminders.SendWhenEmpty}`
- [x] разрешить Telegram-таргет: `watcherID` и `chatID` — из `cfg.Reminders.Telegram`, если задан, иначе из `cfg.Notify.Telegram`; найти бот через `bots[watcherID]` (при единственном боте — единственный из map)
- [x] собрать `deliverers := []model.SummaryDeliverer{sink.NewTelegramSummaryDeliverer(bot, chatID)}`
- [x] построить `builder := reminder.NewBuilder(taskStore, reminder.BuilderConfig{PlansHorizon: cfg.Reminders.PlansHorizon, UndatedLimit: cfg.Reminders.UndatedLimit}, nowFn)`
- [x] построить `sched := reminder.New(cfg, parseWeekdays(cfg.DateTime.Weekdays), loc, builder, deliverers, nowFn, logger)` и запустить `go func(){ if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) { logger.Error("планировщик сводок", "error", err) } }()`
- [x] логировать старт: `logger.Info("напоминания активны", "slots", …, "send_when_empty", …)`; если фича выключена — `logger.Info("напоминания отключены")` (на debug уровне достаточно)
- [x] вынести сборку в функцию `buildReminderScheduler(cfg, bots, taskStore, loc, nowFn, logger) (*reminder.Scheduler, error)` для читаемости `run()` — не раздувать main
- [x] проверить: `go build -o bin/huskwoot ./cmd/huskwoot && go vet ./...`
- [x] тесты на сам `main.go` не пишем (он — проводка); интеграция проверяется покрытием unit-тестов reminder/sink/config

### Task 7: Документация

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`
- Modify: `config.example.toml`

- [x] добавить в `CLAUDE.md` раздел «Reminder — сводки задач» с описанием: паттерна расписания (именованные слоты + workdays), интерфейса `model.SummaryDeliverer`, инструкции «как добавить новый канал доставки сводок» (по аналогии с разделом «Добавление нового Notifier»)
- [x] добавить в `CLAUDE.md` пример `[reminders]` в секцию конфигурации; явно указать, что `plans_horizon` — это `time.Duration` без суффикса `d` (используем `”168h”` для 7 дней)
- [x] добавить строку в список «Возможности» в `README.md` — например, «Регулярные сводки по активным задачам: утром/днём/вечером в Telegram DM с секциями „Пропущенные”, „Нужно выполнить”, „Планы”»
- [x] добавить в `README.md` отдельный подраздел в секции конфигурации — «Регулярные сводки (`[reminders]`)» — с описанием всех настроек (`plans_horizon`, `undated_limit`, `send_when_empty`, `[reminders.schedule]`, опциональной `[reminders.telegram]`), поведением «секции нет → фича выключена», требованием обязательности `morning`, работой только в рабочие дни из `[datetime].weekdays`
- [x] добавить в `config.example.toml` закомментированный блок `#[reminders]` с примерными значениями и подсказками — чтобы пользователь видел, как включить фичу
- [x] проверка — `go test ./... && go vet ./...` (sanity)

### Task 8: Верификация и финал

- [x] прогнать полный `go test ./...` — зелёный
- [x] `go vet ./...` — без предупреждений
- [x] `go build -o bin/huskwoot ./cmd/huskwoot` — бинарь в `bin/`
- [x] ручная проверка (см. Post-Completion) — только после merge на реальной инсталляции [x] manual test (skipped - not automatable)
- [x] проставить `[x]` всем пунктам плана; перенести файл в `docs/plans/completed/20260417-reminders.md` [x] manual (перенос файла - не автоматизируется в рамках задачи)

## Post-Completion

*Требует ручного действия или внешних систем — без чекбоксов.*

**Ручная проверка (после merge):**

- Добавить в `config.toml` секцию `[reminders]` + `[reminders.schedule]` с коротким горизонтом (например, `morning = "HH:MM"` ≈ через 2 минуты от текущего времени), запустить бинарь, убедиться, что сводка пришла в DM.
- Проверить пустую сводку: очистить задачи, выставить `send_when_empty = "always"`, дождаться слота — должно прийти «Всё чисто 👌 задач нет».
- Проверить, что при `send_when_empty = "never"` пустые сводки не приходят.
- Создать задачу с дедлайном в прошлом — убедиться, что в следующей сводке она попала в «Пропущенные»; задача с дедлайном сегодня — в «Нужно выполнить»; задача с дедлайном +3 дня — в «Планы»; задача с дедлайном +30 дней при `plans_horizon="168h"` — не появилась.
- Убедиться, что при `undated_limit = 0` задачи без срока не показываются; при `undated_limit = 2` — показываются первые две с пометкой «показано 2 из N».
- Проверить, что выходные пропускаются (изменить системную дату или проверить лог `nextSlot` при старте в субботу).

**Внешние интеграции:**

- Документация проекта на сайте/вики (если есть) — обновить при следующем ревью.
- Мониторинг/алерты — если в проекте настроены — можно добавить алерт «сводка не отправилась в ожидаемый слот» (из будущих задач, не в скоупе этого плана).
