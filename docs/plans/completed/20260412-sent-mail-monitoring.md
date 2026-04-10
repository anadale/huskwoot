# Мониторинг отправленных писем (Sent folder)

## Overview

Добавить поддержку мониторинга папки «Отправленные» в IMAP watcher, чтобы обещания из ответов пользователя на письма тоже обнаруживались и сохранялись как задачи.

Пример:
```
Subject: Re: Новый аккаунт для Васи Пупкина
да, сделаю
Best regards, Gregory.
> Гриша, заведи плиз новый аккаунт! Очень нужно до завтра!
```
→ задача «Завести аккаунт для Васи Пупкина»

Ключевые изменения:
1. `IMAPConfig.Folder string` → `Folders []string` — один config entry мониторит несколько папок
2. Sent-письмо определяется по `from == cfg.Username` — фильтр `senders` не применяется
3. Тело письма разбивается на reply и quote; `msg.Text = reply`, `msg.ReplyTo.Text = quote`
4. Нормализация пустых строк применяется ко всему извлечённому тексту (не только HTML)

Детектор и экстрактор уже обрабатывают `ReplyTo` в промптах — изменений в AI-слое не требуется.

## Context (from discovery)

- **Файлы к изменению:**
  - `internal/watcher/mime.go` — `splitEmailReply`, нормализация plain text
  - `internal/watcher/mime_test.go` — тесты splitEmailReply
  - `internal/watcher/imap.go` — `Folder` → `Folders`, рефакторинг `watchAccount`, sent-логика
  - `internal/watcher/imap_test.go` — тесты multi-folder и sent-писем
  - `internal/config/config.go` — `Folder string` → `Folders []string`
  - `internal/config/config_test.go` — обновить тесты
  - `cmd/huskwoot/main.go` — `Folder: imapCfg.Folder` → `Folders: imapCfg.Folders`
  - `config.example.toml` — обновить пример

- **Уже готово (не требует изменений):**
  - `model.Message.ReplyTo *Message` — поле существует
  - `PromiseDetector` — шаблон уже содержит `{{if .ReplyTo}}...{{end}}`
  - `TaskExtractor` — шаблон уже использует `ReplyTo` и `Reaction`
  - `Pipeline.Process` — для IMAP owner-check уже пропускается (корректно для sent тоже)
  - StateStore ключ уже включает папку: `imap:username:folder`

- **Паттерны проекта:**
  - TDD: тесты пишутся до реализации
  - table-driven тесты: `[]struct{name, input, want}`
  - ручные моки без фреймворков
  - нет backward compatibility — просто переименовываем поля

## Development Approach

- **Подход к тестированию**: TDD (тесты пишутся до реализации)
- Каждую задачу завершать полностью перед переходом к следующей
- Небольшие, сфокусированные изменения
- **Каждая задача включает тесты** — тесты обязательны
- **Все тесты должны проходить** перед началом следующей задачи
- Команда: `go test ./...`
- Линтер: `go vet ./...`

## Solution Overview

Архитектура poll-цикла:
```
Watch()
  └─ for each config → go watchAccount(cfg)
       └─ for each folder → go watchFolder(cfg, folder)
              └─ ticker → pollOnce(cfg, folder, handler)
```

Логика в `convertIMAPMessage`:
```
if from == cfg.Username:
    reply, quote := splitEmailReply(bodyText)
    msg.Text = reply
    msg.ReplyTo = &Message{Text: quote}
    # senders filter не применяется
else:
    msg.Text = normalizeLines(bodyText)
    if !senderAllowed(from, cfg.Senders): return nil
```

## Technical Details

### `splitEmailReply(text string) (reply, quote string)`

Разбиваем по первому вхождению маркера цитаты:
- Блок строк, начинающихся с `>`
- `On ... wrote:` (Gmail/Apple Mail/Thunderbird)
- `-----Original Message-----` (Outlook)

`reply` = всё до первого маркера (trimmed)
`quote` = всё начиная с маркера (trimmed, без `>` префиксов строк)

Если маркер не найден — `reply = text`, `quote = ""`

### Нормализация пустых строк

Применять `strings.TrimSpace` + замену `\n{3,}` → `\n\n` ко всему финальному тексту — и для HTML, и для plain text. Вынести в `normalizeLines(text string) string`.

### Config

```toml
[[watchers.imap]]
folders = ["INBOX", "[Gmail]/Sent Mail"]
```

Поле `folder` (единственное число) убирается — это внутренний конфиг, нет внешних потребителей.

## Implementation Steps

### Task 1: нормализация пустых строк и `splitEmailReply` в `mime.go`

