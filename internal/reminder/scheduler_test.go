package reminder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// --- scheduler mocks ---

type mockSummaryBuilder struct {
	result model.Summary
	err    error
}

func (m *mockSummaryBuilder) Build(_ context.Context, slot string, at time.Time) (model.Summary, error) {
	if m.err != nil {
		return model.Summary{}, m.err
	}
	s := m.result
	s.Slot = slot
	s.GeneratedAt = at
	return s, nil
}

type mockSummaryDeliverer struct {
	delivererName string
	deliveries    []model.Summary
	err           error
}

func (m *mockSummaryDeliverer) Deliver(_ context.Context, s model.Summary) error {
	m.deliveries = append(m.deliveries, s)
	return m.err
}

func (m *mockSummaryDeliverer) Name() string { return m.delivererName }

// --- helpers ---

var weekdaysMF = []time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday,
}

func threeSlots() []Slot {
	return []Slot{
		{Name: "morning", Hour: 9, Minute: 0},
		{Name: "afternoon", Hour: 14, Minute: 0},
		{Name: "evening", Hour: 20, Minute: 0},
	}
}

func morningOnlySlots() []Slot {
	return []Slot{{Name: "morning", Hour: 9, Minute: 0}}
}

func makeTestScheduler(slots []Slot, workdays []time.Weekday, builder SummaryBuilder, deliverers []model.SummaryDeliverer) *Scheduler {
	now := func() time.Time { return msk(2026, 4, 17, 8, 30) } // Friday 08:30
	return New(
		Config{Slots: slots, SendWhenEmpty: "morning"},
		workdays,
		moscow,
		builder,
		deliverers,
		now,
		nil,
	)
}

// --- TestNextSlot ---

func TestNextSlot(t *testing.T) {
	tests := []struct {
		name     string
		slots    []Slot
		workdays []time.Weekday
		from     time.Time
		wantTime time.Time
		wantSlot string
	}{
		{
			name:     "будний день 08:30 → morning 09:00 сегодня",
			slots:    threeSlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 17, 8, 30), // Friday
			wantTime: msk(2026, 4, 17, 9, 0),
			wantSlot: "morning",
		},
		{
			name:     "будний день 12:00 → afternoon 14:00 сегодня",
			slots:    threeSlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 17, 12, 0), // Friday
			wantTime: msk(2026, 4, 17, 14, 0),
			wantSlot: "afternoon",
		},
		{
			name:     "будний 21:00 → morning следующего рабочего дня",
			slots:    threeSlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 17, 21, 0), // Friday 21:00
			wantTime: msk(2026, 4, 20, 9, 0),  // Monday
			wantSlot: "morning",
		},
		{
			name:     "пятница 21:00, только morning → morning понедельника",
			slots:    morningOnlySlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 17, 21, 0),
			wantTime: msk(2026, 4, 20, 9, 0),
			wantSlot: "morning",
		},
		{
			name:     "пятница 10:00, только morning → morning понедельника",
			slots:    morningOnlySlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 17, 10, 0), // morning has already passed
			wantTime: msk(2026, 4, 20, 9, 0),
			wantSlot: "morning",
		},
		{
			name:     "суббота 08:00 → понедельник morning",
			slots:    threeSlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 18, 8, 0), // Saturday
			wantTime: msk(2026, 4, 20, 9, 0),
			wantSlot: "morning",
		},
		{
			name:     "воскресенье 08:00 → понедельник morning",
			slots:    threeSlots(),
			workdays: weekdaysMF,
			from:     msk(2026, 4, 19, 8, 0), // Sunday
			wantTime: msk(2026, 4, 20, 9, 0),
			wantSlot: "morning",
		},
		{
			name:     "пустой workdays → нулевое время",
			slots:    threeSlots(),
			workdays: []time.Weekday{},
			from:     msk(2026, 4, 17, 8, 30),
			wantTime: time.Time{},
			wantSlot: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := makeTestScheduler(tc.slots, tc.workdays, nil, nil)
			gotTime, gotSlot := s.nextSlot(tc.from)
			if !gotTime.Equal(tc.wantTime) {
				t.Errorf("time: want %v, got %v", tc.wantTime, gotTime)
			}
			if gotSlot != tc.wantSlot {
				t.Errorf("slot: want %q, got %q", tc.wantSlot, gotSlot)
			}
		})
	}
}

