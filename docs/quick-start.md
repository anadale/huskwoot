# Quick Start: Deploy Huskwoot on Ubuntu with Docker Compose

This guide walks you through deploying the Huskwoot server on an Ubuntu VPS using pre-built Docker images from GitHub Container Registry. By the end, you'll have a running instance that monitors your Telegram groups, recognizes promises via AI, and sends you Telegram DM notifications.

**Estimated time:** 20–30 minutes

---

## What You'll Need

- **Ubuntu 22.04+** VPS (1 GB RAM minimum)
- **Public domain** (e.g. `huskwoot.example.com`) with an A record pointing to your VPS IP
- **Telegram bot token** — create a bot with [@BotFather](https://t.me/BotFather)
- **Your Telegram user ID** — send any message to [@userinfobot](https://t.me/userinfobot)
- **OpenAI API key** (or a compatible API, e.g. [Ollama](https://ollama.com) for local models)

---

## Step 1 — Install Docker

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
newgrp docker
```

Verify:

```bash
docker compose version
# Docker Compose version v2.x.x
```

---

## Step 2 — Get the Deploy Files

```bash
git clone --depth 1 https://github.com/anadale/huskwoot.git
cd huskwoot/deploy/huskwoot
```

The directory contains three files:

```
docker-compose.yml   — service definitions (huskwoot + Caddy)
Caddyfile            — reverse proxy config with automatic TLS
config.toml.example  — annotated configuration template
```

---

## Step 3 — Create the `.env` File

```bash
cat > .env <<'EOF'
OPENAI_API_KEY=sk-...
TELEGRAM_BOT_TOKEN=123456789:ABC-DefGhI...
TELEGRAM_OWNER_ID=123456789
EOF
chmod 600 .env
```

Replace the placeholder values:
- `OPENAI_API_KEY` — your OpenAI API key
- `TELEGRAM_BOT_TOKEN` — token from @BotFather (format: `123456789:ABC-...`)
- `TELEGRAM_OWNER_ID` — your numeric Telegram user ID from @userinfobot

> Never commit `.env` to version control.

---

## Step 4 — Configure Huskwoot

```bash
mkdir -p config
cp config.toml.example config/config.toml
```

Open `config/config.toml` and fill in:

```toml
[user]
user_name        = "Alice"              # your name, used in AI prompts
telegram_user_id = "${TELEGRAM_OWNER_ID}"  # leave as-is, reads from .env

[[channels.telegram]]
token    = "${TELEGRAM_BOT_TOKEN}"  # leave as-is
on_join  = "monitor"

[api]
enabled           = true
listen_addr       = "0.0.0.0:8080"
external_base_url = "https://huskwoot.example.com"  # your domain
```

Everything else can stay at its default value for now.

---

## Step 5 — Configure the Domain

```bash
nano Caddyfile
```

Replace `server.example.org` with your domain:

```
huskwoot.example.com {
    reverse_proxy huskwoot:8080
    encode gzip
}
```

Make sure your domain's A record points to the VPS IP before starting — Caddy fetches TLS certificates automatically via Let's Encrypt on first boot.

Open firewall ports if needed:

```bash
sudo ufw allow 80
sudo ufw allow 443
```

---

## Step 6 — Start

```bash
docker compose up -d
```

Docker pulls the images and starts the containers. Watch the logs:

```bash
docker compose logs -f huskwoot
```

---

## Step 7 — Verify

```bash
curl -s https://huskwoot.example.com/healthz
# Expected: {"status":"ok"}
```

Send a message to your bot in Telegram — it should respond. Then add the bot to a monitored group and write a commitment such as _"I'll send you the report by Friday"_. Within a few seconds you should receive a DM notification with the captured task.

---

## What's Next

**Add IMAP email monitoring** — add a `[[channels.imap]]` section to `config.toml`:

```toml
[[channels.imap]]
host     = "imap.gmail.com"
port     = 993
username = "you@example.com"
password = "${IMAP_PASSWORD}"
folders  = ["INBOX", "[Gmail]/Sent Mail"]
label    = "Work email"
on_first_connect = "monitor"
```

Add `IMAP_PASSWORD=...` to `.env` and restart.

**Enable scheduled summaries** — add to `config.toml`:

```toml
[reminders]
plans_horizon   = "168h"
undated_limit   = 5
send_when_empty = "morning"

[reminders.schedule]
morning   = "09:00"
afternoon = "14:00"
evening   = "20:00"
```

**Enable push notifications** — deploy push-relay and then uncomment `[push]` in `config.toml`. See [`deploy/huskwoot-with-relay/`](../deploy/huskwoot-with-relay/) for a single-VPS setup.

**Full configuration reference** — see [`README.md`](../README.md#configuration-reference).

---

## Troubleshooting

**Bot doesn't respond in DM**
- Check `docker compose logs huskwoot` for errors
- Verify `TELEGRAM_BOT_TOKEN` is correct in `.env`
- Confirm `telegram_user_id` in `config.toml` matches your Telegram ID

**TLS certificate not issued**
- Confirm the domain resolves to your VPS: `dig +short huskwoot.example.com`
- Ports 80 and 443 must be open: `sudo ufw allow 80 && sudo ufw allow 443`
- Check Caddy logs: `docker compose logs caddy`

**Tasks not detected in group**
- Verify the group `chat_id` is correct (it's a negative number)
- Check the bot is a member of the group
- Review `docker compose logs huskwoot` — each classified message appears there

**Restart after config change**

```bash
docker compose restart huskwoot
```
