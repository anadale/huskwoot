# Building and Testing

## Requirements

- Go 1.26+
- `modernc.org/sqlite` is used — pure Go, no CGO required

## Build

```bash
# Main binary (instance):
go build -o bin/huskwoot ./cmd/huskwoot/

# Push relay binary:
go build -o bin/huskwoot-push-relay ./cmd/huskwoot-push-relay/
```

## Run Locally

```bash
# Config from XDG default (~/.config/huskwoot/config.toml):
bin/huskwoot serve

# Explicit config directory:
bin/huskwoot --config-dir /path/to/config/dir

# Via environment variable:
HUSKWOOT_CONFIG_DIR=/path/to/config/dir bin/huskwoot serve
```

## Test

```bash
go test ./...
go vet ./...
```

Tests are table-driven (`[]struct{name, input, want}`). External dependencies are mocked manually via interfaces — no `testify/mock`.

## Docker (local build)

```bash
# Build images from source:
docker build -t huskwoot .
docker build -f Dockerfile.push-relay -t huskwoot-push-relay .

# Run with Docker Compose (builds from source):
docker compose up
```

Config is mounted from `./config/`. The SQLite database `huskwoot.db` is created automatically in the same directory.

## CLI Commands

```bash
huskwoot serve                                             # start the daemon
huskwoot devices create --name "iPhone" --platform "ios"  # issue a bearer token (shown once)
huskwoot devices list                                      # list all devices including revoked
huskwoot devices revoke <device-id>                        # revoke a device
```

The `--config-dir` flag is inherited by all subcommands.

## Project Conventions

- **Interfaces:** all key components are defined as interfaces in `internal/model/interfaces.go`. Concrete types appear only in constructors.
- **Mocks:** written by hand in the test file, no `testify/mock`.
- **Constructors:** `(*Type, error)` when initialization is fallible, otherwise `*Type`.
- **Context:** all public methods take `context.Context` as the first parameter.
- **Errors:** `fmt.Errorf("operation description: %w", err)`.
- **Concurrency:** `sync.Mutex`/`sync.RWMutex`; parallel notifiers use `sync.WaitGroup`.
- **Build artifacts:** `go build` always outputs to `bin/`.
