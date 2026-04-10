package i18n

import (
	"embed"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales
var localesFS embed.FS

// NewBundle creates an i18n bundle pre-loaded with Russian and English locale files.
// The defaultLang parameter sets the bundle's default language tag.
func NewBundle(defaultLang string) (*goI18n.Bundle, error) {
	tag, err := language.Parse(defaultLang)
	if err != nil {
		tag = language.Russian
	}

	bundle := goI18n.NewBundle(tag)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	for _, locale := range []string{"ru", "en"} {
		data, err := localesFS.ReadFile(fmt.Sprintf("locales/%s.json", locale))
		if err != nil {
			return nil, fmt.Errorf("reading locale %s: %w", locale, err)
		}
		if _, err := bundle.ParseMessageFileBytes(data, locale+".json"); err != nil {
			return nil, fmt.Errorf("parsing locale %s: %w", locale, err)
		}
	}

	return bundle, nil
}
