# Voice transcription for Telegram DMs

## Overview

Поддерживаем голосовые сообщения в Telegram DM как полноценный способ ввода для агента. Канал получает Telegram-апдейт с `Voice`/`Audio`, скачивает файл, прогоняет байты через настроенный `Transcriber`, подставляет распознанный текст в `Message.Text` и эмитит сообщение в pipeline как обычный текстовый DM. Pipeline и agent ничего о голосе не знают.

Архитектура канал-агностична: интерфейс `Transcriber` универсален и не привязан к Telegram. Когда появятся другие каналы с голосовой поддержкой (WhatsApp, Slack, Signal, …), они подключат тот же singleton.

В первой итерации поставляем два провайдера: локальный `whisper.cpp` через subprocess и Yandex SpeechKit через sync REST v1. Выбор делается секцией `[transcriber]` в `config.toml`. При `provider = ""` голосовые игнорируются с дружелюбной i18n-отбивкой.

**Решения, зафиксированные в brainstorm:**

- Распознавание ВНУТРИ канала (а не отдельным этапом pipeline).
- Скоуп — только `MessageKindDM`. Голосовые в группах (включая `GroupDirect`) не транскрибируем.
- Синхронно, без очереди, без новых SQL-таблиц.
- Реакции стандартные (✍️ → 👍), без отдельной фазы «слушаю».
- Распознанный текст не помечается ни в `Message`, ни в истории.
- Глобальный singleton `Transcriber`; per-channel настройки не делаем.

**Явно вне scope:**

- Очередь распознаваний / persistent state.
- Голосовые в `Group` и `GroupDirect`.
- Эхо распознанного текста перед ответом агента.
- Хранение оригинальных аудио-файлов.
- Fallback chain между провайдерами.
- Plugin registry для `Transcriber`.
- Поддержка >1 минуты через async Yandex / chunk-сплит whisper.

## Context (from discovery)

- **Текущее состояние:** в `internal/channel/telegram.go` нет ни одного упоминания `Voice`/`Audio` — голосовые сейчас попадают в общий handler с пустым `Text` и тихо пропускаются классификатором. Изменения локализованы в одном канале и одной точке `main.go`.
- **`Message.Text` — единственное текстовое поле сообщения** (см. `internal/model/types.go:78`). Никаких полей под audio-bytes/voice-flag не было и не нужно.
- **Конфиг-структуры:** `internal/config/config.go` — `Config` уже содержит `Channels`, `AI`, `User`, `DateTime`, `Reminders`, `API`, `Push`, `Devices`. Добавим параллельно `Transcriber TranscriberConfig`.
- **`TelegramChannelConfig`** (`internal/channel/telegram.go:58`) уже принимает несколько прикладных параметров (BotID, BotUsername, ReactionEnabled, …) — расширяется без переделки конструктора.
- **i18n bundle:** `internal/i18n/locales/{ru,en}.json` — единый формат, ключи плоские.
- **Стиль:** проект следует TDD; моки рукописные в `_test.go`; конструкторы `(*Type, error)` при возможной ошибке инициализации; все публичные методы первым параметром принимают `context.Context`; ошибки оборачиваются `fmt.Errorf("...: %w", err)`; код-комментарии и текст ошибок — на английском (см. memory `feedback_language`).
- **План в стиле проекта:** см. предыдущий `docs/plans/2026-04-24-default-time-of-day.md` — секционная структура, табличные тесты, `go test ./...` + `go vet ./...` после каждой задачи.

## Development Approach

- **testing approach**: TDD — сначала пишем тесты (они падают), потом правим реализацию до зелёного.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run `go test ./...` и `go vet ./...` после каждой задачи
- maintain backward compatibility: отсутствие секции `[transcriber]` в существующих конфигах не должно ломать запуск; голосовые при `provider = ""` корректно отбиваются i18n-сообщением.

## Testing Strategy

