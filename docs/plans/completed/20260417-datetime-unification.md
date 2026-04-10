# Унификация разбора дат: экстрактор ↔ агент, расширение натуральных форм

## Overview

Сейчас разбор дедлайнов живёт в двух несвязанных местах: функция `parseDeadline` в `internal/ai/extractor.go` (используется экстрактором для Group/IMAP) и прямой `time.Parse(time.RFC3339, ...)` в `internal/agent/tool_create_task.go` (агент для DM/GroupDirect). Следствия: DM-задачи не принимают натуральные русские выражения, а системный промпт агента вообще не знает текущей даты, из-за чего модель может ошибаться с годом и часовым поясом.

Цели:
1. Вынести разбор дат в единый пакет `internal/dateparse`, которым пользуются и экстрактор, и инструмент `create_task`.
2. Расширить набор поддерживаемых натуральных русских выражений (дни недели, недели/месяцы, конкретные даты, границы «до конца недели», «в выходные» и т.п.).
3. Дать агенту текущую дату/время в системном промпте через шаблонизацию.
4. Ввести конфигурируемую таймзону для окружений, где системное время не совпадает с ожидаемым пользователем (деплой в UTC-контейнере).
5. Удалить мёртвый `DMExtractorSystemTemplate` — DM-путь полностью переехал в агент.

Интеграция в существующую систему: переименование существующей секции `[parser]` в `[datetime]` с расширением полей (timezone, weekdays, lunch). Этот план считает breaking change для конфига допустимым — проект ранний.

## Context (from discovery)

**Файлы/компоненты:**
- `internal/ai/extractor.go` — содержит `parseDeadline`, `reRelativeAmount`, `reDayTime`, тип `TimeOfDay` (будут удалены/вынесены).
- `internal/ai/prompts/extractor_system.tmpl` — JSON-инструкция «ISO 8601 или null» (расширяется).
- `internal/ai/prompts/extractor_dm_system.tmpl` + `DMExtractorSystemTemplate` — **мёртвый код**, удаляется.
- `internal/agent/agent.go` — статичный системный промпт, нет `Now`.
- `internal/agent/prompts/agent_system.tmpl` — превращается в template.
- `internal/agent/tool_create_task.go:85` — жёсткий `time.Parse(RFC3339)`.
- `internal/config/config.go:111-119` — существующая секция `ParserConfig` с Morning/Afternoon/Evening (переименовывается в `DateTimeConfig`).
- `config.example.toml:75-79` — секция `[parser]` (мигрируется в `[datetime]`).
- `cmd/huskwoot/main.go` — собирает `ExtractorConfig`, `agent.Config`, список инструментов.
- `internal/pipeline/pipeline.go:100` — подтверждено: `MessageKindDM || MessageKindGroupDirect` идут в агент, DM-экстрактор действительно не подключён.

**Релевантные паттерны:**
- Моки вручную (без testify/mock), табличные тесты (см. `internal/ai/extractor_test.go`).
- Конструкторы `(*T, error)` при возможности ошибки — соглашение из CLAUDE.md.
- Ошибки оборачиваются с описанием операции (`fmt.Errorf("операция: %w", err)`).
- Сообщения на русском, `int/string/time` — ключевыми словами языка.
- Логирование через `slog`.
- Промпт-шаблоны: `text/template` + embed через `//go:embed`.

**Зависимости:** стандартная библиотека (`time`, `regexp`, `text/template`), `BurntSushi/toml` (для TOML), `sashabaranov/go-openai` (агент).

## Development Approach

- **Testing approach**: TDD — тесты `internal/dateparse` пишутся до/вместе с реализацией; тесты правим параллельно с изменением сигнатур.
- Завершать каждую задачу полностью перед переходом к следующей.
- Делать небольшие сфокусированные изменения.
- **CRITICAL: каждая задача обязательно включает новые/обновлённые тесты** для изменённого кода в этой задаче:
  - тесты не опциональны — это обязательная часть чеклиста.
  - unit-тесты для новых функций/методов.
  - unit-тесты для изменённых функций/методов.
  - новые кейсы для новых веток кода.
  - обновление существующих кейсов, если поведение изменилось.
  - тесты покрывают и успешный путь, и ошибочные сценарии.
