package dateparse

import (
	"fmt"
	"strings"
	"time"
)

// TimeOfDay defines the hours of day for time-of-day phrase parsing.
type TimeOfDay struct {
	Morning   int
	Lunch     int
	Afternoon int
	Evening   int
}

// Config holds date-parsing parameters.
type Config struct {
	TimeOfDay TimeOfDay
	Weekdays  []time.Weekday // empty = Mon..Fri; others are weekends
}

// Dateparser parses deadline expressions using a language-aware module.
type Dateparser struct {
	cfg  Config
	lang DateLanguage
}

// New creates a Dateparser for the given config and language.
func New(cfg Config, lang DateLanguage) *Dateparser {
	return &Dateparser{cfg: cfg, lang: lang}
}

// Parse parses string s relative to the point in time now.
// Returns (*time.Time, nil) on success, (nil, nil) for an empty/null string,
// (nil, error) for an unrecognised non-empty input.
func (d *Dateparser) Parse(s string, now time.Time) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return nil, nil
	}

	// Language-specific patterns first
	if t, ok := d.lang.Parse(s, now, d.cfg); ok {
		return &t, nil
	}

	// ISO / standard formats (language-neutral)
	if result, ok := matchISOFormats(s, now); ok {
		return result, nil
	}

	return nil, fmt.Errorf("unrecognised date format: %q", s)
}

// Parse parses string s relative to the point in time now and configuration cfg.
// Returns (*time.Time, nil) on success, (nil, nil) for an empty/null string,
// (nil, error) for an unrecognised non-empty input.
// Uses Russian language patterns.
func Parse(s string, now time.Time, cfg Config) (*time.Time, error) {
	return New(cfg, NewDateLanguage("ru")).Parse(s, now)
}

// matchISOFormats checks and parses ISO and standard formats.
func matchISOFormats(s string, now time.Time) (*time.Time, bool) {
	formats := []string{
		time.RFC3339,          // 2006-01-02T15:04:05Z07:00
		"2006-01-02T15:04:05", // no TZ
		"2006-01-02 15:04:05", // space instead of T
		"2006-01-02",          // date only
	}

	for _, format := range formats {
		t, err := time.ParseInLocation(format, s, now.Location())
		if err == nil {
			return &t, true
		}
	}

	return nil, false
}
