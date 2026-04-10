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

- **fast** — classification (promise / command / skip). Optimize for cost and latency.
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

[datetime.time_of_day]
morning   = 11   # used for natural-language deadlines like "by morning"
lunch     = 12
afternoon = 14
evening   = 20
```

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

## Customizing AI Prompts

Huskwoot uses Go templates for AI prompts. Any prompt can be overridden without recompilation by placing files in a `prompts/` subdirectory of the config directory.

```
<config-dir>/
  config.toml
  prompts/
    group-classifier-system.gotmpl
    simple-classifier-system.gotmpl
    command-extractor-system.gotmpl
    extractor-system.gotmpl
    extractor-user.gotmpl
```

| File | Purpose |
|------|---------|
| `group-classifier-system.gotmpl` | System prompt for group chat classifier (promise / command / skip) |
| `simple-classifier-system.gotmpl` | System prompt for IMAP classifier (promise / skip) |
| `command-extractor-system.gotmpl` | System prompt for command extractor |
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

## Developer Documentation

- [Architecture and pipeline](docs/development/architecture.md)
- [Building and testing](docs/development/building.md)
- [OpenAPI specification](api/openapi.yaml)