- **CRITICAL: все тесты должны проходить до перехода к следующей задаче** — без исключений.
- **CRITICAL: обновлять этот файл плана, если в ходе реализации меняется scope**.
- После каждого изменения запускать `go test ./...` и `go vet ./...`.
- Сохранять обратную совместимость API где возможно (исключения: `agent.New` → возвращает error, `NewCreateTaskTool` — новая сигнатура; внешних потребителей нет).

## Testing Strategy

- **Unit-тесты**: обязательны для каждой задачи (см. Development Approach).
- **E2E-тесты**: у проекта нет UI-based e2e; ограничиваемся `go test ./...` по всему модулю.
- Табличные тесты в стиле Go для парсера — `[]struct{name, input, now, want, wantErr}`.
- Для воспроизводимости использовать фиксированное `now` — среда 2026-04-15 14:00 `Europe/Moscow` — относительно него проверяются все «ближайшая пятница», «через неделю», «до конца недели» и т.п.
- Моки AI-клиента через `httptest.Server` (существующий паттерн) или вручную через интерфейс `AIClient`.

## Progress Tracking

- Отмечать выполненные пункты `[x]` сразу по завершении.
- Новые обнаруженные задачи помечать `➕`.
- Блокеры помечать `⚠️`.
- При отклонении от изначального scope — обновлять файл плана.

## Solution Overview

**Архитектурный подход:**

1. **Новый пакет `internal/dateparse`** — единственная точка разбора дат. API:
   ```go
   type Config struct {
       TimeOfDay TimeOfDay
       Weekdays  []time.Weekday // пусто = Mon..Fri; остальное — выходные
   }
   type TimeOfDay struct{ Morning, Lunch, Afternoon, Evening int }
   func Parse(s string, now time.Time, cfg Config) (*time.Time, error)
   ```
   Парсер агностичен к таймзоне — использует `now.Location()`. Сначала пробует ISO/стандартные форматы, затем нормализует строку и прогоняет по упорядоченному списку обработчиков: точные фразы, «через N единиц», день+время суток, день+«в HH:MM», дни недели, «на следующей неделе», «к следующей неделе», конкретные даты, границы недели/месяца, выходные, время в формате `HH` / `HH:MM` / `к N`.

2. **Конфиг `[datetime]`** — переименование существующей `[parser]` с расширением:
   - `timezone` (опционально, string в IANA-формате или GMT-offset).
   - `weekdays` (опционально, массив строк `"mon","tue",...`; умолчание — Mon..Fri).
   - `[datetime.time_of_day]` — вложенная таблица с `morning/lunch/afternoon/evening`.
   - Некорректная таймзона → `slog.Warn` + фолбэк на `time.Local`.
   - Некорректные weekday-строки → `slog.Warn` + игнор.

3. **Единый `nowFn func() time.Time`** в `cmd/huskwoot/main.go`, собирается из резолвленной `loc`. Передаётся и экстрактору (`ExtractorConfig.Now`), и агенту (`agent.Config.Now`).

4. **Системный промпт агента — template**. `agent.New` теперь возвращает `(*Agent, error)`, парсит `systemPrompt` через `text/template`. В `Handle` перед каждым циклом рендерится актуальное `Now` и кладётся в систему-сообщение. Затем `Now` кладётся в контекст по ключу `nowKey`, чтобы `tool_create_task` видел ровно ту же точку отсчёта, что и модель.

5. **Промпты** явно разрешают модели возвращать ISO (предпочтительно) или русское натуральное выражение (если не уверена). Safety-net на стороне парсера гарантирует успех в обоих случаях.

**Ключевые решения и обоснования:**

