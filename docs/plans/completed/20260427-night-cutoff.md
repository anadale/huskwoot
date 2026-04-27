# Night cutoff — opt-in shift for relative day words

## Overview

Добавить опцию `night_cutoff` в секцию `[datetime]` конфига, которая до заданного утреннего времени сдвигает интерпретацию относительных слов «завтра» / «послезавтра» / «tomorrow» / «day after tomorrow» на −1 день. Слово «сегодня» / «today» всегда остаётся literal — текущим календарным днём (asymmetric behavior). Прочие конструкции (дни недели, «через N дней», конкретные даты) не затрагиваются.

Сценарий: пользователь работает по ночам и в 02:00 говорит «завтра», подразумевая текущий календарный день (до сна). Без опции дата уходит на сутки вперёд, теряя смысл обещания.

Опция opt-in: пустое поле / `"00:00"` = выключено (default), поведение существующих пользователей не меняется.

## Context (from discovery)

- Точка применения сдвига: `internal/dateparse/dateparse.go` (общий хелпер) + вызовы в `patterns_ru.go:111-116` и `patterns_en.go:93-95` (таблицы `dayEntry{phrase, offset}`).
- `dateparse.Config` (line 19) не содержит `Now func()` — `now time.Time` передаётся явным параметром в `Parse(s, now, cfg)`. Хелпер должен принимать `now` параметром, не из cfg.
- Конфиг-структура: `internal/config/config.go:124-133` (`DateTimeConfig`). Дефолты и валидация — `applyDefaults` около строки 408 (по аналогии с `TimeOfDay.Default`).
- Пример конфига: `config.example.toml:74-84`, секция `[datetime]` + `[datetime.time_of_day]`. Комментарии **на английском**.
- Тестовые паттерны в репо: table-driven, `now` фиксируется через `time.Date(...)`. См. `internal/dateparse/patterns_ru_test.go`, `patterns_en_test.go`.

## Development Approach

- **testing approach**: TDD — для каждого таска тесты пишутся вместе с кодом и должны зелёные перед переходом к следующему.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run `go test ./...` и `go vet ./...` после каждого таска
- maintain backward compatibility (opt-in default off)

## Testing Strategy

- **unit tests**: обязательны для каждого таска (см. Development Approach).
- **e2e tests**: в проекте нет UI-based e2e — пропускаем.
- Подмена «сейчас»: `now` передаётся как параметр функций; в тестах строится через `time.Date(2026, 4, 27, 2, 0, 0, 0, loc)` и т.п.
- Покрытие включает регрессию (`cutoff = 0` не меняет поведение существующих кейсов).

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## Solution Overview

1. **Конфиг.** В `DateTimeConfig` добавляется `NightCutoff string` (TOML-поле `night_cutoff`, формат `"HH:MM"`). При `applyDefaults`:
   - пустая строка / `"00:00"` → `NightCutoffMinutes = 0` (off)
   - корректный `"HH:MM"` (валидация диапазона `00:00..23:59`) → `NightCutoffMinutes = HH*60 + MM`
   - всё остальное → ошибка `invalid datetime.night_cutoff: %q`
2. **Парсер.** `dateparse.Config` получает поле `NightCutoffMinutes int`. В `dateparse.go` появляется хелпер `shiftRelativeDayOffset(offset int, now time.Time, cfg Config) int`:
   - если `cfg.NightCutoffMinutes == 0` или `offset < 1` → вернуть `offset` без изменений (гарантия: «сегодня» = 0 не сдвигается)
   - если `now.Hour()*60 + now.Minute() < cfg.NightCutoffMinutes` → `offset - 1`
   - иначе → `offset`
   - граница строгая (на `05:00:00` ровно сдвиг **уже не работает**)
3. **Точечная интеграция.** Хелпер вызывается ТОЛЬКО в местах, где офсет берётся из таблицы относительных слов (`dayEntry{phrase, offset}`):
   - `patterns_ru.go:130-135` — простая ветка «сегодня/завтра/послезавтра»
   - `patterns_ru.go:137+` — комбинированная ветка «завтра утром» и т.п.
   - `patterns_ru.go:267-269` — таблица для `<слово> в HH:MM`
   - `patterns_en.go:93-106` — аналогичные ветки
   - `patterns_en.go:127-132` — таблица для `<word> at HH:MM`
   - НЕ применять к коду «через N дней», дни недели, конкретные даты.
