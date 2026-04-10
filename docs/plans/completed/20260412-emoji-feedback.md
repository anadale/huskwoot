# Emoji-реакция ✍️ на обещание в Telegram

## Overview

Когда Pipeline обнаруживает обещание в Telegram-сообщении, бот ставит реакцию ✍️ на это
сообщение прямо в чате — как мгновенная «квитанция» для пользователя, что обещание замечено.
Реакция работает параллельно с DM-уведомлением и записью в синки.

Попутно устраняется дублирование токена Telegram: до сих пор `watchers.telegram[i].token`
и `notify.telegram.bot_token` задавали одного и того же бота дважды. После изменений один
`*tgbotapi.BotAPI` создаётся на watcher и шарится с нотификаторами.

## Context (from discovery)

- **Файлы для изменения:**
  - `internal/model/types.go` — добавить `AccountID` в `Source`
  - `internal/model/types_test.go` — проверить, нет ли затронутых тестов
  - `internal/watcher/telegram.go` — заполнять `Source.AccountID = w.cfg.ID`
  - `internal/watcher/telegram_test.go` — обновить ожидания
  - `internal/config/config.go` — новые поля + обновить валидацию
  - `internal/config/config_test.go` — обновить тесты
  - `internal/sink/telegram_notifier.go` — рефакторинг конструктора
  - `internal/sink/telegram_notifier_test.go` — обновить тесты
  - `cmd/jeeves/main.go` — обновить инициализацию

- **Новые файлы:**
  - `internal/sink/telegram_reaction.go`
  - `internal/sink/telegram_reaction_test.go`

- **Ключевые зависимости:** `go-telegram-bot-api/v5`; `setMessageReaction` вызывается
  через `bot.MakeRequest` (нативной поддержки в v5 нет, API доступен с Bot API 7.0)

## Development Approach

- **Тестирование:** TDD — тесты пишутся перед реализацией
- Каждая задача завершается только после прохождения тестов
- `go test ./...` и `go vet ./...` обязательны после каждой задачи
- Изменения минимальны: не трогаем то, чего задача не требует

## Testing Strategy

- **Unit-тесты:** table-driven, моки пишутся вручную (без фреймворков)
- **`TelegramReactionNotifier`:** httptest-сервер, который мокирует эндпоинт
  `setMessageReaction` и проверяет параметры запроса
- **`TelegramNotifier`:** существующий httptest-подход сохраняется, конструктор меняется
  на приём готового `*tgbotapi.BotAPI`

## Solution Overview

1. `model.Source` получает `AccountID` — идентификатор watcher-а, создавшего сообщение
2. `TelegramWatcher` заполняет это поле при конвертации
3. Конфиг: `notify.telegram.bot_token` удаляется, добавляется `watcher_id`; в watcher — `reaction_enabled`
4. `TelegramNotifier` принимает готовый бот вместо токена
5. Новый `TelegramReactionNotifier` (model.Notifier): фильтрует по `Source.AccountID`,
   ставит реакцию через `MakeRequest("setMessageReaction", ...)`
6. `main.go`: один бот на watcher, шарится между watcher / notifier / reactor

## Technical Details

### Структура `Source` после изменений

```go
type Source struct {
    Kind      string  // "telegram", "imap"
    ID        string  // chat_id для telegram
    Name      string  // отображаемое название
    AccountID string  // id watcher-а: "work", "personal"; пусто для imap
}
```

### Изменения конфига

```toml
[[watchers.telegram]]
id = "work"
token = "${TG_BOT_TOKEN}"
owner_id = "12345"
groups = [-100123456789]
on_join = "monitor"
reaction_enabled = true   # новое поле

[notify.telegram]
# bot_token убирается
watcher_id = "work"        # новое поле; можно опустить если watcher один
chat_id = 12345
```

### Новый конструктор `TelegramNotifier`

```go
func NewTelegramNotifier(bot *tgbotapi.BotAPI, chatID int64) *TelegramNotifier
```

### `TelegramReactionNotifier`

```go
type TelegramReactionNotifier struct {
    bot       *tgbotapi.BotAPI
    watcherID string
    emoji     string
}

func NewTelegramReactionNotifier(bot *tgbotapi.BotAPI, watcherID string) *TelegramReactionNotifier

func (n *TelegramReactionNotifier) Notify(_ context.Context, tasks []model.Task) error
// - пропускает если tasks пусто
// - пропускает если OriginMessage.Source.Kind != "telegram"
// - пропускает если OriginMessage.Source.AccountID != n.watcherID
// - вызывает setMessageReaction через bot.MakeRequest
```

### Инициализация в `main.go`

```go
bots := make(map[string]*tgbotapi.BotAPI)
for _, tgCfg := range cfg.Watchers.Telegrams {
    bot, _ := tgbotapi.NewBotAPI(tgCfg.Token)
    bots[tgCfg.ID] = bot
    tgWatcher := watcher.NewTelegramWatcher(bot, ...)
    watchers = append(watchers, tgWatcher)
    if tgCfg.ReactionEnabled {
        notifiers = append(notifiers, sink.NewTelegramReactionNotifier(bot, tgCfg.ID))
    }
}
// DM-нотификатор: найти бот по watcher_id
notifyBot := bots[cfg.Notify.Telegram.WatcherID]  // или единственный если WatcherID пусто
notifiers = append(notifiers, sink.NewTelegramNotifier(notifyBot, cfg.Notify.Telegram.ChatID))
```