- **Парсер через регекспы + таблицу обработчиков, без внешней либы**: набор форм конечен, регекспы детерминированы, не тянем зависимость.
- **`Weekdays []time.Weekday`, а не `WeekendDays [2]`**: гибче для регионов с нестандартными выходными; стоит ровно один if-check в `isWeekend`.
- **`now` в контексте + в шаблоне**: инвариант «одно сообщение агента = одно время» — модель видит ровно ту дату, относительно которой инструмент будет считать дедлайн.
- **`agent.New` → `(*Agent, error)`**: соглашение проекта (CLAUDE.md) — конструкторы, которые могут упасть при парсинге/валидации, возвращают ошибку.
- **Breaking change `[parser]` → `[datetime]`**: проект ранний, в продакшене конфиг не развёрнут широко, единоразовая миграция дешевле двух секций.

## Technical Details

**Полный набор распознаваемых форм:**

| Группа | Примеры | Значение |
|---|---|---|
| Точные фразы (существующие) | завтра, сегодня, послезавтра | 0:00 целевого дня |
| | вечером, сегодня вечером, к вечеру | Evening (20) |
| | к утру | завтра Morning (11) |
| | к обеду, в обед | Lunch (12) |
| | до обеда | Lunch-1 (11) **новое** |
| | после обеда | Afternoon (14) |
| | через полчаса, через 30 минут, через час | now + ∆ |
| Относительное (существующее) | через N минут/часов/дней | now + N*unit |
| | через N недель **новое** | now.AddDate(0,0,7N) |
| | через N месяцев **новое** | now.AddDate(0,N,0) |
| День + время суток | сегодня/завтра/послезавтра утром/днём/вечером/ночью | целевой день + hour |
| День + точное время **новое** | завтра в 10, сегодня в 18:00, послезавтра в 9:30 | целевой день + HH:MM |
| Дни недели **новое** | в понедельник/вторник/…/воскресенье | ближайший такой день, 0:00 |
| | в следующий понедельник/вторник/… | тот же день + 7 дней, 0:00 |
| | к пятнице / к понедельнику / … | ближайший такой день, 0:00 |
| | в пятницу в 14:30 | комбинация дня недели + HH:MM |
| Недели **новое** | на следующей неделе | ближайший понедельник следующей недели, 0:00 |
| | к следующей неделе | то же |
| | до конца недели | ближайшая пятница 23:59:59 |
| Месяцы **новое** | до конца месяца | последний день месяца 23:59:59 |
| | через месяц / через 2 месяца | now.AddDate(0, N, 0) |
| Выходные **новое** | в выходные, к выходным | ближайшая суббота 0:00 |
| | до выходных | ближайшая пятница 23:59:59 |
| | после выходных | ближайший понедельник 0:00 |
| Даты **новое** | 5 мая, 15 апреля | указанный день месяца 0:00 (если прошёл — следующий год) |
| | 15.04, 15.04.2026 | dd.mm / dd.mm.yyyy |
| | до 20-го | 20-е текущего месяца 23:59:59 (если прошло — след. месяц) |
| Часы числом **новое** | в 18:00, в 9 | сегодня в указанный час (если прошёл — завтра) |
| | к 15 | то же |
| ISO (существующее) | RFC3339, 2006-01-02T15:04:05, 2006-01-02 15:04:05, 2006-01-02 | как есть |

**Граничные правила:**

- «В пятницу», если сегодня пятница → сегодня. «В следующую пятницу» → +7 дней.
- Форматы без TZ интерпретируются в `now.Location()`.
- Пустая строка или «null» → `(nil, nil)` без ошибки.
- Нераспознанная непустая строка → ошибка (экстрактор/агент логируют warn и сохраняют задачу без дедлайна).
- «До обеда» = `Lunch - 1` (11:00), не «к обеду минус час» — зафиксированное значение.
- Если `Weekdays` в конфиге пустой срез — используем `[Mon..Fri]`.
- «В выходные» ищет ближайший не-рабочий день (по умолчанию суббота).

