# huskwoot — deployment

Huskwoot server with HTTP API and Caddy as a reverse proxy with automatic TLS.

## Requirements

- Docker Compose v2
- A public domain (e.g. `huskwoot.example.com`) with an A record pointing to the host IP
- Telegram Bot Token from [@BotFather](https://t.me/BotFather)
- OpenAI API key

## Quick Start

### 1. Create config

```bash
mkdir -p config
cp config.toml.example config/config.toml
```

Edit `config/config.toml`:
- `[user]`: your `user_name` (used in AI prompts)
- `[api]`: replace `server.example.org` with your domain

### 2. Create `.env`

```bash
cat > .env <<'EOF'
OPENAI_API_KEY=sk-...
TELEGRAM_BOT_TOKEN=123456:ABC-...
TELEGRAM_OWNER_ID=123456789
EOF
chmod 600 .env
```

`TELEGRAM_OWNER_ID` is your numeric Telegram user ID — send any message to [@userinfobot](https://t.me/userinfobot) to get it.

### 3. Edit Caddyfile

Replace `server.example.org` in `Caddyfile` with your domain.

### 4. Start

```bash
docker compose up -d
```

Verify:

```bash
curl -s https://server.example.org/healthz
# Expected: {"status":"ok"}
```

## File structure

```
config/
  config.toml    — huskwoot configuration
  huskwoot.db    — SQLite database (created automatically on first run)
```

The `config/` directory is mounted at `/app/config` inside the container.

## Push notifications

To enable push notifications, a push-relay must be deployed separately.
After setting it up, uncomment the `[push]` section in `config/config.toml` and add to `.env`:

```bash
HUSKWOOT_PUSH_SECRET=<secret provided by the relay operator>
```

To deploy huskwoot and push-relay together on the same server, see `../huskwoot-with-relay/`.

## Links

- Configuration reference: [`../../README.md`](../../README.md)
- Quick Start guide: [`../../docs/quick-start.md`](../../docs/quick-start.md)
- Deploy with push-relay on one VPS: [`../huskwoot-with-relay/`](../huskwoot-with-relay/)
- Deploy push-relay only: [`../push-relay/`](../push-relay/)
