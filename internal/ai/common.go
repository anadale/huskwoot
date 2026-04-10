package ai

import (
	"bytes"
	"context"
	"text/template"
)

// Completer is the interface for calling a language model with a text prompt.
// Implemented by *Client and mock objects in tests.
type Completer interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// renderTemplate renders a template with the given data and returns the result.
func renderTemplate(tmpl *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
