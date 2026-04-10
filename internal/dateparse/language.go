package dateparse

import "time"

// DateLanguage parses language-specific temporal expressions.
type DateLanguage interface {
	Parse(expr string, now time.Time, cfg Config) (time.Time, bool)
}

// NewDateLanguage returns a DateLanguage for the given language code.
// Falls back to Russian for unknown languages.
func NewDateLanguage(lang string) DateLanguage {
	if lang == "en" {
		return &englishDateLanguage{}
	}
	return &russianDateLanguage{}
}
