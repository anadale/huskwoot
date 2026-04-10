# I18N Localization: English logs + bilingual ru/en support

## Overview

Prepare Huskwoot for open-source publication by:
1. Translating all `slog.*` log strings and Go code comments to English
2. Implementing bilingual localization (ru/en) configurable via a single `language` field in `[user]` config section

Localization covers: user-facing Telegram messages, AI prompt templates, agent tool descriptions/responses, and date expression parsing patterns.

## Context (from discovery)

- **Config**: `internal/config/config.go` — `UserConfig` struct, needs `Language string` field
- **Date parsing**: `internal/dateparse/patterns.go` — Russian-only patterns, becomes `patterns_ru.go`; new `patterns_en.go`; interface in `language.go`
- **AI prompts**: `internal/ai/prompts_embed.go` — individual `//go:embed` per tmpl file; `internal/agent/prompts_embed.go` — single embed; both need `embed.FS` + language selection
- **Sink strings**: `internal/sink/telegram_notifier.go`, `internal/sink/telegram_summary.go` — hardcoded Russian strings
- **Agent tools**: `internal/agent/tool_*.go` — Russian descriptions and response strings
- **Handler**: `internal/handler/set_project.go` — one Russian string
- **i18n library**: `go-i18n/v2` (not yet a dependency)

## Development Approach

- **Testing approach**: TDD — write failing tests first, then implement
- Complete each task fully before moving to the next
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**
- Run `go test ./...` after each task

## Testing Strategy

- **unit tests**: table-driven, one per changed file minimum
- **dateparse**: `patterns_ru_test.go` and `patterns_en_test.go` — test each language's expressions independently
- **i18n bundle**: verify all message IDs exist in both locale files and plural forms are correct
- No e2e tests in this project

## Progress Tracking

- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix

## Solution Overview

- Single global `language` field ("ru" | "en", default "ru") in `UserConfig`
- `go-i18n/v2` for user-facing strings with proper Russian plural forms (one/few/many)
- AI prompts: paired `*_ru.tmpl` / `*_en.tmpl` files; each contains explicit "Always respond in Russian/English" directive and language-specific temporal expression examples; loaded via `embed.FS`
- Dateparse: `DateLanguage` interface with `Parse(expr, now, cfg) (time.Time, bool)` method; `russianDateLanguage` and `englishDateLanguage` implementations; factory `NewDateLanguage(lang string)`
- All components receive `*i18n.Localizer` via constructor injection — no global state

## Technical Details

### New config field
```toml
[user]
language = "ru"   # "ru" | "en", default "ru"
```

### DateLanguage interface
```go
type DateLanguage interface {
    Parse(expr string, now time.Time, cfg Config) (time.Time, bool)
}
func NewDateLanguage(lang string) DateLanguage
```

### Prompt loading
```go
// embed.FS replaces individual //go:embed vars
//go:embed prompts
var promptsFS embed.FS

func loadPrompt(lang, name string) string  // reads prompts/{name}_{lang}.tmpl
```

### i18n Localizer injection
- `NewTelegramNotifier(bot, chatID, loc *i18n.Localizer)`
- `NewTelegramSummaryDeliverer(bot, chatID, loc *i18n.Localizer)`
- `ClassifierConfig.Language string`
- `ExtractorConfig.Language string`  
- `agent.Config.Language string`

## What Goes Where

**Implementation Steps** — code changes tracked below.

**Post-Completion** — manual steps:
- Verify Telegram messages look correct in both languages on a real bot
- Test YandexGPT response language with `language = "ru"` config
- Test OpenAI response language with `language = "en"` config

---

## Implementation Steps

### Task 1: Translate slog strings and Go comments to English

**Files:**
- Modify: all `*.go` files in `internal/` (grep-driven)

