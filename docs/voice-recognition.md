# Voice Recognition Setup

Huskwoot can transcribe voice messages sent to the bot in Telegram **direct messages**. Once transcribed, the resulting text is fed to the DM agent exactly as if you had typed it. Voice messages in groups and `@mention` chats are **not** transcribed.

Two transcription back-ends are supported:

| Provider | Where it runs | Cost | Quality | Best for |
|----------|---------------|------|---------|----------|
| **whisper.cpp** | Locally on your server | Free | Good (depends on model size) | Self-hosted setups, privacy, offline use |
| **Yandex SpeechKit** | Yandex Cloud (paid) | Per-second pricing | Excellent for Russian | Russian-speaking users, low-RAM servers |

When the `[transcriber]` section is absent from `config.toml` or `provider = ""`, voice messages are not processed: the bot replies with a friendly "voice recognition is not configured" message and continues to handle text input normally.

---

## Common Settings

```toml
[transcriber]
provider     = "whisper"   # "" | "whisper" | "yandex"
max_duration = "5m"        # reject voice messages longer than this (default: 5m)
```

- `provider` — selects the back-end. Empty string disables transcription entirely.
- `max_duration` — Go duration string (`"30s"`, `"2m"`, `"5m"`). Voice messages exceeding this length are rejected before reaching the recognizer; the user gets a localized error reply. Negative values are rejected at config load.

---

## Option 1 — whisper.cpp (Self-Hosted)