**Формат `Now` в промптах:** `2006-01-02 15:04:05 -07:00` (уже используется в экстракторе).

**Контекст в агенте:**
```go
type contextKey int
const (
    defaultProjectIDKey contextKey = iota
    sourceIDKey
    nowKey // новый
)
```

## What Goes Where

- **Implementation Steps** (`[ ]`): весь код, промпты, тесты, миграция конфига, обновление `config.example.toml`.
- **Post-Completion** (без чеклистов): ручная проверка end-to-end в реальном DM и групповом чате.

## Implementation Steps

### Task 1: Скелет пакета `internal/dateparse` и ISO-форматы

**Files:**
- Create: `internal/dateparse/dateparse.go`
- Create: `internal/dateparse/dateparse_test.go`

- [x] создать `internal/dateparse/dateparse.go` с типами `Config`, `TimeOfDay` и функцией `Parse(s string, now time.Time, cfg Config) (*time.Time, error)`
- [x] реализовать обработку пустой строки и `"null"` → `(nil, nil)`
- [x] реализовать разбор ISO/стандартных форматов: RFC3339, `2006-01-02T15:04:05`, `2006-01-02 15:04:05`, `2006-01-02`
- [x] написать табличные тесты `TestParse_ISOFormats` — success cases для всех форматов
- [x] написать тесты для пустой строки и `"null"`
- [x] написать тест `TestParse_NoTimezone_UsesNowLocation` — проверка что формат без TZ интерпретируется в `now.Location()`
- [x] запустить `go test ./internal/dateparse/...` — все тесты должны проходить

### Task 2: Точные фразы существующего набора

**Files:**
- Create: `internal/dateparse/patterns.go`
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] создать `internal/dateparse/patterns.go` с функцией `matchExactPhrase(s string, now time.Time, cfg Config) (*time.Time, bool)` для всех существующих точных фраз: «завтра», «сегодня», «послезавтра», «вечером», «сегодня вечером», «через полчаса», «через 30 минут», «через час», «к вечеру», «к утру», «к обеду», «после обеда»
- [x] добавить новую фразу «до обеда» (Lunch - 1)
- [x] добавить фразу «в обед» как синоним «к обеду»
- [x] подключить обработчик в `Parse`
- [x] написать тесты `TestParse_ExactPhrases` — все фразы с фиксированным `now = Wed 2026-04-15 14:00 Europe/Moscow`
- [x] запустить тесты — все должны проходить

### Task 3: Относительные количества и день+время суток

**Files:**
- Modify: `internal/dateparse/patterns.go`
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] реализовать `matchRelativeAmount` для «через N минут/часов/дней/недель/месяцев» (расширенные формы числа и падежные окончания — минут/минуты/минуту, часов/часа/час, дней/дня/день, недель/недели/неделю, месяцев/месяца/месяц)
- [x] реализовать `matchDayTime` для «сегодня/завтра/послезавтра утром/днём/вечером/ночью» (существующая логика «ночью» → следующий 0:00)
- [x] подключить обработчики в `Parse`
- [x] написать тесты `TestParse_RelativeAmount` — все единицы и падежные формы
- [x] написать тесты `TestParse_DayTime` — все комбинации
- [x] запустить тесты — все должны проходить

### Task 4: Дни недели и «следующий» модификатор

**Files:**
- Modify: `internal/dateparse/patterns.go`
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] реализовать `matchWeekday` для «в понедельник»…«в воскресенье» (ближайший такой день, 0:00; если сегодня — сегодня)
- [x] поддержать «в следующий понедельник/вторник/…» (+7 дней к ближайшему)
- [x] поддержать «к пятнице», «к понедельнику»… (синоним «в <день>»)
- [x] учесть падежные формы: «в пятницу» (не «пятница»), «к пятнице», «в следующую пятницу»
- [x] написать тесты `TestParse_Weekday` — все дни недели, включая случай «сегодня пятница → в пятницу = сегодня»
- [x] написать тесты `TestParse_WeekdayNext` — «в следующий понедельник» и т.п.
- [x] запустить тесты — все должны проходить