- **unit tests** обязательны в каждой задаче.
- `internal/transcribe/whisper_test.go` — мок subprocess через инжектируемый `commandRunner` (интерфейс с одним методом, возвращающим stdout/stderr/exit-code). Кейсы: успешная транскрипция; ненулевой exit; пустой stdout/файл; таймаут (через `context.WithTimeout` и блокирующий мок); отсутствующий бинарник `whisper-cli`; ошибка ffmpeg; передача флагов (`-l`, `-t`, `-m`).
- `internal/transcribe/yandex_test.go` — `httptest.NewServer`, кейсы: успешный 200 с `{"result":"..."}`; пустой `result`; non-2xx с кодом и body; таймаут (через `context.WithTimeout` + хендлер с `time.Sleep`); проверка заголовков (`Authorization: Api-Key …`, `Content-Type: application/octet-stream`); проверка query-params (`folderId`, `lang`, `format=oggopus`); проверка отправляемого body как ровно того же `[]byte`, что был передан в `AudioInput.Data`.
- `internal/transcribe/factory_test.go` — табличный тест: `provider=""` → `(nil, nil)`; `provider="whisper"` с минимально валидным конфигом → `*WhisperCPP`; `provider="yandex"` с минимально валидным конфигом → `*Yandex`; `provider="unknown"` → ошибка; пропущенный обязательный параметр (`binary`/`api_key`) → ошибка.
- `internal/channel/telegram_test.go` — расширяем существующие тесты канала. Кейсы: voice DM + transcriber returns text → `Message.Text` ≡ распознанному тексту, `Kind=DM`, дальше handler вызывается; voice DM + transcriber error → handler НЕ вызывается, `ReplyFn` зовётся с i18n-кодом `voice.error`; voice DM + nil transcriber → handler НЕ вызывается, `ReplyFn` с `voice.disabled`; voice DM при `Duration > MaxVoiceDuration` → `ReplyFn` с `voice.too_long`, transcriber НЕ вызывается; voice DM + transcriber возвращает whitespace → `ReplyFn` с `voice.empty`; voice в групповом чате → транскрипция НЕ запускается, обычная ветка обработки группового сообщения. Мок `Transcriber` пишем рукой в `telegram_test.go`.
- `internal/config/config_test.go` — табличный тест валидации `[transcriber]`: пустая секция; `provider="whisper"` без `binary` или `model` → ошибка; `provider="yandex"` без `api_key`/`folder_id` → ошибка; неизвестный `provider` → ошибка; `max_duration` парсится; `max_duration` отрицательный → ошибка.
- e2e-тестов у проекта нет (Go backend) — ограничиваемся unit.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

1. В `internal/model/interfaces.go` добавляем интерфейс `Transcriber` и тип `AudioInput` (поля `Data []byte`, `MimeType string`, `Duration time.Duration`).
2. В `internal/config/config.go` добавляем `TranscriberConfig`/`WhisperConfig`/`YandexConfig` и поле `Transcriber TranscriberConfig` в `Config`. Валидация в `(*Config).validate` (или новой `validateTranscriber`) — провайдер-специфичные обязательные поля проверяются только если соответствующий `provider` выбран.
3. Создаём пакет `internal/transcribe/` с реализациями `WhisperCPP` (subprocess через инжектируемый `commandRunner`, ffmpeg → temp WAV → whisper-cli → temp .txt) и `Yandex` (HTTP POST на `stt:recognize`).
4. Фабрика `transcribe.New(cfg config.TranscriberConfig) (model.Transcriber, error)` собирает нужный провайдер; при `provider == ""` возвращает `(nil, nil)`.
5. В `TelegramChannelConfig` добавляем `Transcriber model.Transcriber` и `MaxVoiceDuration time.Duration`. В `TelegramChannel` появляется ветка обработки `update.Message.Voice` / `update.Message.Audio` для DM: проверка лимита → скачивание байт через Bot API → вызов `Transcriber.Transcribe` → подмена `Text` → передача дальше в существующий handler (тот же путь, что и для текстовых DM, включая ✍️/👍).
6. В `cmd/huskwoot/main.go` — собрать `Transcriber` через фабрику, прокинуть в `TelegramChannelConfig` вместе с `MaxVoiceDuration`.
7. Новые i18n-ключи `voice.disabled`, `voice.error`, `voice.empty`, `voice.too_long` в `ru.json` и `en.json`. Ключ `voice.too_long` принимает параметр `Limit` (string-форматированный лимит, например `5m`).
8. Обновляем `CLAUDE.md` (раздел Configuration + directory structure) и `README.md` (список фич + раздел про настройку транскрайберов).

