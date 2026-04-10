package dateparse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type russianDateLanguage struct{}

func (r *russianDateLanguage) Parse(expr string, now time.Time, cfg Config) (time.Time, bool) {
	if t, ok := matchRelativeAmount(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchDayTime(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchDayAtTime(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchWeekdayAtTime(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchWeekday(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchClockTime(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchWeekBoundary(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchWeekend(expr, now, cfg); ok {
		return *t, true
	}
	if t, ok := matchExplicitDate(expr, now, cfg); ok {
		return *t, true
	}
	return time.Time{}, false
}

// matchRelativeAmount parses relative-time phrases ("in N minutes/hours/days/weeks/months").
func matchRelativeAmount(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	switch lower {
	case "через полчаса", "через 30 минут":
		t := now.Add(30 * time.Minute)
		return &t, true
	case "через час":
		t := now.Add(time.Hour)
		return &t, true
	}

	re := regexp.MustCompile(`^через\s+(\d+)\s+(минут|минуты|минуту|часов|часа|час|дней|дня|день|недель|недели|неделю|месяцев|месяца|месяц)$`)
	matches := re.FindStringSubmatch(lower)
	if matches == nil {
		return nil, false
	}

	num, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil, false
	}

	var result time.Time
	switch matches[2] {
	case "минут", "минуты", "минуту":
		result = now.Add(time.Duration(num) * time.Minute)
	case "часов", "часа", "час":
		result = now.Add(time.Duration(num) * time.Hour)
	case "дней", "дня", "день":
		result = now.AddDate(0, 0, num)
	case "недель", "недели", "неделю":
		result = now.AddDate(0, 0, num*7)
	case "месяцев", "месяца", "месяц":
		result = now.AddDate(0, num, 0)
	default:
		return nil, false
	}

	return &result, true
}

// matchDayTime parses day and/or time-of-day phrases.
func matchDayTime(s string, now time.Time, cfg Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))
	for strings.Contains(lower, "  ") {
		lower = strings.ReplaceAll(lower, "  ", " ")
	}

	const (
		markerMorning = iota
		markerLunch
		markerBeforeLunch
		markerAfternoon
		markerEvening
		markerNight
	)

	type dayEntry struct {
		phrase string
		offset int
	}
	type timeEntry struct {
		phrase string
		marker int
	}

	days := []dayEntry{
		{"", 0},
		{"сегодня", 0},
		{"завтра", 1},
		{"послезавтра", 2},
	}
	times := []timeEntry{
		{"утром", markerMorning},
		{"к утру", markerMorning},
		{"днём", markerAfternoon},
		{"после обеда", markerAfternoon},
		{"вечером", markerEvening},
		{"к вечеру", markerEvening},
		{"ночью", markerNight},
		{"к обеду", markerLunch},
		{"в обед", markerLunch},
		{"до обеда", markerBeforeLunch},
	}

	for _, d := range days {
		if d.phrase != "" && lower == d.phrase {
			t := time.Date(now.Year(), now.Month(), now.Day()+d.offset, 0, 0, 0, 0, now.Location())
			return &t, true
		}
	}

	for _, d := range days {
		for _, tp := range times {
			var full string
			if d.phrase == "" {
				full = tp.phrase
			} else {
				full = d.phrase + " " + tp.phrase
			}
			if lower != full {
				continue
			}

			if tp.phrase == "к утру" && d.phrase == "" && now.Hour() >= cfg.TimeOfDay.Morning {
				t := time.Date(now.Year(), now.Month(), now.Day()+1, cfg.TimeOfDay.Morning, 0, 0, 0, now.Location())
				return &t, true
			}

			targetDay := time.Date(now.Year(), now.Month(), now.Day()+d.offset, 0, 0, 0, 0, now.Location())

			switch tp.marker {
			case markerMorning:
				t := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), cfg.TimeOfDay.Morning, 0, 0, 0, targetDay.Location())
				return &t, true
			case markerLunch:
				t := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), cfg.TimeOfDay.Lunch, 0, 0, 0, targetDay.Location())
				return &t, true
			case markerBeforeLunch:
				hour := cfg.TimeOfDay.Lunch - 1
				if hour < 0 {
					hour = 0
				}
				t := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), hour, 0, 0, 0, targetDay.Location())
				return &t, true
			case markerAfternoon:
				t := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), cfg.TimeOfDay.Afternoon, 0, 0, 0, targetDay.Location())
				return &t, true
			case markerEvening:
				t := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), cfg.TimeOfDay.Evening, 0, 0, 0, targetDay.Location())
				return &t, true
			case markerNight:
				nextDay := targetDay.AddDate(0, 0, 1)
				t := time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), 0, 0, 0, 0, nextDay.Location())
				return &t, true
			}
		}
	}

	return nil, false
}

