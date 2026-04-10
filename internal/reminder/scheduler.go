package reminder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// Scheduler drives periodic delivery of task summaries on a schedule.
type Scheduler struct {
	cfg        Config
	workdays   []time.Weekday
	loc        *time.Location
	builder    SummaryBuilder
	deliverers []model.SummaryDeliverer
	now        func() time.Time
	sleep      func(ctx context.Context, t time.Time) error
	logger     *slog.Logger
}

// New creates a Scheduler.
func New(
	cfg Config,
	workdays []time.Weekday,
	loc *time.Location,
	builder SummaryBuilder,
	deliverers []model.SummaryDeliverer,
	now func() time.Time,
	logger *slog.Logger,
) *Scheduler {
	if now == nil {
		now = time.Now
	}
	if loc == nil {
		loc = time.Local
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Scheduler{
		cfg:        cfg,
		workdays:   workdays,
		loc:        loc,
		builder:    builder,
		deliverers: deliverers,
		now:        now,
		logger:     logger,
	}
	s.sleep = s.defaultSleep
	return s
}

func (s *Scheduler) defaultSleep(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run starts the scheduler. Blocks until ctx is cancelled, returns ctx.Err().
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		t, slotName := s.nextSlot(s.now())
		if t.IsZero() {
			return fmt.Errorf("could not find next schedule slot: no workdays or slots not configured")
		}
		if err := s.sleep(ctx, t); err != nil {
			return err
		}
		s.fire(ctx, slotName, t)
	}
}

func (s *Scheduler) fire(ctx context.Context, slot string, at time.Time) {
	summary, err := s.builder.Build(ctx, slot, at)
	if err != nil {
		s.logger.Error("building summary", "slot", slot, "error", err)
		return
	}
	if summary.IsEmpty && !s.shouldSendEmpty(slot) {
		s.logger.Debug("summary is empty, skipping delivery", "slot", slot)
		return
	}
	for _, d := range s.deliverers {
		if err := d.Deliver(ctx, summary); err != nil {
			s.logger.Error("delivering summary", "deliverer", d.Name(), "slot", slot, "error", err)
		}
	}
}

func (s *Scheduler) shouldSendEmpty(slot string) bool {
	switch s.cfg.SendWhenEmpty {
	case "always":
		return true
	case "never":
		return false
	case "morning":
		return slot == "morning"
	}
	return false
}

// nextSlot finds the nearest firing strictly after from.
// Only workdays are considered. Iterates at most 14 days.
func (s *Scheduler) nextSlot(from time.Time) (time.Time, string) {
	from = from.In(s.loc)
	for i := 0; i < 14; i++ {
		day := from.AddDate(0, 0, i)
		if !isWorkday(day.Weekday(), s.workdays) {
			continue
		}
		for _, slot := range s.cfg.Slots {
			candidate := time.Date(day.Year(), day.Month(), day.Day(), slot.Hour, slot.Minute, 0, 0, s.loc)
			if candidate.After(from) {
				return candidate, slot.Name
			}
		}
	}
	return time.Time{}, ""
}

func isWorkday(w time.Weekday, workdays []time.Weekday) bool {
	for _, wd := range workdays {
		if w == wd {
			return true
		}
	}
	return false
}
