# Push Relay Setup: APNs and FCM

Instructions for operators deploying `huskwoot-push-relay`.

## Requirements

- A public host accessible over HTTPS (Caddy + Let's Encrypt recommended)
- Apple Developer Account (for APNs)
- Firebase project (for FCM/Android)
- Docker and Docker Compose

---

## 1. APNs (iOS / macOS)

### 1.1 Apple Developer Setup

1. Sign in at [developer.apple.com](https://developer.apple.com) → **Certificates, Identifiers & Profiles**.
2. **Identifiers** → create an App ID with **Push Notifications** capability enabled (or verify it's already enabled for your bundle, e.g. `com.huskwoot.client`).
3. **Keys** → click **+** → enter a name, check **Apple Push Notifications service (APNs)** → **Continue** → **Register**.
4. Download the `.p8` file (**available once only**), save it to `deploy/push-relay/secrets/apns-key.p8`.
5. Note down:
   - **Key ID** — 10-character key identifier (e.g. `ABCDEF1234`)
   - **Team ID** — 10-character team identifier (visible in the top-right corner of Developer Portal, e.g. `TEAM123456`)
   - **Bundle ID** — your iOS/macOS app's bundle identifier (e.g. `com.huskwoot.client`)

### 1.2 Relay Config

```toml
[apns]
key_file  = "/run/secrets/apns-key.p8"
key_id    = "ABCDEF1234"
team_id   = "TEAM123456"
bundle_id = "com.huskwoot.client"
# production = true   # uncomment for production (App Store / TestFlight)
```

By default (without `production = true`), the relay uses the **sandbox** APNs endpoint (`api.sandbox.push.apple.com`). Uncomment `production` for production builds.

---

## 2. FCM (Android)

### 2.1 Firebase Console Setup

1. Go to [console.firebase.google.com](https://console.firebase.google.com) → open or create a project.
2. Connect your Android app if not already: **Project Overview** → **Add app** → Android.
3. Go to **Project Settings** → **Service Accounts** tab.
4. Click **Generate new private key** → confirm → download the JSON file.
5. Save it to `deploy/push-relay/secrets/fcm-sa.json`.

### 2.2 Relay Config

```toml
[fcm]
service_account_file = "/run/secrets/fcm-sa.json"
```

---

## 3. Mounting Secrets in Docker Compose

In `deploy/push-relay/docker-compose.yml`, secrets are mounted as read-only files:

```yaml
services:
  push-relay:
    volumes:
      - ./secrets/apns-key.p8:/run/secrets/apns-key.p8:ro
      - ./secrets/fcm-sa.json:/run/secrets/fcm-sa.json:ro
```

Verify the files are in place before starting:

```bash
ls deploy/push-relay/secrets/
# apns-key.p8  fcm-sa.json
```

---

## 4. Start

```bash
cd deploy/push-relay

# Add instance secret to .env:
echo "HUSKWOOT_RELAY_SECRET_BOB=$(openssl rand -hex 32)" >> .env

# Start:
docker compose up -d

# Verify:
curl -s https://push.example.com/healthz
# → {"status":"ok"}
```

---

## 5. Smoke Test

Send a test push manually (replace with a real device token):

```bash
SECRET="<instance_secret>"
INSTANCE_ID="bob"
BASE_URL="https://push.example.com"
DEVICE_TOKEN="<apns_or_fcm_token>"

TS=$(date +%s)
BODY='{"deviceId":"test-device","priority":"high","notification":{"title":"Test","body":"Push check"},"data":{"kind":"task_created","eventSeq":1}}'
BODY_HASH=$(printf '%s' "$BODY" | openssl dgst -sha256 | awk '{print $2}')
CANONICAL=$(printf "POST\n/v1/push\n%s\n%s" "$TS" "$BODY_HASH")
SIG=$(printf '%s' "$CANONICAL" | openssl dgst -sha256 -mac HMAC -macopt "key:${SECRET}" | awk '{print $2}')

curl -s -X POST "$BASE_URL/v1/push" \
  -H "Content-Type: application/json" \
  -H "X-Huskwoot-Instance: $INSTANCE_ID" \
  -H "X-Huskwoot-Timestamp: $TS" \
  -H "X-Huskwoot-Signature: $SIG" \
  -d "$BODY"
```

Expected response: `{"status":"sent"}` (or `{"status":"invalid_token"}` if the device token is invalid — this is normal for a first test).

---

## 6. Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `BadDeviceToken` (APNs) | Token is invalid or expired | Client must re-register (pairing or `PATCH /v1/devices/me`) |
| `Unregistered` (APNs) | App was removed from the device | Normal; relay returns `invalid_token`, instance removes the token |
| `DeviceTokenNotForTopic` (APNs) | `bundle_id` mismatch | Check `bundle_id` in `relay.toml` — must match the app's bundle |
| `registration-token-not-registered` (FCM) | FCM token is stale | Same as `Unregistered` — instance removes the token |
| `InvalidArgument` (FCM) | Malformed token | Verify the client sends a valid FCM registration token |
| `401 bad_signature` | Wrong secret or signing error | Check `instance_secret` in `config.toml` against `[[instances]]` in `relay.toml` |
| `401 timestamp_skew` | Instance and relay clocks differ by >5 min | Sync NTP on both hosts |