- [x] grep for Cyrillic in slog calls: `grep -rn --include="*.go" 'slog\.' internal/ | grep -P '[А-Яа-яЁё]'`
- [x] translate all found slog message strings to English
- [x] grep for Cyrillic in Go comments: `grep -rn --include="*.go" '//' internal/ | grep -P '[А-Яа-яЁё]'`
- [x] translate all found comments to English (package doc comments, field comments, inline)
- [x] run `go build ./...` — must succeed with no errors
- [x] run `go test ./...` — all existing tests must pass (no logic changed)

### Task 2: Add Language field to UserConfig

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] write test: `Language = ""` → defaults to `"ru"` after `Validate()`
- [x] write test: `Language = "en"` → valid
- [x] write test: `Language = "fr"` → returns validation error
- [x] run tests — they must FAIL (field doesn't exist yet)
- [x] add `Language string \`toml:"language"\`` to `UserConfig`
- [x] add validation in `Config.Validate()` or equivalent: if empty → set `"ru"`; if not in `{"ru","en"}` → return error
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

### Task 3: Add go-i18n/v2 and create internal/i18n package

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/i18n/bundle.go`
- Create: `internal/i18n/localizer.go`
- Create: `internal/i18n/locales/ru.json`
- Create: `internal/i18n/locales/en.json`
- Create: `internal/i18n/i18n_test.go`

- [x] write test: `NewBundle("ru")` → localizer translates `"tasks_created_header"` to Russian string
- [x] write test: `NewBundle("en")` → localizer translates `"tasks_created_header"` to English string
- [x] write test: `"tasks_more"` with `PluralCount=1` → "1 задача" (ru) / "1 task" (en)
- [x] write test: `"tasks_more"` with `PluralCount=3` → "3 задячи" (ru) / "3 tasks" (en)
- [x] write test: `"tasks_more"` with `PluralCount=5` → "5 задач" (ru) / "5 tasks" (en)
- [x] run tests — must FAIL
- [x] run `go get github.com/nicksnyder/go-i18n/v2`
- [x] create `ru.json` with all message IDs from `telegram_notifier.go` and `telegram_summary.go` (see Technical Details below for full list)
- [x] create `en.json` with English equivalents
- [x] create `bundle.go`: `//go:embed locales/*.json`, `func NewBundle(lang string) (*i18n.Bundle, error)`
- [x] create `localizer.go`: `func NewLocalizer(bundle *i18n.Bundle, lang string) *i18n.Localizer` + `Translate(loc, id string, data any, count ...int) string` helper
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

**Message IDs for ru.json / en.json:**
```
tasks_created_header, source_with_project, source_no_project,
context_label, summary_morning_title, summary_afternoon_title,
summary_evening_title, summary_generic_title,
section_overdue, section_today, section_upcoming, section_undated,
section_undated_limited, task_overdue_since, tasks_more,
summary_empty, project_bound_confirmation,
agent_task_already_exists, agent_task_not_found,
agent_task_moved, agent_invalid_ref_format,
agent_task_id_or_ref_required
```

### Task 4: Refactor dateparse — DateLanguage interface

**Files:**
- Create: `internal/dateparse/language.go`
- Rename: `internal/dateparse/patterns.go` → `internal/dateparse/patterns_ru.go`
- Create: `internal/dateparse/patterns_en.go`
- Modify: `internal/dateparse/dateparse.go`
- Create: `internal/dateparse/patterns_ru_test.go`
- Create: `internal/dateparse/patterns_en_test.go`
- Modify: `internal/dateparse/dateparse_test.go`

- [x] write `patterns_ru_test.go`: table-driven tests for Russian expressions ("через час", "завтра утром", "к пятнице", "до конца недели", etc.) — replicate existing tests from `dateparse_test.go`
- [x] write `patterns_en_test.go`: table-driven tests for English expressions ("tomorrow", "by Friday", "in 2 hours", "next week", "end of month", etc.)
- [x] run tests — must FAIL (no `DateLanguage` interface yet)
- [x] create `language.go`: `type DateLanguage interface { Parse(expr string, now time.Time, cfg Config) (time.Time, bool) }` + `func NewDateLanguage(lang string) DateLanguage`
- [x] rename `patterns.go` → `patterns_ru.go`; wrap all existing parse functions in `type russianDateLanguage struct{}` implementing `DateLanguage.Parse` (dispatches to existing funcs)
- [x] create `patterns_en.go`: `type englishDateLanguage struct{}` with English temporal expressions
- [x] modify `dateparse.go`: `Dateparser` accepts `DateLanguage` in constructor `func New(cfg Config, lang DateLanguage) *Dateparser`; delegates to `lang.Parse` first, falls back to absolute date parsing
- [x] update `dateparse_test.go` to use `NewDateLanguage("ru")`
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

### Task 5: AI prompts — language-aware loading

**Files:**
- Modify: `internal/ai/prompts_embed.go`
- Rename: `internal/ai/prompts/*.tmpl` → `internal/ai/prompts/*_ru.tmpl`
- Create: `internal/ai/prompts/*_en.tmpl` (6 files)
- Modify: `internal/agent/prompts_embed.go`
- Rename: `internal/agent/prompts/agent_system.tmpl` → `internal/agent/prompts/agent_system_ru.tmpl`
- Create: `internal/agent/prompts/agent_system_en.tmpl`
- Modify: `internal/ai/classifier.go`
- Modify: `internal/ai/extractor.go`
- Modify: `internal/agent/agent.go`
- Create: `internal/ai/prompts_test.go`

- [x] write test: `loadPrompt(promptsFS, "ru", "classifier_simple_system")` → non-empty string containing "Always respond in Russian"
- [x] write test: `loadPrompt(promptsFS, "en", "classifier_simple_system")` → non-empty string containing "Always respond in English"
- [x] write test: `loadPrompt(promptsFS, "xx", "classifier_simple_system")` → falls back to "ru"
- [x] run tests — must FAIL
- [x] change `internal/ai/prompts_embed.go`: replace individual `//go:embed` vars with `//go:embed prompts` `var promptsFS embed.FS`; add `func loadPrompt(fs embed.FS, lang, name string) string`
- [x] rename all `*.tmpl` → `*_ru.tmpl` in `internal/ai/prompts/`; add "Always respond in Russian." at end of each
- [x] create `*_en.tmpl` variants with English translations + "Always respond in English."; replace Russian temporal examples with English ones in `extractor_user_en.tmpl` and `command_extractor_user_en.tmpl`
- [x] add `Language string` to `ClassifierConfig` and `ExtractorConfig`; use `loadPrompt(promptsFS, cfg.Language, "classifier_simple_system")` etc.
- [x] same for `internal/agent/`: `embed.FS`, `loadPrompt`, add `Language string` to `Config`
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

### Task 6: Inject Localizer into sink components

**Files:**
- Modify: `internal/sink/telegram_notifier.go`
- Modify: `internal/sink/telegram_summary.go`
- Modify: `internal/sink/telegram_notifier_test.go` (if exists) or create
- Modify: `internal/sink/telegram_summary_test.go` (if exists) or create

- [x] write test for `TelegramNotifier.Notify`: Russian localizer → message contains "Новые задачи"
- [x] write test for `TelegramNotifier.Notify`: English localizer → message contains "New tasks"
- [x] write test for `TelegramSummaryDeliverer`: Russian localizer → summary contains "Утренняя сводка"
- [x] write test for `TelegramSummaryDeliverer`: English localizer → summary contains "Morning summary"
- [x] run tests — must FAIL
- [x] add `loc *i18n.Localizer` field to `TelegramNotifier`; update `NewTelegramNotifier(bot, chatID, loc)`
- [x] replace all hardcoded Russian strings in `telegram_notifier.go` with `i18n.Translate(loc, "message_id", data)`
- [x] add `loc *i18n.Localizer` field to `TelegramSummaryDeliverer`; update `NewTelegramSummaryDeliverer(bot, chatID, loc)`
- [x] replace all hardcoded Russian strings in `telegram_summary.go` with localizer calls
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

### Task 7: Inject Localizer into agent tools

**Files:**
- Modify: `internal/agent/tool_create_task.go`
- Modify: `internal/agent/tool_complete_task.go`
- Modify: `internal/agent/tool_list_tasks.go`
- Modify: `internal/agent/tool_create_project.go`
- Modify: `internal/agent/tool_move_task.go`
- Modify: `internal/agent/tool_list_projects.go`
- Modify: `internal/agent/tool_set_project.go`
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/tools_test.go`

- [x] write test: tool Description() returns English string when localizer is English
- [x] write test: tool response strings are localized correctly
- [x] run tests — must FAIL
- [x] add `loc *i18n.Localizer` to each tool struct; update constructors or pass via `Config`
- [x] replace Russian strings in `Description()`, `Parameters()`, and response messages in each tool file
- [x] tool descriptions use message IDs like `"tool_create_task_desc"`, `"tool_create_task_param_summary"`, etc. — add these to ru.json and en.json
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

### Task 8: Inject Localizer into handler

**Files:**
- Modify: `internal/handler/set_project.go`
- Modify or create: `internal/handler/set_project_test.go`

- [x] write test: Russian localizer → reply contains "Чат привязан к проекту"
- [x] write test: English localizer → reply contains "Chat linked to project"
- [x] run tests — must FAIL
- [x] add `loc *i18n.Localizer` to `SetProjectHandler`; update constructor
- [x] replace hardcoded string with localizer call
- [x] run tests — must PASS
- [x] run `go test ./...` — all tests pass

### Task 9: Wire everything in main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] create `DateLanguage` from `cfg.User.Language`: `dateLang := dateparse.NewDateLanguage(cfg.User.Language)`
- [x] create i18n bundle and localizer: `bundle, _ := i18n.NewBundle(cfg.User.Language)` + `loc := i18n.NewLocalizer(bundle, cfg.User.Language)`
- [x] pass `dateLang` to `dateparse.New(cfg.Datetime, dateLang)` call
- [x] pass `loc` to `NewTelegramNotifier(bot, chatID, loc)`
- [x] pass `loc` to `NewTelegramSummaryDeliverer(bot, chatID, loc)`
- [x] pass `loc` and `cfg.User.Language` to classifier, extractor, and agent constructors
- [x] pass `loc` to `NewSetProjectHandler(...)` and other handler constructors
- [x] run `go build ./...` — must succeed
- [x] run `go test ./...` — all tests pass

### Task 10: Verify acceptance criteria

**Files:** none (verification only)

- [x] run `go vet ./...` — no warnings
- [x] run `go test ./...` — all tests pass
- [x] verify `internal/i18n/locales/ru.json` and `en.json` contain identical set of message IDs
- [x] verify every slog call in `internal/` uses English strings (grep check)
- [x] verify no Cyrillic in Go comments in `internal/` (grep check)
- [x] verify `patterns_ru_test.go` covers all expression types from original `dateparse_test.go`
- [x] verify `patterns_en_test.go` covers equivalent English expressions

### Task 11: [Final] Update documentation

**Files:**
- Modify: `CLAUDE.md`
- Move: this plan to `docs/plans/completed/`

- [x] update CLAUDE.md: add `language` field to `[user]` config section example
- [x] update CLAUDE.md: add `internal/i18n/` to directory structure
- [x] move this plan: `mkdir -p docs/plans/completed && mv docs/plans/20260420-i18n-localization.md docs/plans/completed/`

## Post-Completion

**Manual verification:**
- Deploy with `language = "ru"` and send a test task via Telegram; verify Telegram messages are in Russian
- Deploy with `language = "en"` and repeat; verify Telegram messages are in English
- Test YandexGPT with `language = "ru"`: verify model responds in Russian to DM messages
- Test OpenAI model with `language = "en"`: verify model responds in English
- Test date expressions: `"through tomorrow"` with `en`, `"завтра"` with `ru`

**External dependencies:**
- No deploy config changes required (language is in user config file, not env vars)
