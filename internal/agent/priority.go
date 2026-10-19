package agent

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// priorityParam returns the JSON schema of the optional priority tool argument.
func priorityParam(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"minimum":     int(model.TaskPriorityNone),
		"maximum":     int(model.TaskPriorityHigh),
		"description": description,
	}
}

// parsePriorityArg decodes the optional priority tool argument. A JSON integer
// or a numeric string in the 0–3 range is accepted. A missing value or null
// means "not set" and returns set == false. Anything else yields a localized
// error the model can read and correct.
func parsePriorityArg(raw json.RawMessage, loc *goI18n.Localizer) (p model.TaskPriority, set bool, err error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return model.TaskPriorityNone, false, nil
	}
	if unquoted, uErr := strconv.Unquote(value); uErr == nil {
		value = strings.TrimSpace(unquoted)
	}
	n, convErr := strconv.Atoi(value)
	p = model.TaskPriority(n)
	if convErr != nil || !p.Valid() {
		return model.TaskPriorityNone, false, errors.New(huskwootI18n.Translate(loc, "agent_priority_invalid", nil))
	}
	return p, true, nil
}

// priorityMarker renders a priority as "!", "!!" or "!!!"; empty for no priority.
func priorityMarker(p model.TaskPriority) string {
	if p <= model.TaskPriorityNone || !p.Valid() {
		return ""
	}
	return strings.Repeat("!", int(p))
}

// addPriority adds "priority" and "priority_marker" to a tool output map when
// the task has a priority; both keys are omitted for priority 0.
func addPriority(data map[string]any, p model.TaskPriority) {
	marker := priorityMarker(p)
	if marker == "" {
		return
	}
	data["priority"] = int(p)
	data["priority_marker"] = marker
}
