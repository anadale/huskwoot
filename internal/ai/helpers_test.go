package ai_test

import (
	"context"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// mockCompleter is a mock AI client for testing AI components.
type mockCompleter struct {
	response   string
	err        error
	calls      int
	lastSystem string
	lastUser   string
}

func (m *mockCompleter) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	m.calls++
	m.lastSystem = systemPrompt
	m.lastUser = userPrompt
	return m.response, m.err
}

// capturingCompleter records the prompts passed to Complete for verifying template rendering.
type capturingCompleter struct {
	response     string
	systemPrompt string
	userPrompt   string
}

func (c *capturingCompleter) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	c.systemPrompt = systemPrompt
	c.userPrompt = userPrompt
	return c.response, nil
}

// ownerMsg creates a test message from the owner with the given text.
func ownerMsg(text string) model.Message {
	return model.Message{
		ID:         "msg1",
		Author:     "user123",
		AuthorName: "Григорий",
		Text:       text,
		Timestamp:  time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		Source:     model.Source{Kind: "telegram", ID: "chat1"},
	}
}
