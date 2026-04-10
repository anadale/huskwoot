# Telegram Bot Guard — защита от несанкционированного добавления

## Overview

Любой пользователь Telegram может добавить персонального бота Huskwoot в свой чат. Злоумышленник может добавить бота в тысячи групп, постепенно расходуя память и хранилище (история сообщений). При этом реальных обещаний не создаётся, но нагрузка растёт.

**Решение:** при добавлении бота в группу он отправляет приветственное сообщение и ждёт подтверждения от владельца — ответа (reply) или реакции на это сообщение. Если подтверждение не поступает в течение таймаута (дефолт: 1 минута), бот покидает группу. Владелец, добавивший бота в свою группу, подтверждает мгновенно.

## Context

- **Ключевые файлы:**
  - `internal/channel/telegram.go` — основная логика, `TelegramChannel`, `Watch()`
  - `internal/channel/telegram_test.go` — тесты
  - `internal/config/config.go` — `TelegramWatcherConfig`
  - `cmd/huskwoot/main.go` — сборка `TelegramChannelConfig`
- **Паттерны:** интерфейс `botAPI`, raw API через `MakeRequest`, OwnerIDs для идентификации владельца
- **Ограничение:** tgbotapi v5.5.1 не имеет нативной поддержки `message_reaction`. Решение: заменить `GetUpdatesChan` на raw polling через `MakeRequest("getUpdates", ...)` с ручным парсингом JSON

## Development Approach

- **Testing approach:** Regular (реализация + тесты в рамках каждой задачи)
- Каждая задача включает написание тестов до перехода к следующей
- Все тесты должны проходить перед началом следующей задачи
- `go test ./...` после каждой задачи

## Solution Overview

1. При получении `my_chat_member` update (бот добавлен в группу) — отправляем welcome message, регистрируем `pending[chatID]`
2. Ждём от владельца (OwnerIDs): reply на welcome message **или** реакцию на него
3. Подтверждение → удаляем из pending, бот работает нормально
4. Таймаут (1 мин) → `leaveChat`, удаляем pending
5. Пока чат pending — его сообщения не обрабатываются пайплайном

**Для реакций:** заменяем `GetUpdatesChan` на raw polling через `MakeRequest("getUpdates", ...)`. Ответ парсим в собственный `rawUpdate` с полем `MessageReaction`. Polling timeout: 5 секунд (для отзывчивого shutdown через context).

`allowed_updates` при наличии ownerIDs: `["message", "edited_message", "my_chat_member", "message_reaction"]`

## Technical Details

### Новые типы

```go
// rawUpdate — обёртка для парсинга всех типов обновлений включая реакции
type rawUpdate struct {
    UpdateID        int                         `json:"update_id"`
    Message         *tgbotapi.Message           `json:"message"`
    EditedMessage   *tgbotapi.Message           `json:"edited_message"`
    MyChatMember    *tgbotapi.ChatMemberUpdated `json:"my_chat_member"`
    MessageReaction *rawMessageReaction         `json:"message_reaction"`
}

// rawMessageReaction — подмножество полей MessageReactionUpdated
type rawMessageReaction struct {
    Chat      tgbotapi.Chat  `json:"chat"`
    MessageID int            `json:"message_id"`
    User      *tgbotapi.User `json:"user"`
    Date      int            `json:"date"`
}

// pendingApproval — состояние ожидания подтверждения для группы
type pendingApproval struct {
    welcomeMsgID int
    deadline     time.Time
}
```

### Изменения в TelegramChannelConfig

```go
WelcomeMessage string        // дефолт: "Привет! Ответьте на это сообщение или поставьте реакцию для подтверждения."
ConfirmTimeout time.Duration // дефолт: 1 минута; 0 = guard отключён
```

### Изменения в TelegramWatcherConfig (TOML)

```toml
welcome_message = "..."  # опционально
confirm_timeout = "1m"   # опционально, дефолт 1m
```

### Watch() — новый polling loop

