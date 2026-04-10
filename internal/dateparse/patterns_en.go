package dateparse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type englishDateLanguage struct{}

func (e *englishDateLanguage) Parse(expr string, now time.Time, cfg Config) (time.Time, bool) {
	if t, ok := matchEnRelativeAmount(expr, now); ok {
		return t, true
	}
	if t, ok := matchEnDayTime(expr, now, cfg); ok {
		return t, true
	}
	if t, ok := matchEnDayAtTime(expr, now); ok {
		return t, true
	}
	if t, ok := matchEnWeekdayAtTime(expr, now); ok {
		return t, true
	}
	if t, ok := matchEnWeekday(expr, now); ok {
		return t, true
	}
	if t, ok := matchEnClockTime(expr, now); ok {
		return t, true
	}
	if t, ok := matchEnWeekBoundary(expr, now, cfg); ok {
		return t, true
	}
	if t, ok := matchEnWeekend(expr, now, cfg); ok {
		return t, true
	}
	if t, ok := matchEnExplicitDate(expr, now); ok {
		return t, true
	}
	return time.Time{}, false
}

func matchEnRelativeAmount(s string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	switch lower {
	case "in half an hour", "in 30 minutes":
		return now.Add(30 * time.Minute), true
	case "in an hour":
		return now.Add(time.Hour), true
	}

	re := regexp.MustCompile(`^in\s+(\d+)\s+(minute|minutes|hour|hours|day|days|week|weeks|month|months)$`)
	matches := re.FindStringSubmatch(lower)
	if matches == nil {
		return time.Time{}, false
	}

	num, err := strconv.Atoi(matches[1])
	if err != nil {
		return time.Time{}, false
	}

	switch matches[2] {
	case "minute", "minutes":
		return now.Add(time.Duration(num) * time.Minute), true
	case "hour", "hours":
		return now.Add(time.Duration(num) * time.Hour), true
	case "day", "days":
		return now.AddDate(0, 0, num), true
	case "week", "weeks":
		return now.AddDate(0, 0, num*7), true
	case "month", "months":
		return now.AddDate(0, num, 0), true
	}

	return time.Time{}, false
}

func matchEnDayTime(s string, now time.Time, cfg Config) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))
	for strings.Contains(lower, "  ") {
		lower = strings.ReplaceAll(lower, "  ", " ")
	}

	type entry struct {
		phrase  string
		dayOff  int
		hour    int // -1 = day only (0:00)
	}

	entries := []entry{
		{"today", 0, -1},
		{"tomorrow", 1, -1},
		{"day after tomorrow", 2, -1},
		{"this morning", 0, cfg.TimeOfDay.Morning},
		{"this afternoon", 0, cfg.TimeOfDay.Afternoon},
		{"this evening", 0, cfg.TimeOfDay.Evening},
		{"tonight", 0, cfg.TimeOfDay.Evening},
		{"tomorrow morning", 1, cfg.TimeOfDay.Morning},
		{"tomorrow afternoon", 1, cfg.TimeOfDay.Afternoon},
		{"tomorrow evening", 1, cfg.TimeOfDay.Evening},
		{"tomorrow night", 1, cfg.TimeOfDay.Evening},
		{"the day after tomorrow morning", 2, cfg.TimeOfDay.Morning},
		{"the day after tomorrow afternoon", 2, cfg.TimeOfDay.Afternoon},
		{"the day after tomorrow evening", 2, cfg.TimeOfDay.Evening},
	}

	for _, e := range entries {
		if lower == e.phrase {
			hour := 0
			if e.hour >= 0 {
				hour = e.hour
			}
			t := time.Date(now.Year(), now.Month(), now.Day()+e.dayOff, hour, 0, 0, 0, now.Location())
			return t, true
		}
	}

	return time.Time{}, false
}

