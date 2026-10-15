# Huskwoot — Personal Promise Tracker

A self-hosted background service that monitors your communication channels — Telegram groups and IMAP email — and automatically captures the commitments you make. Promises are recognized with a two-stage AI pipeline: a fast model filters the message stream, and a smart model extracts structured tasks. Found tasks are saved to a local SQLite database and delivered as Telegram DM notifications.

**The problem:** commitments made in chats and at meetings get lost — there's no single place they land automatically.

**The solution:** passive channel monitoring → AI recognition → task saved + notification sent.

---

## Features

### Telegram Group Monitoring

Add the bot to any Telegram group and it will silently watch for promises you make. As soon as a commitment is detected, the bot acknowledges it with a ✍️ reaction on the original message and follows up with 👍 once the task is saved — giving you immediate, low-noise feedback without replying in the group thread.

Enable reactions with `reaction_enabled = true` in the `[channels.telegram]` config section.

#### Bot Guard

When a bot is added to a group by someone other than the owner, there's a risk of it landing in unintended chats. The bot guard feature mitigates this: upon joining a new group, the bot posts a `welcome_message` and waits `confirm_timeout` for the owner to reply or react. If no confirmation arrives, the bot leaves automatically.

```toml
[channels.telegram]
confirm_timeout = "1m"
welcome_message = "Hello! Reply to this message or react to confirm."
```

Set `confirm_timeout = "0"` or omit the field to disable the guard.

### DM Agent

Send a message directly to your bot and get a full task management assistant powered by tool calling. The agent understands natural language — no commands to memorize.

Available in DM:
- **Create and list projects** — organize tasks across areas of work
- **Create tasks** — add tasks with summaries, deadlines, and project assignment
- **List and filter tasks** — by project and status
- **Complete and move tasks** — between projects
- **Bind a chat to a project** — so tasks from a specific group go directly to the right project

### Extracting Promises from Forwarded 1:1 Messages

In a private Telegram chat you can't add a bot — so commitments you make there get lost. Huskwoot solves this: forward any selection of messages from a 1:1 conversation into your DM with the bot. The bot accumulates them silently, extracts the promises **you** made, and checks for existing obligations before creating tasks.

- **Trigger:** wait for the silence timeout (`forward_batch_timeout`) or send any non-forwarded message to kick off processing immediately. Include `#<project-slug-or-alias>` (e.g. `обработай #вася` or just `#вася`) to route tasks to a specific project; otherwise they go to Inbox.
- **What's recognized:** only your promises — detected from Telegram forward metadata (`forward_from.id`). Your messages are labeled `[я, …]`; the other person's messages provide context.
- **Cleanup:** set `delete_forwards_after_extract = true` to delete the processed forwarded messages and trigger from DM only when the batch immediately creates at least one task. A pending-only or duplicate-only batch leaves the Telegram messages intact.

```toml
[channels.telegram]
forward_batch_timeout         = "10s"   # auto-process after 10 s of silence; "" or "0s" disables
delete_forwards_after_extract = false   # clean up forwarded messages after extraction
```

### Voice Messages in Telegram DM

Send a voice message directly to your bot and it will be transcribed and processed by the DM agent — no typing required. Transcription runs asynchronously in the background; the bot replies once processing completes and automatically retries on transient failures.

Voice messages are supported only in direct messages (DM). Group and @mention voice messages are not transcribed.

Two transcription providers are available:

- **whisper.cpp** — runs `whisper-cli` locally as a subprocess via ffmpeg. Requires `whisper-cli`, `ffmpeg`, and a model file on the server.
- **Yandex SpeechKit** — sends audio to Yandex Cloud STT sync REST API. Requires an API key and a folder ID.

When `provider = ""` (or the `[transcriber]` section is absent), the bot replies with a friendly "not configured" message on any voice message and continues to work normally for text input.

See **[Voice recognition setup](docs/voice-recognition.md)** for installation and configuration walkthroughs for both providers.

---

### @mention and Reply in Groups