// matchWeekday parses weekday reference phrases ("on Monday", "next Monday", "by Friday").
func matchWeekday(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

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
		if lower == "в "+wd.vForm {
			target := findNearestWeekday(now, wd.weekday)
			t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
			return &t, true
		}

		switch wd.weekday {
		case time.Monday, time.Tuesday, time.Thursday:
			if lower == "в следующий "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday)
				target = target.AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
				return &t, true
			}
		case time.Wednesday, time.Friday, time.Saturday:
			if lower == "в следующую "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday)
				target = target.AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
				return &t, true
			}
		case time.Sunday:
			if lower == "в следующее "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday)
				target = target.AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
				return &t, true
			}
		}

		if lower == "к "+wd.kForm {
			target := findNearestWeekday(now, wd.weekday)
			t := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
			return &t, true
		}
	}

	return nil, false
}

// findNearestWeekday returns the nearest date with the given weekday.
func findNearestWeekday(now time.Time, target time.Weekday) time.Time {
	currentWeekday := now.Weekday()
	var daysToAdd int
	if target == currentWeekday {
		daysToAdd = 0
	} else if target > currentWeekday {
		daysToAdd = int(target - currentWeekday)
	} else {
		daysToAdd = 7 - int(currentWeekday) + int(target)
	}
	return now.AddDate(0, 0, daysToAdd)
}

// matchDayAtTime parses combinations of a day and an exact time.
func matchDayAtTime(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	dayPhrases := map[string]int{
		"сегодня":     0,
		"завтра":      1,
		"послезавтра": 2,
	}

	re := regexp.MustCompile(`^(сегодня|завтра|послезавтра)\s+в\s+(\d{1,2})(?::(\d{2}))?$`)
	matches := re.FindStringSubmatch(lower)
	if matches == nil {
		return nil, false
	}

	dayOffset, ok := dayPhrases[matches[1]]
	if !ok {
		return nil, false
	}

	hour, err := strconv.Atoi(matches[2])
	if err != nil || hour < 0 || hour > 23 {
		return nil, false
	}

	minute := 0
	if matches[3] != "" {
		minute, err = strconv.Atoi(matches[3])
		if err != nil || minute < 0 || minute > 59 {
			return nil, false
		}
	}

	t := time.Date(now.Year(), now.Month(), now.Day()+dayOffset, hour, minute, 0, 0, now.Location())
	return &t, true
}

// matchWeekdayAtTime parses combinations of a weekday and an exact time.
func matchWeekdayAtTime(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	reTime := regexp.MustCompile(`\s+в\s+(\d{1,2})(?::(\d{2}))?$`)
	timeMatches := reTime.FindStringSubmatch(lower)
	if timeMatches == nil {
		return nil, false
	}

	hour, err := strconv.Atoi(timeMatches[1])
	if err != nil || hour < 0 || hour > 23 {
		return nil, false
	}

	minute := 0
	if timeMatches[2] != "" {
		minute, err = strconv.Atoi(timeMatches[2])
		if err != nil || minute < 0 || minute > 59 {
			return nil, false
		}
	}

	dayPart := lower[:len(lower)-len(timeMatches[0])]

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
				target := findNearestWeekday(now, wd.weekday)
				target = target.AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
				return &t, true
			}
		case time.Wednesday, time.Friday, time.Saturday:
			if dayPart == "в следующую "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday)
				target = target.AddDate(0, 0, 7)
				t := time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, now.Location())
				return &t, true
			}
		case time.Sunday:
			if dayPart == "в следующее "+wd.vForm {
				target := findNearestWeekday(now, wd.weekday)
				target = target.AddDate(0, 0, 7)
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

// matchClockTime parses an absolute clock time.
func matchClockTime(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	re := regexp.MustCompile(`^(в|к)\s+(\d{1,2})(?::(\d{2}))?$`)
	matches := re.FindStringSubmatch(lower)
	if matches == nil {
		return nil, false
	}

	hour, err := strconv.Atoi(matches[2])
	if err != nil || hour < 0 || hour > 23 {
		return nil, false
	}

	minute := 0
	if matches[3] != "" {
		minute, err = strconv.Atoi(matches[3])
		if err != nil || minute < 0 || minute > 59 {
			return nil, false
		}
	}

	targetTime := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !targetTime.After(now) {
		targetTime = targetTime.AddDate(0, 0, 1)
	}

	return &targetTime, true
}

// isWeekend reports whether the given day is a weekend according to the configuration.
func isWeekend(day time.Weekday, cfg Config) bool {
	if len(cfg.Weekdays) > 0 {
		for _, workday := range cfg.Weekdays {
			if day == workday {
				return false
			}
		}
		return true
	}
	return day == time.Saturday || day == time.Sunday
}

// matchWeekBoundary parses week/month boundary phrases ("by end of week", "by end of month", "next week").
func matchWeekBoundary(s string, now time.Time, cfg Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	switch lower {
	case "до конца недели":
		target := findNearestWeekday(now, time.Friday)
		result := time.Date(target.Year(), target.Month(), target.Day(), 23, 59, 59, 0, now.Location())
		return &result, true

	case "до конца месяца":
		firstDayNextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
		lastDay := firstDayNextMonth.AddDate(0, 0, -1)
		result := time.Date(lastDay.Year(), lastDay.Month(), lastDay.Day(), 23, 59, 59, 0, now.Location())
		return &result, true

	case "на следующей неделе", "к следующей неделе":
		target := findNearestWeekday(now, time.Monday)
		if !target.After(now) {
			target = target.AddDate(0, 0, 7)
		}
		result := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, now.Location())
		return &result, true
	}

	return nil, false
}