4. **Bridge.** В `cmd/huskwoot/main.go` (или там, где `dateparse.Config` собирается из `config.DateTimeConfig`) пробросить `NightCutoffMinutes` в `dateparse.Config`.
5. **Документация.** Обновить `config.example.toml`, `README.md`, `CLAUDE.md` (комментарии в TOML — на английском).

## Technical Details

### Data structures

```go
// internal/config/config.go (DateTimeConfig)
type DateTimeConfig struct {
    Timezone   string             `toml:"timezone"`
    Weekdays   []string           `toml:"weekdays"`
    TimeOfDay  TimeOfDayConfig    `toml:"time_of_day"`
    NightCutoff        string `toml:"night_cutoff"` // "" or "HH:MM"
    NightCutoffMinutes int    `toml:"-"`            // parsed: 0 = off
}

// internal/dateparse/dateparse.go
type Config struct {
    TimeOfDay          TimeOfDay
    Weekdays           []time.Weekday
    NightCutoffMinutes int // 0 = off
}

// internal/dateparse/dateparse.go (helper)
func shiftRelativeDayOffset(offset int, now time.Time, cfg Config) int {
    if cfg.NightCutoffMinutes == 0 || offset < 1 {
        return offset
    }
    if now.Hour()*60+now.Minute() < cfg.NightCutoffMinutes {
        return offset - 1
    }
    return offset
}
```

### Validation

`parseHHMM(s string) (int, error)` — общий хелпер в `internal/config/config.go` (если ещё нет; иначе reuse). Принимает `"HH:MM"`, возвращает минуты. Используется и для `night_cutoff`, и при необходимости для `[reminders.schedule]`.

Ошибки конфига:
- `invalid datetime.night_cutoff: %q` — невалидный формат / диапазон.

### Processing flow

```
config.toml → config.Load → applyDefaults (parseHHMM → NightCutoffMinutes int)
  → cmd/huskwoot/main.go: dateparse.Config{NightCutoffMinutes: cfg.DateTime.NightCutoffMinutes, ...}
  → dateparse.Parse(s, now, cfg) → patterns_*.go
    → match dayEntry → offset := shiftRelativeDayOffset(offset, now, cfg)
    → time.Date(... now.Day()+offset ...)
```

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): код, тесты, документация в репо.
- **Post-Completion** (no checkboxes): ручная проверка пользовательского сценария «реальное обещание в 02:00».

## Implementation Steps

### Task 1: Add NightCutoffMinutes to dateparse.Config and shift helper

**Files:**
- Modify: `internal/dateparse/dateparse.go`
- Modify: `internal/dateparse/dateparse_test.go` (или создать новый `shift_test.go`, если файла нет — на этапе имплементации проверить и решить)

- [x] добавить поле `NightCutoffMinutes int` в `dateparse.Config`
- [x] реализовать хелпер `shiftRelativeDayOffset(offset int, now time.Time, cfg Config) int` со строгой границей `<`
- [x] написать table-driven тесты на хелпер: `cutoff=0`, `offset=0`, `now < cutoff & offset≥1`, `now == cutoff`, `now > cutoff`, `cutoff=5:00 & offset=2`
- [x] запустить `go test ./internal/dateparse/...` — должны пройти

### Task 2: Wire helper into Russian patterns

**Files:**
- Modify: `internal/dateparse/patterns_ru.go`
- Modify: `internal/dateparse/patterns_ru_test.go`

- [x] вызвать `shiftRelativeDayOffset` для офсета `d.offset` в простой ветке `if d.phrase != "" && lower == d.phrase` (~line 130)
- [x] вызвать для офсета в комбинированной ветке «завтра утром»/«послезавтра ночью» (~line 137)
- [x] вызвать для офсета в таблице `<слово> в HH:MM` (~line 267)
- [x] добавить table-driven тесты: 02:00 пн c cutoff=300 → «завтра»→пн, «послезавтра»→вт, «сегодня»→пн (literal); «завтра в 14:00»→пн 14:00; «послезавтра утром»→вт+morning; 06:00→без сдвига; 04:59→сдвиг; 05:00→без сдвига; cutoff=0 (regression) — без сдвига
- [x] добавить тест на «до завтра» с cutoff: подтвердить, что deadline-выражение тоже сдвигается (desired)
- [x] запустить `go test ./internal/dateparse/...` — должны пройти

### Task 3: Wire helper into English patterns

**Files:**
- Modify: `internal/dateparse/patterns_en.go`
- Modify: `internal/dateparse/patterns_en_test.go`

