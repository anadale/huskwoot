package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)

// Yandex transcribes audio using the Yandex SpeechKit synchronous REST API.
type Yandex struct {
	cfg    config.YandexConfig
	client *http.Client
}

// NewYandex creates a Yandex transcriber with a production HTTP client.
func NewYandex(cfg config.YandexConfig) *Yandex {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return newYandex(cfg, &http.Client{Timeout: timeout})
}

func newYandex(cfg config.YandexConfig, client *http.Client) *Yandex {
	return &Yandex{cfg: cfg, client: client}
}

// Name returns the transcriber name for logging.
func (y *Yandex) Name() string {
	return "yandex"
}

// mimeToYandexFormat maps audio MIME types to Yandex SpeechKit format identifiers.
var mimeToYandexFormat = map[string]string{
	"audio/ogg":  "oggopus",
	"audio/mpeg": "mp3",
}

// Transcribe sends audio to Yandex SpeechKit and returns the recognized text.
func (y *Yandex) Transcribe(ctx context.Context, audio model.AudioInput) (string, error) {
	mimeBase := strings.TrimSpace(strings.SplitN(audio.MimeType, ";", 2)[0])
	format, ok := mimeToYandexFormat[mimeBase]
	if !ok {
		return "", fmt.Errorf("yandex stt: unsupported audio format %q", audio.MimeType)
	}
	endpoint, err := buildYandexURL(y.cfg.Endpoint, y.cfg.FolderID, y.cfg.Language, format)
	if err != nil {
		return "", fmt.Errorf("yandex stt: build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(audio.Data))
	if err != nil {
		return "", fmt.Errorf("yandex stt: create request: %w", err)
	}
	req.Header.Set("Authorization", "Api-Key "+y.cfg.APIKey)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := y.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("yandex stt: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("yandex stt: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("yandex stt: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("yandex stt: decode response: %w", err)
	}

	return strings.TrimSpace(result.Result), nil
}

func buildYandexURL(endpoint, folderID, language, format string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("folderId", folderID)
	q.Set("lang", language)
	q.Set("format", format)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
