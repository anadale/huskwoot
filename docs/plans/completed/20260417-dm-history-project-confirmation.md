# История DM и переспрос о проекте в DM

## Overview

Агент в DM не распознал проект «На Старт» во фразе «нужно пройти проверку разработчика Android для На Старт» и молча создал задачу в Inbox. Текущий системный промпт обязывает перед `create_task` звать `list_projects` при упоминании проекта, но не описывает, что делать, когда проекта нет. В результате модель выбирает «безопасный» путь — пишет в Inbox без предупреждения.

Цель: агент в DM должен при упоминании неизвестного проекта переспросить пользователя («Создать новый проект „X“?»), дождаться подтверждения или отказа и только после этого либо создать проект + задачу, либо положить задачу в Inbox. Подтверждение пользователь даёт текстом: «да / нет / ок / +  / 👍 / ❤️ / 💯 / давай / ага / отмена» и т.п. в обычном сообщении (настоящие Telegram-реакции в этом плане не поддерживаем — требуется миграция на tgbotapi v6+, отложено).

Для того чтобы агент помнил свой предыдущий вопрос между вызовами `Handle`, нужно включить сохранение истории для DM-сообщений в `TelegramChannel` и писать в историю исходящие ответы бота. Без истории в DM агент работает stateless и не увидит контекст переспроса.

## Context (from discovery)

**Файлы/компоненты:**