## Technical Details

### Интерфейс Transcriber

```go
// Transcriber converts a voice/audio recording to plain text.
// Implementations live in internal/transcribe (WhisperCPP, Yandex).
type Transcriber interface {
    // Transcribe returns the recognized text for the given audio payload.
    // An empty string with nil error indicates the recognizer ran successfully but
    // produced no text (silence/noise).
    Transcribe(ctx context.Context, audio AudioInput) (string, error)
    // Name returns the human-readable transcriber name for logging.
    Name() string
}

// AudioInput carries the raw audio payload for a single recognition call.
type AudioInput struct {
    // Data is the full audio body in its original encoding.
    Data []byte
    // MimeType describes the encoding (e.g. "audio/ogg" for Telegram voice).
    MimeType string
    // Duration is the announced length of the recording, used for logging
    // and to short-circuit recognizers with hard duration limits.
    Duration time.Duration
}
```

### TOML-конфиг

```toml
[transcriber]
provider     = "whisper"   # "" | "whisper" | "yandex"
max_duration = "5m"        # hard upper bound on voice duration; "" defaults to 5m

[transcriber.whisper]
binary   = "/usr/local/bin/whisper-cli"
model    = "/var/huskwoot/models/ggml-base.bin"
language = "ru"           # default: empty (auto-detect)
ffmpeg   = "ffmpeg"       # default: "ffmpeg" (resolved via PATH)
threads  = 4              # default: 0 (the -t flag is not passed)
timeout  = "60s"          # default: 60s

[transcriber.yandex]
api_key   = "${YANDEX_SPEECHKIT_API_KEY}"
folder_id = "${YANDEX_FOLDER_ID}"
language  = "ru-RU"       # default: "ru-RU"
endpoint  = "https://stt.api.cloud.yandex.net/speech/v1/stt:recognize"  # default
timeout   = "30s"         # default: 30s
```

Валидация:
- `provider == "whisper"` → `binary` и `model` обязательны.
- `provider == "yandex"` → `api_key` и `folder_id` обязательны.
- `provider` ∉ `{"", "whisper", "yandex"}` → ошибка.
- `max_duration` парсится через `time.ParseDuration`; пустое → 5m; отрицательное → ошибка.

### WhisperCPP — pipeline

1. Создаём временный файл `<tmp>/huskwoot-whisper-<rand>.wav` (deferred remove).
2. Запускаем `ffmpeg -i pipe:0 -ar 16000 -ac 1 -f wav <tmp>/<...>.wav`, в stdin отдаём `audio.Data`. На ошибке возвращаем `fmt.Errorf("ffmpeg convert: %w", err)`.
3. Запускаем `whisper-cli -m <model> -l <lang> -f <tmp>/<...>.wav -otxt -nt -np [-t <threads>]`. На ошибке возвращаем `fmt.Errorf("whisper-cli: %w", err)`.
4. Читаем `<tmp>/<...>.wav.txt`, удаляем оба файла.
5. Возвращаем `strings.TrimSpace(content)`.

`commandRunner` — небольшой интерфейс с двумя методами (`Run(ctx, name, args, stdin) ([]byte, error)`) для полной мокаемости в тестах. В продакшене реализуется через `os/exec`. Конструктор `NewWhisperCPP(cfg WhisperConfig) *WhisperCPP` использует дефолтный runner; для тестов есть internal-конструктор `newWhisperCPP(cfg, runner)`.

