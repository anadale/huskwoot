package dateparse

import (
	"testing"
	"time"
)

// Fixed time for testing: Wed 2026-04-15 14:00 Europe/Moscow
func testNow(t *testing.T) time.Time {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("failed to load Europe/Moscow: %v", err)
	}
	return time.Date(2026, 4, 15, 14, 0, 0, 0, loc)
}

// Helper to create a time in the given location
func testTime(t *testing.T, year, month, day, hour, min, sec int, loc *time.Location) time.Time {
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, loc)
}

func TestParse_EmptyAndNull(t *testing.T) {
	now := testNow(t)
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  *time.Time
	}{
		{"empty string", "", nil},
		{"whitespace only", "   ", nil},
		{"null", "null", nil},
		{"null with spaces", "  null  ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input, now, cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.want {
				t.Errorf("want %v, got %v", tt.want, result)
			}
		})
	}
}

func TestParse_ISOFormats(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			"RFC3339 with timezone",
			"2026-04-15T15:30:45+03:00",
			time.Date(2026, 4, 15, 15, 30, 45, 0, time.FixedZone("", 3*3600)),
		},
		{
			"ISO 8601 without timezone",
			"2026-04-15T15:30:45",
			testTime(t, 2026, 4, 15, 15, 30, 45, loc),
		},
		{
			"ISO with space instead of T, no timezone",
			"2026-04-15 15:30:45",
			testTime(t, 2026, 4, 15, 15, 30, 45, loc),
		},
		{
			"date only",
			"2026-04-15",
			testTime(t, 2026, 4, 15, 0, 0, 0, loc),
		},
		{
			"different date",
			"2025-12-31",
			testTime(t, 2025, 12, 31, 0, 0, 0, loc),
		},
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

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_NoTimezone_UsesNowLocation(t *testing.T) {
	// Test in Moscow time
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("failed to load Europe/Moscow: %v", err)
	}
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, loc)
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	result, err := Parse("2026-04-15T15:30:45", now, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatalf("want time, got nil")
	}

	// Verify the location matches now.Location()
	if result.Location() != loc {
		t.Errorf("want location %v, got %v", loc, result.Location())
	}
}

func TestParse_UnrecognizedFormat_ReturnsError(t *testing.T) {
	now := testNow(t)
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	result, err := Parse("invalid date format!!!", now, cfg)
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if result != nil {
		t.Errorf("want nil, got %v", result)
	}
}