Mention the bot (`@botname`) or reply to one of its messages directly in a monitored group. The agent responds in your Telegram DM, with access to all task management tools except project creation and listing (those are DM-only).

This is useful for asking quick questions about your task list or making ad-hoc updates without switching to a DM conversation.

### IMAP Email Monitoring

Connect one or more email accounts. Huskwoot monitors both incoming and outgoing mail:

- **Inbox** — monitors incoming emails: messages from others, including meeting summaries and batch content forwarded to yourself
- **Sent folder** — captures commitments you made in outgoing emails and replies

Each account supports its own folder list, optional sender filter (applied only to incoming mail), and an independent read cursor. Multiple accounts are fully supported.

```toml
[[channels.imap]]
host     = "imap.gmail.com"
port     = 993
username = "you@example.com"
password = "${IMAP_PASSWORD}"
folders  = ["INBOX", "[Gmail]/Sent Mail"]
label    = "Work email"
senders  = ["boss@company.com"]
```

### Scheduled Summaries

The bot can send digest messages up to three times a day. Each digest is organized into four sections:

| Section | Contents |
|---------|----------|
| **Overdue** | Tasks with a past deadline |
| **Due today** | Tasks due today |
| **Upcoming** | Tasks with a deadline within `plans_horizon` |
| **No deadline** | Tasks without a date (limited by `undated_limit`) |

Digests are sent only on working days (`weekdays`). Missed slots on restart are not replayed.

### HTTP API and Push Notifications

Huskwoot exposes a REST API for mobile clients — projects, tasks, SSE event stream, and a secure device pairing flow via Telegram DM. Push notifications are delivered through an optional `huskwoot-push-relay` service.

Mobile clients can send voice recordings via `POST /v1/chat/voice` (multipart upload). The server transcribes the audio asynchronously and delivers the transcript and the agent's reply as `chat_message_created` SSE events. The `GET /v1/me` response includes a `voiceEnabled` flag indicating whether transcription is configured.

---

## Deployment

Three deployment variants are available in the `deploy/` directory:

| Variant | When to use |
|---------|-------------|
| [`deploy/huskwoot/`](deploy/huskwoot/) | No mobile app, or you'll set up push separately |
| [`deploy/huskwoot-with-relay/`](deploy/huskwoot-with-relay/) | Mobile app with push notifications on a single VPS |
| [`deploy/push-relay/`](deploy/push-relay/) | You operate a shared relay for multiple Huskwoot instances |

**→ [Quick Start: deploy on Ubuntu with Docker Compose](docs/quick-start.md)**

---

## Configuration Reference

Config file: `config.toml` in the config directory.
Environment variable substitution: `field = "${VAR_NAME}"` syntax is supported throughout.

**Config directory** (in priority order):
1. `--config-dir` flag
2. `HUSKWOOT_CONFIG_DIR` environment variable
3. XDG default: `~/.config/huskwoot`

Full annotated example: [`config.example.toml`](config.example.toml)

---

### `[user]`

```toml
[user]
user_name        = "Alice"
aliases          = ["Alya", "Al"]    # other names people use to mention you
telegram_user_id = 123456789         # your numeric Telegram user ID
language         = "ru"              # "ru" | "en", default "ru"
```

