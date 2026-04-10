package dateparse

import (
	"testing"
	"time"
)

func TestRussianDateLanguage_RelativeAmount(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"завтра", "завтра", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
		{"сегодня", "сегодня", time.Date(2026, 4, 15, 0, 0, 0, 0, loc)},
		{"послезавтра", "послезавтра", time.Date(2026, 4, 17, 0, 0, 0, 0, loc)},
		{"завтра утром", "завтра утром", time.Date(2026, 4, 16, 11, 0, 0, 0, loc)},
		{"завтра вечером", "завтра вечером", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"сегодня вечером", "сегодня вечером", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"к утру", "к утру", time.Date(2026, 4, 16, 11, 0, 0, 0, loc)},
		{"к вечеру", "к вечеру", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"в среду (today)", "в среду", time.Date(2026, 4, 15, 0, 0, 0, 0, loc)},
		{"в четверг", "в четверг", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
		{"в пятницу", "в пятницу", time.Date(2026, 4, 17, 0, 0, 0, 0, loc)},
		{"к пятнице", "к пятнице", time.Date(2026, 4, 17, 0, 0, 0, 0, loc)},
		{"в следующий четверг", "в следующий четверг", time.Date(2026, 4, 23, 0, 0, 0, 0, loc)},
		{"в следующую пятницу", "в следующую пятницу", time.Date(2026, 4, 24, 0, 0, 0, 0, loc)},
		{"в следующее воскресенье", "в следующее воскресенье", time.Date(2026, 4, 26, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_WeekBoundary(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"до конца недели", "до конца недели", time.Date(2026, 4, 17, 23, 59, 59, 0, loc)},
		{"до конца месяца", "до конца месяца", time.Date(2026, 4, 30, 23, 59, 59, 0, loc)},
		{"на следующей неделе", "на следующей неделе", time.Date(2026, 4, 20, 0, 0, 0, 0, loc)},
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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"5 мая", "5 мая", time.Date(2026, 5, 5, 0, 0, 0, 0, loc)},
		{"15.04 (прошедшая -> следующий год)", "15.04", time.Date(2027, 4, 15, 0, 0, 0, 0, loc)},
		{"15.04.2026", "15.04.2026", time.Date(2026, 4, 15, 0, 0, 0, 0, loc)},
		{"до 20-го", "до 20-го", time.Date(2026, 4, 20, 23, 59, 59, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match, got none")
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRussianDateLanguage_NoMatch(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("ru")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	_, ok := lang.Parse("tomorrow", now, cfg)
	if ok {
		t.Error("Russian language should not match 'tomorrow'")
	}

	_, ok = lang.Parse("in 2 hours", now, cfg)
	if ok {
		t.Error("Russian language should not match 'in 2 hours'")
	}
}
