# huskwoot-push-relay — deployment

Push relay is a standalone public service that holds APNs/FCM keys and accepts HMAC-signed requests from huskwoot instances. Each instance registers its devices with the relay; when events occur, the instance sends a push job to the relay, which delivers it to the device.

## Requirements

- Docker Compose v2
- A public domain (e.g. `push.example.com`) with an A record pointing to the host IP
- APNs `.p8` auth key (for iOS/macOS push notifications)
- FCM service account JSON (for Android push notifications)

## Quick Start

### 1. Prepare secrets

```bash
mkdir -p secrets
cp ~/Downloads/AuthKey_XXXXXXXX.p8 secrets/apns-key.p8
cp ~/Downloads/fcm-sa.json secrets/fcm-sa.json
chmod 600 secrets/apns-key.p8 secrets/fcm-sa.json
```

### 2. Create config

```bash
cp relay.toml.example relay.toml
```

Edit `relay.toml`:
- `[apns]`: fill in `key_id`, `team_id`, `bundle_id`
- `[fcm]`: path is already set (`/run/secrets/fcm-sa.json`)
- Add an `[[instances]]` section for each huskwoot instance (see step 3)

### 3. Register an instance

For each huskwoot instance you want to authorize:

```bash
INSTANCE_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
INSTANCE_SECRET=$(openssl rand -hex 32)

echo "instance_id     = $INSTANCE_ID"
echo "instance_secret = $INSTANCE_SECRET"
```

Add to `relay.toml`:

```toml
[[instances]]
id            = "<INSTANCE_ID>"
owner_contact = "bob@example.com"
secret        = "${HUSKWOOT_RELAY_SECRET_BOB}"
```

Add to `.env`:

```bash
echo "HUSKWOOT_RELAY_SECRET_BOB=<INSTANCE_SECRET>" >> .env
```

Also add the env var to the `environment:` section in `docker-compose.yml`.

Give the instance owner:
- `instance_id`
- `instance_secret` (send over a secure channel)
- The relay URL, e.g. `https://push.example.com`

The instance owner adds to their `config.toml`:

```toml
[push]
relay_url       = "https://push.example.com"
instance_id     = "<INSTANCE_ID>"
instance_secret = "${HUSKWOOT_PUSH_SECRET}"
```

### 4. Edit Caddyfile

Replace `push.example.com` in `Caddyfile` with your domain.

### 5. Start

```bash
docker compose up -d
```

Verify:

```bash
curl -s https://push.example.com/healthz
# Expected: {"status":"ok"}
```

### 6. Hot-reload config (SIGHUP)

After adding a new instance to `relay.toml`, reload without restarting the container:

```bash
docker compose kill -s HUP push-relay
# Logs will show: "config reloaded, instances=N"
```

## File structure

```
secrets/
  apns-key.p8     — APNs Auth Key (from Apple Developer)
  fcm-sa.json     — FCM Service Account JSON (from Firebase Console)
```

Files are mounted read-only at `/run/secrets/` inside the container.

## Links

- APNs/FCM key setup guide: [`../../docs/push-relay/setup.md`](../../docs/push-relay/setup.md)
- HMAC protocol reference: [`../../docs/push-relay/hmac.md`](../../docs/push-relay/hmac.md)