func TestParse_ExactPhrases(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Days
		{
			"завтра",
			"завтра",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc), // April 16 0:00
		},
		{
			"сегодня",
			"сегодня",
			testTime(t, 2026, 4, 15, 0, 0, 0, loc), // April 15 0:00
		},
		{
			"послезавтра",
			"послезавтра",
			testTime(t, 2026, 4, 17, 0, 0, 0, loc), // April 17 0:00
		},

		// Time of day
		{
			"вечером",
			"вечером",
			testTime(t, 2026, 4, 15, 20, 0, 0, loc), // April 15 20:00
		},
		{
			"сегодня вечером",
			"сегодня вечером",
			testTime(t, 2026, 4, 15, 20, 0, 0, loc),
		},
		{
			"к вечеру",
			"к вечеру",
			testTime(t, 2026, 4, 15, 20, 0, 0, loc),
		},
		{
			"к утру",
			"к утру",
			testTime(t, 2026, 4, 16, 11, 0, 0, loc), // tomorrow at 11:00
		},

		// Lunch
		{
			"к обеду",
			"к обеду",
			testTime(t, 2026, 4, 15, 12, 0, 0, loc),
		},
		{
			"в обед",
			"в обед",
			testTime(t, 2026, 4, 15, 12, 0, 0, loc),
		},
		{
			"до обеда",
			"до обеда",
			testTime(t, 2026, 4, 15, 11, 0, 0, loc),
		},
		{
			"после обеда",
			"после обеда",
			testTime(t, 2026, 4, 15, 14, 0, 0, loc),
		},

		// Relative time expressions
		{
			"через полчаса",
			"через полчаса",
			now.Add(30 * time.Minute),
		},
		{
			"через 30 минут",
			"через 30 минут",
			now.Add(30 * time.Minute),
		},
		{
			"через час",
			"через час",
			now.Add(time.Hour),
		},

		// Case-insensitive
		{
			"ЗАВТРА (uppercase)",
			"ЗАВТРА",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc),
		},
		{
			"Сегодня вечером (mixed case)",
			"Сегодня вечером",
			testTime(t, 2026, 4, 15, 20, 0, 0, loc),
		},

		// With whitespace
		{
			"  завтра  (with spaces)",
			"  завтра  ",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc),
		},
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

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_RelativeAmount(t *testing.T) {
	now := testNow(t) // Wed 2026-04-15 14:00 Europe/Moscow
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Minutes — all forms
		{
			"через 15 минут",
			"через 15 минут",
			now.Add(15 * time.Minute),
		},
		{
			"через 5 минуты",
			"через 5 минуты",
			now.Add(5 * time.Minute),
		},
		{
			"через 1 минуту",
			"через 1 минуту",
			now.Add(1 * time.Minute),
		},

		// Hours — all forms
		{
			"через 2 часов",
			"через 2 часов",
			now.Add(2 * time.Hour),
		},
		{
			"через 5 часов",
			"через 5 часов",
			now.Add(5 * time.Hour),
		},
		{
			"через 1 час",
			"через 1 час",
			now.Add(time.Hour),
		},
		{
			"через 2 часа",
			"через 2 часа",
			now.Add(2 * time.Hour),
		},

		// Days — all forms
		{
			"через 1 день",
			"через 1 день",
			now.AddDate(0, 0, 1),
		},
		{
			"через 2 дня",
			"через 2 дня",
			now.AddDate(0, 0, 2),
		},
		{
			"через 5 дней",
			"через 5 дней",
			now.AddDate(0, 0, 5),
		},

		// Weeks — new
		{
			"через 1 неделю",
			"через 1 неделю",
			now.AddDate(0, 0, 7),
		},
		{
			"через 2 недели",
			"через 2 недели",
			now.AddDate(0, 0, 14),
		},
		{
			"через 3 недель",
			"через 3 недель",
			now.AddDate(0, 0, 21),
		},

		// Months — new
		{
			"через 1 месяц",
			"через 1 месяц",
			now.AddDate(0, 1, 0),
		},
		{
			"через 2 месяца",
			"через 2 месяца",
			now.AddDate(0, 2, 0),
		},
		{
			"через 5 месяцев",
			"через 5 месяцев",
			now.AddDate(0, 5, 0),
		},

		// Case insensitive
		{
			"ЧЕРЕЗ 3 ДНЕЙ (uppercase)",
			"ЧЕРЕЗ 3 ДНЕЙ",
			now.AddDate(0, 0, 3),
		},

		// With extra spaces
		{
			"через  2  дня (multiple spaces)",
			"через  2  дня",
			now.AddDate(0, 0, 2),
		},
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

			// Compare time to microsecond precision
			if result.Unix() != tt.want.Unix() {
				t.Errorf("want %v, got %v (diff %d sec)", tt.want, *result, result.Unix()-tt.want.Unix())
			}
		})
	}
}