- [x] вызвать `shiftRelativeDayOffset` в аналогичных местах en-патернов (`today/tomorrow/day after tomorrow` и комбинации с временем суток ~line 93-106; таблица `<word> at HH:MM` ~line 127)
- [x] добавить зеркальные table-driven тесты: 02:00 c cutoff=300 → "tomorrow"→today (calendar), "day after tomorrow"→tomorrow (calendar), "today"→literal; "tomorrow at 14:00"→pn 14:00; "tomorrow morning"→same-day morning; cutoff=0 regression
- [x] запустить `go test ./internal/dateparse/...` — должны пройти

### Task 4: Add night_cutoff to config loading + validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] добавить поля `NightCutoff string` и `NightCutoffMinutes int` (`toml:"-"`) в `DateTimeConfig`
- [x] в `applyDefaults` (или в фазе валидации, по аналогии с `TimeOfDay.Default`): пустая строка / `"00:00"` → `NightCutoffMinutes = 0`; парсить `"HH:MM"` через хелпер `parseHHMM` (создать или reuse, если есть для reminders schedule); невалидный формат → ошибка `invalid datetime.night_cutoff: %q`
- [x] добавить table-driven тесты: `""` → 0, `"00:00"` → 0, `"05:00"` → 300, `"23:59"` → 1439, `"5"` → ошибка, `"05:60"` → ошибка, `"abc"` → ошибка, `"24:00"` → ошибка
- [x] запустить `go test ./internal/config/...` — должны пройти

### Task 5: Bridge config → dateparse and verify end-to-end

**Files:**
- Modify: `cmd/huskwoot/main.go` (или другое место, где собирается `dateparse.Config` из `config.DateTimeConfig` — найти grep'ом `dateparse.Config{`)
- При необходимости modify: `internal/dateparse/dateparse_test.go` (интеграционный тест)

- [x] grep по `dateparse.Config{` — определить точку сборки и пробросить `NightCutoffMinutes: cfg.DateTime.NightCutoffMinutes`
- [x] добавить интеграционный тест в `dateparse_test.go`: загрузить минимальный конфиг с `night_cutoff = "05:00"`, парсить «завтра» с фиксированным `now=02:00` и проверить, что вернулась дата текущего календарного дня
- [x] запустить `go test ./...` — все пакеты должны быть зелёными
- [x] запустить `go vet ./...` — без замечаний

### Task 6: Update example config

**Files:**
- Modify: `config.example.toml`

- [x] добавить в секцию `[datetime]` (~line 76, после `weekdays`) закомментированную строку `# night_cutoff = "05:00"` с английским пояснением: семантика, asymmetric behavior с `today`, что `""`/`"00:00"` = off
- [x] убедиться, что комментарий на английском, без эмоджи
- [x] (тесты не требуются для example-файла)

### Task 7: Verify acceptance criteria

- [x] verify: 02:00 пн с `night_cutoff="05:00"` — «завтра»/«tomorrow» → пн (текущий календарный)
- [x] verify: 02:00 пн с `night_cutoff="05:00"` — «сегодня»/«today» → пн (literal, не меняется)
- [x] verify: 06:00 пн — без сдвига (cutoff не сработал)
- [x] verify: `night_cutoff=""` (default) — поведение всех существующих тестов не изменилось
- [x] verify: невалидный `night_cutoff` — ошибка загрузки конфига
- [x] run full test suite: `go test ./...`
- [x] run `go vet ./...`

### Task 8: [Final] Update documentation

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [x] в `README.md` (datetime-секция) добавить короткий абзац про `night_cutoff` с примером ночного сценария — на русском, как и остальной README
- [x] в `CLAUDE.md` (блок `[datetime]`) добавить строку про `night_cutoff` параллельно с `time_of_day.default`, явно указав, что «сегодня»/«today» не сдвигается
- [x] переместить этот план в `docs/plans/completed/` (создать директорию при необходимости — она уже существует)

## Post-Completion

**Manual verification**:
- Реальный ночной сценарий: в 02:00–03:00 отправить боту «напомни завтра позвонить врачу» — задача должна создаться на текущий календарный день, дедлайн в формате «evening» (или соответствующий `time_of_day.default`).
- Проверить, что дневное использование (например, в 14:00 «завтра») по-прежнему уходит на следующий календарный день.

**External system updates**: не требуется — изменение чисто конфиг + парсер, миграции БД, push relay и API не затрагиваются.