`telegram_user_id` is used for DM notifications and as the default target for scheduled summaries. To find yours, send any message to [@userinfobot](https://t.me/userinfobot).

`language` sets the language for Telegram notifications, AI prompts, and natural-language date parsing. Supported values: `"ru"` (Russian) and `"en"` (English).

---

### `[ai.fast]` and `[ai.smart]`

```toml
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key  = "${OPENAI_API_KEY}"
model    = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key  = "${OPENAI_API_KEY}"
model    = "gpt-4o"
```

- **fast** — classification (promise / skip). Optimize for cost and latency.
- **smart** — task extraction and the DM agent. Optimize for accuracy.

Both support any OpenAI-compatible API. For local models via Ollama, set `base_url = "http://localhost:11434/v1"`.

---

### `[channels.telegram]`

```toml
[channels.telegram]
token    = "${TELEGRAM_BOT_TOKEN}"
# name   = "@myhuskwootbot"      # optional display name shown in pairing messages
on_join  = "monitor"             # "monitor" (new messages only) | "backfill" (fetch history on startup)
# reaction_enabled = true        # react ✍️ on detection, 👍 after saving (default: false)
# confirm_timeout  = "1m"        # bot guard wait time; "0" or omit to disable
# welcome_message  = "Hello!"    # sent when bot joins a group; reply/react to confirm
```

Only one Telegram bot is supported. Omitting this section disables Telegram entirely.

---

### `[[channels.imap]]`

```toml
[[channels.imap]]
host     = "imap.gmail.com"
port     = 993
username = "user@example.com"
password = "${IMAP_PASSWORD}"
folders  = ["INBOX"]
# folders = ["INBOX", "[Gmail]/Sent Mail"]   # include sent mail
label    = "Work email"                      # display name in notifications (defaults to folder name)
# senders = ["boss@company.com"]             # only process mail from these addresses (not applied to sent)
on_first_connect = "monitor"                 # "monitor" | "backfill"
```

Multiple `[[channels.imap]]` sections are supported. Each folder in a single account has its own read cursor and goroutine.

---

### `[history]`

```toml
[history]
max_messages = 200   # max messages per channel kept in memory for AI context
ttl          = "24h" # how long to keep messages (Go duration format)
```

History provides conversational context to the extraction model — surrounding messages help it understand what the commitment refers to.

---

### `[datetime]`

```toml
[datetime]
timezone = "Europe/Moscow"                      # IANA timezone (default: system local)
weekdays = ["mon", "tue", "wed", "thu", "fri"]  # working days; others are treated as weekends
# night_cutoff = "05:00"                         # see below

[datetime.time_of_day]
morning   = 11   # used for natural-language deadlines like "by morning"
lunch     = 12
afternoon = 14
evening   = 20
default   = "evening"  # hour applied when a date is given without an explicit time (e.g. "tomorrow", "on Monday"); does not affect "until …" deadline expressions
```

**`night_cutoff`** — опциональная настройка для ночных пользователей. Если вы часто работаете ночью и говорите боту «завтра» в 02:00, имея в виду текущий календарный день (до сна), включите эту опцию:

```toml
[datetime]
night_cutoff = "05:00"  # до 05:00 «завтра» означает «сегодня», «послезавтра» — «завтра»
```

До указанного времени (граница строгая: в `05:00:00` сдвиг уже **не работает**) слова «завтра»/«tomorrow» и «послезавтра»/«day after tomorrow» сдвигаются на −1 день; дедлайн-выражения «до завтра» и «до послезавтра» сдвигаются так же. Слово «сегодня»/«today» всегда остаётся буквальным — текущим календарным днём. Остальные конструкции (дни недели, «через N дней», конкретные даты) не затрагиваются. Значение `""` или `"00:00"` отключает функцию (поведение по умолчанию).

---

### `[reminders]`

Enable scheduled summaries by adding a `[reminders.schedule]` section. Without it, no digests are sent. Summaries are sent to `telegram_user_id` from `[user]`.

```toml
[reminders]
plans_horizon   = "168h"     # deadline window for "Upcoming" section (Go duration; "d" not supported)
undated_limit   = 5          # max tasks without a deadline to include (0 = hide all)
send_when_empty = "morning"  # "always" | "never" | "morning"

[reminders.schedule]
morning   = "09:00"   # required (24-hour format "HH:MM")
afternoon = "14:00"   # leave empty ("") to disable this slot
evening   = "20:00"
```

---

### `[api]`

```toml
[api]
enabled              = true
listen_addr          = "127.0.0.1:8080"
external_base_url    = "https://huskwoot.example.com"  # used in pairing links sent via Telegram DM
request_timeout      = "30s"
chat_timeout         = "60s"
events_retention     = "168h"    # SSE event history window (Go duration; "d" suffix not supported)
cors_allowed_origins = []
pairing_link_ttl            = "5m"
pairing_status_long_poll    = "60s"
rate_limit_pair_per_hour    = 5
voice_max_bytes             = "10MiB"  # MaxBytesReader limit for POST /v1/chat/voice (default: 10MiB)
voice_rate_limit_per_minute = 10       # per-device rate limit for POST /v1/chat/voice (default: 10; negative → startup error)
```

**Device pairing:** a client sends `POST /v1/pair/request`. The owner receives a Telegram DM with a confirmation link. After approval, the client receives a bearer token for all subsequent API calls.

---

### `[push]`

```toml
[push]
relay_url       = "https://push.huskwoot.app"
instance_id     = "your-instance-id"    # assigned by the relay operator
instance_secret = "${HUSKWOOT_PUSH_SECRET}"
# timeout             = "10s"
# dispatcher_interval = "2s"
# batch_size          = 32
# retry_max_attempts  = 4
```

Without this section (or with any field empty), the push dispatcher does not start. SSE and the REST API continue to work normally.

---

### `[devices]`

Retention windows for paired devices. The server runs an hourly sweep that auto-revokes stale devices and physically removes long-revoked records so bearer tokens from abandoned re-pairings do not accumulate in the database.

```toml
[devices]
inactive_threshold = "720h"    # 30 days of inactivity → auto-revoke (default: 720h)
retention_period   = "2160h"   # 90 days after revoke → DELETE from the store (default: 2160h)
```

**How it works:**

- A device is considered inactive when `COALESCE(last_seen_at, created_at) < now - inactive_threshold`. `last_seen_at` is updated on every successful authenticated API request.
- When a device is auto-revoked, the push relay is notified via `DeleteRegistration` so it stops delivering pushes to that token. Relay errors are logged but do not block the local revoke.
- Rows where `revoked_at < now - retention_period` are physically deleted on the same hourly tick.
- Setting either field to `0s` disables the corresponding sweep. Omitting the `[devices]` section uses the defaults above.

Both values use Go duration format; the `"d"` suffix is not supported (use `"720h"`, not `"30d"`).

---

### `[tasks]`

Пороги смысловой проверки применяются только к автоматическому извлечению из Telegram, не к явным командам агента и не к REST API.

```toml
[tasks]
duplicate_threshold = 0.95  # minimum sameness score for skipping a confirmed duplicate
review_threshold    = 0.70  # distinct candidates below this score may be created
closed_retention    = "336h"
cleanup_time        = "04:00"
```

Требуется `0 < review_threshold < duplicate_threshold <= 1`. Оценка модели — не откалиброванная вероятность. Даже высокий балл не позволяет пропустить кандидата при конфликте требований или совпадении только с закрытой задачей.

`closed_retention` задаёт срок хранения выполненных и отменённых задач; `0s` отключает их ежедневную очистку. `cleanup_time` — время очистки в часовом поясе пользователя.

---

### `[transcriber]`

Configures voice message transcription for Telegram DM and for `POST /v1/chat/voice` from HTTP API clients. Voice messages in groups and @mention chats are not transcribed regardless of this setting. When the section is absent or `provider = ""`, the bot replies with a "not configured" message on any Telegram voice input and the API returns `503` on voice upload attempts.

```toml
[transcriber]
provider     = "whisper"   # "" | "whisper" | "yandex"; empty = disabled
max_duration = "5m"        # reject voice messages longer than this (default: 5m)
uploads_dir  = ""          # directory for HTTP API voice uploads; default: <config-dir>/voice_uploads (created on startup)
```

Provider-specific subsections (`[transcriber.whisper]`, `[transcriber.yandex]`) hold the credentials and runtime options. Step-by-step setup — installing whisper.cpp, picking and downloading a GGML model, creating a Yandex Cloud service account and API key, plus troubleshooting tips — is covered in a dedicated guide:

**→ [Voice recognition setup](docs/voice-recognition.md)**

---

## Customizing AI Prompts

Huskwoot uses Go templates for AI prompts. The prompts listed below can be overridden without recompilation by placing files in a `prompts/` subdirectory of the config directory. The duplicate checker's language-specific prompts are built into the binary.

```
<config-dir>/
  config.toml
  prompts/
    group-classifier-system.gotmpl
    simple-classifier-system.gotmpl
    extractor-system.gotmpl
    extractor-user.gotmpl
```

| File | Purpose |
|------|---------|
| `group-classifier-system.gotmpl` | System prompt for group chat classifier (promise / skip) |
| `simple-classifier-system.gotmpl` | System prompt for IMAP classifier (promise / skip) |
| `extractor-system.gotmpl` | System prompt for task extractor |
| `extractor-user.gotmpl` | User-turn prompt for task extractor (all routes) |

Missing files fall back to the built-in templates.

**Template variables:**

*Classifier system prompts:*

| Variable | Type | Description |
|----------|------|-------------|
| `.UserName` | `string` | Monitored user's name |
| `.Aliases` | `[]string` | User's aliases |

*Extractor system prompt:*

| Variable | Type | Description |
|----------|------|-------------|
| `.UserName` | `string` | Monitored user's name |
| `.Aliases` | `[]string` | User's aliases |

*Extractor user prompt:*

| Variable | Type | Description |
|----------|------|-------------|
| `.Text` | `string` | Message text |
| `.Subject` | `string` | Email subject (IMAP only) |
| `.ReplyTo` | `*model.Message` | Message being replied to |
| `.Reaction` | `*model.Reaction` | Emoji reaction |
| `.History` | `[]model.HistoryEntry` | Conversation history (`AuthorName`, `Text`, `Timestamp`) |
| `.Now` | `time.Time` | Current time |

---

## Push Relay

Huskwoot includes a push notification system for mobile clients (iOS / Android). It consists of two components:

- **huskwoot** — when `[push]` is configured, runs a dispatcher that reads the push queue and sends HMAC-signed requests to the relay.
- **huskwoot-push-relay** — a standalone public service that holds APNs/FCM keys and forwards notifications to devices. Stores only `(instance_id, device_id) → tokens` mappings — no user data.

Device registration happens automatically:
- At pairing (Telegram DM → browser confirmation) — the instance registers tokens with the relay.
- At `PATCH /v1/devices/me` (push token update) — upsert in the relay.
- At device revoke — the registration is removed from the relay.

**Links:**
- [Push relay operator guide](docs/push-relay/setup.md) — APNs/FCM key setup, smoke test
- [HMAC protocol reference](docs/push-relay/hmac.md) — for client developers
- [Onboarding an instance to the relay](deploy/push-relay/README.md)

---

## Извлечение обещаний из форварднутых сообщений

### Зачем

В личных чатах Telegram (1:1) нельзя добавить бота. Обещания, которые вы даёте собеседнику, нигде не фиксируются автоматически — их нужно вручную пересказывать боту или записывать отдельно.

### Как пользоваться

1. Выберите несколько сообщений из личной переписки с кем-либо и перешлите их в DM к вашему боту.
2. Бот молча накапливает пересланные сообщения.
3. Либо подождите паузу (задаётся `forward_batch_timeout`), либо напишите боту любое обычное сообщение-триггер — обработка запустится немедленно.
4. Бот извлечёт ваши обещания и проверит их на дубли. Новые задачи появятся в списке, а сомнительные предложения потребуют решения в личном чате.

**Выбор проекта:** добавьте в текст триггера `#<slug-или-alias-проекта>` (например, `обработай #вася` или просто `#вася`) — задачи попадут в указанный проект. Без хинта — в Inbox.

### Что распознаётся

Только **ваши** обещания — т.е. сообщения, которые вы отправляли собеседнику. Определение основано на метаданных Telegram-форварда (`forward_from.id`). Ваши реплики в диалоге помечаются `[я, …]`, сообщения собеседника — `[Имя, …]`; всё это передаётся языковой модели в виде размеченного текста переписки.

**Не распознаются:**
- форварды из групп и каналов (обрабатываются как обычные DM)
- обещания собеседника (только ваши)
- голосовые и медиа-сообщения без подписи (помечаются `[голосовое/вложение]`, обычно классифицируются как skip)
- переписка с пользователями, скрывшими историю пересылок (имя/ID может отсутствовать)

### Удаление форвардов

При `delete_forwards_after_extract = true` бот удаляет обработанные пересланные сообщения и сообщение-триггер из DM, только если пакет сразу создал хотя бы одну задачу. Подтверждение бота остаётся. При классификации `skip`, одних дублях или только ожидающих решения кандидатах форварды **не удаляются**.

### Конфигурация

```toml
[channels.telegram]
forward_batch_timeout         = "10s"   # пауза тишины для авто-обработки; "" или "0s" — фича выключена
delete_forwards_after_extract = false   # удалять форварды + триггер из DM при успешном извлечении
```

### Ограничения

- Окно удаления сообщений в Telegram — 48 часов. Ошибка удаления не отменяет уже сохранённые задачи и решения.
- Если сервер упал между сохранением результатов и удалением форвардов в Telegram, результаты уже в базе, а форварды могут остаться в DM. Повтор того же входа распознаётся по сохранённым receipts; повторная пересылка с новым message ID проходит смысловую проверку.
- При перезапуске сервера sweeper подхватит незавершённые батчи на первом тике.

## Защита от дублей задач Telegram

Автоматическое извлечение из групп и пересылок сравнивает кандидатов со всеми открытыми задачами целевого проекта, ожидающими подтверждения предложениями и ранее отклонёнными обязательствами. Закрытые задачи за последние 14 дней служат контекстом, но не основанием автоматически скрыть новую работу. Кандидаты одного извлечения также сравниваются между собой.

Проверка использует smart-модель и сопоставляет действие, объект, период работы и исходный текст. Одна тема не делает задачи дублями; отчёты за разные периоды могут быть отдельными задачами. Неоднозначная смена срока требует решения, а не молчаливого изменения существующей задачи.

- Явный дубль не создаётся.
- Явно новое обязательство создаётся обычным способом.
- Сомнительный кандидат сохраняется отдельно от задач. Бот отправляет владельцу личный вопрос с похожей задачей, причиной и inline-кнопками **«👍 Добавить» / «👎 Не добавлять»**. Это кнопки, не реакции на сообщение: получение событий настоящих реакций требует прав администратора в чате.
- Положительный ответ создаёт задачу ровно один раз; отрицательный сохраняет отказ. Первое решение окончательно, повторный клик не создаёт и не удаляет задачу.
- Без ответа задача не создаётся. Перезапуск не теряет кандидатов; неотправленные вопросы доставляются повторно. При сбое между отправкой вопроса и сохранением его message ID возможен повтор вопроса, но обе кнопки относятся к одному кандидату.
- Ошибка сравнения приводит к вопросу, а не к потере кандидата или созданию вслепую. Если сравнивать не с чем, первая задача создаётся без дополнительного AI-запроса.

Результаты обработки входов, кандидаты, решения, задачи, events и push queue сохраняются транзакционно. Для пересылок та же транзакция очищает только обработанный снимок буфера; сообщения, пришедшие во время AI-запроса, остаются для следующей обработки.

Групповой update подтверждается Telegram только после успешной обработки. Поэтому polling может ждать AI-запрос группы; после ошибки сохранения вход повторяется с задержкой. Явные команды создания, REST API и IMAP не переводятся на этот механизм. Уже накопившиеся дубли автоматически не удаляются.

---

## Developer Documentation

- [Architecture and pipeline](docs/development/architecture.md)
- [Building and testing](docs/development/building.md)
- [OpenAPI specification](api/openapi.yaml)
