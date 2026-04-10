# huskwoot + push-relay — single VPS deployment

Huskwoot and push-relay run in the same Docker Compose project. Caddy serves both domains and issues TLS certificates automatically. Traffic between huskwoot and push-relay travels over the internal Docker network and never leaves the VPS.

## Requirements

- Docker Compose v2
- Two public domains with A records pointing to the host IP:
  - `server.example.org` — huskwoot
  - `relay.example.org` — push-relay
- Telegram Bot Token from [@BotFather](https://t.me/BotFather)
- OpenAI API key
- APNs `.p8` auth key (for iOS/macOS push notifications)
- FCM service account JSON (for Android push notifications)

## Quick Start

### 1. Prepare push-relay secrets

```bash
mkdir -p secrets
cp ~/Downloads/AuthKey_XXXXXXXX.p8 secrets/apns-key.p8
cp ~/Downloads/fcm-sa.json secrets/fcm-sa.json
chmod 600 secrets/apns-key.p8 secrets/fcm-sa.json
```

See [docs/push-relay/setup.md](../../docs/push-relay/setup.md) for instructions on obtaining APNs and FCM keys.

### 2. Create relay config

```bash
cp relay.toml.example relay.toml
```

Edit `relay.toml`:
- `[apns]`: fill in `key_id`, `team_id`, `bundle_id`

### 3. Create huskwoot config

```bash
mkdir -p config
cp config.toml.example config/config.toml
```

Edit `config/config.toml`:
- `[user]`: your `user_name`
- `[api]`: replace `server.example.org` with your domain
- `[push]`: `relay_url = "http://push-relay:8080"` is already set

### 4. Create `.env`

```bash
PUSH_SECRET=$(openssl rand -hex 32)

cat > .env <<EOF
OPENAI_API_KEY=sk-...
TELEGRAM_BOT_TOKEN=123456:ABC-...
TELEGRAM_OWNER_ID=123456789
HUSKWOOT_PUSH_SECRET=${PUSH_SECRET}
EOF
chmod 600 .env
```

`HUSKWOOT_PUSH_SECRET` is used as the HMAC secret for both huskwoot and push-relay — they must share the same value.

### 5. Edit Caddyfile

Replace `server.example.org` and `relay.example.org` in `Caddyfile` with your domains.

### 6. Start

```bash
docker compose up -d
```

Verify:

```bash
curl -s https://server.example.org/healthz
# Expected: {"status":"ok"}

curl -s https://relay.example.org/healthz
# Expected: {"status":"ok"}
```

## File structure

```
config/
  config.toml    — huskwoot configuration
  huskwoot.db    — SQLite database (created automatically)
secrets/
  apns-key.p8    — APNs Auth Key (from Apple Developer)
  fcm-sa.json    — FCM Service Account JSON (from Firebase Console)
relay.toml       — push-relay configuration
.env             — secrets (never commit this file)
```

## Adding another instance to the relay

To allow an additional huskwoot instance to send push through this relay, add an `[[instances]]` section to `relay.toml` and reload without restarting:

```bash
docker compose kill -s HUP push-relay
# Logs will show: "config reloaded, instances=N"
```

## Links

- APNs/FCM key setup: [`../../docs/push-relay/setup.md`](../../docs/push-relay/setup.md)
- HMAC protocol reference: [`../../docs/push-relay/hmac.md`](../../docs/push-relay/hmac.md)
- Deploy huskwoot only (without relay): [`../huskwoot/`](../huskwoot/)
- Deploy push-relay only: [`../push-relay/`](../push-relay/)