### Task 5: День + точное время и «в HH:MM»

**Files:**
- Modify: `internal/dateparse/patterns.go`
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] реализовать `matchDayAtTime` для «сегодня/завтра/послезавтра в HH», «завтра в HH:MM»
- [x] реализовать `matchWeekdayAtTime` для «в пятницу в 14:30», «в следующий понедельник в 10»
- [x] реализовать `matchClockTime` для «в 18:00», «в 9», «к 15» — сегодня указанный час, если прошёл → завтра
- [x] написать тесты `TestParse_DayAtTime` и `TestParse_ClockTime`
- [x] запустить тесты — все должны проходить

### Task 6: Границы недели/месяца, следующая неделя, выходные

**Files:**
- Modify: `internal/dateparse/patterns.go`
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] реализовать хелпер `isWeekend(day time.Weekday, cfg Config) bool` — учёт поля `Weekdays`
- [x] реализовать `matchWeekBoundary` для «до конца недели» (ближайшая пятница 23:59:59), «до конца месяца» (последний день месяца 23:59:59), «на следующей неделе» / «к следующей неделе» (ближайший понедельник следующей недели 0:00)
- [x] реализовать `matchWeekend` для «в выходные»/«к выходным» (ближайшая суббота 0:00), «до выходных» (ближайшая пятница 23:59:59), «после выходных» (ближайший понедельник 0:00)
- [x] написать тесты `TestParse_WeekBoundaries` — все случаи
- [x] написать тесты `TestParse_Weekend` — все случаи, включая нестандартный `Weekdays` (например Mon..Thu)
- [x] запустить тесты — все должны проходить

### Task 7: Конкретные даты

**Files:**
- Modify: `internal/dateparse/patterns.go`
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] реализовать `matchExplicitDate` для «5 мая», «15 апреля» (все 12 месяцев в родительном падеже), dd.mm, dd.mm.yyyy
- [x] реализовать логику «в этом году» vs «в следующем году» — если дата в этом году прошла, берём следующий
- [x] реализовать `matchDayOfMonth` для «до 20-го» — 20-е текущего месяца 23:59:59, если прошло → следующий месяц (поддержать «до 1-го», «до 5-го», «до 20-го», «до 31-го»)
- [x] написать тесты `TestParse_ExplicitDate` — все месяцы, dd.mm, dd.mm.yyyy, обработка прошедшей даты → следующий год
- [x] написать тесты `TestParse_DayOfMonth` — в середине месяца, в конце месяца, для даты из прошлого месяца
- [x] запустить тесты — все должны проходить

### Task 8: Секция `[datetime]` в конфиге и миграция `[parser]`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.toml`

- [x] переименовать `ParserConfig` → `DateTimeConfig` в `internal/config/config.go`, изменить tag `toml:"parser"` → `toml:"datetime"`
- [x] добавить поле `Timezone string \`toml:"timezone"\``
- [x] добавить поле `Weekdays []string \`toml:"weekdays"\``
- [x] вынести часы в вложенную структуру `TimeOfDayConfig` с tag `toml:"time_of_day"` (поля Morning/Lunch/Afternoon/Evening)
- [x] добавить поле `Lunch` с умолчанием 12 в validate()
- [x] в `Config`: переименовать поле `Parser` → `DateTime`
- [x] обновить умолчания в `validate()` для `time_of_day` (Morning=11, Lunch=12, Afternoon=14, Evening=20)
- [x] обновить `config.example.toml`: переименовать `[parser]` → `[datetime]`, добавить `timezone = "Europe/Moscow"` (закомментирован как пример), `weekdays = [...]` (закомментирован), вложенная секция `[datetime.time_of_day]`
- [x] обновить существующие тесты `config_test.go` под новую структуру
- [x] написать `TestConfig_ParsesDateTimeSection` — проверка парсинга всех новых полей
- [x] написать `TestConfig_DateTimeDefaults` — проверка умолчаний
- [x] запустить `go test ./internal/config/...` — все тесты должны проходить