[whisper.cpp](https://github.com/ggerganov/whisper.cpp) is a pure C/C++ port of OpenAI's Whisper model. Huskwoot invokes the `whisper-cli` binary as a subprocess and pipes audio through `ffmpeg` for format conversion.

### 1. Install ffmpeg

`ffmpeg` converts incoming Opus audio (the format Telegram uses for voice messages) to 16 kHz mono WAV — the format whisper-cli expects.

**Ubuntu / Debian:**

```bash
sudo apt update
sudo apt install -y ffmpeg
```

**macOS (Homebrew):**

```bash
brew install ffmpeg
```

Verify:

```bash
ffmpeg -version
```

### 2. Install whisper.cpp

#### Option A: Build from source (recommended for Linux servers)

```bash
sudo apt install -y build-essential cmake git
git clone https://github.com/ggerganov/whisper.cpp.git
cd whisper.cpp
cmake -B build
cmake --build build --config Release -j
```

The compiled binary will be at `build/bin/whisper-cli`. Move it to a stable location:

```bash
sudo install -m 0755 build/bin/whisper-cli /usr/local/bin/whisper-cli
```

Verify:

```bash
whisper-cli --help
```

#### Option B: Homebrew (macOS / Linux)

```bash
brew install whisper-cpp
```

Homebrew installs the binary as `whisper-cpp`; you can either reference it by that name in the config (`binary = "whisper-cpp"`) or symlink it to `whisper-cli` to match the default examples.

### 3. Download a Model

whisper.cpp uses GGML model files. Pick a size based on your available RAM and accuracy needs:

| Model | Size on disk | RAM required | Quality | Recommended for |
|-------|--------------|--------------|---------|-----------------|
| `tiny` | 75 MB | ~390 MB | Low | Quick experiments only |
| `base` | 142 MB | ~500 MB | Acceptable | Low-resource VPS, English/Russian |
| `small` | 466 MB | ~1.0 GB | Good | Default recommendation |
| `medium` | 1.5 GB | ~2.6 GB | Very good | Better accuracy on noisy audio |
| `large-v3` | 2.9 GB | ~4.7 GB | Best | High-accuracy needs, GPU helpful |

The whisper.cpp repository ships a download helper:

```bash
cd whisper.cpp
bash ./models/download-ggml-model.sh small
```

Model files are saved as `models/ggml-<name>.bin`. Move the chosen model to a stable directory:

```bash
sudo mkdir -p /var/huskwoot/models
sudo mv models/ggml-small.bin /var/huskwoot/models/
```

Alternatively, download directly from the [Hugging Face mirror](https://huggingface.co/ggerganov/whisper.cpp/tree/main):

```bash
curl -L -o /var/huskwoot/models/ggml-small.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin
```

> For Russian-only audio, the multilingual `small` model usually offers the best quality-to-size ratio. The English-only models (`*.en`) are slightly faster but cannot recognize Russian.

### 4. Configure Huskwoot

Add to `config.toml`:

```toml
[transcriber]
provider     = "whisper"
max_duration = "5m"

[transcriber.whisper]
binary   = "/usr/local/bin/whisper-cli"          # path to whisper-cli
model    = "/var/huskwoot/models/ggml-small.bin" # path to GGML model file
language = "ru"     # ISO-639-1 code; empty = auto-detect
ffmpeg   = "ffmpeg" # binary name or absolute path; resolved via PATH
threads  = 4        # CPU threads; 0 = whisper-cli default
timeout  = "60s"    # hard timeout per recognition call
```

**Field reference:**

- `binary` — **required**. Absolute path to `whisper-cli`. Must be executable by the user running Huskwoot.
- `model` — **required**. Absolute path to a GGML model file. Read access is enough; the file is opened read-only.
- `language` — ISO-639-1 code (`"ru"`, `"en"`, `"de"`, …). Leave empty for auto-detect; specifying the language is faster and more accurate when you know it.
- `ffmpeg` — name or absolute path of the `ffmpeg` binary. Default `"ffmpeg"` resolves via `$PATH`.
- `threads` — number of CPU threads passed via `-t`. Set to the number of physical cores you can spare; `0` omits the flag and uses the whisper.cpp default.
- `timeout` — Go duration. Combined budget for both the `ffmpeg` conversion and `whisper-cli` recognition; the call is aborted if exceeded.

### 5. Verify

Restart Huskwoot, send yourself a short voice message in Telegram DM, and check the logs. You should see entries from the transcriber and the recognized text being passed to the agent. If recognition fails, the bot replies with a localized error and the underlying error is logged with `slog`.

### Performance Notes

- A `small` model on 4 CPU cores transcribes a 15-second voice message in roughly 5–10 seconds. The `medium` model is 3–4× slower.
- whisper.cpp benefits massively from BLAS / Metal / CUDA acceleration when building from source. See the upstream README for build flags.
- The `timeout` budget should comfortably cover your worst case. A model 2–3× larger than `small` on a small VPS may exceed the default 60s on long messages — bump it to `120s` or restrict `max_duration`.

### Docker Deployment

The standard Huskwoot Docker image does not bundle `whisper-cli` or `ffmpeg`. To use whisper.cpp with the official image, either:

1. Mount the binary, the model, and `ffmpeg` from the host into the container, and adjust the config paths accordingly, **or**
2. Build a custom image based on `ghcr.io/anadale/huskwoot` with `apt-get install -y ffmpeg` and a copy of `whisper-cli`.

In a `docker-compose.yml` mount example:

```yaml
services:
  huskwoot:
    volumes:
      - ./config:/etc/huskwoot
      - ./data:/var/lib/huskwoot
      - /usr/local/bin/whisper-cli:/usr/local/bin/whisper-cli:ro
      - /var/huskwoot/models:/var/huskwoot/models:ro
```

You will still need `ffmpeg` inside the container — the easiest path is to extend the image rather than mount it, because `ffmpeg` has runtime library dependencies.

---

## Option 2 — Yandex SpeechKit

[Yandex SpeechKit](https://yandex.cloud/services/speechkit) provides high-quality speech recognition for Russian (and many other languages) via a synchronous REST API. No local CPU/RAM is consumed — the audio is uploaded and recognized in Yandex Cloud.

Pricing as of writing: roughly free for the first hour per month, then per-second billing. Check the [official pricing page](https://yandex.cloud/en/docs/speechkit/pricing) for the current rates.

### 1. Create a Yandex Cloud Folder

If you do not yet have a Yandex Cloud account, sign up at [console.yandex.cloud](https://console.yandex.cloud). New accounts get a trial credit.

A "folder" is the basic resource container. Either use the default folder created with your billing account or create a dedicated one:

1. Open the [Cloud Console](https://console.yandex.cloud).
2. Pick or create a cloud, then create a folder named e.g. `huskwoot`.
3. Note the folder ID — it appears in the folder overview as a string like `b1g...`. You will need it as `folder_id`.

### 2. Create a Service Account with the SpeechKit Role

1. In the folder, open **Service accounts** → **Create service account**.
2. Name it `huskwoot-stt`.
3. Assign the role `ai.speechkit-stt.user` (this role grants only what is needed for synchronous recognition).
4. Save.

### 3. Issue an API Key

1. Open the service account you just created.
2. Click **Create new key** → **API key**.
3. Copy the generated key value — it is shown only once. Treat it like a password.

> Yandex Cloud also supports IAM tokens for short-lived auth, but Huskwoot expects a long-lived API key. Rotate it periodically through the same console.

### 4. Configure Huskwoot

Store the secret outside `config.toml` and reference it via env variable expansion:

```bash
# In your .env file (or systemd EnvironmentFile, docker-compose env_file, etc.)
YANDEX_SPEECHKIT_API_KEY=AQVN...
YANDEX_FOLDER_ID=b1g...
```

```toml
[transcriber]
provider     = "yandex"
max_duration = "5m"

[transcriber.yandex]
api_key   = "${YANDEX_SPEECHKIT_API_KEY}"  # required
folder_id = "${YANDEX_FOLDER_ID}"          # required
language  = "ru-RU"                        # default: "ru-RU"
endpoint  = "https://stt.api.cloud.yandex.net/speech/v1/stt:recognize"  # default
timeout   = "30s"                          # default: 30s
```

**Field reference:**

- `api_key` — **required**. The API key string from step 3. Exposed via `Authorization: Api-Key <value>` header.
- `folder_id` — **required**. The Yandex Cloud folder containing your service account.
- `language` — BCP-47 locale tag (`"ru-RU"`, `"en-US"`, `"tr-TR"`, …). See the [supported languages list](https://yandex.cloud/docs/speechkit/stt/models). Default is `"ru-RU"`.
- `endpoint` — full URL of the synchronous STT endpoint. Override only if you need a regional endpoint or a private connectivity setup.
- `timeout` — Go duration. Hard limit for the HTTP request; covers both upload and processing on the Yandex side.

### 5. Verify

Restart Huskwoot, send yourself a short voice message in Telegram DM, and confirm the text reaches the agent. On error, the bot replies with a localized failure message and details are logged with `slog`. The most common failure modes:

- `401 Unauthorized` — wrong or revoked API key.
- `403 Forbidden` — the service account does not have the `ai.speechkit-stt.user` role on the folder.
- `400 Bad Request` — `folder_id` mismatch with the API key, or audio exceeds the synchronous-API size limits (see the upstream docs; large messages should be capped via `max_duration`).

### Limits and Trade-offs

- The synchronous STT endpoint accepts audio up to roughly 30 seconds / ~1 MB. Longer voice messages should be capped via `max_duration` to stay within those limits.
- Pricing accumulates per second of recognized audio. Set a sensible `max_duration` (the default `5m` is generous — consider `1m` or `2m` for cost control).
- Audio is uploaded to Yandex Cloud servers. If that conflicts with your data-handling policy, prefer whisper.cpp.

---

## Troubleshooting

**Voice messages are silently ignored.**
Check `provider` is set to a non-empty value. With `provider = ""` (the default), the bot intentionally only sends a "not configured" reply and does not attempt recognition.

**`whisper-cli: exec: "whisper-cli": executable file not found in $PATH`.**
Use an absolute path in the `binary` field, or ensure the binary is on the `PATH` of the user running Huskwoot. When running under systemd, the unit's `PATH` is minimal — prefer absolute paths.

**`ffmpeg convert: exit status 1`.**
Run `ffmpeg -version` as the same user that runs Huskwoot. Reinstall if missing. Inside Docker, `ffmpeg` must be installed in the container image, not just on the host.

**Whisper recognition is empty or garbled.**
Try a larger model first (`small` → `medium`). If recognition is still poor, set `language` explicitly instead of relying on auto-detect, and verify the input is not silence (Telegram sometimes records very quiet audio if the mic permission is denied).

**Yandex returns `400 audioEncoding`-related errors.**
Huskwoot uploads OGG/Opus directly. Confirm you are not using a deprecated endpoint — the default `https://stt.api.cloud.yandex.net/speech/v1/stt:recognize` is correct as of writing.

**The dispatcher logs `transcription deadline exceeded`.**
Increase `timeout` for the chosen provider, or reduce `max_duration`. For whisper.cpp on slow CPUs, also try a smaller model.
