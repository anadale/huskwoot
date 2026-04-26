package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil, err
	}
	return out, nil
}

// WhisperCPP transcribes audio using a local whisper.cpp subprocess.
type WhisperCPP struct {
	cfg    config.WhisperConfig
	runner commandRunner
}

// NewWhisperCPP creates a WhisperCPP transcriber with the production command runner.
func NewWhisperCPP(cfg config.WhisperConfig) *WhisperCPP {
	return newWhisperCPP(cfg, osCommandRunner{})
}

func newWhisperCPP(cfg config.WhisperConfig, runner commandRunner) *WhisperCPP {
	return &WhisperCPP{cfg: cfg, runner: runner}
}

// Name returns the transcriber name for logging.
func (w *WhisperCPP) Name() string {
	return "whisper"
}

// Transcribe converts audio to text via ffmpeg → whisper-cli.
func (w *WhisperCPP) Transcribe(ctx context.Context, audio model.AudioInput) (string, error) {
	if w.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.cfg.Timeout)
		defer cancel()
	}

	wavFile, err := os.CreateTemp("", "huskwoot-whisper-*.wav")
	if err != nil {
		return "", fmt.Errorf("create temp wav: %w", err)
	}
	wavPath := wavFile.Name()
	defer os.Remove(wavPath)
	defer os.Remove(wavPath + ".txt")
	if err := wavFile.Close(); err != nil {
		return "", fmt.Errorf("close temp wav: %w", err)
	}

	// Convert audio to 16 kHz mono WAV via ffmpeg (reads from stdin).
	ffmpegArgs := []string{"-i", "pipe:0", "-ar", "16000", "-ac", "1", "-f", "wav", "-y", wavPath}
	if _, err := w.runner.Run(ctx, w.cfg.Ffmpeg, ffmpegArgs, audio.Data); err != nil {
		return "", fmt.Errorf("ffmpeg convert: %w", err)
	}

	// Transcribe WAV with whisper-cli; output written to <wavPath>.txt.
	whisperArgs := w.buildWhisperArgs(wavPath)
	if _, err := w.runner.Run(ctx, w.cfg.Binary, whisperArgs, nil); err != nil {
		return "", fmt.Errorf("whisper-cli: %w", err)
	}

	content, err := os.ReadFile(wavPath + ".txt")
	if err != nil {
		return "", fmt.Errorf("read whisper output: %w", err)
	}

	return strings.TrimSpace(string(content)), nil
}

func (w *WhisperCPP) buildWhisperArgs(wavPath string) []string {
	args := []string{"-m", w.cfg.Model, "-f", wavPath, "-otxt", "-nt", "-np"}
	if w.cfg.Language != "" {
		args = append(args, "-l", w.cfg.Language)
	}
	if w.cfg.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(w.cfg.Threads))
	}
	return args
}