### Task 9: Резолвинг таймзоны и `nowFn` в main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`
- Create (или Modify): связанные тесты если есть для main

- [x] в `cmd/huskwoot/main.go` добавить функцию/блок, принимающий `cfg.DateTime.Timezone`, делающий `time.LoadLocation(s)`
- [x] при ошибке парсинга — `slog.Warn("некорректная таймзона в конфиге, используется локальная", "timezone", s, "error", err)`, `loc = time.Local`
- [x] при пустой строке → `loc = time.Local`
- [x] создать `nowFn := func() time.Time { return time.Now().In(loc) }`
- [x] добавить парсинг `cfg.DateTime.Weekdays` в `[]time.Weekday` (map из строк `"mon"/"tue"/.../"sun"`, lowercase); невалидные → `slog.Warn` и игнор; пустой → `[Mon..Fri]`
- [x] собрать `dateparseCfg := dateparse.Config{TimeOfDay: ..., Weekdays: ...}`
- [x] передать `nowFn` в `ExtractorConfig.Now`, `agent.Config.Now`
- [x] передать `dateparseCfg` в `ExtractorConfig.DateParse` и `agent.NewCreateTaskTool(...)` (подготовлена переменная, будет использована после обновления сигнатур в Task 10/14)
- [x] написать или обновить тесты, если есть для функций резолвинга (возможно вынести хелперы в `cmd/huskwoot/` или `internal/config/`)
- [x] запустить `go test ./...` — не должно быть регрессий (зависит от синхронизации с задачами 10+)

### Task 10: Миграция экстрактора на `dateparse`

**Files:**
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/extractor_test.go`

- [x] в `internal/ai/extractor.go` удалить функцию `parseDeadline`, регекспы `reRelativeAmount` и `reDayTime`, тип `TimeOfDay`
- [x] в `ExtractorConfig` убрать поле `TimeOfDay`, добавить `DateParse dateparse.Config`
- [x] в `NewTaskExtractor` убрать блок дефолтов для `TimeOfDay.Morning/Afternoon/Evening` (дефолты теперь в main/config)
- [x] в `Extract` заменить вызов `parseDeadline(*resp.Deadline, msgTime, e.cfg.TimeOfDay)` на `dateparse.Parse(*resp.Deadline, msgTime, e.cfg.DateParse)`
- [x] в `internal/ai/extractor_test.go` удалить все тесты, напрямую проверявшие `parseDeadline` (переехали в `internal/dateparse/dateparse_test.go`)
- [x] обновить оставшиеся тесты `TaskExtractor`: заменить `cfg.TimeOfDay = ai.TimeOfDay{...}` на `cfg.DateParse = dateparse.Config{TimeOfDay: dateparse.TimeOfDay{...}}`
- [x] запустить `go test ./internal/ai/...` — все тесты должны проходить

### Task 11: Удаление мёртвого DM-экстрактора

**Files:**
- Delete: `internal/ai/prompts/extractor_dm_system.tmpl`
- Modify: `internal/ai/prompts_embed.go`
- Modify: `internal/ai/extractor_test.go`

- [x] удалить файл `internal/ai/prompts/extractor_dm_system.tmpl`
- [x] в `internal/ai/prompts_embed.go` удалить директиву `//go:embed prompts/extractor_dm_system.tmpl` и переменную `DMExtractorSystemTemplate` (строки 20-21)
- [x] удалить тест `TestTaskExtractor_DMExtractorTemplate` в `internal/ai/extractor_test.go` (примерно строки 727+) — тест не найден, видимо уже удалён или не существовал
- [x] убедиться, что `DMExtractorSystemTemplate` больше нигде не используется: `grep -r "DMExtractorSystemTemplate" .` — только в docs
- [x] запустить `go build ./...` — компиляция должна проходить
- [x] запустить `go test ./...` — все тесты должны проходить

### Task 12: Правка системного промпта экстрактора