```
for {
    select { case <-ctx.Done(): return ctx.Err(); default: }
    checkAndExpirePending(ctx)                     // проверка таймаутов
    resp = MakeRequest("getUpdates", {offset, timeout:5, allowed_updates})
    parse resp.Result → []rawUpdate
    for each update:
        update offset
        switch:
          MyChatMember (bot added) → handleJoin
          MessageReaction           → handleReactionConfirmation
          Message/EditedMessage     → if pending[chatID]: skip; else: convertMessage + handler
        save cursor
}
```

## What Goes Where

**Implementation Steps** — задачи с кодом и тестами в этом репозитории.

**Post-Completion** — ручная проверка в реальном Telegram.

## Implementation Steps

### Task 1: Конфиг — добавить настройки guard

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] добавить поля `WelcomeMessage string` и `ConfirmTimeoutRaw string` в `TelegramWatcherConfig`
- [ ] добавить unexported поле `ConfirmTimeout time.Duration` (тег `toml:"-"`)
- [ ] в `validate()`: парсить `ConfirmTimeoutRaw` в `ConfirmTimeout`; если пусто → `1 * time.Minute`; отрицательное → ошибка
- [ ] написать тест: дефолтный таймаут 1 минута когда поле пустое
- [ ] написать тест: корректный парсинг `confirm_timeout = "2m30s"`
- [ ] написать тест: отрицательный таймаут → ошибка валидации
- [ ] `go test ./internal/config/...` — должно пройти

### Task 2: Guard-типы и расширение TelegramChannel

**Files:**
- Modify: `internal/channel/telegram.go`

- [ ] добавить типы `rawUpdate`, `rawMessageReaction`, `pendingApproval` в `telegram.go`
- [ ] добавить поля `WelcomeMessage string`, `ConfirmTimeout time.Duration` в `TelegramChannelConfig`
- [ ] добавить поля `pending map[int64]*pendingApproval`, `pendingMu sync.Mutex` в `TelegramChannel`
- [ ] в `newTelegramChannel`: инициализировать `pending` map; применить дефолты (`WelcomeMessage`, `ConfirmTimeout`)
- [ ] удалить `GetUpdatesChan` и `StopReceivingUpdates` из интерфейса `botAPI` (Watch больше не использует их)
- [ ] удалить поля `updates chan`, `stopped bool`, `lastCfg` из `mockBot` в тест-файле
- [ ] обновить `newMockBot` и методы мока (убрать `GetUpdatesChan`, `StopReceivingUpdates`)
- [ ] добавить в `mockBot` поле `updateBatches []json.RawMessage` и `cancelFn func()`
- [ ] добавить метод `mockBot.setUpdates(updates []rawUpdate)` — сериализует в JSON и добавляет батч; после последнего батча вызывает `cancelFn`
- [ ] добавить helper `makeRawUpdate(id int, msg *tgbotapi.Message) rawUpdate`
- [ ] `go build ./...` — должно компилироваться

### Task 3: Guard-методы

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [ ] реализовать `sendWelcomeMessage(ctx, chatID int64) (int, error)` — отправляет welcome message, возвращает message ID
- [ ] реализовать `handleJoin(ctx, upd *tgbotapi.ChatMemberUpdated)` — если бот добавлен (new=member/admin, old≠member/admin/creator) и `ConfirmTimeout > 0`: вызывает sendWelcomeMessage, регистрирует pending
- [ ] реализовать `confirmChat(chatID int64)` — удаляет из pending
- [ ] реализовать `isReplyConfirmation(msg *tgbotapi.Message) bool` — проверяет: msg.From ∈ ownerIDs && msg.ReplyToMessage.MessageID == pending[chatID].welcomeMsgID
- [ ] реализовать `isReactionConfirmation(r *rawMessageReaction) bool` — проверяет: r.User ∈ ownerIDs && r.MessageID == pending[chatID].welcomeMsgID
- [ ] реализовать `leaveChat(ctx, chatID int64) error` — `MakeRequest("leaveChat", ...)`
- [ ] реализовать `checkAndExpirePending(ctx)` — итерируем pending, если `time.Now().After(deadline)` → leaveChat + delete
- [ ] написать тест: `handleJoin` для нового чата → Send вызван, pending зарегистрирован
- [ ] написать тест: `handleJoin` для группы из cfg.Groups → guard не активируется (уже в whitelist, пропустить добавление в pending)
- [ ] написать тест: `handleJoin` когда `ConfirmTimeout == 0` → Send не вызван
- [ ] написать тест: `isReplyConfirmation` от владельца с правильным reply → true
- [ ] написать тест: `isReplyConfirmation` от не-владельца → false
- [ ] написать тест: `isReactionConfirmation` от владельца на правильное сообщение → true
- [ ] написать тест: `checkAndExpirePending` — просроченный pending → leaveChat вызван
- [ ] написать тест: `checkAndExpirePending` — непросроченный pending → leaveChat не вызван
- [ ] `go test ./internal/channel/...` — должно пройти

