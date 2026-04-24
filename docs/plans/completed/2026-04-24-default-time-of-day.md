# Default time-of-day for dateless deadlines

## Overview

Когда пользователь говорит «сделаю сегодня», «завтра», «в понедельник», «1 мая» — парсер выставляет время `00:00:00` (полночь начала дня). Это неправильно: дедлайн «сегодня в 00:00» уже просрочен, а «завтра в полночь» означает конец сегодняшнего дня и рассылает напоминания посреди ночи.

Решение — добавить в блок `[datetime.time_of_day]` новый параметр `default` со значением одного из существующих маркеров (`morning` | `lunch` | `afternoon` | `evening`). Парсер подставляет соответствующий час, когда дата задана любым способом, а время — нет. По умолчанию `evening` (20:00 при стандартных настройках).

**Не затрагиваем**:
- Выражения с явно указанным временем (`завтра в 18:00`, `в среду в 10`).
- Выражения с явно заданным временем суток (`завтра утром`, `сегодня вечером`).
- Относительные выражения (`через 2 часа`, `через 3 дня`).
- Выражения с семантикой крайнего срока (`до конца недели`, `до 15-го`, `до выходных`) — там корректно `23:59:59`.

## Context (from discovery)

- Файлы для изменения:
  - `internal/dateparse/patterns_ru.go` — 5 функций (`matchDayTime`, `matchWeekday`, `matchWeekBoundary`, `matchWeekend`, `matchExplicitDate`).
  - `internal/dateparse/patterns_en.go` — симметричные функции.
  - `internal/dateparse/dateparse.go` — структура `TimeOfDay` (поле `Default int`).
  - `internal/config/config.go` — структура `TimeOfDayConfig` (поле `Default string`, валидация + defaulting).
  - `cmd/huskwoot/main.go` — проброс `Default` из config в `dateparse.Config`.
  - Тесты: `internal/dateparse/patterns_ru_test.go`, `patterns_en_test.go`, `internal/config/config_test.go`.
- Паттерны конфига: `TimeOfDayConfig` хранит `int`-часы, defaulting реализован в `config.go` (морниг=11, ланч=12, афтернун=14, ивнинг=20 при нулевом значении).
- i18n выражения для `ru` и `en` живут в отдельных файлах, структура функций симметрична — подход переносится 1:1.
- В CLAUDE.md явно указано: значения `weekdays`, `plans_horizon`, `events_retention` без суффикса `d` — поэтому `default` делаем тоже строкой-маркером, а не часом, для единообразия с остальными полями TimeOfDay.

## Development Approach

- **testing approach**: TDD — сначала меняем/добавляем тесты (они падают), потом правим реализацию, пока всё не зелёное.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run `go test ./...` and `go vet ./...` после каждой задачи
- maintain backward compatibility: отсутствие поля `default` в существующих конфигах должно давать `evening` без ошибок и без изменения поведения для выражений с явным временем

## Testing Strategy

- **unit tests**: обязательны для каждой задачи.
- Табличные тесты в `patterns_ru_test.go` / `patterns_en_test.go` обновляются там, где раньше ожидалось `00:00:00`.
- Добавляем отдельный набор кейсов для всех четырёх значений `default` (morning/lunch/afternoon/evening) — чтобы убедиться, что конфиг действительно пробрасывается.
- Явно проверяем, что ветки-«крайние сроки» (`до конца недели`, `до выходных`, `до 15-го`) по-прежнему возвращают `23:59:59` независимо от `default`.
- Тесты `config_test.go` покрывают валидацию `Default`: принятие валидных значений, отклонение невалидных, дефолт при пустом поле.
- e2e-тестов у проекта нет (Go-бэкенд), поэтому ограничиваемся unit-тестами.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

1. В `dateparse.TimeOfDay` добавляем поле `Default int` — час, который используется, когда дата есть, а времени нет.
2. В `config.TimeOfDayConfig` добавляем поле `Default string` — маркер (`morning` | `lunch` | `afternoon` | `evening`). Валидация и defaulting (пусто → `evening`) в `config.Load`.
3. В `main.go` при построении `dateparse.Config` резолвим маркер в конкретный час (используя уже заданные `Morning/Lunch/Afternoon/Evening`).
4. Во всех 5 целевых функциях `patterns_ru.go` / `patterns_en.go` заменяем литералы `0, 0, 0` в конструкторе `time.Date(..., hour, min, sec, ...)` на `cfg.TimeOfDay.Default, 0, 0` — но **только** в выделенных пользователем ветках.
5. Обновляем документацию (`CLAUDE.md`, `config.example.toml`, `deploy/*/config.toml.example`).

## Technical Details

### Структуры

```go
// internal/dateparse/dateparse.go
type TimeOfDay struct {
    Morning   int
    Lunch     int
    Afternoon int
    Evening   int
    Default   int // hour used when a date is given without an explicit time
}
```