**Files:**
- Modify: `internal/watcher/mime.go`
- Modify: `internal/watcher/mime_test.go`

- [x] написать табличные тесты для `normalizeLines`: строки без лишних пустых строк, 3+ пустых строк → 2, только whitespace
- [x] написать табличные тесты для `splitEmailReply`: без цитаты, `>` цитата, `On ... wrote:`, `-----Original Message-----`, вложенные `>>`, пустое тело
- [x] вынести `reExtraLines.ReplaceAllString` из `stripHTML` в `normalizeLines(text string) string`
- [x] применить `normalizeLines` к plain text в `extractPartText` (case `text/plain`)
- [x] реализовать `splitEmailReply(text string) (reply, quote string)`
- [x] убедиться что `go test ./internal/watcher/...` проходит

### Task 2: `Folder` → `Folders` в конфиге

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] написать тест: `folders = ["INBOX"]` парсится корректно
- [x] написать тест: `folders = ["INBOX", "Sent"]` парсится как срез из двух элементов
- [x] написать тест: конфиг без `folders` не ломает валидацию (поле не обязательно)
- [x] переименовать `IMAPConfig.Folder string` → `Folders []string`, обновить TOML-тег
- [x] убедиться что `go test ./internal/config/...` проходит

### Task 3: multi-folder polling в `IMAPWatcher`

**Files:**
- Modify: `internal/watcher/imap.go`
- Modify: `internal/watcher/imap_test.go`

- [x] написать тест: при `Folders = ["INBOX", "Sent"]` watcher вызывает handler для писем из обеих папок
- [x] написать тест: каждая папка использует отдельный StateStore ключ
- [x] переименовать `IMAPWatcherConfig.Folder string` → `Folders []string`
- [x] рефакторить `watchAccount(cfg)`: запустить по одной горутине на папку через `watchFolder(cfg, folder)`
- [x] `watchFolder` содержит ticker-цикл и вызывает `pollOnce(cfg, folder, handler)` — переработать сигнатуру `pollOnce` чтобы принимать `folder string` явно
- [x] убедиться что `go test ./internal/watcher/...` проходит

### Task 4: sent-письма в `convertIMAPMessage`

**Files:**
- Modify: `internal/watcher/imap.go`
- Modify: `internal/watcher/imap_test.go`

- [x] написать тест: письмо где `from == cfg.Username` → `msg.Text = reply`, `msg.ReplyTo.Text = quote`, фильтр senders не применяется
- [x] написать тест: письмо без цитаты от пользователя → `msg.ReplyTo == nil`, `msg.Text = полное тело`
- [x] написать тест: входящее письмо (`from != username`) — поведение не изменилось
- [x] написать тест: входящее письмо не из senders → `nil` (как раньше)
- [x] в `convertIMAPMessage`: если `from == cfg.Username` → вызвать `splitEmailReply`, заполнить `Text` и `ReplyTo`
- [x] применить `normalizeLines` к `msg.Text` для не-sent писем (входящие текст уже нормализован через `extractPartText`, но добавить явный вызов после `convertIMAPMessage`)
- [x] убедиться что `go test ./internal/watcher/...` проходит

### Task 5: обновить `main.go` и `config.example.toml`

**Files:**
- Modify: `cmd/huskwoot/main.go`
- Modify: `config.example.toml`

- [x] заменить `Folder: imapCfg.Folder` → `Folders: imapCfg.Folders` в `main.go` (строка ~209)
- [x] обновить `config.example.toml`: `folder = "INBOX"` → `folders = ["INBOX"]`, добавить закомментированный пример с Sent
- [x] убедиться что `go build ./cmd/huskwoot/` проходит

### Task 6: проверка критериев приёмки

- [x] проверить что все требования из Overview реализованы
- [x] проверить граничные случаи: письмо без цитаты, письмо с многоуровневой цитатой (`>>`), письмо с Outlook-маркером
- [x] запустить полный тестовый набор: `go test ./...`
- [x] запустить линтер: `go vet ./...`
- [x] убедиться что компиляция проходит: `go build ./...`

### Task 7: [Final] Документация

- [x] обновить `CLAUDE.md` если обнаружены новые паттерны
- [x] переместить план в `docs/plans/completed/`

## Post-Completion

**Ручная проверка:**
- Настроить Sent-папку в реальном конфиге и запустить в режиме `backfill` на нескольких отправленных письмах с явными обещаниями
- Убедиться что короткий ответ («да, сделаю») + цитата правильно извлекают задачу
- Проверить что входящие письма из INBOX продолжают обрабатываться без регрессий
