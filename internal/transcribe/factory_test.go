package transcribe

import (
	"strings"
	"testing"

	"github.com/anadale/huskwoot/internal/config"
)

func TestNew(t *testing.T) {
	validWhisper := config.WhisperConfig{
		Binary: "/usr/bin/whisper-cli",
		Model:  "/models/ggml-base.bin",
	}
	validYandex := config.YandexConfig{
		APIKey:   "key123",
		FolderID: "folder456",
	}

	tests := []struct {
		name        string
		cfg         config.TranscriberConfig
		wantNil     bool
		wantType    string
		wantErrFrag string
	}{
		{
			name:     "empty provider returns nil",
			cfg:      config.TranscriberConfig{Provider: ""},
			wantNil:  true,
			wantType: "",
		},
		{
			name:     "whisper with valid config returns WhisperCPP",
			cfg:      config.TranscriberConfig{Provider: "whisper", Whisper: validWhisper},
			wantNil:  false,
			wantType: "whisper",
		},
		{
			name:     "yandex with valid config returns Yandex",
			cfg:      config.TranscriberConfig{Provider: "yandex", Yandex: validYandex},
			wantNil:  false,
			wantType: "yandex",
		},
		{
			name:        "unknown provider returns error",
			cfg:         config.TranscriberConfig{Provider: "deepgram"},
			wantErrFrag: `unknown transcriber provider: "deepgram"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(tt.cfg)

			if tt.wantErrFrag != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrFrag)
				}
				if msg := err.Error(); !strings.Contains(msg, tt.wantErrFrag) {
					t.Fatalf("error %q does not contain %q", msg, tt.wantErrFrag)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil transcriber, got %T", got)
				}
				return
			}

			if got == nil {
				t.Fatal("expected non-nil transcriber, got nil")
			}
			if got.Name() != tt.wantType {
				t.Fatalf("Name() = %q, want %q", got.Name(), tt.wantType)
			}

			switch tt.cfg.Provider {
			case "whisper":
				w, ok := got.(*WhisperCPP)
				if !ok {
					t.Fatalf("expected *WhisperCPP, got %T", got)
				}
				if w.cfg.Binary != tt.cfg.Whisper.Binary {
					t.Errorf("WhisperCPP.cfg.Binary = %q, want %q", w.cfg.Binary, tt.cfg.Whisper.Binary)
				}
				if w.cfg.Model != tt.cfg.Whisper.Model {
					t.Errorf("WhisperCPP.cfg.Model = %q, want %q", w.cfg.Model, tt.cfg.Whisper.Model)
				}
			case "yandex":
				y, ok := got.(*Yandex)
				if !ok {
					t.Fatalf("expected *Yandex, got %T", got)
				}
				if y.cfg.APIKey != tt.cfg.Yandex.APIKey {
					t.Errorf("Yandex.cfg.APIKey = %q, want %q", y.cfg.APIKey, tt.cfg.Yandex.APIKey)
				}
				if y.cfg.FolderID != tt.cfg.Yandex.FolderID {
					t.Errorf("Yandex.cfg.FolderID = %q, want %q", y.cfg.FolderID, tt.cfg.Yandex.FolderID)
				}
			}
		})
	}
}

