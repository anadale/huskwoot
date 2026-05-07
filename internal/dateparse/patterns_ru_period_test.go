package dateparse

import (
	"testing"
	"time"
)

func TestApplyDayPeriod(t *testing.T) {
	tests := []struct {
		name   string
		hour   int
		period string
		want   int
		wantOK bool
	}{
		// утра
		{"в 7 утра", 7, "утра", 7, true},
		{"в 1 утра", 1, "утра", 1, true},
		{"в 11 утра", 11, "утра", 11, true},
		{"в 12 утра → midnight", 12, "утра", 0, true},
		{"в 13 утра invalid", 13, "утра", 0, false},
		{"в 0 утра invalid", 0, "утра", 0, false},
		// дня
		{"в 1 дня", 1, "дня", 13, true},
		{"в 3 дня", 3, "дня", 15, true},
		{"в 5 дня", 5, "дня", 17, true},
		{"в 12 дня → noon", 12, "дня", 12, true},
		{"в 6 дня invalid", 6, "дня", 0, false},
		{"в 0 дня invalid", 0, "дня", 0, false},
		// вечера
		{"в 5 вечера", 5, "вечера", 17, true},
		{"в 7 вечера", 7, "вечера", 19, true},
		{"в 11 вечера", 11, "вечера", 23, true},
		{"в 12 вечера", 12, "вечера", 12, true},
		{"в 4 вечера invalid", 4, "вечера", 0, false},
		// ночи
		{"в 12 ночи → midnight", 12, "ночи", 0, true},
		{"в 1 ночи", 1, "ночи", 1, true},
		{"в 4 ночи", 4, "ночи", 4, true},
		{"в 5 ночи invalid", 5, "ночи", 0, false},
		{"в 11 ночи invalid", 11, "ночи", 0, false},
		// unknown period
		{"unknown period", 7, "вечером", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := applyDayPeriod(tt.hour, tt.period)
			if ok != tt.wantOK {
				t.Fatalf("applyDayPeriod(%d, %q) ok = %v, want %v", tt.hour, tt.period, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("applyDayPeriod(%d, %q) = %d, want %d", tt.hour, tt.period, got, tt.want)
			}
		})
	}
}

