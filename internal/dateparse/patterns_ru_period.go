package dateparse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// applyDayPeriod applies a Russian time-of-day period (утра/дня/вечера/ночи)
// to a 12-hour numeric hour and returns the corresponding 24-hour value.
// Reports false when the (hour, period) combination is not idiomatic Russian.
func applyDayPeriod(hour int, period string) (int, bool) {
	switch period {
	case "утра":
		switch {
		case hour == 12:
			return 0, true
		case hour >= 1 && hour <= 11:
			return hour, true
		}
	case "дня":
		switch {
		case hour == 12:
			return 12, true
		case hour >= 1 && hour <= 5:
			return hour + 12, true
		}
	case "вечера":
		switch {
		case hour == 12:
			return 12, true
		case hour >= 5 && hour <= 11:
			return hour + 12, true
		}
	case "ночи":
		switch {
		case hour == 12:
			return 0, true
		case hour >= 1 && hour <= 4:
			return hour, true
		}
	}
	return 0, false
}

// periodClockBody matches a "<HH>[:MM][ час|часа|часов] <period>" tail.
// Capture groups: 1 = hour, 2 = optional minute, 3 = period.
var periodClockBody = `(\d{1,2})(?::(\d{2}))?(?:\s+час(?:а|ов)?)?\s+(утра|дня|вечера|ночи)`

var (
	rePeriodClockTime        = regexp.MustCompile(`^(?:в|к)\s+` + periodClockBody + `$`)
	reDayPeriodClockTime     = regexp.MustCompile(`^(сегодня|завтра|послезавтра)\s+в\s+` + periodClockBody + `$`)
	reWeekdayPeriodClockTail = regexp.MustCompile(`\s+в\s+` + periodClockBody + `$`)
)

// parsePeriodHourMinute resolves a (hour, minute) pair from a numeric hour,
// optional numeric minute string, and a period word.
func parsePeriodHourMinute(rawHour, rawMinute, period string) (int, int, bool) {
	h, err := strconv.Atoi(rawHour)
	if err != nil {
		return 0, 0, false
	}
	hour, ok := applyDayPeriod(h, period)
	if !ok {
		return 0, 0, false
	}
	minute := 0
	if rawMinute != "" {
		minute, err = strconv.Atoi(rawMinute)
		if err != nil || minute < 0 || minute > 59 {
			return 0, 0, false
		}
	}
	return hour, minute, true
}

// matchPeriodClockTime parses standalone period clock-time phrases such as
// "в 7 вечера", "к 8 утра", "в 3 часа дня". When the resolved instant is not
// after now, it rolls to the next day.
func matchPeriodClockTime(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := normaliseSpaces(strings.ToLower(strings.TrimSpace(s)))

	matches := rePeriodClockTime.FindStringSubmatch(lower)
	if matches == nil {
		return nil, false
	}
	hour, minute, ok := parsePeriodHourMinute(matches[1], matches[2], matches[3])
	if !ok {
		return nil, false
	}

	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return &target, true
}

// matchDayPeriodClockTime parses phrases such as "сегодня в 7 вечера",
// "завтра в 8 утра", "послезавтра в 3 часа дня".
func matchDayPeriodClockTime(s string, now time.Time, cfg Config) (*time.Time, bool) {
	lower := normaliseSpaces(strings.ToLower(strings.TrimSpace(s)))

	matches := reDayPeriodClockTime.FindStringSubmatch(lower)
	if matches == nil {
		return nil, false
	}

	dayOffset, ok := map[string]int{
		"сегодня":     0,
		"завтра":      1,
		"послезавтра": 2,
	}[matches[1]]
	if !ok {
		return nil, false
	}
	dayOffset = shiftRelativeDayOffset(dayOffset, now, cfg)

	hour, minute, ok := parsePeriodHourMinute(matches[2], matches[3], matches[4])
	if !ok {
		return nil, false
	}

	t := time.Date(now.Year(), now.Month(), now.Day()+dayOffset, hour, minute, 0, 0, now.Location())
	return &t, true
}

// matchWeekdayPeriodClockTime parses phrases such as "в пятницу в 7 вечера",
// "в следующий понедельник в 8 утра", "к четвергу в 3 часа дня".
func matchWeekdayPeriodClockTime(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := normaliseSpaces(strings.ToLower(strings.TrimSpace(s)))

	tail := reWeekdayPeriodClockTail.FindStringSubmatch(lower)
	if tail == nil {
		return nil, false
	}
	hour, minute, ok := parsePeriodHourMinute(tail[1], tail[2], tail[3])
	if !ok {
		return nil, false
	}
	dayPart := lower[:len(lower)-len(tail[0])]

	type weekdayForms struct {
		vForm   string
		kForm   string
		weekday time.Weekday
	}
	weekdays := []weekdayForms{
		{"воскресенье", "воскресенью", time.Sunday},
		{"понедельник", "понедельнику", time.Monday},
		{"вторник", "вторнику", time.Tuesday},
		{"среду", "среде", time.Wednesday},
		{"четверг", "четвергу", time.Thursday},
		{"пятницу", "пятнице", time.Friday},
		{"субботу", "субботе", time.Saturday},
	}

	for _, wd := range weekdays {
		if dayPart == "в "+wd.vForm {
			target := findNearestWeekday(now, wd.weekday)
			t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
			return &t, true
		}
		switch wd.weekday {
		case time.Monday, time.Tuesday, time.Thursday:
			if dayPart == "в следующий "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday).AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
				return &t, true
			}
		case time.Wednesday, time.Friday, time.Saturday:
			if dayPart == "в следующую "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday).AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
				return &t, true
			}
		case time.Sunday:
			if dayPart == "в следующее "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday).AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
				return &t, true
			}
		}
		if dayPart == "к "+wd.kForm {
			target := findNearestWeekday(now, wd.weekday)
			t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
			return &t, true
		}
	}
	return nil, false
}

// matchNoonMidnight parses "в полдень" → 12:00 and "в полночь" → 00:00.
// Both roll to the next day if the resolved instant is not after now.
func matchNoonMidnight(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := normaliseSpaces(strings.ToLower(strings.TrimSpace(s)))

	var hour int
	switch lower {
	case "в полдень", "к полудню":
		hour = 12
	case "в полночь":
		hour = 0
	default:
		return nil, false
	}

	target := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return &target, true
}

// normaliseSpaces collapses runs of inner whitespace into single spaces.
func normaliseSpaces(s string) string {
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}
