package dateparse

import (
	"testing"
	"time"
)

func TestRussianDateLanguage_RelativeAmount(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"через полчаса", "через полчаса", now.Add(30 * time.Minute)},
		{"через 30 минут", "через 30 минут", now.Add(30 * time.Minute)},
		{"через час", "через час", now.Add(time.Hour)},
		{"через 2 часа", "через 2 часа", now.Add(2 * time.Hour)},
		{"через 1 день", "через 1 день", now.AddDate(0, 0, 1)},
		{"через 3 дня", "через 3 дня", now.AddDate(0, 0, 3)},
		{"через 5 дней", "через 5 дней", now.AddDate(0, 0, 5)},
		{"через 1 неделю", "через 1 неделю", now.AddDate(0, 0, 7)},
		{"через 2 недели", "через 2 недели", now.AddDate(0, 0, 14)},
		{"через 1 месяц", "через 1 месяц", now.AddDate(0, 1, 0)},
		{"через 3 месяца", "через 3 месяца", now.AddDate(0, 3, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Unix() != tt.want.Unix() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_DayTime(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"завтра", "завтра", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"сегодня", "сегодня", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"послезавтра", "послезавтра", time.Date(2026, 4, 17, 20, 0, 0, 0, loc)},
		{"завтра утром", "завтра утром", time.Date(2026, 4, 16, 11, 0, 0, 0, loc)},
		{"завтра вечером", "завтра вечером", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"сегодня вечером", "сегодня вечером", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"к утру", "к утру", time.Date(2026, 4, 16, 11, 0, 0, 0, loc)},
		{"к вечеру", "к вечеру", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"до вечера", "до вечера", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"сегодня до вечера", "сегодня до вечера", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"завтра до вечера", "завтра до вечера", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"послезавтра до вечера", "послезавтра до вечера", time.Date(2026, 4, 17, 20, 0, 0, 0, loc)},
		{"к обеду", "к обеду", time.Date(2026, 4, 15, 12, 0, 0, 0, loc)},
		{"после обеда", "после обеда", time.Date(2026, 4, 15, 14, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Minute() != tt.want.Minute() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_Weekday(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"в среду (today)", "в среду", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"в четверг", "в четверг", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"в пятницу", "в пятницу", time.Date(2026, 4, 17, 20, 0, 0, 0, loc)},
		{"к пятнице", "к пятнице", time.Date(2026, 4, 17, 20, 0, 0, 0, loc)},
		{"в следующий четверг", "в следующий четверг", time.Date(2026, 4, 23, 20, 0, 0, 0, loc)},
		{"в следующую пятницу", "в следующую пятницу", time.Date(2026, 4, 24, 20, 0, 0, 0, loc)},
		{"в следующее воскресенье", "в следующее воскресенье", time.Date(2026, 4, 26, 20, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() || got.Hour() != tt.want.Hour() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_WeekBoundary(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"до конца недели", "до конца недели", time.Date(2026, 4, 17, 23, 59, 59, 0, loc)},
		{"до конца месяца", "до конца месяца", time.Date(2026, 4, 30, 23, 59, 59, 0, loc)},
		{"на следующей неделе", "на следующей неделе", time.Date(2026, 4, 20, 20, 0, 0, 0, loc)},
		{"к следующей неделе", "к следующей неделе", time.Date(2026, 4, 20, 20, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Second() != tt.want.Second() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_ExplicitDate(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"5 мая", "5 мая", time.Date(2026, 5, 5, 20, 0, 0, 0, loc)},
		// testNow=14:00, Default=20 → 2026-04-15 20:00 is still in the future today
		{"15.04 (сегодня, ещё впереди)", "15.04", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		// 1 января 2026 is in the past → rolls over to 2027; rolled-over date also uses Default hour
		{"1 января (прошедшая дата -> следующий год)", "1 января", time.Date(2027, 1, 1, 20, 0, 0, 0, loc)},
		{"15.04.2026", "15.04.2026", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"до 20-го", "до 20-го", time.Date(2026, 4, 20, 23, 59, 59, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() || got.Hour() != tt.want.Hour() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_NoMatch(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	_, ok := lang.Parse("tomorrow", now, cfg)
	if ok {
		t.Error("Russian language should not match 'tomorrow'")
	}

	_, ok = lang.Parse("in 2 hours", now, cfg)
	if ok {
		t.Error("Russian language should not match 'in 2 hours'")
	}
}

// TestParse_DefaultTimeOfDay verifies that cfg.TimeOfDay.Default is used as the hour for
// dateless deadline expressions across all four named time-of-day values.
func TestParse_DefaultTimeOfDay(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("ru")

	cases := []struct {
		name        string
		defaultHour int
		input       string
		wantHour    int
	}{
		{"morning/завтра", 11, "завтра", 11},
		{"lunch/завтра", 12, "завтра", 12},
		{"afternoon/завтра", 14, "завтра", 14},
		{"evening/завтра", 20, "завтра", 20},
		{"morning/в понедельник", 11, "в понедельник", 11},
		{"evening/в понедельник", 20, "в понедельник", 20},
		{"morning/в выходные", 11, "в выходные", 11},
		{"lunch/в выходные", 12, "в выходные", 12},
		{"afternoon/в выходные", 14, "в выходные", 14},
		{"evening/после выходных", 20, "после выходных", 20},
		{"morning/после выходных", 11, "после выходных", 11},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: tt.defaultHour},
			}
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Hour() != tt.wantHour {
				t.Errorf("%q: got hour %d, want %d (full: %v)", tt.input, got.Hour(), tt.wantHour, got)
			}
		})
	}
}

// TestRussianDateLanguage_NightCutoff verifies that shiftRelativeDayOffset is applied in
// the Russian day-phrase patterns when NightCutoffMinutes is set.
func TestRussianDateLanguage_NightCutoff(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("failed to load Europe/Moscow: %v", err)
	}
	// Monday 2026-04-27 02:00 – a night-owl scenario before cutoff
	nightNow := time.Date(2026, 4, 27, 2, 0, 0, 0, loc)
	// Monday 2026-04-27 06:00 – after the cutoff
	morningNow := time.Date(2026, 4, 27, 6, 0, 0, 0, loc)
	// Monday 2026-04-27 04:59 – just before the cutoff boundary
	beforeCutoff := time.Date(2026, 4, 27, 4, 59, 0, 0, loc)
	// Monday 2026-04-27 05:00 – exactly at the cutoff boundary (no shift)
	atCutoff := time.Date(2026, 4, 27, 5, 0, 0, 0, loc)

	cfgCutoff := Config{
		TimeOfDay:          TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20},
		NightCutoffMinutes: 300, // 05:00
	}
	cfgNoCutoff := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20},
	}
	lang := NewDateLanguage("ru")

	tests := []struct {
		name  string
		now   time.Time
		cfg   Config
		input string
		want  time.Time
	}{
		// before cutoff: relative offsets are shifted by -1
		{
			"завтра before cutoff → today (пн)",
			nightNow, cfgCutoff, "завтра",
			time.Date(2026, 4, 27, 20, 0, 0, 0, loc), // shift: 1→0 → same day
		},
		{
			"послезавтра before cutoff → tomorrow (вт)",
			nightNow, cfgCutoff, "послезавтра",
			time.Date(2026, 4, 28, 20, 0, 0, 0, loc), // shift: 2→1 → next day
		},
		{
			"сегодня before cutoff → today (literal, no shift)",
			nightNow, cfgCutoff, "сегодня",
			time.Date(2026, 4, 27, 20, 0, 0, 0, loc), // offset=0, never shifted
		},
		{
			"завтра утром before cutoff → today at morning",
			nightNow, cfgCutoff, "завтра утром",
			time.Date(2026, 4, 27, 11, 0, 0, 0, loc), // shift: 1→0, morning=11
		},
		{
			"послезавтра утром before cutoff → tomorrow at morning",
			nightNow, cfgCutoff, "послезавтра утром",
			time.Date(2026, 4, 28, 11, 0, 0, 0, loc), // shift: 2→1, morning=11
		},
		{
			"завтра до вечера before cutoff → today at evening",
			nightNow, cfgCutoff, "завтра до вечера",
			time.Date(2026, 4, 27, 20, 0, 0, 0, loc), // shift: 1→0, evening=20
		},
		{
			"завтра в 14:00 before cutoff → today 14:00",
			nightNow, cfgCutoff, "завтра в 14:00",
			time.Date(2026, 4, 27, 14, 0, 0, 0, loc), // shift: 1→0
		},
		// after cutoff: no shift
		{
			"завтра after cutoff → tomorrow",
			morningNow, cfgCutoff, "завтра",
			time.Date(2026, 4, 28, 20, 0, 0, 0, loc),
		},
		// boundary: 04:59 < 05:00 → shift
		{
			"завтра at 04:59 → today (shift)",
			beforeCutoff, cfgCutoff, "завтра",
			time.Date(2026, 4, 27, 20, 0, 0, 0, loc),
		},
		// boundary: 05:00 is NOT < 05:00 → no shift
		{
			"завтра at 05:00 → tomorrow (no shift, strict <)",
			atCutoff, cfgCutoff, "завтра",
			time.Date(2026, 4, 28, 20, 0, 0, 0, loc),
		},
		// regression: cutoff=0 → no shift
		{
			"завтра cutoff=0 regression → tomorrow",
			nightNow, cfgNoCutoff, "завтра",
			time.Date(2026, 4, 28, 20, 0, 0, 0, loc),
		},
		{
			"послезавтра cutoff=0 regression",
			nightNow, cfgNoCutoff, "послезавтра",
			time.Date(2026, 4, 29, 20, 0, 0, 0, loc),
		},
		// "до завтра" deadline expression shifts too
		{
			"до завтра before cutoff → today 23:59:59",
			nightNow, cfgCutoff, "до завтра",
			time.Date(2026, 4, 27, 23, 59, 59, 0, loc),
		},
		{
			"до завтра after cutoff → tomorrow 23:59:59",
			morningNow, cfgCutoff, "до завтра",
			time.Date(2026, 4, 28, 23, 59, 59, 0, loc),
		},
		{
			"до послезавтра before cutoff → tomorrow 23:59:59",
			nightNow, cfgCutoff, "до послезавтра",
			time.Date(2026, 4, 28, 23, 59, 59, 0, loc),
		},
		// night-time phrases: the markerNight adds +1 to the already-shifted base day
		{
			"завтра ночью before cutoff → Tue 00:00",
			nightNow, cfgCutoff, "завтра ночью",
			time.Date(2026, 4, 28, 0, 0, 0, 0, loc), // shift: 1→0 (Mon), +1 for night = Tue
		},
		{
			"послезавтра ночью before cutoff → Wed 00:00",
			nightNow, cfgCutoff, "послезавтра ночью",
			time.Date(2026, 4, 29, 0, 0, 0, 0, loc), // shift: 2→1 (Tue), +1 for night = Wed
		},
		// "к утру" standalone before cutoff: falls through to normal path, offset=0 stays 0
		{
			"к утру before cutoff (02:00) → today morning",
			nightNow, cfgCutoff, "к утру",
			time.Date(2026, 4, 27, 11, 0, 0, 0, loc), // offset=0, never shifted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, tt.now, tt.cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Minute() != tt.want.Minute() || got.Second() != tt.want.Second() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRussianDateLanguage_KUTru_CutoffGtMorning verifies that "к утру" respects the
// night-cutoff shift even when the cutoff is later than the morning time-of-day hour.
// Without the shiftRelativeDayOffset call in the early-return branch, the test would fail.
func TestRussianDateLanguage_KUTru_CutoffGtMorning(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("failed to load Europe/Moscow: %v", err)
	}
	lang := NewDateLanguage("ru")
	// cutoff = 13:00, morning = 11; at 12:00 we are past morning but still before cutoff
	cfg := Config{
		TimeOfDay:          TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20},
		NightCutoffMinutes: 780, // 13:00
	}
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, loc)
	got, ok := lang.Parse("к утру", now, cfg)
	if !ok {
		t.Fatal("expected match for \"к утру\", got none")
	}
	want := time.Date(2026, 4, 27, 11, 0, 0, 0, loc) // shift: 1→0 → today morning
	if got.Year() != want.Year() || got.Month() != want.Month() || got.Day() != want.Day() || got.Hour() != want.Hour() {
		t.Errorf("к утру at 12:00 with cutoff=13:00: got %v, want %v", got, want)
	}
}

// TestRussianDateLanguage_DeadlineExpressions checks that deadline phrases always produce
// 23:59:59 regardless of cfg.TimeOfDay.Default.
func TestRussianDateLanguage_DeadlineExpressions(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	deadlines := []string{
		"до конца недели",
		"до конца месяца",
		"до выходных",
		"до 15-го",
		"до завтра",
		"до послезавтра",
	}

	for _, input := range deadlines {
		t.Run(input, func(t *testing.T) {
			got, ok := lang.Parse(input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", input)
			}
			if got.Hour() != 23 || got.Minute() != 59 || got.Second() != 59 {
				t.Errorf("%q: got %v, want 23:59:59", input, got)
			}
		})
	}
}
