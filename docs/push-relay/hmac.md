# HMAC Protocol: instance ↔ relay

Reference for client developers or alternative implementations. The canonical implementation is in `internal/pushproto/hmac.go`.

## Required Headers

All requests to relay `/v1/*` endpoints must include three headers:

| Header | Type | Description |
|--------|------|-------------|
| `X-Huskwoot-Instance` | string | Instance identifier (from `instance_id` in config) |
| `X-Huskwoot-Timestamp` | string | Current UNIX time in seconds (decimal string) |
| `X-Huskwoot-Signature` | string | `lower(hex(HMAC-SHA256(secret, canonical)))` |

## Canonical String

The signature is computed over a string in the following format:

```
METHOD\nPATH\nTIMESTAMP\nlower(hex(SHA256(body)))
```

Where:
- `METHOD` — HTTP method in uppercase (`POST`, `PUT`, `DELETE`).
- `PATH` — request path including query string if present (`/v1/push`, `/v1/registrations/device-id`).
- `TIMESTAMP` — the same value as in the `X-Huskwoot-Timestamp` header.
- `lower(hex(SHA256(body)))` — SHA-256 hash of the request body in lowercase hex. **For empty body** (`DELETE`) — `sha256("")` = `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- Separator is newline `\n` (LF, 0x0A). Carriage return is not used.

## Time Window

The timestamp must be within ±5 minutes of the server time. Violation returns `401 timestamp_skew`. Synchronize clocks via NTP.

## Go Example

```go
package main

import (
    "fmt"

    "github.com/anadale/huskwoot/internal/pushproto"
)

func main() {
    secret := []byte("my-instance-secret")
    ts := "1713600000"

    body := []byte(`{"deviceId":"abc","priority":"high","notification":{"title":"Test","body":"Hello"},"data":{"kind":"task_created","eventSeq":42}}`)

    sig := pushproto.Sign(secret, "POST", "/v1/push", ts, body)
    fmt.Println("X-Huskwoot-Signature:", sig)
}
```

## Shell Example (openssl + curl)

```sh
SECRET="my-instance-secret"
INSTANCE_ID="bob"
BASE_URL="https://push.example.com"

# Request body:
BODY='{"deviceId":"device-uuid","priority":"high","notification":{"title":"Test","body":"Hello"},"data":{"kind":"task_created","eventSeq":1}}'

# Timestamp:
TS=$(date +%s)

# SHA-256 hash of the body:
BODY_HASH=$(printf '%s' "$BODY" | openssl dgst -sha256 | awk '{print $2}')

# Canonical string (LF separators):
CANONICAL=$(printf "POST\n/v1/push\n%s\n%s" "$TS" "$BODY_HASH")

# HMAC-SHA256:
SIG=$(printf '%s' "$CANONICAL" | openssl dgst -sha256 -mac HMAC -macopt "key:${SECRET}" | awk '{print $2}')

# Request:
curl -s -X POST "$BASE_URL/v1/push" \
  -H "Content-Type: application/json" \
  -H "X-Huskwoot-Instance: $INSTANCE_ID" \
  -H "X-Huskwoot-Timestamp: $TS" \
  -H "X-Huskwoot-Signature: $SIG" \
  -d "$BODY"
```

Note: `openssl dgst -mac HMAC -macopt key:SECRET` uses the key as a raw string. If the secret contains special characters, pass it via a file: `-macopt hexkey:$(echo -n "$SECRET" | xxd -p -c 256)`.

## Error Codes

| Code | `code` in body | Cause |
|------|----------------|-------|
| 401 | `unauthorized` | One or more `X-Huskwoot-*` headers are missing |
| 401 | `timestamp_skew` | Timestamp is outside the ±5-minute window |
| 401 | `unknown_instance` | `X-Huskwoot-Instance` not found in the relay allowlist |
| 401 | `bad_signature` | Signature does not match |
| 413 | — | Request body exceeds 1 MiB |

## Error Body Format

```json
{"code": "bad_signature"}
```

## Relay Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Health check (no auth required) |
| `PUT` | `/v1/registrations/{device_id}` | Register or update device push tokens |
| `DELETE` | `/v1/registrations/{device_id}` | Remove device registration |
| `POST` | `/v1/push` | Send a push notification to a device |

### PUT /v1/registrations/{device_id}

Body (`application/json`):

```json
{
  "apnsToken": "hex-or-base64-apns-token",
  "fcmToken": "fcm-registration-token",
  "platform": "ios"
}
```

At least one of `apnsToken`/`fcmToken` is required. `platform`: `"ios"`, `"macos"`, `"android"`.

### POST /v1/push

Body:

```json
{
  "deviceId": "uuid-of-device",
  "priority": "high",
  "collapseKey": "tasks",
  "notification": {
    "title": "New task",
    "body": "inbox#42: write the report (by Apr 25 18:00)",
    "badge": 1
  },
  "data": {
    "kind": "task_created",
    "eventSeq": 123,
    "taskId": "uuid",
    "displayId": "inbox#42"
  }
}
```

Response:

```json
{"status": "sent"}
{"status": "invalid_token"}
{"status": "upstream_error", "retryAfter": 30, "message": "..."}
{"status": "bad_payload", "message": "..."}
```