### Task 4: Watch() — переход на raw polling

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [ ] заменить `Watch()`: убрать `GetUpdatesChan`/`StopReceivingUpdates`, реализовать raw polling через `MakeRequest("getUpdates", ...)` с timeout=5
- [ ] добавить `allowed_updates: ["message","edited_message","my_chat_member","message_reaction"]` в params когда `len(ownerIDs) > 0`; иначе пустой список
- [ ] в polling loop: context check → `checkAndExpirePending` → poll → per-update dispatch:
  - `MyChatMember` → `handleJoin`
  - `MessageReaction` → если `isReactionConfirmation` → `confirmChat`
  - `Message`/`EditedMessage` → если `pending[chatID]` существует и `isReplyConfirmation` → `confirmChat` + пропустить как сообщение; иначе если pending → пропустить; иначе — обычная обработка (`convertUpdate` + handler)
- [ ] сохранять cursor после каждого обновления (включая guard-обработанные)
- [ ] обновить `mockBot.MakeRequest` для "getUpdates": возвращать batch из `updateBatches`; после последнего батча — вызвать `cancelFn`
- [ ] обновить все `TestWatch_*` тесты: заменить `bot.updates <- update; close(bot.updates)` на `bot.setUpdates([]rawUpdate{...})` + `ctx, cancel := context.WithCancel(ctx); bot.cancelFn = cancel`
- [ ] написать тест: `TestWatch_Guard_BotAdded_SendsWelcome` — MyChatMember update → bot.Send вызван
- [ ] написать тест: `TestWatch_Guard_ReplyConfirms` — reply от владельца на welcome msg → сообщение подтверждено, нормальная обработка
- [ ] написать тест: `TestWatch_Guard_ReactionConfirms` — reaction от владельца → подтверждение
- [ ] написать тест: `TestWatch_Guard_Timeout_Leaves` — expired pending → leaveChat вызван (через makeReqCalls)
- [ ] написать тест: `TestWatch_Guard_PendingMessagesSkipped` — обычное сообщение в pending-чате → handler не вызван
- [ ] `go test ./internal/channel/...` — должно пройти

### Task 5: Интеграция в main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [ ] в `buildTelegramComponents`: передавать `WelcomeMessage` и `ConfirmTimeout` из `tgCfg` в `channel.TelegramChannelConfig`
- [ ] `go build ./...` — компилируется

### Task 6: Финальная проверка

**Files:**
- Modify: `config.example.toml` — добавить документацию новых полей (как комментарий)

- [ ] проверить, что все требования из Overview реализованы
- [ ] `go test ./...` — все тесты проходят
- [ ] `go vet ./...` — нет предупреждений
- [ ] добавить в `config.example.toml` закомментированные примеры `welcome_message` и `confirm_timeout`
- [ ] переместить план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка в Telegram:**
- добавить бота в свою группу → убедиться, что приходит welcome message
- ответить на welcome message → бот работает нормально
- поставить реакцию на welcome message → бот работает нормально
- не отвечать в течение 1 минуты → бот покидает группу
- попросить кого-то другого добавить бота в чужую группу → бот уходит

**Конфигурация:**
- убедиться, что `confirm_timeout` и `welcome_message` правильно читаются из `config.toml`
