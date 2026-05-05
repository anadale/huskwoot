package transcribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/anadale/huskwoot/internal/model"
)

// --- telegramDownloader tests ---

type mockFileGetter struct {
	file tgbotapi.File
	err  error
}

func (m *mockFileGetter) GetFile(_ tgbotapi.FileConfig) (tgbotapi.File, error) {
	return m.file, m.err
}

func TestTelegramDownloader_Download_ReadsBytes(t *testing.T) {
	want := []byte("audio-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	getter := &mockFileGetter{file: tgbotapi.File{FilePath: "voice/file.ogg"}}
	d := &telegramDownloader{
		getter: getter,
		linkFn: func(f tgbotapi.File) string { return srv.URL + "/" + f.FilePath },
		client: srv.Client(),
	}

	job := model.VoiceJob{Payload: "AgADCQAD"}
	got, err := d.Download(context.Background(), job)
	if err != nil {
		t.Fatalf("Download: unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Download: got %q, want %q", got, want)
	}
}

func TestTelegramDownloader_Download_GetFileError(t *testing.T) {
	getter := &mockFileGetter{err: errGetFile}
	d := &telegramDownloader{
		getter: getter,
		linkFn: func(_ tgbotapi.File) string { return "" },
		client: http.DefaultClient,
	}

	_, err := d.Download(context.Background(), model.VoiceJob{Payload: "id"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

type errType struct{}

func (errType) Error() string { return "get file error" }

var errGetFile = errType{}

func TestTelegramDownloader_Download_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	getter := &mockFileGetter{file: tgbotapi.File{FilePath: "voice/file.ogg"}}
	d := &telegramDownloader{
		getter: getter,
		linkFn: func(f tgbotapi.File) string { return srv.URL + "/" + f.FilePath },
		client: srv.Client(),
	}

	_, err := d.Download(context.Background(), model.VoiceJob{Payload: "id"})
	if err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
}

func TestTelegramDownloader_Download_LimitsTo10MiB(t *testing.T) {
	// Server sends slightly more than 10 MiB; downloader must cap the read.
	bigPayload := make([]byte, maxTelegramDownloadBytes+1)
	for i := range bigPayload {
		bigPayload[i] = 0xAB
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bigPayload)
	}))
	defer srv.Close()

	getter := &mockFileGetter{file: tgbotapi.File{FilePath: "voice/big.ogg"}}
	d := &telegramDownloader{
		getter: getter,
		linkFn: func(f tgbotapi.File) string { return srv.URL + "/" + f.FilePath },
		client: srv.Client(),
	}

	got, err := d.Download(context.Background(), model.VoiceJob{Payload: "id"})
	if err != nil {
		t.Fatalf("Download: unexpected error: %v", err)
	}
	if len(got) > maxTelegramDownloadBytes {
		t.Errorf("Download: read %d bytes, limit is %d", len(got), maxTelegramDownloadBytes)
	}
}

// --- localFileDownloader tests ---

func TestLocalFileDownloader_Download_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "2026", "05")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	fpath := filepath.Join(sub, "abc.ogg")
	want := []byte("voice-audio")
	if err := os.WriteFile(fpath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	d := &localFileDownloader{uploadsDir: dir}
	job := model.VoiceJob{Payload: filepath.Join("2026", "05", "abc.ogg")}
	got, err := d.Download(context.Background(), job)
	if err != nil {
		t.Fatalf("Download: unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocalFileDownloader_Download_NonExistentFile(t *testing.T) {
	d := &localFileDownloader{uploadsDir: t.TempDir()}
	_, err := d.Download(context.Background(), model.VoiceJob{Payload: "2026/05/missing.ogg"})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLocalFileDownloader_Download_PathTraversalDotDot(t *testing.T) {
	d := &localFileDownloader{uploadsDir: t.TempDir()}
	_, err := d.Download(context.Background(), model.VoiceJob{Payload: "../secret"})
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLocalFileDownloader_Download_PathTraversalAbsolute(t *testing.T) {
	d := &localFileDownloader{uploadsDir: t.TempDir()}
	_, err := d.Download(context.Background(), model.VoiceJob{Payload: "/etc/passwd"})
	if err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("unexpected error: %v", err)
	}
}
