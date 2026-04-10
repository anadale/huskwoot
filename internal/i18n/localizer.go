package i18n

import (
	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"
)

// NewLocalizer creates a localizer for the given language from a bundle.
func NewLocalizer(bundle *goI18n.Bundle, lang string) *goI18n.Localizer {
	return goI18n.NewLocalizer(bundle, lang)
}

// Translate returns the localized string for msgID. When count is provided,
// it is used for plural form selection and exposed as {{.Count}} in the template.
// Returns msgID on error.
func Translate(loc *goI18n.Localizer, msgID string, data any, count ...int) string {
	cfg := &goI18n.LocalizeConfig{
		MessageID:    msgID,
		TemplateData: data,
	}
	if len(count) > 0 {
		cfg.PluralCount = count[0]
		if data == nil {
			cfg.TemplateData = map[string]any{"Count": count[0]}
		}
	}
	s, err := loc.Localize(cfg)
	if err != nil {
		return msgID
	}
	return s
}