func matchEnDayAtTime(s string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	dayPhrases := map[string]int{
		"today":              0,
		"tomorrow":           1,
		"day after tomorrow": 2,
	}

	re := regexp.MustCompile(`^(today|tomorrow|day after tomorrow)\s+at\s+(\d{1,2})(?::(\d{2}))?$`)
	matches := re.FindStringSubmatch(lower)
	if matches == nil {
		return time.Time{}, false
	}

	dayOffset := dayPhrases[matches[1]]
	hour, err := strconv.Atoi(matches[2])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, false
	}

	minute := 0
	if matches[3] != "" {
		minute, err = strconv.Atoi(matches[3])
		if err != nil || minute < 0 || minute > 59 {
			return time.Time{}, false
		}
	}

	t := time.Date(now.Year(), now.Month(), now.Day()+dayOffset, hour, minute, 0, 0, now.Location())
	return t, true
}

var enWeekdayNames = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

func matchEnWeekday(s string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	for name, wd := range enWeekdayNames {
		if lower == "on "+name || lower == "by "+name {
			target := findNearestWeekday(now, wd)
			t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
			return t, true
		}
		if lower == "next "+name {
			target := findNearestWeekday(now, wd)
			target = target.AddDate(0, 0, 7)
			t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
			return t, true
		}
	}

	return time.Time{}, false
}

func matchEnWeekdayAtTime(s string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	reTime := regexp.MustCompile(`\s+at\s+(\d{1,2})(?::(\d{2}))?$`)
	timeMatches := reTime.FindStringSubmatch(lower)
	if timeMatches == nil {
		return time.Time{}, false
	}

	hour, err := strconv.Atoi(timeMatches[1])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, false
	}

	minute := 0
	if timeMatches[2] != "" {
		minute, err = strconv.Atoi(timeMatches[2])
		if err != nil || minute < 0 || minute > 59 {
			return time.Time{}, false
		}
	}

	dayPart := lower[:len(lower)-len(timeMatches[0])]

	for name, wd := range enWeekdayNames {
		if dayPart == "on "+name || dayPart == "by "+name {
			target := findNearestWeekday(now, wd)
			t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
			return t, true
		}
		if dayPart == "next "+name {
			target := findNearestWeekday(now, wd)
			target = target.AddDate(0, 0, 7)
			t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
			return t, true
		}
	}

	return time.Time{}, false
}

func matchEnClockTime(s string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	re := regexp.MustCompile(`^at\s+(\d{1,2})(?::(\d{2}))?$`)
	matches := re.FindStringSubmatch(lower)
	if matches == nil {
		return time.Time{}, false
	}

	hour, err := strconv.Atoi(matches[1])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, false
	}

	minute := 0
	if matches[2] != "" {
		minute, err = strconv.Atoi(matches[2])
		if err != nil || minute < 0 || minute > 59 {
			return time.Time{}, false
		}
	}

	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target, true
}

func matchEnWeekBoundary(s string, now time.Time, cfg Config) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	switch lower {
	case "end of week", "by end of week":
		target := findNearestWeekday(now, time.Friday)
		result := time.Date(target.Year(), target.Month(), target.Day(), 23, 59, 59, 0, now.Location())
		return result, true

	case "end of month", "by end of month":
		firstDayNextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
		lastDay := firstDayNextMonth.AddDate(0, 0, -1)
		result := time.Date(lastDay.Year(), lastDay.Month(), lastDay.Day(), 23, 59, 59, 0, now.Location())
		return result, true

	case "next week", "by next week":
		target := findNearestWeekday(now, time.Monday)
		if !target.After(now) {
			target = target.AddDate(0, 0, 7)
		}
		result := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
		return result, true
	}

	return time.Time{}, false
}