### Yandex — pipeline

1. `req := http.NewRequestWithContext(ctx, http.MethodPost, endpointWithQuery, bytes.NewReader(audio.Data))`.
2. `req.Header.Set("Authorization", "Api-Key "+cfg.APIKey)`; `req.Header.Set("Content-Type", "application/octet-stream")`.
3. URL c query: `?folderId=<folder>&lang=<lang>&format=oggopus`.
4. HTTP-клиент с `Timeout = cfg.Timeout`. Status ≠ 200 → `fmt.Errorf("yandex stt: status %d: %s", resp.StatusCode, string(body))`.
5. Декодируем `{"result": "..."}`, возвращаем `strings.TrimSpace(result.Result)`.

### TelegramChannel — обработка voice

В функции `messageToModel` (или там, где собирается `model.Message`) — сразу после определения `Kind`:

```go
if msg.Voice != nil || msg.Audio != nil {
    if kind != model.MessageKindDM {
        return nil, false  // group voice messages are not transcribed; treated like any other group message
    }

    duration := time.Duration(voiceOrAudioDuration) * time.Second
    if c.cfg.MaxVoiceDuration > 0 && duration > c.cfg.MaxVoiceDuration {
        c.replyVoiceI18n(ctx, msg, "voice.too_long", map[string]string{"Limit": c.cfg.MaxVoiceDuration.String()})
        return nil, false
    }

    if c.cfg.Transcriber == nil {
        c.replyVoiceI18n(ctx, msg, "voice.disabled", nil)
        return nil, false
    }

    data, err := c.downloadFile(ctx, fileID)
    if err != nil {
        slog.ErrorContext(ctx, "download voice", "err", err)
        c.replyVoiceI18n(ctx, msg, "voice.error", nil)
        return nil, false
    }

    text, err := c.cfg.Transcriber.Transcribe(ctx, model.AudioInput{
        Data: data, MimeType: "audio/ogg", Duration: duration,
    })
    if err != nil {
        slog.ErrorContext(ctx, "transcribe voice", "transcriber", c.cfg.Transcriber.Name(), "err", err)
        c.replyVoiceI18n(ctx, msg, "voice.error", nil)
        return nil, false
    }
    text = strings.TrimSpace(text)
    if text == "" {
        c.replyVoiceI18n(ctx, msg, "voice.empty", nil)
        return nil, false
    }

    // text replaces empty Text; rest of message goes through pipeline as a regular DM
    builtMsg.Text = text
}
```

`downloadFile` — обёртка над `bot.GetFile` + `http.Get(file.Link(token))`. `replyVoiceI18n` — вспомогательный метод, рендерит ключ через `*i18n.Localizer` и шлёт через `bot.Send`. Точная сигнатура и место уточняются по факту, но идея — не плодить boilerplate.

### Поведение при `Transcriber == nil`

Если `[transcriber]` секция отсутствует или `provider = ""`:
- `transcribe.New` возвращает `(nil, nil)`.
- `main.go` без проблем передаёт `nil` в `TelegramChannelConfig`.
- При получении голосового канал отвечает `voice.disabled`.

Это «дефолт» для свежего конфига — голос не падает с ошибкой, пользователь видит, что нужно настроить.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): код, тесты, документация — всё внутри репозитория.
- **Post-Completion** (без чекбоксов): требования к окружению (установка `whisper-cli`/`ffmpeg`, получение Yandex API key) — это инструкция пользователю, а не задача для имплементации.

## Implementation Steps