```go
// internal/config/config.go
type TimeOfDayConfig struct {
    Morning   int    `toml:"morning"`
    Lunch     int    `toml:"lunch"`
    Afternoon int    `toml:"afternoon"`
    Evening   int    `toml:"evening"`
    Default   string `toml:"default"` // "morning" | "lunch" | "afternoon" | "evening"
}
```

### Разрешение `default` → час (в `main.go` при построении `dateparse.Config`)

```go
func resolveDefaultHour(tod config.TimeOfDayConfig) int {
    switch tod.Default {
    case "morning":
        return tod.Morning
    case "lunch":
        return tod.Lunch
    case "afternoon":
        return tod.Afternoon
    case "evening":
        return tod.Evening
    default:
        return tod.Evening // safety net; validated in config.Load
    }
}
```

### Валидация в `config.Load`

После существующей defaulting-секции:

```go
if c.DateTime.TimeOfDay.Default == "" {
    c.DateTime.TimeOfDay.Default = "evening"
}
switch c.DateTime.TimeOfDay.Default {
case "morning", "lunch", "afternoon", "evening":
    // ok
default:
    return fmt.Errorf("invalid datetime.time_of_day.default: %q (expected one of: morning, lunch, afternoon, evening)", c.DateTime.TimeOfDay.Default)
}
```

### Места замены `00:00:00` → `Default, 0, 0`

В `internal/dateparse/patterns_ru.go`:

| Функция              | Строка (~) | Контекст                                                |
|----------------------|-----------|---------------------------------------------------------|
| `matchDayTime`       | 132       | ветка `d.phrase != "" && lower == d.phrase`             |
| `matchWeekday`       | 210       | `в <weekday>`                                           |
| `matchWeekday`       | 219       | `в следующий <weekday>` (mon/tue/thu)                   |
| `matchWeekday`       | 233       | `в следующую <weekday>` (wed/fri/sat)                   |
| `matchWeekday`       | 233       | `в следующее <weekday>` (sun)                           |
| `matchWeekday`       | 240       | `к <weekday>`                                           |
| `matchWeekBoundary`  | 447       | `на следующей неделе / к следующей неделе`              |
| `matchWeekend`       | 463       | `в выходные / к выходным`                               |
| `matchWeekend`       | 507       | `после выходных`                                        |
| `matchExplicitDate`  | 549       | `N <месяц>` (e.g. `1 мая`)                              |
| `matchExplicitDate`  | 569       | `DD.MM`                                                 |
| `matchExplicitDate`  | 591       | `DD.MM.YYYY`                                            |

Аналогичные места в `patterns_en.go` — проходим симметрично после открытия файла.

**Не трогаем в `matchWeekBoundary`**: `до конца недели`, `до конца месяца` (23:59:59).
**Не трогаем в `matchWeekend`**: `до выходных` (23:59:59).
**Не трогаем в `matchExplicitDate`**: `до N-го` (через `matchDayOfMonth` → 23:59:59).

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): изменения в коде, тестах и документации внутри репозитория.
- **Post-Completion** (без чекбоксов): внешняя верификация поведения в реальных сценариях (телеграм, агентский чат).

## Implementation Steps

### Task 1: Extend `dateparse.TimeOfDay` with `Default` field

**Files:**
- Modify: `internal/dateparse/dateparse.go`

- [x] добавить поле `Default int` в структуру `TimeOfDay` с комментарием на английском
- [x] убедиться, что в `Parse` / `New` / fallback-ветках `nil` `Default == 0` не ломает поведение ISO-форматов (они не используют `TimeOfDay`)
- [x] изменение чисто структурное, тестов в этом таске нет; проверяем компиляцию: `go build ./...`
- [x] запустить `go vet ./...` — должен пройти

### Task 2: Extend `TimeOfDayConfig` with `Default` and add validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] добавить поле `Default string \`toml:"default"\`` в `TimeOfDayConfig` с комментарием на английском
- [x] в `Load` добавить defaulting (`"" → "evening"`) сразу после существующих defaulting-строк для `Morning/Lunch/Afternoon/Evening`
- [x] в `Load` добавить валидацию с `fmt.Errorf` (сообщение на английском) для невалидных значений
- [x] в `config_test.go` добавить табличный подтест: `default_empty` → резолв в `"evening"`, `default_morning/lunch/afternoon/evening` → принимается, `default_garbage` → ошибка `Load`
- [x] запустить `go test ./internal/config/...` — должен пройти

### Task 3: Wire `default` marker from config to `dateparse.TimeOfDay.Default` in main

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] найти место, где строится `dateparse.Config` из `config.TimeOfDayConfig`
- [x] добавить резолв маркера в час (switch по `tod.Default` → соответствующий `Morning/Lunch/Afternoon/Evening`); вынести в приватную функцию файла `resolveDefaultHour`, если это снижает дубль
- [x] заполнить `dateparse.TimeOfDay.Default` полученным значением
- [x] запустить `go build ./...` и `go vet ./...` — должны пройти
- [x] тестов нет, логика тривиальная и покрывается интеграционно в Task 4 через передачу cfg в парсер