func matchEnWeekend(s string, now time.Time, cfg Config) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	switch lower {
	case "this weekend", "on the weekend", "by the weekend":
		checkDay := now.AddDate(0, 0, 1)
		for i := 0; i < 7; i++ {
			if isWeekend(checkDay.Weekday(), cfg) {
				result := time.Date(checkDay.Year(), checkDay.Month(), checkDay.Day(), 0, 0, 0, 0, now.Location())
				return result, true
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		return time.Time{}, false

	case "before the weekend":
		var lastWorkday time.Time
		checkDay := now
		for i := 0; i < 14; i++ {
			if isWeekend(checkDay.Weekday(), cfg) {
				if !lastWorkday.IsZero() {
					break
				}
			} else {
				lastWorkday = checkDay
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		if !lastWorkday.IsZero() {
			result := time.Date(lastWorkday.Year(), lastWorkday.Month(), lastWorkday.Day(), 23, 59, 59, 0, now.Location())
			return result, true
		}
		return time.Time{}, false

	case "after the weekend":
		checkDay := now.AddDate(0, 0, 1)
		var weekendDay time.Time
		for i := 0; i < 7; i++ {
			if isWeekend(checkDay.Weekday(), cfg) {
				weekendDay = checkDay
				break
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		if weekendDay.IsZero() {
			return time.Time{}, false
		}
		checkDay = weekendDay.AddDate(0, 0, 1)
		for i := 0; i < 7; i++ {
			if !isWeekend(checkDay.Weekday(), cfg) {
				result := time.Date(checkDay.Year(), checkDay.Month(), checkDay.Day(), 0, 0, 0, 0, now.Location())
				return result, true
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		return time.Time{}, false
	}

	return time.Time{}, false
}

var enMonthNames = map[string]time.Month{
	"january":   time.January,
	"february":  time.February,
	"march":     time.March,
	"april":     time.April,
	"may":       time.May,
	"june":      time.June,
	"july":      time.July,
	"august":    time.August,
	"september": time.September,
	"october":   time.October,
	"november":  time.November,
	"december":  time.December,
}

func matchEnExplicitDate(s string, now time.Time) (time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	monthList := `january|february|march|april|may|june|july|august|september|october|november|december`

	// "Month Day" e.g. "May 5"
	re := regexp.MustCompile(`^(` + monthList + `)\s+(\d{1,2})(?:st|nd|rd|th)?$`)
	if matches := re.FindStringSubmatch(lower); matches != nil {
		month := enMonthNames[matches[1]]
		day, err := strconv.Atoi(matches[2])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		return resolveEnDate(now, month, day)
	}

	// "Day Month" e.g. "5 May"
	re = regexp.MustCompile(`^(\d{1,2})(?:st|nd|rd|th)?\s+(` + monthList + `)$`)
	if matches := re.FindStringSubmatch(lower); matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		month := enMonthNames[matches[2]]
		return resolveEnDate(now, month, day)
	}

	// "Month Day Year" e.g. "May 5 2026"
	re = regexp.MustCompile(`^(` + monthList + `)\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(\d{4})$`)
	if matches := re.FindStringSubmatch(lower); matches != nil {
		month := enMonthNames[matches[1]]
		day, err := strconv.Atoi(matches[2])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		year, err := strconv.Atoi(matches[3])
		if err != nil {
			return time.Time{}, false
		}
		t := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
		return t, true
	}

	// dd.mm.yyyy
	re = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{4})$`)
	if matches := re.FindStringSubmatch(lower); matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		monthNum, err := strconv.Atoi(matches[2])
		if err != nil || monthNum < 1 || monthNum > 12 {
			return time.Time{}, false
		}
		year, err := strconv.Atoi(matches[3])
		if err != nil {
			return time.Time{}, false
		}
		t := time.Date(year, time.Month(monthNum), day, 0, 0, 0, 0, now.Location())
		return t, true
	}

	// "by the Nth" e.g. "by the 20th"
	re = regexp.MustCompile(`^by\s+the\s+(\d{1,2})(?:st|nd|rd|th)$`)
	if matches := re.FindStringSubmatch(lower); matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		t, ok := matchDayOfMonth(day, now)
		if !ok {
			return time.Time{}, false
		}
		return *t, true
	}

	return time.Time{}, false
}

func resolveEnDate(now time.Time, month time.Month, day int) (time.Time, bool) {
	year := now.Year()
	target := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	if target.Before(now) {
		target = time.Date(year+1, month, day, 0, 0, 0, 0, now.Location())
	}
	return target, true
}