### Task 1: Add Transcriber interface and AudioInput type

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/model/types.go` (если решим положить `AudioInput` ближе к остальным типам — иначе оставляем в interfaces.go рядом с интерфейсом)

- [x] добавить тип `AudioInput` (поля `Data []byte`, `MimeType string`, `Duration time.Duration`) с английскими комментариями
- [x] добавить интерфейс `Transcriber` с методами `Transcribe(ctx, AudioInput) (string, error)` и `Name() string`
- [x] запустить `go build ./...` и `go vet ./...` — должно собираться (новый интерфейс пока не используется)
- [x] тестов как таковых нет (определение типа), но вместе с задачей 2 тесты появляются — отмечаем «covered by Task 2 factory tests»

### Task 2: Add transcriber config structures and validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] добавить `TranscriberConfig` (общий: `Provider`, `MaxDurationRaw`, `MaxDuration`, `Whisper`, `Yandex`), `WhisperConfig`, `YandexConfig` со всеми полями, описанными в Technical Details
- [x] добавить поле `Transcriber TranscriberConfig` в `Config` с TOML-тегом `transcriber`
- [x] реализовать `validateTranscriber()` (вызывается из существующей `(*Config).validate`): провайдер-специфичные обязательные поля, парсинг `MaxDuration`, дефолт 5m, отказ на отрицательном/неизвестном значении
- [x] написать табличные тесты: пустая секция (всё ок); `provider="whisper"` с/без обязательных полей; `provider="yandex"` с/без обязательных полей; неизвестный `provider`; парсинг `max_duration`; отрицательный `max_duration`
- [x] прогнать `go test ./internal/config/...` и `go vet ./internal/config/...`

### Task 3: Implement WhisperCPP transcriber

**Files:**
- Create: `internal/transcribe/whisper.go`
- Create: `internal/transcribe/whisper_test.go`

- [x] определить интерфейс `commandRunner` с методом `Run(ctx, name string, args []string, stdin []byte) (stdout []byte, err error)` и его прод-реализацию `osCommandRunner` через `os/exec`
- [x] реализовать `WhisperCPP` со структурой `cfg WhisperConfig`, `runner commandRunner`; конструкторы `NewWhisperCPP(cfg) *WhisperCPP` (прод) и `newWhisperCPP(cfg, runner) *WhisperCPP` (тест)
- [x] реализовать `Transcribe`: temp WAV-файл (`os.CreateTemp` + `defer os.Remove`), ffmpeg на stdin, whisper-cli по пути, чтение `<wav>.txt`, `strings.TrimSpace`
- [x] реализовать `Name() string` → `"whisper"`
- [x] написать табличные тесты: успех, ошибка ffmpeg, ошибка whisper-cli, пустой txt-файл, таймаут (через `context.WithTimeout` и блокирующий runner), корректная сборка args для `-l`, `-t`, `-m`
- [x] прогнать `go test ./internal/transcribe/...` и `go vet ./internal/transcribe/...`

### Task 4: Implement Yandex SpeechKit transcriber

**Files:**
- Create: `internal/transcribe/yandex.go`
- Create: `internal/transcribe/yandex_test.go`

- [x] реализовать `Yandex` со структурой `cfg YandexConfig`, `client *http.Client`; конструктор `NewYandex(cfg YandexConfig) *Yandex` (прод-клиент с `Timeout = cfg.Timeout`); опциональный internal-конструктор `newYandex(cfg, client)` для подстановки `httptest`-клиента
- [x] реализовать `Transcribe`: построить URL c query (`folderId`, `lang`, `format=oggopus`), `POST` с заголовками `Authorization: Api-Key …`, `Content-Type: application/octet-stream`, body = `bytes.NewReader(audio.Data)`; декодировать `{"result": "..."}`, `strings.TrimSpace`
- [x] реализовать `Name() string` → `"yandex"`
- [x] написать табличные тесты через `httptest.NewServer`: успех; non-2xx (412/500) с проверкой текста ошибки; пустой `result`; таймаут (хендлер блокируется через `time.Sleep`); проверка отправленных заголовков и query-params; проверка совпадения переданного body и `audio.Data`
- [x] прогнать `go test ./internal/transcribe/...` и `go vet ./internal/transcribe/...`

### Task 5: Implement Transcriber factory

**Files:**
- Create: `internal/transcribe/factory.go`
- Create: `internal/transcribe/factory_test.go`

- [x] реализовать `New(cfg config.TranscriberConfig) (model.Transcriber, error)`: switch по `cfg.Provider` → `""` возвращает `(nil, nil)`, `"whisper"` → `NewWhisperCPP`, `"yandex"` → `NewYandex`, иначе ошибка `fmt.Errorf("unknown transcriber provider: %q", cfg.Provider)`
- [x] написать табличные тесты: provider пустой → `(nil, nil)`; whisper с минимально валидным конфигом → `*WhisperCPP`; yandex с минимально валидным конфигом → `*Yandex`; неизвестный provider → ошибка
- [x] прогнать `go test ./internal/transcribe/...` и `go vet ./internal/transcribe/...`

### Task 6: Add i18n keys for voice transcription

**Files:**
- Modify: `internal/i18n/locales/ru.json`
- Modify: `internal/i18n/locales/en.json`
- Modify: `internal/i18n/i18n_test.go` (если в проекте есть тест на валидность бандла — иначе создаём минимальный smoke)

- [x] добавить ключи `voice.disabled`, `voice.error`, `voice.empty` (без параметров) и `voice.too_long` (с параметром `{{.Limit}}`) в `ru.json` и `en.json`
- [x] тексты RU: «Распознавание голоса не настроено», «Не удалось распознать голос, попробуй ещё раз», «Не разобрал, повтори, пожалуйста», «Голосовое слишком длинное (лимит {{.Limit}})»
- [x] тексты EN: «Voice transcription is not configured», «Failed to transcribe voice, please try again», «Couldn't make out the voice, please repeat», «Voice message is too long (limit {{.Limit}})»
- [x] проверить, что бандл корректно загружается (smoke-тест либо ручной запуск `go test ./internal/i18n/...`)
- [x] прогнать `go test ./internal/i18n/...` и `go vet ./internal/i18n/...`

### Task 7: Wire voice handling into TelegramChannel

**Files:**
- Modify: `internal/channel/telegram.go`
- Modify: `internal/channel/telegram_test.go`

- [x] добавить в `TelegramChannelConfig` поля `Transcriber model.Transcriber` и `MaxVoiceDuration time.Duration` (с английскими комментариями)
- [x] в месте конвертации `tgbotapi.Update` → `model.Message` обработать `Voice`/`Audio`: если `kind == DM` — выполнить flow из Technical Details; в остальных случаях текущая логика без изменений
- [x] добавить вспомогательный метод `replyVoiceI18n(ctx, msg, key, params)` или эквивалент: рендер ключа через локализатор + `bot.Send`
- [x] добавить вспомогательный метод `downloadVoice(ctx, fileID) ([]byte, error)` через `bot.GetFile` + `http.Get(file.Link(token))`
- [x] написать в `telegram_test.go` рукописный мок `Transcriber` (поля `transcribeFn`, `nameFn`)
- [x] добавить тест-кейсы: voice DM + transcriber returns text → handler получает `Message{Text: ..., Kind: DM}`; voice DM + transcriber error → handler НЕ вызывается, `ReplyFn` зовётся с переведённым `voice.error`; voice DM + nil transcriber → `voice.disabled`; voice DM при `Duration > MaxVoiceDuration` → `voice.too_long`, transcriber НЕ вызывался; voice DM + transcriber возвращает `"   "` → `voice.empty`; voice в группе → транскрипция НЕ запускалась
- [x] прогнать `go test ./internal/channel/...` и `go vet ./internal/channel/...`

### Task 8: Wire factory into main.go

**Files:**
- Modify: `cmd/huskwoot/main.go`

- [x] вызвать `transcribe.New(cfg.Transcriber)` после загрузки конфига и до создания `TelegramChannel`; при ошибке — `slog.Error` + `os.Exit(1)`/`return err` (как делается для остальных компонентов)
- [x] прокинуть `Transcriber` и `MaxVoiceDuration` в `TelegramChannelConfig` при создании канала
- [x] добавить `slog.Info("transcriber configured", "provider", t.Name())` при ненулевом `t`
- [x] прогнать `go build ./...` (сборку CLI), `go test ./...`, `go vet ./...`

### Task 9: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [x] в раздел `Directory structure` добавить строку `internal/transcribe/ — Transcriber implementations (WhisperCPP subprocess, Yandex SpeechKit)`
- [x] в раздел `Configuration` добавить подраздел `### \`[transcriber]\`` с примером TOML, перечнем обязательных и необязательных полей для обоих провайдеров, описанием поведения при `provider = ""`
- [x] упомянуть, что голосовые сообщения обрабатываются только в DM Telegram-канале