**Files:**
- Modify: `internal/ai/prompts/extractor_system.tmpl`
- Modify: `internal/ai/extractor_test.go`

- [x] в `internal/ai/prompts/extractor_system.tmpl` перед финальной строкой с JSON-инструкцией добавить блок:
```
Расчёт дедлайна:
- Используй значение «Текущая дата и время» из пользовательского промпта как точку отсчёта.
- Если можешь однозначно вычислить абсолютное время — возвращай в формате ISO 8601 с часовым поясом.
- Если не уверен в точном времени — допустимо вернуть русское натуральное выражение: «завтра», «завтра в 10», «в пятницу», «к выходным», «через 2 дня», «до конца недели». Парсер их распознает.
- Если срок в тексте не указан вообще — null.
```
- [x] обновить/добавить тест, проверяющий наличие блока в отрендеренном промпте (`TestExtractor_SystemPromptHasDeadlineRules`)
- [x] запустить `go test ./internal/ai/...` — все тесты должны проходить

### Task 13: Шаблонизация системного промпта агента и `Now` в контекст

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/prompts/agent_system.tmpl`
- Modify: `internal/agent/agent_test.go`

- [x] в `internal/agent/prompts/agent_system.tmpl` в конец файла добавить блок:
```
Текущая дата и время: {{.Now}}.