func TestParse_DayTime(t *testing.T) {
	now := testNow(t) // Wed 2026-04-15 14:00 Europe/Moscow
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Today
		{
			"сегодня утром",
			"сегодня утром",
			testTime(t, 2026, 4, 15, 11, 0, 0, loc),
		},
		{
			"сегодня днём",
			"сегодня днём",
			testTime(t, 2026, 4, 15, 14, 0, 0, loc),
		},
		{
			"сегодня вечером",
			"сегодня вечером",
			testTime(t, 2026, 4, 15, 20, 0, 0, loc),
		},
		{
			"сегодня ночью",
			"сегодня ночью",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc), // next day 0:00
		},

		// Tomorrow
		{
			"завтра утром",
			"завтра утром",
			testTime(t, 2026, 4, 16, 11, 0, 0, loc),
		},
		{
			"завтра днём",
			"завтра днём",
			testTime(t, 2026, 4, 16, 14, 0, 0, loc),
		},
		{
			"завтра вечером",
			"завтра вечером",
			testTime(t, 2026, 4, 16, 20, 0, 0, loc),
		},
		{
			"завтра ночью",
			"завтра ночью",
			testTime(t, 2026, 4, 17, 0, 0, 0, loc), // day after tomorrow 0:00
		},

		// Day after tomorrow
		{
			"послезавтра утром",
			"послезавтра утром",
			testTime(t, 2026, 4, 17, 11, 0, 0, loc),
		},
		{
			"послезавтра днём",
			"послезавтра днём",
			testTime(t, 2026, 4, 17, 14, 0, 0, loc),
		},
		{
			"послезавтра вечером",
			"послезавтра вечером",
			testTime(t, 2026, 4, 17, 20, 0, 0, loc),
		},
		{
			"послезавтра ночью",
			"послезавтра ночью",
			testTime(t, 2026, 4, 18, 0, 0, 0, loc), // two days later 0:00
		},

		// Combinations with time-of-day phrases ("by noon", "by evening", "by morning", "after noon")
		{
			"сегодня к обеду",
			"сегодня к обеду",
			testTime(t, 2026, 4, 15, 12, 0, 0, loc),
		},
		{
			"завтра к обеду",
			"завтра к обеду",
			testTime(t, 2026, 4, 16, 12, 0, 0, loc),
		},
		{
			"послезавтра к обеду",
			"послезавтра к обеду",
			testTime(t, 2026, 4, 17, 12, 0, 0, loc),
		},
		{
			"завтра к вечеру",
			"завтра к вечеру",
			testTime(t, 2026, 4, 16, 20, 0, 0, loc),
		},
		{
			"послезавтра к вечеру",
			"послезавтра к вечеру",
			testTime(t, 2026, 4, 17, 20, 0, 0, loc),
		},
		{
			"завтра к утру",
			"завтра к утру",
			testTime(t, 2026, 4, 16, 11, 0, 0, loc),
		},
		{
			"послезавтра к утру",
			"послезавтра к утру",
			testTime(t, 2026, 4, 17, 11, 0, 0, loc),
		},
		{
			"завтра после обеда",
			"завтра после обеда",
			testTime(t, 2026, 4, 16, 14, 0, 0, loc),
		},
		{
			"послезавтра после обеда",
			"послезавтра после обеда",
			testTime(t, 2026, 4, 17, 14, 0, 0, loc),
		},

		// Case insensitive
		{
			"ЗАВТРА УТРОМ (uppercase)",
			"ЗАВТРА УТРОМ",
			testTime(t, 2026, 4, 16, 11, 0, 0, loc),
		},
		{
			"Послезавтра после обеда (смешанный регистр)",
			"Послезавтра после обеда",
			testTime(t, 2026, 4, 17, 14, 0, 0, loc),
		},

		// With extra spaces
		{
			"сегодня  днём (multiple spaces)",
			"сегодня  днём",
			testTime(t, 2026, 4, 15, 14, 0, 0, loc),
		},
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

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_Weekday(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	// Calendar:
	// - Tue 2026-04-14 (yesterday)
	// - Wed 2026-04-15 (today)
	// - Thu 2026-04-16
	// - Fri 2026-04-17
	// - Sat 2026-04-18
	// - Sun 2026-04-19
	// - Mon 2026-04-20
	// - Tue 2026-04-21
	// - Wed 2026-04-22 (next week)

	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// "on <weekday>" — nearest such day (0:00); if today — today
		{
			"в среду (сегодня среда)",
			"в среду",
			testTime(t, 2026, 4, 15, 0, 0, 0, loc), // today
		},
		{
			"в четверг",
			"в четверг",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc), // Tomorrow
		},
		{
			"в пятницу",
			"в пятницу",
			testTime(t, 2026, 4, 17, 0, 0, 0, loc),
		},
		{
			"в субботу",
			"в субботу",
			testTime(t, 2026, 4, 18, 0, 0, 0, loc),
		},
		{
			"в воскресенье",
			"в воскресенье",
			testTime(t, 2026, 4, 19, 0, 0, 0, loc),
		},
		{
			"в понедельник",
			"в понедельник",
			testTime(t, 2026, 4, 20, 0, 0, 0, loc),
		},
		{
			"в вторник",
			"в вторник",
			testTime(t, 2026, 4, 21, 0, 0, 0, loc),
		},

		// "by <weekday>" (synonym for "on <weekday>")
		{
			"к среде (сегодня среда)",
			"к среде",
			testTime(t, 2026, 4, 15, 0, 0, 0, loc), // today
		},
		{
			"к четвергу",
			"к четвергу",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc),
		},
		{
			"к пятнице",
			"к пятнице",
			testTime(t, 2026, 4, 17, 0, 0, 0, loc),
		},
		{
			"к субботе",
			"к субботе",
			testTime(t, 2026, 4, 18, 0, 0, 0, loc),
		},
		{
			"к воскресенью",
			"к воскресенью",
			testTime(t, 2026, 4, 19, 0, 0, 0, loc),
		},
		{
			"к понедельнику",
			"к понедельнику",
			testTime(t, 2026, 4, 20, 0, 0, 0, loc),
		},
		{
			"к вторнику",
			"к вторнику",
			testTime(t, 2026, 4, 21, 0, 0, 0, loc),
		},

		// Case insensitive
		{
			"В ПЯТНИЦУ (uppercase)",
			"В ПЯТНИЦУ",
			testTime(t, 2026, 4, 17, 0, 0, 0, loc),
		},

		// With spaces
		{
			"  в пятницу  (with spaces)",
			"  в пятницу  ",
			testTime(t, 2026, 4, 17, 0, 0, 0, loc),
		},
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

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_WeekdayNext(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	// Calendar:
	// - Wed 2026-04-15 (today)
	// - Thu 2026-04-16
	// - Fri 2026-04-17
	// - Sat 2026-04-18
	// - Sun 2026-04-19
	// - Mon 2026-04-20
	// - Tue 2026-04-21
	// - Wed 2026-04-22 (next week)

	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// "next <weekday>" — day + 7 days
		{
			"в следующий четверг",
			"в следующий четверг",
			testTime(t, 2026, 4, 23, 0, 0, 0, loc), // +7 days
		},
		{
			"в следующий понедельник",
			"в следующий понедельник",
			testTime(t, 2026, 4, 27, 0, 0, 0, loc), // +7 days
		},
		{
			"в следующий вторник",
			"в следующий вторник",
			testTime(t, 2026, 4, 28, 0, 0, 0, loc), // +7 days
		},

		// "next <weekday>" (feminine form)
		{
			"в следующую среду",
			"в следующую среду",
			testTime(t, 2026, 4, 22, 0, 0, 0, loc), // +7 days
		},
		{
			"в следующую пятницу",
			"в следующую пятницу",
			testTime(t, 2026, 4, 24, 0, 0, 0, loc), // +7 days
		},
		{
			"в следующую субботу",
			"в следующую субботу",
			testTime(t, 2026, 4, 25, 0, 0, 0, loc), // +7 days
		},

		// "next <weekday>" (neuter form)
		{
			"в следующее воскресенье",
			"в следующее воскресенье",
			testTime(t, 2026, 4, 26, 0, 0, 0, loc), // +7 days
		},

		// Case insensitive
		{
			"В СЛЕДУЮЩИЙ ЧЕТВЕРГ (uppercase)",
			"В СЛЕДУЮЩИЙ ЧЕТВЕРГ",
			testTime(t, 2026, 4, 23, 0, 0, 0, loc),
		},

		// With spaces
		{
			"  в следующую пятницу  (with spaces)",
			"  в следующую пятницу  ",
			testTime(t, 2026, 4, 24, 0, 0, 0, loc),
		},
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

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_DayAtTime(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Today
		{
			"сегодня в 10",
			"сегодня в 10",
			testTime(t, 2026, 4, 15, 10, 0, 0, loc),
		},
		{
			"сегодня в 18:30",
			"сегодня в 18:30",
			testTime(t, 2026, 4, 15, 18, 30, 0, loc),
		},
		{
			"сегодня в 0",
			"сегодня в 0",
			testTime(t, 2026, 4, 15, 0, 0, 0, loc),
		},

		// Tomorrow
		{
			"завтра в 10",
			"завтра в 10",
			testTime(t, 2026, 4, 16, 10, 0, 0, loc),
		},
		{
			"завтра в 9:15",
			"завтра в 9:15",
			testTime(t, 2026, 4, 16, 9, 15, 0, loc),
		},
		{
			"завтра в 23:59",
			"завтра в 23:59",
			testTime(t, 2026, 4, 16, 23, 59, 0, loc),
		},

		// Day after tomorrow
		{
			"послезавтра в 14",
			"послезавтра в 14",
			testTime(t, 2026, 4, 17, 14, 0, 0, loc),
		},
		{
			"послезавтра в 8:45",
			"послезавтра в 8:45",
			testTime(t, 2026, 4, 17, 8, 45, 0, loc),
		},

		// Case insensitive
		{
			"ЗАВТРА В 10 (uppercase)",
			"ЗАВТРА В 10",
			testTime(t, 2026, 4, 16, 10, 0, 0, loc),
		},

		// With spaces
		{
			"  сегодня в 15  (with spaces)",
			"  сегодня в 15  ",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc),
		},
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
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_WeekdayAtTime(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Standard form "on <weekday> at HH"
		{
			"в среду в 15",
			"в среду в 15",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc), // today (Wednesday)
		},
		{
			"в четверг в 10",
			"в четверг в 10",
			testTime(t, 2026, 4, 16, 10, 0, 0, loc),
		},
		{
			"в пятницу в 14:30",
			"в пятницу в 14:30",
			testTime(t, 2026, 4, 17, 14, 30, 0, loc),
		},
		{
			"в субботу в 9:45",
			"в субботу в 9:45",
			testTime(t, 2026, 4, 18, 9, 45, 0, loc),
		},
		{
			"в воскресенье в 20",
			"в воскресенье в 20",
			testTime(t, 2026, 4, 19, 20, 0, 0, loc),
		},
		{
			"в понедельник в 8",
			"в понедельник в 8",
			testTime(t, 2026, 4, 20, 8, 0, 0, loc),
		},

		// "next <weekday> at HH" (next week)
		{
			"в следующий четверг в 14",
			"в следующий четверг в 14",
			testTime(t, 2026, 4, 23, 14, 0, 0, loc), // +7 days
		},
		{
			"в следующий понедельник в 10:30",
			"в следующий понедельник в 10:30",
			testTime(t, 2026, 4, 27, 10, 30, 0, loc),
		},

		// "next <weekday> at HH" (feminine form)
		{
			"в следующую среду в 18",
			"в следующую среду в 18",
			testTime(t, 2026, 4, 22, 18, 0, 0, loc),
		},
		{
			"в следующую пятницу в 12:00",
			"в следующую пятницу в 12:00",
			testTime(t, 2026, 4, 24, 12, 0, 0, loc),
		},

		// "next <weekday> at HH" (neuter form)
		{
			"в следующее воскресенье в 11",
			"в следующее воскресенье в 11",
			testTime(t, 2026, 4, 26, 11, 0, 0, loc),
		},

		// "by <weekday> at HH" form
		{
			"к среде в 15",
			"к среде в 15",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc),
		},
		{
			"к пятнице в 14:30",
			"к пятнице в 14:30",
			testTime(t, 2026, 4, 17, 14, 30, 0, loc),
		},

		// Case insensitive
		{
			"В ПЯТНИЦУ В 14:30 (uppercase)",
			"В ПЯТНИЦУ В 14:30",
			testTime(t, 2026, 4, 17, 14, 30, 0, loc),
		},

		// With spaces
		{
			"  в пятницу в 15  (with spaces)",
			"  в пятницу в 15  ",
			testTime(t, 2026, 4, 17, 15, 0, 0, loc),
		},
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
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_ClockTime(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	// Current hour is 14, so times before 14 should be today, times from 14 onwards should be tomorrow
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Times before current time (14:00) → tomorrow
		{
			"в 10 (before current time 14:00)",
			"в 10",
			testTime(t, 2026, 4, 16, 10, 0, 0, loc), // tomorrow (already past)
		},
		{
			"в 13 (before current time 14:00)",
			"в 13",
			testTime(t, 2026, 4, 16, 13, 0, 0, loc), // tomorrow (already past)
		},
		{
			"в 13:30 (before current time 14:00)",
			"в 13:30",
			testTime(t, 2026, 4, 16, 13, 30, 0, loc), // tomorrow
		},

		// Time exactly at current time → tomorrow
		{
			"в 14 (exactly current time 14:00)",
			"в 14",
			testTime(t, 2026, 4, 16, 14, 0, 0, loc), // tomorrow (next 14:00)
		},
		{
			"в 14:00 (exactly current time 14:00)",
			"в 14:00",
			testTime(t, 2026, 4, 16, 14, 0, 0, loc), // tomorrow (next 14:00)
		},

		// Times after current time (14:00) → today
		{
			"в 15 (after current time 14:00)",
			"в 15",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc), // today
		},
		{
			"в 20 (after current time 14:00)",
			"в 20",
			testTime(t, 2026, 4, 15, 20, 0, 0, loc), // today
		},
		{
			"в 20:45 (after current time 14:00)",
			"в 20:45",
			testTime(t, 2026, 4, 15, 20, 45, 0, loc), // today
		},
		{
			"в 23 (after current time 14:00)",
			"в 23",
			testTime(t, 2026, 4, 15, 23, 0, 0, loc), // today
		},

		// Edge case: midnight
		{
			"в 0 (midnight, before current time 14:00)",
			"в 0",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc), // tomorrow
		},
		{
			"в 0:00 (midnight, before current time 14:00)",
			"в 0:00",
			testTime(t, 2026, 4, 16, 0, 0, 0, loc), // tomorrow
		},

		// "by HH" (synonym for "at HH")
		{
			"к 15 (after current time)",
			"к 15",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc), // today
		},
		{
			"к 10 (before current time)",
			"к 10",
			testTime(t, 2026, 4, 16, 10, 0, 0, loc), // tomorrow
		},
		{
			"к 14:30 (after current time)",
			"к 14:30",
			testTime(t, 2026, 4, 15, 14, 30, 0, loc), // today
		},

		// Case insensitive
		{
			"В 15 (uppercase)",
			"В 15",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc),
		},

		// With spaces
		{
			"  в 15  (with spaces)",
			"  в 15  ",
			testTime(t, 2026, 4, 15, 15, 0, 0, loc),
		},
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
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_WeekBoundaries(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	// Calendar:
	// - Wed 2026-04-15 (today)
	// - Thu 2026-04-16
	// - Fri 2026-04-17 (Friday)
	// - Sat 2026-04-18
	// - Sun 2026-04-19
	// - Mon 2026-04-20 (Monday)
	// - Tue 2026-04-21
	// - Wed 2026-04-22 (next week Wed)

	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			"до конца недели",
			"до конца недели",
			testTime(t, 2026, 4, 17, 23, 59, 59, loc), // Friday 23:59:59
		},
		{
			"до конца месяца",
			"до конца месяца",
			testTime(t, 2026, 4, 30, 23, 59, 59, loc), // April 30 (last day of April) 23:59:59
		},
		{
			"на следующей неделе",
			"на следующей неделе",
			testTime(t, 2026, 4, 20, 0, 0, 0, loc), // Monday 0:00
		},
		{
			"к следующей неделе",
			"к следующей неделе",
			testTime(t, 2026, 4, 20, 0, 0, 0, loc), // Monday 0:00
		},

		// Case insensitive
		{
			"ДО КОНЦА НЕДЕЛИ (uppercase)",
			"ДО КОНЦА НЕДЕЛИ",
			testTime(t, 2026, 4, 17, 23, 59, 59, loc),
		},

		// With spaces
		{
			"  до конца недели  (with spaces)",
			"  до конца недели  ",
			testTime(t, 2026, 4, 17, 23, 59, 59, loc),
		},
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

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_Weekend(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	// Calendar (default Weekdays Mon..Fri working):
	// - Wed 2026-04-15 (today) - working
	// - Thu 2026-04-16 - working
	// - Fri 2026-04-17 - working (last working day)
	// - Sat 2026-04-18 - weekend
	// - Sun 2026-04-19 - weekend
	// - Mon 2026-04-20 - working

	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		cfg   Config
		want  time.Time
	}{
		// Default (Mon..Fri working, Sat..Sun weekend)
		{
			"в выходные (дефолт)",
			"в выходные",
			cfg,
			testTime(t, 2026, 4, 18, 0, 0, 0, loc), // Saturday 0:00
		},
		{
			"к выходным (дефолт)",
			"к выходным",
			cfg,
			testTime(t, 2026, 4, 18, 0, 0, 0, loc), // Saturday 0:00
		},
		{
			"до выходных (дефолт)",
			"до выходных",
			cfg,
			testTime(t, 2026, 4, 17, 23, 59, 59, loc), // Friday 23:59:59
		},
		{
			"после выходных (дефолт)",
			"после выходных",
			cfg,
			testTime(t, 2026, 4, 20, 0, 0, 0, loc), // Monday 0:00
		},

		// Custom Weekdays: Mon..Thu working, Fri..Sun weekend
		{
			"в выходные (Mon..Thu рабочие)",
			"в выходные",
			Config{
				TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
				Weekdays:  []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
			},
			testTime(t, 2026, 4, 17, 0, 0, 0, loc), // Friday (first weekend day) 0:00
		},
		{
			"до выходных (Mon..Thu рабочие)",
			"до выходных",
			Config{
				TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
				Weekdays:  []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
			},
			testTime(t, 2026, 4, 16, 23, 59, 59, loc), // Thursday (last working day) 23:59:59
		},
		{
			"после выходных (Mon..Thu рабочие)",
			"после выходных",
			Config{
				TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
				Weekdays:  []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
			},
			testTime(t, 2026, 4, 20, 0, 0, 0, loc), // Monday (first working day after weekend) 0:00
		},

		// Case insensitive
		{
			"В ВЫХОДНЫЕ (uppercase)",
			"В ВЫХОДНЫЕ",
			cfg,
			testTime(t, 2026, 4, 18, 0, 0, 0, loc),
		},

		// With spaces
		{
			"  до выходных  (with spaces)",
			"  до выходных  ",
			cfg,
			testTime(t, 2026, 4, 17, 23, 59, 59, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input, now, tt.cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatalf("want time, got nil")
			}

			// Compare year, month, day, hour, minute, second
			if result.Year() != tt.want.Year() ||
				result.Month() != tt.want.Month() ||
				result.Day() != tt.want.Day() ||
				result.Hour() != tt.want.Hour() ||
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_WeekendEdgeCases(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			"до выходных в пятницу → сегодня 23:59:59",
			time.Date(2026, 4, 17, 10, 0, 0, 0, loc), // Friday
			time.Date(2026, 4, 17, 23, 59, 59, 0, loc),
		},
		{
			"до выходных в субботу → следующая пятница",
			time.Date(2026, 4, 18, 10, 0, 0, 0, loc), // Saturday
			time.Date(2026, 4, 24, 23, 59, 59, 0, loc),
		},
		{
			"до выходных в воскресенье → следующая пятница",
			time.Date(2026, 4, 19, 10, 0, 0, 0, loc), // Sunday
			time.Date(2026, 4, 24, 23, 59, 59, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse("до выходных", tt.now, cfg)
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

func TestParse_ExplicitDate(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Russian month names in the genitive case (non-past dates)
		{
			"5 мая (не прошедшая дата)",
			"5 мая",
			testTime(t, 2026, 5, 5, 0, 0, 0, loc),
		},
		{
			"15 апреля (прошедшая дата в этом месяце -> следующий год)",
			"15 апреля",
			testTime(t, 2027, 4, 15, 0, 0, 0, loc), // April 15 already passed in 2026
		},
		{
			"30 июня (не прошедшая дата)",
			"30 июня",
			testTime(t, 2026, 6, 30, 0, 0, 0, loc),
		},
		{
			"1 января (не прошедшая дата)",
			"1 января",
			testTime(t, 2027, 1, 1, 0, 0, 0, loc), // 2026-01-01 already passed
		},

		// All 12 months
		{
			"10 февраля",
			"10 февраля",
			testTime(t, 2027, 2, 10, 0, 0, 0, loc),
		},
		{
			"20 марта",
			"20 марта",
			testTime(t, 2027, 3, 20, 0, 0, 0, loc),
		},
		{
			"5 июля",
			"5 июля",
			testTime(t, 2026, 7, 5, 0, 0, 0, loc),
		},
		{
			"15 августа",
			"15 августа",
			testTime(t, 2026, 8, 15, 0, 0, 0, loc),
		},
		{
			"1 сентября",
			"1 сентября",
			testTime(t, 2026, 9, 1, 0, 0, 0, loc),
		},
		{
			"31 октября",
			"31 октября",
			testTime(t, 2026, 10, 31, 0, 0, 0, loc),
		},
		{
			"25 ноября",
			"25 ноября",
			testTime(t, 2026, 11, 25, 0, 0, 0, loc),
		},
		{
			"25 декабря",
			"25 декабря",
			testTime(t, 2026, 12, 25, 0, 0, 0, loc),
		},

		// Format dd.mm
		{
			"15.04 (прошедшая дата -> следующий год)",
			"15.04",
			testTime(t, 2027, 4, 15, 0, 0, 0, loc),
		},
		{
			"5.05 (не прошедшая дата)",
			"5.05",
			testTime(t, 2026, 5, 5, 0, 0, 0, loc),
		},
		{
			"31.12 (будущая дата)",
			"31.12",
			testTime(t, 2026, 12, 31, 0, 0, 0, loc),
		},
		{
			"01.01",
			"01.01",
			testTime(t, 2027, 1, 1, 0, 0, 0, loc),
		},

		// Format dd.mm.yyyy (explicit year)
		{
			"15.04.2026",
			"15.04.2026",
			testTime(t, 2026, 4, 15, 0, 0, 0, loc),
		},
		{
			"1.1.2025",
			"1.1.2025",
			testTime(t, 2025, 1, 1, 0, 0, 0, loc),
		},
		{
			"31.12.2030",
			"31.12.2030",
			testTime(t, 2030, 12, 31, 0, 0, 0, loc),
		},

		// Case insensitive for Russian text
		{
			"5 Мая (with capital)",
			"5 Мая",
			testTime(t, 2026, 5, 5, 0, 0, 0, loc),
		},

		// With whitespace
		{
			"  15 апреля  (with spaces)",
			"  15 апреля  ",
			testTime(t, 2027, 4, 15, 0, 0, 0, loc),
		},
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
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}

func TestParse_DayOfMonth_SkipsMonthWithoutDay(t *testing.T) {
	// Edge case: now is Jan 31 after 23:59:59 — "until the 31st" must resolve to March 31,
	// not February (which has no 31st — Go normalises it to March 3).
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("failed to load Europe/Moscow: %v", err)
	}
	now := time.Date(2026, 1, 31, 23, 59, 59, 500_000_000, loc)
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}

	result, err := Parse("до 31-го", now, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatalf("want time, got nil")
	}
	want := time.Date(2026, 3, 31, 23, 59, 59, 0, loc)
	if !result.Equal(want) {
		t.Errorf("want %v, got %v", want, *result)
	}
}

func TestDateparser_WithNewDateLanguage(t *testing.T) {
	now := testNow(t)
	loc := now.Location()
	cfg := Config{TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20}}
	dp := New(cfg, NewDateLanguage("ru"))

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"завтра via Dateparser", "завтра", testTime(t, 2026, 4, 16, 0, 0, 0, loc)},
		{"ISO date via Dateparser", "2026-05-01", testTime(t, 2026, 5, 1, 0, 0, 0, loc)},
		{"через час via Dateparser", "через час", now.Add(time.Hour)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := dp.Parse(tt.input, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == nil {
				t.Fatalf("expected time, got nil")
			}
			if result.Unix() != tt.want.Unix() {
				t.Errorf("got %v, want %v", *result, tt.want)
			}
		})
	}

	// Empty and null
	result, err := dp.Parse("", now)
	if err != nil || result != nil {
		t.Errorf("empty string: got (%v, %v), want (nil, nil)", result, err)
	}
	result, err = dp.Parse("null", now)
	if err != nil || result != nil {
		t.Errorf("null: got (%v, %v), want (nil, nil)", result, err)
	}

	// Unknown format
	_, err = dp.Parse("not a date", now)
	if err == nil {
		t.Error("expected error for unknown format, got nil")
	}
}