### Task 10: Update README.md

**Files:**
- Modify: `README.md`

- [x] в список фич/возможностей добавить пункт про поддержку голосовых сообщений в Telegram DM
- [x] добавить раздел `## Voice transcription` (или аналогичный по стилю README): краткое описание; требования (отдельно для whisper.cpp — установить `whisper-cli` и `ffmpeg`, скачать модель; отдельно для Yandex — получить API key и folder ID); пример секций `[transcriber]`, `[transcriber.whisper]`, `[transcriber.yandex]`; явное указание поведения при `provider = ""` (бот отвечает «не настроено» на голосовые)

### Task 11: Verify acceptance criteria

- [x] голосовое в DM при `provider = "whisper"` корректно распознаётся (manual test skipped - requires installed whisper-cli; verified by unit tests in Task 3/7)
- [x] голосовое в DM при `provider = "yandex"` корректно распознаётся (manual test skipped - requires Yandex API key; verified by unit tests in Task 4/7)
- [x] голосовое в DM при `provider = ""` отвечает `voice.disabled` (covered by unit tests in Task 7)
- [x] голосовое > `max_duration` отвечает `voice.too_long` без вызова транскрайбера (covered by unit tests in Task 7)
- [x] голосовое в группе НЕ распознаётся (covered by unit tests in Task 7)
- [x] прогнать полный test suite: `go test ./...` (зелёный)
- [x] прогнать `go vet ./...` (без замечаний)

