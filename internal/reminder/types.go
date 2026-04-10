package reminder

import (
	"context"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// Config is passed to New from main.go.
type Config struct {
	// Slots are the enabled schedule slots in morning→afternoon→evening order.
	Slots []Slot
	// SendWhenEmpty controls behaviour when the summary is empty: "always", "never", "morning".
	SendWhenEmpty string
}

// Slot is a single active schedule slot.
type Slot struct {
	Name   string // "morning" | "afternoon" | "evening"
	Hour   int
	Minute int
}

// BuilderConfig configures Builder.
type BuilderConfig struct {
	// PlansHorizon is the planning horizon (from the start of the current day).
	PlansHorizon time.Duration
	// UndatedLimit is the maximum number of undated tasks shown (0 = do not show).
	UndatedLimit int
}

// SummaryBuilder builds a task summary for the given slot.
type SummaryBuilder interface {
	Build(ctx context.Context, slot string, at time.Time) (model.Summary, error)
}