// matchWeekend parses weekend phrases.
func matchWeekend(s string, now time.Time, cfg Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	switch lower {
	case "в выходные", "к выходным":
		checkDay := now.AddDate(0, 0, 1)
		for i := 0; i < 7; i++ {
			if isWeekend(checkDay.Weekday(), cfg) {
				result := time.Date(checkDay.Year(), checkDay.Month(), checkDay.Day(), 0, 0, 0, 0, now.Location())
				return &result, true
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		return nil, false

	case "до выходных":
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
			return &result, true
		}
		return nil, false

	case "после выходных":
		checkDay := now.AddDate(0, 0, 1)
		foundWeekend := false
		var weekendDay time.Time
		for i := 0; i < 7; i++ {
			if isWeekend(checkDay.Weekday(), cfg) {
				foundWeekend = true
				weekendDay = checkDay
				break
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		if !foundWeekend {
			return nil, false
		}
		checkDay = weekendDay.AddDate(0, 0, 1)
		for i := 0; i < 7; i++ {
			if !isWeekend(checkDay.Weekday(), cfg) {
				result := time.Date(checkDay.Year(), checkDay.Month(), checkDay.Day(), 0, 0, 0, 0, now.Location())
				return &result, true
			}
			checkDay = checkDay.AddDate(0, 0, 1)
		}
		return nil, false
	}

	return nil, false
}

// matchExplicitDate parses explicit dates (Russian month names, dd.mm, dd.mm.yyyy, ordinal day).
func matchExplicitDate(s string, now time.Time, _ Config) (*time.Time, bool) {
	lower := strings.ToLower(strings.TrimSpace(s))

	monthMap := map[string]time.Month{
		"января":   time.January,
		"февраля":  time.February,
		"марта":    time.March,
		"апреля":   time.April,
		"мая":      time.May,
		"июня":     time.June,
		"июля":     time.July,
		"августа":  time.August,
		"сентября": time.September,
		"октября":  time.October,
		"ноября":   time.November,
		"декабря":  time.December,
	}

	re := regexp.MustCompile(`^(\d{1,2})\s+(января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря)$`)
	matches := re.FindStringSubmatch(lower)
	if matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return nil, false
		}
		month, ok := monthMap[matches[2]]
		if !ok {
			return nil, false
		}
		year := now.Year()
		target := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
		if target.Before(now) {
			target = time.Date(year+1, month, day, 0, 0, 0, 0, now.Location())
		}
		return &target, true
	}

	re = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})$`)
	matches = re.FindStringSubmatch(lower)
	if matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return nil, false
		}
		monthNum, err := strconv.Atoi(matches[2])
		if err != nil || monthNum < 1 || monthNum > 12 {
			return nil, false
		}
		month := time.Month(monthNum)
		year := now.Year()
		target := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
		if target.Before(now) {
			target = time.Date(year+1, month, day, 0, 0, 0, 0, now.Location())
		}
		return &target, true
	}

	re = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{4})$`)
	matches = re.FindStringSubmatch(lower)
	if matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return nil, false
		}
		monthNum, err := strconv.Atoi(matches[2])
		if err != nil || monthNum < 1 || monthNum > 12 {
			return nil, false
		}
		year, err := strconv.Atoi(matches[3])
		if err != nil {
			return nil, false
		}
		target := time.Date(year, time.Month(monthNum), day, 0, 0, 0, 0, now.Location())
		return &target, true
	}

	re = regexp.MustCompile(`^до\s+(\d{1,2})-го$`)
	matches = re.FindStringSubmatch(lower)
	if matches != nil {
		day, err := strconv.Atoi(matches[1])
		if err != nil || day < 1 || day > 31 {
			return nil, false
		}
		return matchDayOfMonth(day, now)
	}

	return nil, false
}

// matchDayOfMonth parses the ordinal day-of-month format.
func matchDayOfMonth(day int, now time.Time) (*time.Time, bool) {
	year := now.Year()
	month := now.Month()

	target := time.Date(year, month, day, 23, 59, 59, 0, now.Location())

	if target.Day() != day || !target.After(now) {
		for i := 1; i <= 12; i++ {
			nextMonthStart := time.Date(year, month+time.Month(i), 1, 0, 0, 0, 0, now.Location())
			candidate := time.Date(nextMonthStart.Year(), nextMonthStart.Month(), day, 23, 59, 59, 0, now.Location())
			if candidate.Day() == day {
				target = candidate
				break
			}
		}
	}

	return &target, true
}
