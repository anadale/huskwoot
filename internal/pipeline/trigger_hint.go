package pipeline

import "regexp"

// triggerHintRe matches the first #<slug> token where slug is one or more
// unicode letters, digits, underscores, or hyphens.
var triggerHintRe = regexp.MustCompile(`#([\pL\d_-]+)`)

// parseTriggerProjectHint extracts the first "#slug" token from text.
// Returns the slug as hint and the text with the token removed as rest.
// Both are empty strings when no token is found.
func parseTriggerProjectHint(text string) (hint, rest string) {
	m := triggerHintRe.FindStringSubmatchIndex(text)
	if m == nil {
		return "", text
	}
	hint = text[m[2]:m[3]]
	rest = text[:m[0]] + text[m[1]:]
	return hint, rest
}