## Implementation Steps

### Task 1: Добавить `AccountID` в `model.Source`

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/types_test.go`

- [x] добавить поле `AccountID string` в структуру `Source` с комментарием
- [x] проверить `types_test.go` — обновить тесты, если есть ожидания по `Source`
- [x] `go test ./internal/model/...` — должны пройти
- [x] `go vet ./internal/model/...`

### Task 2: `TelegramWatcher` заполняет `Source.AccountID`

**Files:**
- Modify: `internal/watcher/telegram.go`
- Modify: `internal/watcher/telegram_test.go`

- [x] в тестах обновить ожидаемые `Source.AccountID` для сообщений (до реализации)
- [x] в `convertMessage` добавить `Source.AccountID = w.cfg.ID`
- [x] в `convertReplyMessage` добавить `Source.AccountID = w.cfg.ID`
- [x] `go test ./internal/watcher/...` — должны пройти
- [x] `go vet ./internal/watcher/...`

### Task 3: Обновить конфигурацию

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] написать тесты для новых случаев валидации (до реализации):
  - `reaction_enabled = true` без ошибок
  - `watcher_id` пустой при одном watcher — ок
  - `watcher_id` пустой при нескольких watcher-ах — ошибка
  - `watcher_id` указывает на несуществующий watcher — ошибка
  - `bot_token` в `notify.telegram` больше не требуется
- [x] добавить `ReactionEnabled bool` в `TelegramWatcherConfig`
- [x] добавить `WatcherID string` в `TelegramNotifyConfig`, убрать `BotToken string`
- [x] обновить `validate()`:
  - удалить проверку `notify.telegram.bot_token`
  - добавить проверку `watcher_id`: пустой при >1 watcher-ах → ошибка
  - добавить проверку: `watcher_id` должен совпадать с одним из `watchers.telegram[i].id`
- [x] `go test ./internal/config/...` — должны пройти
- [x] `go vet ./internal/config/...`

### Task 4: Рефакторинг `TelegramNotifier`

**Files:**
- Modify: `internal/sink/telegram_notifier.go`
- Modify: `internal/sink/telegram_notifier_test.go`

- [x] обновить тесты: создавать бот через `tgbotapi.NewBotAPIWithClient` с httptest-эндпоинтом
  и передавать готовый бот в конструктор (до реализации)
- [x] заменить `TelegramNotifierConfig` на `(bot *tgbotapi.BotAPI, chatID int64)` в конструкторе
- [x] убрать создание `*tgbotapi.BotAPI` внутри `NewTelegramNotifier`
- [x] `go test ./internal/sink/...` — должны пройти
- [x] `go vet ./internal/sink/...`

### Task 5: Реализовать `TelegramReactionNotifier`

**Files:**
- Create: `internal/sink/telegram_reaction.go`
- Create: `internal/sink/telegram_reaction_test.go`

- [x] написать table-driven тесты (до реализации):
  - сообщение из правильного watcher-а → вызов `setMessageReaction` с правильными параметрами
  - сообщение из другого watcher-а (`AccountID` не совпадает) → реакция не ставится
  - источник не telegram (imap) → реакция не ставится
  - пустой список задач → реакция не ставится
  - API возвращает ошибку → `Notify` возвращает ошибку
- [x] реализовать `TelegramReactionNotifier` со структурой и конструктором
- [x] реализовать `Notify`: фильтрация по `Source.Kind` и `Source.AccountID`, вызов `MakeRequest`
- [x] `go test ./internal/sink/...` — должны пройти
- [x] `go vet ./internal/sink/...`

### Task 6: Обновить `main.go`

**Files:**
- Modify: `cmd/jeeves/main.go`

- [x] создать `map[string]*tgbotapi.BotAPI` для хранения ботов по id
- [x] для каждого Telegram-watcher-а: создать бот, сохранить в map, создать watcher
- [x] если `ReactionEnabled` — создать `TelegramReactionNotifier` и добавить в notifiers
- [x] найти бот для DM-нотификатора:
  - если `WatcherID` непустой → `bots[cfg.Notify.Telegram.WatcherID]`
  - если пустой → единственный бот в map
- [x] создать `TelegramNotifier(bot, chatID)` и добавить в notifiers
- [x] убрать старое создание бота через `cfg.Notify.Telegram.BotToken`
- [x] `go build ./...` — должен успешно собраться

### Task 7: Верификация

- [x] `go test ./...` — все тесты проходят
- [x] `go vet ./...` — нет предупреждений
- [x] проверить, что `Source.AccountID` пустой для IMAP-источников (не сломали imap-путь)
- [x] переместить план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Убедиться, что бот имеет права ставить реакции в группе (нужны права участника)
- Проверить, что `setMessageReaction` работает с конкретным эмодзи ✍️ (не все эмодзи
  доступны в группах по умолчанию — зависит от настроек группы)
- Убедиться, что при IMAP-обещаниях реакция не пытается поставиться

**Обновление конфига:**
- Обновить `config.toml` или его пример: убрать `notify.telegram.bot_token`,
  добавить `watcher_id` и `reaction_enabled`
