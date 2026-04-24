package usecase

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// aliasRe matches a normalised alias: starts and ends with a letter or digit,
// may contain hyphens in between. Length is checked separately (2–32 runes).
var aliasRe = regexp.MustCompile(`^[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?$`)

// validateAlias trims whitespace, lowercases the input and validates it.
// Returns the normalised alias or ErrAliasInvalid.
func validateAlias(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	n := utf8.RuneCountInString(s)
	if n < 2 || n > 32 {
		return "", ErrAliasInvalid
	}
	if !aliasRe.MatchString(s) {
		return "", ErrAliasInvalid
	}
	return s, nil
}