// TestParse_PeriodClockTime covers the standalone "в|к <hour>[:mm][ часов] <period>" form.
// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow.
func TestParse_PeriodClockTime(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// вечера: today (now=14:00, target>now)
		{"в 7 вечера", "в 7 вечера", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
		{"в 11 вечера", "в 11 вечера", time.Date(2026, 4, 15, 23, 0, 0, 0, loc)},
		{"в 8:30 вечера", "в 8:30 вечера", time.Date(2026, 4, 15, 20, 30, 0, 0, loc)},
		{"в 7 часов вечера", "в 7 часов вечера", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
		{"в 8 часа вечера", "в 8 часа вечера", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"к 7 вечера", "к 7 вечера", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
		// утра: in the past for today (now=14:00) → tomorrow
		{"в 7 утра → tomorrow", "в 7 утра", time.Date(2026, 4, 16, 7, 0, 0, 0, loc)},
		{"в 11 утра → tomorrow", "в 11 утра", time.Date(2026, 4, 16, 11, 0, 0, 0, loc)},
		{"в 12 утра → tomorrow midnight", "в 12 утра", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
		{"в 9:15 утра → tomorrow", "в 9:15 утра", time.Date(2026, 4, 16, 9, 15, 0, 0, loc)},
		// дня: now=14:00, "в 3 дня"=15:00 today; "в 1 дня"=13:00 tomorrow.
		{"в 3 дня today", "в 3 дня", time.Date(2026, 4, 15, 15, 0, 0, 0, loc)},
		{"в 5 дня today", "в 5 дня", time.Date(2026, 4, 15, 17, 0, 0, 0, loc)},
		{"в 12 дня → tomorrow noon", "в 12 дня", time.Date(2026, 4, 16, 12, 0, 0, 0, loc)},
		{"в 1 дня → tomorrow", "в 1 дня", time.Date(2026, 4, 16, 13, 0, 0, 0, loc)},
		{"в 3 часа дня today", "в 3 часа дня", time.Date(2026, 4, 15, 15, 0, 0, 0, loc)},
		// ночи: target < now (14:00) → next day
		{"в 1 ночи → tomorrow 01:00", "в 1 ночи", time.Date(2026, 4, 16, 1, 0, 0, 0, loc)},
		{"в 12 ночи → tomorrow 00:00", "в 12 ночи", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
		{"в 3 ночи → tomorrow 03:00", "в 3 ночи", time.Date(2026, 4, 16, 3, 0, 0, 0, loc)},
		// case-insensitive and whitespace tolerance
		{"В 7 ВЕЧЕРА (uppercase)", "В 7 ВЕЧЕРА", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
		{"  в 7 вечера  (with spaces)", "  в 7 вечера  ", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
		{"в  7  вечера (multiple spaces)", "в  7  вечера", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input, now, cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatalf("want time, got nil")
			}
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

// TestParse_PeriodClockTime_InvalidCombinations verifies that idiomatic-only
// combinations are rejected (e.g. "в 13 вечера", "в 7 ночи").
func TestParse_PeriodClockTime_InvalidCombinations(t *testing.T) {
	now := testNow(t)
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	invalid := []string{
		"в 13 вечера",
		"в 4 вечера",
		"в 7 ночи",
		"в 13 утра",
		"в 6 дня",
	}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input, now, cfg)
			if err == nil {
				t.Errorf("expected error for %q, got nil", input)
			}
		})
	}
}

// TestParse_DayPeriodClockTime covers "сегодня|завтра|послезавтра в <H>[:mm][ часов] <period>".
func TestParse_DayPeriodClockTime(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"сегодня в 7 вечера", "сегодня в 7 вечера", time.Date(2026, 4, 15, 19, 0, 0, 0, loc)},
		{"завтра в 7 вечера", "завтра в 7 вечера", time.Date(2026, 4, 16, 19, 0, 0, 0, loc)},
		{"завтра в 8 утра", "завтра в 8 утра", time.Date(2026, 4, 16, 8, 0, 0, 0, loc)},
		{"послезавтра в 3 дня", "послезавтра в 3 дня", time.Date(2026, 4, 17, 15, 0, 0, 0, loc)},
		{"завтра в 9:30 утра", "завтра в 9:30 утра", time.Date(2026, 4, 16, 9, 30, 0, 0, loc)},
		{"завтра в 7 часов вечера", "завтра в 7 часов вечера", time.Date(2026, 4, 16, 19, 0, 0, 0, loc)},
		{"сегодня в 1 ночи (в прошлом — но день фиксирован)", "сегодня в 1 ночи", time.Date(2026, 4, 15, 1, 0, 0, 0, loc)},
		// Mixed case
		{"ЗАВТРА В 7 ВЕЧЕРА", "ЗАВТРА В 7 ВЕЧЕРА", time.Date(2026, 4, 16, 19, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input, now, cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatalf("want time, got nil")
			}
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

// TestParse_WeekdayPeriodClockTime covers "в <weekday> в <H>[:mm][ часов] <period>".
func TestParse_WeekdayPeriodClockTime(t *testing.T) {
	// testNow = Wed 2026-04-15 14:00 Moscow; calendar:
	// Thu 2026-04-16, Fri 2026-04-17, Mon 2026-04-20, next Mon 2026-04-27, next Sun 2026-04-26.
	now := testNow(t)
	loc := now.Location()
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"в пятницу в 7 вечера", "в пятницу в 7 вечера", time.Date(2026, 4, 17, 19, 0, 0, 0, loc)},
		{"в понедельник в 8 утра", "в понедельник в 8 утра", time.Date(2026, 4, 20, 8, 0, 0, 0, loc)},
		{"в субботу в 3 дня", "в субботу в 3 дня", time.Date(2026, 4, 18, 15, 0, 0, 0, loc)},
		{"в следующий понедельник в 8 утра", "в следующий понедельник в 8 утра", time.Date(2026, 4, 27, 8, 0, 0, 0, loc)},
		{"в следующую пятницу в 9 вечера", "в следующую пятницу в 9 вечера", time.Date(2026, 4, 24, 21, 0, 0, 0, loc)},
		{"в следующее воскресенье в 11 утра", "в следующее воскресенье в 11 утра", time.Date(2026, 4, 26, 11, 0, 0, 0, loc)},
		{"к пятнице в 7 вечера", "к пятнице в 7 вечера", time.Date(2026, 4, 17, 19, 0, 0, 0, loc)},
		{"в четверг в 9:15 утра", "в четверг в 9:15 утра", time.Date(2026, 4, 16, 9, 15, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input, now, cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatalf("want time, got nil")
			}
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

// TestParse_NoonMidnight covers "в полдень", "в полночь", "к полудню".
func TestParse_NoonMidnight(t *testing.T) {
	now := testNow(t) // Wed 2026-04-15 14:00 Moscow
	loc := now.Location()
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// noon already passed today (now=14:00) → tomorrow
		{"в полдень → tomorrow noon", "в полдень", time.Date(2026, 4, 16, 12, 0, 0, 0, loc)},
		{"к полудню → tomorrow noon", "к полудню", time.Date(2026, 4, 16, 12, 0, 0, 0, loc)},
		// midnight (00:00 today already past) → tomorrow 00:00
		{"в полночь → tomorrow 00:00", "в полночь", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input, now, cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatalf("want time, got nil")
			}
			if !result.Equal(tt.want) {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

// TestParse_NoonMidnight_TodayWhenFuture verifies that "в полдень" returns
// today when noon is still ahead.
func TestParse_NoonMidnight_TodayWhenFuture(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	morning := time.Date(2026, 4, 15, 9, 0, 0, 0, loc)
	got, err := Parse("в полдень", morning, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 4, 15, 12, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("want %v, got %v", want, *got)
	}
}
