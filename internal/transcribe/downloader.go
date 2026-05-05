package transcribe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/anadale/huskwoot/internal/model"
)

const maxTelegramDownloadBytes = 10 << 20 // 10 MiB

// fileGetter abstracts the GetFile call on tgbotapi.BotAPI for testing.
type fileGetter interface {
	GetFile(config tgbotapi.FileConfig) (tgbotapi.File, error)
}

// telegramDownloader fetches voice audio from Telegram file storage.
type telegramDownloader struct {
	getter fileGetter
	linkFn func(file tgbotapi.File) string
	client *http.Client
}

// NewTelegramDownloader creates a VoiceDownloader backed by a real Telegram bot.
func NewTelegramDownloader(bot *tgbotapi.BotAPI) model.VoiceDownloader {
	return &telegramDownloader{
		getter: bot,
		linkFn: func(f tgbotapi.File) string { return f.Link(bot.Token) },
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (d *telegramDownloader) Download(ctx context.Context, job model.VoiceJob) ([]byte, error) {
	file, err := d.getter.GetFile(tgbotapi.FileConfig{FileID: job.Payload})
	if err != nil {
		return nil, fmt.Errorf("getting file info: %w", err)
	}
	url := d.linkFn(file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTelegramDownloadBytes))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	return data, nil
}

// localFileDownloader reads voice audio from the local filesystem.
// Payload is a relative path under uploadsDir (e.g. "<uuid>.ogg").
type localFileDownloader struct {
	uploadsDir string
}

// NewLocalFileDownloader creates a VoiceDownloader that reads files from uploadsDir.
func NewLocalFileDownloader(uploadsDir string) model.VoiceDownloader {
	return &localFileDownloader{uploadsDir: uploadsDir}
}

func (d *localFileDownloader) Download(_ context.Context, job model.VoiceJob) ([]byte, error) {
	// Guard against path traversal. On Unix filepath.Join does NOT neutralise absolute
	// paths in the second argument ("/uploads" + "/etc/passwd" → "/etc/passwd"), so
	// absolute paths must be rejected explicitly before the join. The canonical-prefix
	// check below catches relative traversal (e.g. "../").
	if strings.HasPrefix(job.Payload, "/") || strings.HasPrefix(job.Payload, `\`) {
		return nil, fmt.Errorf("invalid payload: path traversal not allowed")
	}
	path := filepath.Join(d.uploadsDir, job.Payload)
	if !strings.HasPrefix(path, filepath.Clean(d.uploadsDir)+string(filepath.Separator)) {
		return nil, fmt.Errorf("invalid payload: path traversal not allowed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading voice file: %w", err)
	}
	return data, nil
}
