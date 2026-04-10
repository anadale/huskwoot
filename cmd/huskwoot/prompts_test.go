package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrompt(t *testing.T) {
	t.Run("файл существует — возвращает содержимое", func(t *testing.T) {
		dir := t.TempDir()
		promptsDir := filepath.Join(dir, "prompts")
		if err := os.MkdirAll(promptsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "Привет, {{.UserName}}!"
		if err := os.WriteFile(filepath.Join(promptsDir, "detector-system.gotmpl"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := loadPrompt(dir, "detector-system")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != content {
			t.Errorf("got %q, want %q", got, content)
		}
	})

	t.Run("файл не существует — возвращает пустую строку", func(t *testing.T) {
		dir := t.TempDir()

		got, err := loadPrompt(dir, "detector-system")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("папка prompts отсутствует — возвращает пустую строку", func(t *testing.T) {
		dir := t.TempDir()

		got, err := loadPrompt(dir, "extractor-system")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("другой файл в папке не мешает", func(t *testing.T) {
		dir := t.TempDir()
		promptsDir := filepath.Join(dir, "prompts")
		if err := os.MkdirAll(promptsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Create a different template, not the one we are requesting.
		if err := os.WriteFile(filepath.Join(promptsDir, "extractor-system.gotmpl"), []byte("другой"), 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := loadPrompt(dir, "detector-system")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})
}
