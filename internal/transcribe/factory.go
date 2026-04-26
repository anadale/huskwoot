package transcribe

import (
	"fmt"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)

// New creates a Transcriber from the given config.
// Returns (nil, nil) when cfg.Provider is empty (voice transcription disabled).
func New(cfg config.TranscriberConfig) (model.Transcriber, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	case "whisper":
		return NewWhisperCPP(cfg.Whisper), nil
	case "yandex":
		return NewYandex(cfg.Yandex), nil
	default:
		return nil, fmt.Errorf("unknown transcriber provider: %q", cfg.Provider)
	}
}