func TestParse_DayOfMonth(t *testing.T) {
	// Fixed time: Wed 2026-04-15 14:00 Europe/Moscow
	now := testNow(t)
	loc := now.Location()
	cfg := Config{
		TimeOfDay: TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
	}

	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		// Mid-month (date still in the future this month)
		{
			"до 20-го (текущий месяц, дата в будущем)",
			"до 20-го",
			testTime(t, 2026, 4, 20, 23, 59, 59, loc), // April 20 23:59:59
		},
		{
			"до 25-го",
			"до 25-го",
			testTime(t, 2026, 4, 25, 23, 59, 59, loc),
		},

		// End of month (date already passed → next month)
		{
			"до 10-го (уже прошло в текущем месяце -> май)",
			"до 10-го",
			testTime(t, 2026, 5, 10, 23, 59, 59, loc),
		},
		{
			"до 5-го (уже прошло -> май)",
			"до 5-го",
			testTime(t, 2026, 5, 5, 23, 59, 59, loc),
		},

		// Edge cases
		{
			"до 1-го (уже прошло -> май)",
			"до 1-го",
			testTime(t, 2026, 5, 1, 23, 59, 59, loc),
		},
		{
			"до 31-го (май, есть 31 число)",
			"до 31-го",
			testTime(t, 2026, 5, 31, 23, 59, 59, loc),
		},

		// Case insensitive
		{
			"ДО 20-ГО (uppercase)",
			"ДО 20-ГО",
			testTime(t, 2026, 4, 20, 23, 59, 59, loc),
		},

		// With whitespace
		{
			"  до 20-го  (with spaces)",
			"  до 20-го  ",
			testTime(t, 2026, 4, 20, 23, 59, 59, loc),
		},
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
				result.Minute() != tt.want.Minute() ||
				result.Second() != tt.want.Second() {
				t.Errorf("want %v, got %v", tt.want, *result)
			}
		})
	}
}