### Task 12: [Final] Move plan to completed

- [x] переместить `docs/plans/2026-04-26-voice-transcription.md` в `docs/plans/completed/`
- [x] убедиться, что все чекбоксы отмечены `[x]`

## Post-Completion

*Items requiring manual intervention or external systems — no checkboxes, informational only*

**Установка зависимостей пользователем (для whisper.cpp):**

- собрать или установить `whisper-cli` (`brew install whisper-cpp` / сборка из `https://github.com/ggerganov/whisper.cpp`).
- установить `ffmpeg` (`brew install ffmpeg` / `apt install ffmpeg`).
- скачать модель (`ggml-base.bin` или больше — по балансу качество/скорость) и указать её путь в `transcriber.whisper.model`.

**Получение Yandex SpeechKit API key:**

- создать сервисный аккаунт в Yandex Cloud, выдать роль `ai.speechkit-stt.user`.
- выпустить API-key для сервисного аккаунта.
- положить в env (`YANDEX_SPEECHKIT_API_KEY`, `YANDEX_FOLDER_ID`) и сослаться через `${...}` в config.toml.

**Ручное тестирование:**

- голосовое 5–10 секунд на русском в DM при `provider = "whisper"` → корректный текст в ответе агента.
- голосовое 5–10 секунд на русском в DM при `provider = "yandex"` → корректный текст в ответе агента.
- голосовое > `max_duration` (например, 6 минут при дефолте 5m) → `voice.too_long`.
- голосовое в групповом чате → бот молчит на сам факт голоса, обычная логика группы.
- запуск без секции `[transcriber]` → старые конфиги не ломаются, голосовое отвечает `voice.disabled`.