Работа с дедлайнами:
- Если пользователь указывает срок выполнения явно или относительно («завтра», «к пятнице», «через 2 дня», «в выходные», «до конца недели», «завтра в 10»), передавай его в параметр deadline инструмента create_task.
- Предпочтительно: вычисляй абсолютное время в ISO 8601/RFC3339, используя текущую дату и часовой пояс.
- Если не уверен в точном значении — передавай русское натуральное выражение без преобразования. Парсер на стороне инструмента поддерживает все перечисленные формы.
- Если срока нет — параметр deadline не передавай.
```
- [x] в `internal/agent/agent.go` в `Config` добавить `Now func() time.Time`
- [x] в `Agent` добавить поля `now func() time.Time` и `systemTmpl *template.Template`
- [x] изменить сигнатуру `New(...) *Agent` → `New(...) (*Agent, error)`; парсить `systemPrompt` через `text/template.New("agent-system").Parse(sp)`; ошибка парсинга → `nil, fmt.Errorf("парсинг системного шаблона агента: %w", err)`
- [x] добавить новый ключ `contextKey`: `NowKey` (экспортирован для тестов)
- [x] в `Handle`: сгенерировать `data := struct{ Now string }{Now: a.now().Format("2006-01-02 15:04:05 -07:00")}`, отрендерить `systemTmpl` в строку, использовать её как базу для `sysContent`
- [x] положить `ctx = context.WithValue(ctx, NowKey, a.now())` перед tool calling (один раз на вызов `Handle`)
- [x] добавить в `cfg.Now` фолбэк `time.Now` если nil
- [x] обновить существующие тесты под новую сигнатуру `New` (возвращает error)
- [x] добавить тест `TestAgent_InjectsNowIntoSystemPrompt` — spy-AIClient запоминает `messages[0].Content`, проверяем наличие ожидаемой строки
- [x] добавить тест `TestAgent_PutsNowInToolContext` — spy-tool, чей `Execute` читает `ctx.Value(NowKey)`; проверяем что вернулось фиксированное `now`
- [x] обновить `cmd/huskwoot/main.go`: передать `Now: nowFn` в `agent.Config`, обработать ошибку от `agent.New`
- [x] запустить `go test ./internal/agent/...` — все тесты должны проходить

### Task 14: Миграция `tool_create_task` на `dateparse`

**Files:**
- Modify: `internal/agent/tool_create_task.go`
- Modify: `internal/agent/tools_test.go`
- Modify: `cmd/huskwoot/main.go`

- [x] в `internal/agent/tool_create_task.go`:
  - структура `createTaskTool`: добавить поле `cfg dateparse.Config`
  - сигнатура `NewCreateTaskTool(store model.TaskStore, cfg dateparse.Config) Tool`
  - описание параметра `deadline`: `"Срок выполнения (опционально). RFC3339 или русское натуральное выражение: \"завтра\", \"завтра в 10\", \"в пятницу\", \"через 2 дня\", \"к выходным\", \"до конца недели\"."`
  - в `Execute`: если `params.Deadline != ""` — читать `now` из `ctx.Value(nowKey).(time.Time)` с фолбэком `time.Now()`; вызывать `dateparse.Parse(params.Deadline, now, t.cfg)`; при ошибке возвращать `"", fmt.Errorf("разбор deadline: %w", err)`
- [x] в `cmd/huskwoot/main.go` обновить вызов `agent.NewCreateTaskTool(taskStore, dateparseCfg)`
- [x] обновить `internal/agent/tools_test.go`:
  - `TestCreateTaskTool_*` — новый конструктор, подготовка контекста с `nowKey`
  - новые кейсы: дедлайны «завтра в 10», «в пятницу», «через 2 дня», «к выходным» → проверка абсолютного `Deadline`
  - кейс «неизвестный формат» → инструмент возвращает ошибка (покрыто TestCreateTaskTool_Execute_InvalidDeadline)
- [x] запустить `go test ./internal/agent/...` — все тесты должны проходить

### Task 15: Проверка acceptance criteria

- [x] запустить `go build -o bin/huskwoot ./cmd/huskwoot` — собирается без ошибок
- [x] запустить `go test ./...` — все тесты зелёные
- [x] запустить `go vet ./...` — без замечаний
- [x] убедиться вручную: `grep -rn "parseDeadline\|DMExtractorSystemTemplate\|ParserConfig" . | grep -v docs/plans/` — только в удаляемых/переименованных местах, либо в исторических планах в `docs/plans/completed/`
- [x] проверить, что `config.example.toml` после обновления валиден (скопировать во временную директорию, запустить `go run ./cmd/huskwoot --config-dir <tmp>` — должен стартовать или упасть только на отсутствии токенов/базы, не на парсинге)
- [x] убедиться, что поведение экстрактора для Group/IMAP сохранилось: существующие тесты проходят
- [x] убедиться, что все новые формы покрыты тестами в `internal/dateparse/dateparse_test.go`

### Task 16: Обновление документации

**Files:**
- Modify: `CLAUDE.md`
- Move: `docs/plans/20260417-datetime-unification.md` → `docs/plans/completed/`

- [x] в `CLAUDE.md` обновить раздел «Структура директорий» — добавить `internal/dateparse/` с описанием
- [x] в `CLAUDE.md` в разделе «Конфигурация» или новом подразделе документировать секцию `[datetime]` с примером и описанием параметров (timezone, weekdays, time_of_day)
- [x] в `CLAUDE.md` в описании агента упомянуть, что `Now` из `Config` попадает и в системный промпт, и в контекст инструмента (`nowKey`)
- [x] переместить файл плана в `docs/plans/completed/20260417-datetime-unification.md`

## Post-Completion

*Информационный блок — ручные проверки после слияния.*

**Ручная проверка в реальном окружении:**
- В DM боту написать несколько сообщений с разными формами дедлайна: «напомни завтра в 10», «надо бы к пятнице доделать», «через 2 недели сделать ревью». Проверить, что задачи создаются с правильными датами в логе и в БД.
- В групповом чате спровоцировать обещание с натуральной датой («сделаю до конца недели»). Проверить, что экстрактор корректно разобрал.
- Проверить работу в UTC-контейнере: задеплоить с `timezone = "Europe/Moscow"` и без — убедиться, что разница в поведении именно такая, как ожидается.

**Документация/коммуникация:**
- Если у других пользователей проекта уже есть рабочие `config.toml` — им нужно вручную переименовать секцию `[parser]` → `[datetime]` и, при желании, добавить `timezone`. Упомянуть в CHANGELOG или release notes.
