package transcribe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)

// mockCall records a single invocation of commandRunner.Run.
type mockCall struct {
	name  string
	args  []string
	stdin []byte
}

// mockResponse defines the result for one Run invocation.
type mockResponse struct {
	stdout     []byte
	err        error
	sideEffect func(name string, args []string)
}

// mockRunner delivers sequential mock responses.
type mockRunner struct {
	calls     []mockCall
	responses []mockResponse
	idx       int
}

func (m *mockRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	m.calls = append(m.calls, mockCall{name: name, args: args, stdin: stdin})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if m.idx >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d to %q", m.idx, name)
	}
	resp := m.responses[m.idx]
	m.idx++
	if resp.sideEffect != nil {
		resp.sideEffect(name, args)
	}
	return resp.stdout, resp.err
}

// blockingRunner blocks until the context is cancelled.
type blockingRunner struct {
	calls []mockCall
}

func (r *blockingRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	r.calls = append(r.calls, mockCall{name: name, args: args, stdin: stdin})
	<-ctx.Done()
	return nil, ctx.Err()
}

// writeWhisperTxt finds the -f flag in args and writes content to <wavpath>.txt.
func writeWhisperTxt(args []string, content string) {
	for i, arg := range args {
		if arg == "-f" && i+1 < len(args) {
			_ = os.WriteFile(args[i+1]+".txt", []byte(content), 0600)
			return
		}
	}
}

func minimalWhisperCfg() config.WhisperConfig {
	return config.WhisperConfig{
		Binary: "whisper-cli",
		Model:  "/model/ggml-base.bin",
		Ffmpeg: "ffmpeg",
	}
}

func audioInput() model.AudioInput {
	return model.AudioInput{
		Data:     []byte("fake-ogg-data"),
		MimeType: "audio/ogg",
		Duration: 3 * time.Second,
	}
}

func TestWhisperCPP_Transcribe(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.WhisperConfig
		runner      commandRunner
		wantText    string
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			cfg:  minimalWhisperCfg(),
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg succeeds
					{
						sideEffect: func(_ string, args []string) {
							writeWhisperTxt(args, "  Hello world  \n")
						},
					},
				},
			},
			wantText: "Hello world",
		},
		{
			name: "ffmpeg error",
			cfg:  minimalWhisperCfg(),
			runner: &mockRunner{
				responses: []mockResponse{
					{err: errors.New("ffmpeg not found")},
				},
			},
			wantErr:     true,
			errContains: "ffmpeg convert",
		},
		{
			name: "whisper-cli error",
			cfg:  minimalWhisperCfg(),
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg succeeds
					{err: errors.New("model load failed")},
				},
			},
			wantErr:     true,
			errContains: "whisper-cli",
		},
		{
			name: "missing whisper binary",
			cfg:  minimalWhisperCfg(),
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg succeeds
					{err: fmt.Errorf("exec: \"whisper-cli\": executable file not found in $PATH")},
				},
			},
			wantErr:     true,
			errContains: "whisper-cli",
		},
		{
			name: "txt file not created by whisper",
			cfg:  minimalWhisperCfg(),
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg succeeds
					{}, // whisper-cli succeeds but writes no .txt
				},
			},
			wantErr:     true,
			errContains: "read whisper output",
		},
		{
			name: "empty txt file",
			cfg:  minimalWhisperCfg(),
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg succeeds
					{
						sideEffect: func(_ string, args []string) {
							writeWhisperTxt(args, "   \n  ")
						},
					},
				},
			},
			wantText: "",
		},
		{
			name: "timeout via cfg",
			cfg: config.WhisperConfig{
				Binary:  "whisper-cli",
				Model:   "/model/ggml-base.bin",
				Ffmpeg:  "ffmpeg",
				Timeout: 10 * time.Millisecond,
			},
			runner:      &blockingRunner{},
			wantErr:     true,
			errContains: "context deadline exceeded",
		},
		{
			name: "language flag passed",
			cfg: config.WhisperConfig{
				Binary:   "whisper-cli",
				Model:    "/model/ggml-base.bin",
				Ffmpeg:   "ffmpeg",
				Language: "ru",
			},
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg
					{
						sideEffect: func(_ string, args []string) {
							writeWhisperTxt(args, "текст")
						},
					},
				},
			},
			wantText: "текст",
		},
		{
			name: "threads flag passed",
			cfg: config.WhisperConfig{
				Binary:  "whisper-cli",
				Model:   "/model/ggml-base.bin",
				Ffmpeg:  "ffmpeg",
				Threads: 4,
			},
			runner: &mockRunner{
				responses: []mockResponse{
					{}, // ffmpeg
					{
						sideEffect: func(_ string, args []string) {
							writeWhisperTxt(args, "text with threads")
						},
					},
				},
			},
			wantText: "text with threads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWhisperCPP(tt.cfg, tt.runner)
			text, err := w.Transcribe(context.Background(), audioInput())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if text != tt.wantText {
				t.Errorf("got text %q, want %q", text, tt.wantText)
			}
		})
	}
}

func TestWhisperCPP_BuildWhisperArgs(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.WhisperConfig
		wavPath  string
		wantArgs []string
	}{
		{
			name: "minimal config",
			cfg: config.WhisperConfig{
				Binary: "whisper-cli",
				Model:  "/model/base.bin",
				Ffmpeg: "ffmpeg",
			},
			wavPath:  "test.wav",
			wantArgs: []string{"-m", "/model/base.bin", "-f", "test.wav", "-otxt", "-nt", "-np"},
		},
		{
			name: "with language",
			cfg: config.WhisperConfig{
				Binary:   "whisper-cli",
				Model:    "/model/base.bin",
				Ffmpeg:   "ffmpeg",
				Language: "ru",
			},
			wavPath:  "test.wav",
			wantArgs: []string{"-m", "/model/base.bin", "-f", "test.wav", "-otxt", "-nt", "-np", "-l", "ru"},
		},
		{
			name: "with threads",
			cfg: config.WhisperConfig{
				Binary:  "whisper-cli",
				Model:   "/model/base.bin",
				Ffmpeg:  "ffmpeg",
				Threads: 4,
			},
			wavPath:  "test.wav",
			wantArgs: []string{"-m", "/model/base.bin", "-f", "test.wav", "-otxt", "-nt", "-np", "-t", "4"},
		},
		{
			name: "with language and threads",
			cfg: config.WhisperConfig{
				Binary:   "whisper-cli",
				Model:    "/model/base.bin",
				Ffmpeg:   "ffmpeg",
				Language: "en",
				Threads:  2,
			},
			wavPath:  "test.wav",
			wantArgs: []string{"-m", "/model/base.bin", "-f", "test.wav", "-otxt", "-nt", "-np", "-l", "en", "-t", "2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WhisperCPP{cfg: tt.cfg}
			got := w.buildWhisperArgs(tt.wavPath)
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("got args %v (len %d), want %v (len %d)", got, len(got), tt.wantArgs, len(tt.wantArgs))
			}
			for i := range got {
				if got[i] != tt.wantArgs[i] {
					t.Errorf("arg[%d]: got %q, want %q", i, got[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestWhisperCPP_Name(t *testing.T) {
	w := NewWhisperCPP(minimalWhisperCfg())
	if w.Name() != "whisper" {
		t.Errorf("got %q, want %q", w.Name(), "whisper")
	}
}
