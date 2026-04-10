package dateparse

import (
	"testing"
	"time"
)

func TestEnglishDateLanguage_RelativeAmount(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"in half an hour", "in half an hour", now.Add(30 * time.Minute)},
		{"in 30 minutes", "in 30 minutes", now.Add(30 * time.Minute)},
		{"in an hour", "in an hour", now.Add(time.Hour)},
		{"in 2 hours", "in 2 hours", now.Add(2 * time.Hour)},
		{"in 1 day", "in 1 day", now.AddDate(0, 0, 1)},
		{"in 3 days", "in 3 days", now.AddDate(0, 0, 3)},
		{"in 1 week", "in 1 week", now.AddDate(0, 0, 7)},
		{"in 2 weeks", "in 2 weeks", now.AddDate(0, 0, 14)},
		{"in 1 month", "in 1 month", now.AddDate(0, 1, 0)},
		{"in 3 months", "in 3 months", now.AddDate(0, 3, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Unix() != tt.want.Unix() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_DayTime(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"today", "today", time.Date(2026, 4, 15, 0, 0, 0, 0, loc)},
		{"tomorrow", "tomorrow", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
		{"day after tomorrow", "day after tomorrow", time.Date(2026, 4, 17, 0, 0, 0, 0, loc)},
		{"this morning", "this morning", time.Date(2026, 4, 15, 11, 0, 0, 0, loc)},
		{"this afternoon", "this afternoon", time.Date(2026, 4, 15, 14, 0, 0, 0, loc)},
		{"this evening", "this evening", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"tonight", "tonight", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"tomorrow morning", "tomorrow morning", time.Date(2026, 4, 16, 11, 0, 0, 0, loc)},
		{"tomorrow afternoon", "tomorrow afternoon", time.Date(2026, 4, 16, 14, 0, 0, 0, loc)},
		{"tomorrow evening", "tomorrow evening", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Minute() != tt.want.Minute() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_Weekday(t *testing.T) {
	// now is Wed 2026-04-15
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"on Wednesday (today)", "on Wednesday", time.Date(2026, 4, 15, 0, 0, 0, 0, loc)},
		{"on Thursday", "on Thursday", time.Date(2026, 4, 16, 0, 0, 0, 0, loc)},
		{"by Friday", "by Friday", time.Date(2026, 4, 17, 0, 0, 0, 0, loc)},
		{"next Monday", "next Monday", time.Date(2026, 4, 27, 0, 0, 0, 0, loc)},
		{"next Friday", "next Friday", time.Date(2026, 4, 24, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_WeekBoundary(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"end of week", "end of week", time.Date(2026, 4, 17, 23, 59, 59, 0, loc)},
		{"end of month", "end of month", time.Date(2026, 4, 30, 23, 59, 59, 0, loc)},
		{"next week", "next week", time.Date(2026, 4, 20, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Second() != tt.want.Second() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_ClockTime(t *testing.T) {
	// now is 14:00; times > 14:00 are today, times <= 14:00 are tomorrow
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"at 15:00 (today)", "at 15:00", time.Date(2026, 4, 15, 15, 0, 0, 0, loc)},
		{"at 20 (today)", "at 20", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"at 10 (tomorrow)", "at 10", time.Date(2026, 4, 16, 10, 0, 0, 0, loc)},
		{"at 14:30 (today)", "at 14:30", time.Date(2026, 4, 15, 14, 30, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Minute() != tt.want.Minute() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_Weekend(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"this weekend", "this weekend", time.Date(2026, 4, 18, 0, 0, 0, 0, loc)},
		{"before the weekend", "before the weekend", time.Date(2026, 4, 17, 23, 59, 59, 0, loc)},
		{"after the weekend", "after the weekend", time.Date(2026, 4, 20, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() || got.Second() != tt.want.Second() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_ExplicitDate(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"May 5", "May 5", time.Date(2026, 5, 5, 0, 0, 0, 0, loc)},
		{"5 May", "5 May", time.Date(2026, 5, 5, 0, 0, 0, 0, loc)},
		{"April 15 (past -> next year)", "April 15", time.Date(2027, 4, 15, 0, 0, 0, 0, loc)},
		{"by the 20th", "by the 20th", time.Date(2026, 4, 20, 23, 59, 59, 0, loc)},
		{"15.04.2026", "15.04.2026", time.Date(2026, 4, 15, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_NoMatch(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	_, ok := lang.Parse("завтра", now, cfg)
	if ok {
		t.Error("English language should not match 'завтра'")
	}

	_, ok = lang.Parse("через час", now, cfg)
	if ok {
		t.Error("English language should not match 'через час'")
	}
}
