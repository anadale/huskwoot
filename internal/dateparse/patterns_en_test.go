package dateparse

import (
	"testing"
	"time"
)

func TestEnglishDateLanguage_RelativeAmount(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"today", "today", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"tomorrow", "tomorrow", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"day after tomorrow", "day after tomorrow", time.Date(2026, 4, 17, 20, 0, 0, 0, loc)},
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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"on Wednesday (today)", "on Wednesday", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"on Thursday", "on Thursday", time.Date(2026, 4, 16, 20, 0, 0, 0, loc)},
		{"by Friday", "by Friday", time.Date(2026, 4, 17, 20, 0, 0, 0, loc)},
		{"next Monday", "next Monday", time.Date(2026, 4, 27, 20, 0, 0, 0, loc)},
		{"next Friday", "next Friday", time.Date(2026, 4, 24, 20, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_WeekBoundary(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"end of week", "end of week", time.Date(2026, 4, 17, 23, 59, 59, 0, loc)},
		{"end of month", "end of month", time.Date(2026, 4, 30, 23, 59, 59, 0, loc)},
		{"next week", "next week", time.Date(2026, 4, 20, 20, 0, 0, 0, loc)},
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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"this weekend", "this weekend", time.Date(2026, 4, 18, 20, 0, 0, 0, loc)},
		{"before the weekend", "before the weekend", time.Date(2026, 4, 17, 23, 59, 59, 0, loc)},
		{"after the weekend", "after the weekend", time.Date(2026, 4, 20, 20, 0, 0, 0, loc)},
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
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"May 5", "May 5", time.Date(2026, 5, 5, 20, 0, 0, 0, loc)},
		{"5 May", "5 May", time.Date(2026, 5, 5, 20, 0, 0, 0, loc)},
		{"April 15 (today evening, still future)", "April 15", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"January 1 (past -> next year)", "January 1", time.Date(2027, 1, 1, 20, 0, 0, 0, loc)},
		{"by the 20th", "by the 20th", time.Date(2026, 4, 20, 23, 59, 59, 0, loc)},
		{"15.04.2026", "15.04.2026", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q, got none", tt.input)
			}
			if got.Year() != tt.want.Year() || got.Month() != tt.want.Month() || got.Day() != tt.want.Day() ||
				got.Hour() != tt.want.Hour() {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnglishDateLanguage_NoMatch(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20}}

	_, ok := lang.Parse("завтра", now, cfg)
	if ok {
		t.Error("English language should not match 'завтра'")
	}

	_, ok = lang.Parse("через час", now, cfg)
	if ok {
		t.Error("English language should not match 'через час'")
	}
}