// --- TestShouldSendEmpty ---

func TestShouldSendEmpty(t *testing.T) {
	tests := []struct {
		sendWhenEmpty string
		slot          string
		want          bool
	}{
		{"always", "morning", true},
		{"always", "afternoon", true},
		{"always", "evening", true},
		{"never", "morning", false},
		{"never", "afternoon", false},
		{"never", "evening", false},
		{"morning", "morning", true},
		{"morning", "afternoon", false},
		{"morning", "evening", false},
	}

	for _, tc := range tests {
		t.Run(tc.sendWhenEmpty+"/"+tc.slot, func(t *testing.T) {
			s := &Scheduler{cfg: Config{SendWhenEmpty: tc.sendWhenEmpty}}
			got := s.shouldSendEmpty(tc.slot)
			if got != tc.want {
				t.Errorf("shouldSendEmpty(%q) с sendWhenEmpty=%q: want %v, got %v",
					tc.slot, tc.sendWhenEmpty, tc.want, got)
			}
		})
	}
}

// --- TestRun ---

func TestRun_OneFire(t *testing.T) {
	builder := &mockSummaryBuilder{result: model.Summary{IsEmpty: false}}
	deliverer := &mockSummaryDeliverer{delivererName: "mock"}

	s := makeTestScheduler(morningOnlySlots(), weekdaysMF, builder, []model.SummaryDeliverer{deliverer})

	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	s.sleep = func(_ context.Context, _ time.Time) error {
		callCount++
		if callCount == 1 {
			return nil // first slot: allow fire
		}
		cancel()
		return context.Canceled
	}

	err := s.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(deliverer.deliveries) != 1 {
		t.Errorf("want 1 delivery, got %d", len(deliverer.deliveries))
	}
	if deliverer.deliveries[0].Slot != "morning" {
		t.Errorf("want slot=morning, got %q", deliverer.deliveries[0].Slot)
	}
}

func TestRun_DelivererErrorDoesNotBlockOthers(t *testing.T) {
	builder := &mockSummaryBuilder{result: model.Summary{IsEmpty: false}}
	d1 := &mockSummaryDeliverer{delivererName: "fail", err: errors.New("сбой доставки")}
	d2 := &mockSummaryDeliverer{delivererName: "ok"}

	s := makeTestScheduler(morningOnlySlots(), weekdaysMF, builder, []model.SummaryDeliverer{d1, d2})

	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	s.sleep = func(_ context.Context, _ time.Time) error {
		callCount++
		if callCount == 1 {
			return nil
		}
		cancel()
		return context.Canceled
	}

	_ = s.Run(ctx)

	if len(d1.deliveries) != 1 {
		t.Errorf("d1 should receive 1 attempt, got %d", len(d1.deliveries))
	}
	if len(d2.deliveries) != 1 {
		t.Errorf("d2 should receive 1 delivery despite d1 error, got %d", len(d2.deliveries))
	}
}

func TestRun_CancelCtx(t *testing.T) {
	builder := &mockSummaryBuilder{result: model.Summary{IsEmpty: false}}
	s := makeTestScheduler(morningOnlySlots(), weekdaysMF, builder, nil)

	ctx, cancel := context.WithCancel(context.Background())
	s.sleep = func(_ context.Context, _ time.Time) error {
		cancel()
		return context.Canceled
	}

	err := s.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRun_SkipsEmptySummary(t *testing.T) {
	builder := &mockSummaryBuilder{result: model.Summary{IsEmpty: true}}
	deliverer := &mockSummaryDeliverer{delivererName: "mock"}

	s := makeTestScheduler(morningOnlySlots(), weekdaysMF, builder, []model.SummaryDeliverer{deliverer})
	s.cfg.SendWhenEmpty = "never"

	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	s.sleep = func(_ context.Context, _ time.Time) error {
		callCount++
		if callCount == 1 {
			return nil
		}
		cancel()
		return context.Canceled
	}

	_ = s.Run(ctx)

	if len(deliverer.deliveries) != 0 {
		t.Errorf("with IsEmpty=true and SendWhenEmpty=never delivery should not happen, got %d", len(deliverer.deliveries))
	}
}