### Task 4: Update Russian parser to use `Default` hour in dateless branches (TDD)

**Files:**
- Modify: `internal/dateparse/patterns_ru_test.go`
- Modify: `internal/dateparse/patterns_ru.go`

- [x] в тестах: установить `cfg.TimeOfDay.Default = cfg.TimeOfDay.Evening` (20) для существующих кейсов и обновить ожидания для `сегодня`, `завтра`, `послезавтра`, `в понедельник`, `в следующий четверг`, `к пятнице`, `на следующей неделе`, `в выходные`, `после выходных`, `1 мая`, `01.05`, `01.05.2027`: час = 20 вместо 0
- [x] добавить новый табличный тест `TestParse_DefaultTimeOfDay` с четырьмя подкейсами (morning=11, lunch=12, afternoon=14, evening=20) для фразы `завтра` и ещё одной опорной (`в понедельник`)
- [x] добавить тесты, фиксирующие неизменность `23:59:59` для `до конца недели`, `до конца месяца`, `до выходных`, `до 15-го`
- [x] запустить тесты — они должны падать на всех перечисленных выше ветках
- [x] в `patterns_ru.go` заменить `0, 0, 0, 0` на `cfg.TimeOfDay.Default, 0, 0, 0` в ветках, перечисленных в секции «Technical Details» (12 мест)
- [x] запустить `go test ./internal/dateparse/...` — всё должно пройти

### Task 5: Update English parser to use `Default` hour in dateless branches (TDD)

**Files:**
- Modify: `internal/dateparse/patterns_en_test.go`
- Modify: `internal/dateparse/patterns_en.go`

- [x] прочитать `patterns_en.go` и найти симметричные места с `0, 0, 0` в аналогичных функциях (`matchDayTime`, `matchWeekday`, `matchWeekBoundary`, `matchWeekend`, `matchExplicitDate` или их EN-аналогах)
- [x] если структура отличается от русского парсера, задокументировать расхождения в комментариях ⚠️ в плане
- [x] обновить существующие ожидания в `patterns_en_test.go` для дат без времени на час из `Default` (Evening=20)
- [x] добавить табличный `TestParse_DefaultTimeOfDay_EN` с 4 значениями `Default` для одной-двух опорных фраз (`tomorrow`, `on monday`)
- [x] заменить литералы в соответствующих ветках `patterns_en.go`
- [x] запустить `go test ./internal/dateparse/...` — всё зелёное

### Task 6: Update config examples and CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`
- Modify: `config.example.toml`
- Modify: `deploy/huskwoot/config.toml.example`
- Modify: `deploy/huskwoot-with-relay/config.toml.example`

- [x] в `CLAUDE.md` в блоке `[datetime]` → `[datetime.time_of_day]` добавить `default = "evening"` с кратким комментарием: время суток, которое используется, когда дата задана, а время — нет; не влияет на выражения с `до ...`
- [x] обновить те же блоки в трёх `*.toml.example` файлах
- [x] документационный таск, отдельных тестов не требует; `go test ./...` всё ещё должен быть зелёным (регресс-проверка)

### Task 7: Verify acceptance criteria

- [x] проверить, что дефолтный конфиг (без `default` в TOML) даёт `evening` и ни один существующий деплой-пример не сломан
- [x] проверить, что невалидное значение `default` даёт понятную ошибку загрузки
- [x] проверить, что фразы с `до ...` по-прежнему возвращают `23:59:59` (зафиксировано в тестах)
- [x] запустить `go test ./...` и `go vet ./...` — всё зелёное
- [x] запустить `go build -o bin/huskwoot ./cmd/huskwoot` — успешная сборка

### Task 8: Finalize documentation and move plan

- [x] обновить CLAUDE.md разделы, если в ходе работы обнаружены новые паттерны, которые стоит задокументировать
- [x] перенести этот план в `docs/plans/completed/`: `git mv docs/plans/2026-04-24-default-time-of-day.md docs/plans/completed/`

## Post-Completion

**Manual verification**:
- создать задачу в телеграме фразой «завтра» — убедиться, что дедлайн выставляется на 20:00 (или другое `default`), а не полночь
- создать задачу фразой «сегодня в 18:00» — убедиться, что явное время не перезаписывается
- создать задачу фразой «до конца недели» — убедиться, что дедлайн остаётся 23:59:59 пятницы
- поменять `default = "morning"` в конфиге, перезапустить, повторить первый сценарий — час должен стать 11 (или настроенным `morning`)

**External system updates**: не требуются, изменение локальное для парсера.