- [internal/channel/telegram.go](../../internal/channel/telegram.go) — `Watch` пишет в историю только Group/GroupDirect ([telegram.go:182](../../internal/channel/telegram.go#L182)); `ReplyFn` нигде не фиксирует ответы бота в истории.
- [internal/agent/prompts/agent_system.tmpl](../../internal/agent/prompts/agent_system.tmpl) — системный промпт агента, пункт про проект на строке 17.
- [internal/agent/tool_create_task.go](../../internal/agent/tool_create_task.go) — `create_task`; сейчас молча падает в default при `project_id == 0`.
- [internal/agent/tool_create_project.go](../../internal/agent/tool_create_project.go) — `create_project`; без логов о создании.
- [internal/channel/telegram_test.go](../../internal/channel/telegram_test.go) — существующий `TestWatch_DMMessage_HistoryNotCalledAndHistoryFnNil` фиксирует **старое** поведение и подлежит переписыванию.

**Выбранные решения (подтверждены пользователем):**

- История DM строится через `history.RecentActivity(source, SilenceGap, FallbackLimit)` — единообразно с Group.
- Имя бота в истории: `cfg.BotUsername`, fallback `"bot"` при пустом значении.
- Настоящие Telegram-реакции не поддерживаем — только текстовые символы `👍 / ❤️ / 💯` в обычном сообщении.

**Архитектурные особенности:**

- `Source.ID` для DM — константа `"dm"` в рамках одного `TelegramChannel`. Используем как ключ истории.
- `Agent.Handle` уже подклеивает `HistoryFn` к system-промпту ([agent.go:124-137](../../internal/agent/agent.go#L124-L137)) — никаких изменений в самом агенте не требуется, если `HistoryFn` установится каналом.
- Для GroupDirect `HistoryFn` уже исключает текущее сообщение из истории; тот же механизм нужен и для DM.

## Development Approach

- **testing approach**: TDD (проектный дефолт, зафиксированный в CLAUDE.md)
- каждый task завершается зелёным `go test ./...` и `go vet ./...`
- без расширения публичного API: сигнатуры `NewTelegramChannel`, `HistoryConfig`, `TelegramChannelConfig` не меняются
- никаких новых миграций БД, новых полей конфига TOML
- при расхождении реального поведения и плана — обновлять этот файл сразу (`➕` для новых задач, `⚠️` для блокеров)

## Testing Strategy

- **unit-тесты** Go (`internal/channel/telegram_test.go`, `internal/agent/*_test.go`) — единственный уровень автоматических тестов в проекте
- проверяем:
  - `history.Add` вызывается для DM-сообщения от владельца
  - `HistoryFn` для DM установлен и делегирует в `RecentActivity`, исключая текущее сообщение
  - `ReplyFn` пишет ответ бота в историю (для DM и Group/GroupDirect)
  - пустой `BotUsername` → AuthorName = `"bot"`
  - ошибка `history.Add` не ломает отправку ответа
- ручная проверка (после merge) — по сценарию из Post-Completion

## Progress Tracking

- `[x]` — сделано; `➕` — добавлено по ходу; `⚠️` — блокер
- обновлять сразу после каждого task

## Solution Overview

### Поток DM + переспрос

```
User: "нужно пройти проверку разработчика Android для На Старт"
  → TelegramChannel.Watch пишет DM в history (новое)
  → TelegramChannel.Watch ставит HistoryFn на msg (новое)
  → Pipeline → Agent.Handle → system-prompt + история
  → LLM: list_projects → «На Старт» нет → НЕ create_task
  → LLM возвращает текстовый ответ: «Не нашёл проект „На Старт“. Создать новый?»
  → msg.ReplyFn(...) — пишет ответ бота в history (новое)

User: "👍"
  → TelegramChannel.Watch пишет DM в history
  → Agent.Handle видит прошлый ход в history
  → LLM распознаёт согласие → create_project("На Старт") → create_task(project_id=…)
  → msg.ReplyFn(ответ) — пишет в history
```

### Ключевые решения

1. **История DM**: условие в `Watch` расширяется на `MessageKindDM`. `HistoryFn` для DM использует тот же `RecentActivity` и тот же дедуп текущего сообщения, что и для `GroupDirect`.
2. **Запись ответов бота в историю**: `ReplyFn` — и в `convertMessage`, и в `convertDMMessage` — после `bot.Send` дополнительно дёргает `history.Add` с `AuthorName = cfg.BotUsername || "bot"`. Ошибка записи логируется warn'ом, пользователю не возвращается.
3. **Промпт**: переписываем блок про проект. Новое правило: при упоминании проекта **всегда** `list_projects`, не нашёл → переспрос, без `create_task` до ответа. При явной команде «создай проект X» переспрос не нужен.
4. **Лексикон подтверждения/отказа**: задаётся в промпте. Подтверждения — `да, ок, ага, давай, конечно, хорошо, +, 👍, ❤️, 💯`. Отказы — `нет, не надо, отмена, -, не, не нужно`.
5. **Логи**: `create_task` info-лог при создании задачи без явного `project_id` (упала в Inbox); `create_project` info-лог при успехе. Оба используют `slog.Default()` с контекстом — отдельный logger в инструменты не прокидываем, чтобы не ломать сигнатуры.

## Technical Details

### Изменения в TelegramChannel

- новый приватный метод `botDisplayName() string` — возвращает `cfg.BotUsername` или `"bot"`
- новый приватный метод `recordBotReply(ctx, sourceID, text string, date int)` — пишет entry в `w.history`; no-op при `history == nil` или пустом тексте
- `ReplyFn` в `convertMessage` и `convertDMMessage` — после успешного `bot.Send(reply)` вызывают `recordBotReply`
- `Watch`: условие для `history.Add`/`HistoryFn` расширяется на `MessageKindDM`
- `HistoryFn` для DM и GroupDirect одинаково фильтрует текущее сообщение (дедуп по `Timestamp` + `Text`)

### Изменения в промпте агента

- удалить текущий пункт «Если пользователь явно называет проект … — сначала вызови list_projects» (частично перекрывается новым)
- добавить блок «Работа с проектами в DM» с алгоритмом list_projects → переспрос → create_project → create_task
- добавить короткий блок для `GroupDirect`: `list_projects` и `create_project` недоступны, поэтому при упоминании проекта — задача в текущий проект чата и подсказка про `set_project`

### Логи

- `tool_create_task.go`: при `params.ProjectID == 0` → `slog.InfoContext(ctx, "create_task без явного project_id", "default_project_id", projectID)`
- `tool_create_project.go`: после успешного `CreateProject` → `slog.InfoContext(ctx, "создан проект через агента", "project_id", p.ID, "name", p.Name)`

## What Goes Where

- **Implementation Steps**: изменения в Go-коде, шаблоне промпта, тестах — всё в этом репозитории
- **Post-Completion**: ручная проверка сценария в реальном Telegram-аккаунте, подтверждение, что переспрос работает и по текстовому «да», и по «👍»

## Implementation Steps

### Task 1: История DM в TelegramChannel

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] удалить устаревший тест `TestWatch_DMMessage_HistoryNotCalledAndHistoryFnNil` и заменить его на `TestWatch_DMMessage_HistoryAddCalledAndHistoryFnSet` (red)
- [x] добавить тест `TestWatch_DMMessage_HistoryFnDelegatesToRecentActivity_ExcludesCurrent`: `HistoryFn` для DM зовёт `RecentActivity` с параметрами из `HistoryConfig` и исключает текущее сообщение по `(Timestamp, Text)` (red)
- [x] расширить условие в [telegram.go:182](../../internal/channel/telegram.go#L182) на `MessageKindDM`
- [x] в собранной `HistoryFn` сделать ветку: `DM` и `GroupDirect` дедупят текущее сообщение; только `GroupDirect` → оставить как сейчас, добавить DM параллельно
- [x] прогнать `go test ./internal/channel/...` — тесты должны стать зелёными

### Task 2: Запись ответов бота в историю через ReplyFn

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] добавить в `mockBot` поле `sendResult tgbotapi.Message` и вернуть его из `Send`
- [x] написать тест `TestConvertMessage_Group_ReplyFn_WritesHistory`: после вызова `ReplyFn` `history.Add` получает entry с `AuthorName == "testbot"`, `Text == <ответ>`, `source == chatID` (red)
- [x] написать тест `TestConvertMessage_DM_ReplyFn_WritesHistory`: то же для DM (source == "dm") (red)
- [x] написать тест `TestConvertMessage_ReplyFn_EmptyBotUsername_DefaultsToBot`: при пустом `BotUsername` → `AuthorName == "bot"` (red)
- [x] написать тест `TestConvertMessage_ReplyFn_NilHistory_NoPanic`: `ReplyFn` работает без паники, когда `history == nil` (red)
- [x] написать тест `TestConvertMessage_ReplyFn_HistoryAddError_StillReturnsNil`: ошибка `history.Add` не превращается в ошибку `ReplyFn` (red)
- [x] реализовать `botDisplayName()` и `recordBotReply(ctx, sourceID, text, date)` в `telegram.go`
- [x] обернуть `ReplyFn` в `convertMessage` и `convertDMMessage` так, чтобы после `bot.Send` вызывался `recordBotReply`
- [x] прогнать `go test ./internal/channel/...` — все тесты зелёные

### Task 3: Обновление системного промпта агента

**Files:**
- Modify: `internal/agent/prompts/agent_system.tmpl`

- [x] переписать пункт про проекты на строке 17: удалить текущую инструкцию, добавить блок «Работа с проектами в DM» с алгоритмом: `list_projects` → найден → `create_task` с id; не найден → вопрос пользователю «Не нашёл проект „X”. Создать его и завести задачу? (да/нет, можно 👍/❤️/💯)», БЕЗ `create_task` до ответа
- [x] прописать словари согласия/отказа: согласие — `да, ок, ага, давай, конечно, хорошо, +, 👍, ❤️, 💯`, отказ — `нет, не надо, отмена, -, не, не нужно`. При согласии: `create_project` → `create_task` с новым id. При отказе: `create_task` без project_id + короткий ответ «записал в Inbox»
- [x] прописать исключение: явная команда «создай проект X» — сразу `create_project`, без переспроса
- [x] прописать поведение для `GroupDirect`: `list_projects`/`create_project` недоступны → создать задачу в текущем проекте чата, в ответе подсказать команду «Это проект X» для привязки
- [x] проверить, что шаблон корректно парсится (`go test ./internal/agent/...` — существующий `TestNew_SystemTemplateParsesWithNow` или аналог должен проходить)

### Task 4: Логи в инструментах create_task и create_project

**Files:**
- Modify: `internal/agent/tool_create_task.go`
- Modify: `internal/agent/tool_create_project.go`
- Modify: `internal/agent/tools_test.go`

- [x] добавить тест на логирование не обязателен — достаточно убедиться, что существующие тесты продолжают проходить; вместо этого добавить табличный тест `TestCreateTaskTool_Execute_WithoutProjectID_UsesDefault` (если ещё нет), фиксирующий fallback-поведение
- [x] в `createTaskTool.Execute` при `params.ProjectID == 0` → `slog.InfoContext(ctx, "create_task без явного project_id, задача попадёт в дефолтный проект", "default_project_id", projectID, "summary", params.Summary)`
- [x] в `createProjectTool.Execute` после успешного `CreateProject` → `slog.InfoContext(ctx, "создан проект через агента", "project_id", p.ID, "name", p.Name)`
- [x] прогнать `go test ./internal/agent/...` — все тесты зелёные

### Task 5: Проверка приёмочных критериев

- [x] `go test ./...` — всё зелёное
- [x] `go vet ./...` — без замечаний
- [x] вручную просмотреть diff в `telegram.go`, `agent_system.tmpl`, `tool_create_task.go`, `tool_create_project.go`: нет лишних изменений, backwards compatibility сохраняется [x] manual test (skipped - not automatable)
- [x] убедиться, что сценарии из Overview (и промпт, и история, и логи) покрыты [x] manual test (skipped - not automatable)

### Task 6: Документация

**Files:**
- Modify: `CLAUDE.md` (опционально)

- [x] если изменилось поведение, задокументированное в CLAUDE.md про `HistoryFn` («nil для DM и IMAP») — обновить на актуальное («устанавливается также для DM при наличии history»)
- [x] переместить этот план в `docs/plans/completed/20260417-dm-history-project-confirmation.md`

## Post-Completion

**Ручная проверка в боевом Telegram-DM** (выполняется автором после merge):

- отправить «нужно пройти проверку разработчика Android для На Старт» → бот должен переспросить про создание проекта
- ответить «да» → задача уходит в новый проект «На Старт»
- отправить «нужно купить билеты для Отпуска» → бот переспрашивает
- ответить «нет» → задача уходит в Inbox
- отправить «нужно купить билеты для Отпуска» → бот переспрашивает
- ответить «👍» → задача уходит в новый проект «Отпуск»
- отправить «создай проект Ремонт» → бот создаёт без переспроса
- проверить в логах: видны info-сообщения про создание проекта и про задачи без project_id

**Отложенные задачи** (не в этом плане):

- поддержка настоящих Telegram-реакций на вход (требует миграции tgbotapi → v6+/v7)
- возможное добавление инструмента `find_project(name)` с fuzzy-поиском — как альтернатива `list_projects` для больших инсталляций