func TestParse_DefaultTimeOfDay_EN(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	lang := NewDateLanguage("en")

	cases := []struct {
		defaultMarker string
		defaultHour   int
	}{
		{"morning", 11},
		{"lunch", 12},
		{"afternoon", 14},
		{"evening", 20},
	}

	for _, c := range cases {
		cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: c.defaultHour}}

		t.Run("tomorrow/"+c.defaultMarker, func(t *testing.T) {
			got, ok := lang.Parse("tomorrow", now, cfg)
			if !ok {
				t.Fatalf("expected match for 'tomorrow'")
			}
			want := time.Date(2026, 4, 16, c.defaultHour, 0, 0, 0, loc)
			if got.Hour() != want.Hour() || got.Day() != want.Day() {
				t.Errorf("got %v, want %v", got, want)
			}
		})

		t.Run("on monday/"+c.defaultMarker, func(t *testing.T) {
			got, ok := lang.Parse("on monday", now, cfg)
			if !ok {
				t.Fatalf("expected match for 'on monday'")
			}
			if got.Hour() != c.defaultHour {
				t.Errorf("got hour %d, want %d", got.Hour(), c.defaultHour)
			}
		})

		t.Run("this weekend/"+c.defaultMarker, func(t *testing.T) {
			got, ok := lang.Parse("this weekend", now, cfg)
			if !ok {
				t.Fatalf("expected match for 'this weekend'")
			}
			want := time.Date(2026, 4, 18, c.defaultHour, 0, 0, 0, loc)
			if got.Hour() != want.Hour() || got.Day() != want.Day() {
				t.Errorf("got %v, want %v", got, want)
			}
		})

		t.Run("after the weekend/"+c.defaultMarker, func(t *testing.T) {
			got, ok := lang.Parse("after the weekend", now, cfg)
			if !ok {
				t.Fatalf("expected match for 'after the weekend'")
			}
			want := time.Date(2026, 4, 20, c.defaultHour, 0, 0, 0, loc)
			if got.Hour() != want.Hour() || got.Day() != want.Day() {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestEnglishDateLanguage_NightCutoff(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("failed to load Europe/Moscow: %v", err)
	}
	lang := NewDateLanguage("en")
	cfgCutoff := Config{
		TimeOfDay:          TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20},
		NightCutoffMinutes: 300, // 05:00
	}
	cfgNoCutoff := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 20},
	}

	// now = Monday 2026-04-13 02:00 — before cutoff
	nowBefore := time.Date(2026, 4, 13, 2, 0, 0, 0, loc)
	// now = Monday 2026-04-13 04:59 — still before cutoff
	nowBefore459 := time.Date(2026, 4, 13, 4, 59, 0, 0, loc)
	// now = Monday 2026-04-13 05:00 — exactly at cutoff, no shift
	nowAt := time.Date(2026, 4, 13, 5, 0, 0, 0, loc)
	// now = Monday 2026-04-13 06:00 — after cutoff
	nowAfter := time.Date(2026, 4, 13, 6, 0, 0, 0, loc)

	tests := []struct {
		name  string
		now   time.Time
		cfg   Config
		input string
		want  time.Time
	}{
		// before cutoff, with cutoff enabled: tomorrow → same calendar day (Monday)
		{"tomorrow before cutoff", nowBefore, cfgCutoff, "tomorrow", time.Date(2026, 4, 13, 20, 0, 0, 0, loc)},
		// day after tomorrow → next calendar day (Tuesday)
		{"day after tomorrow before cutoff", nowBefore, cfgCutoff, "day after tomorrow", time.Date(2026, 4, 14, 20, 0, 0, 0, loc)},
		// today is always literal (offset=0, never shifted)
		{"today literal", nowBefore, cfgCutoff, "today", time.Date(2026, 4, 13, 20, 0, 0, 0, loc)},
		// tomorrow morning → same calendar day, morning hour
		{"tomorrow morning before cutoff", nowBefore, cfgCutoff, "tomorrow morning", time.Date(2026, 4, 13, 11, 0, 0, 0, loc)},
		// tomorrow at 14:00 → same calendar day 14:00
		{"tomorrow at 14:00 before cutoff", nowBefore, cfgCutoff, "tomorrow at 14:00", time.Date(2026, 4, 13, 14, 0, 0, 0, loc)},
		// day after tomorrow morning → Tuesday morning
		{"day after tomorrow morning before cutoff", nowBefore, cfgCutoff, "the day after tomorrow morning", time.Date(2026, 4, 14, 11, 0, 0, 0, loc)},
		// 04:59: still shifted
		{"tomorrow at 04:59", nowBefore459, cfgCutoff, "tomorrow", time.Date(2026, 4, 13, 20, 0, 0, 0, loc)},
		// exactly at cutoff: no shift
		{"tomorrow at 05:00 exact", nowAt, cfgCutoff, "tomorrow", time.Date(2026, 4, 14, 20, 0, 0, 0, loc)},
		// after cutoff: no shift
		{"tomorrow after cutoff", nowAfter, cfgCutoff, "tomorrow", time.Date(2026, 4, 14, 20, 0, 0, 0, loc)},
		// regression: cutoff=0 → no shift
		{"tomorrow no cutoff", nowBefore, cfgNoCutoff, "tomorrow", time.Date(2026, 4, 14, 20, 0, 0, 0, loc)},
		{"day after tomorrow no cutoff", nowBefore, cfgNoCutoff, "day after tomorrow", time.Date(2026, 4, 15, 20, 0, 0, 0, loc)},
		{"today no cutoff", nowBefore, cfgNoCutoff, "today", time.Date(2026, 4, 13, 20, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, tt.now, tt.cfg)
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

func TestEnglishDateLanguage_DeadlineTimesUnchanged(t *testing.T) {
	now := testNow(t)
	lang := NewDateLanguage("en")
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: 11}}

	tests := []struct {
		name  string
		input string
	}{
		{"end of week", "end of week"},
		{"by end of week", "by end of week"},
		{"end of month", "end of month"},
		{"before the weekend", "before the weekend"},
		{"by the 20th", "by the 20th"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lang.Parse(tt.input, now, cfg)
			if !ok {
				t.Fatalf("expected match for %q", tt.input)
			}
			if got.Hour() != 23 || got.Minute() != 59 || got.Second() != 59 {
				t.Errorf("%q: got %v, want 23:59:59", tt.input, got)
			}
		})
	}
}
