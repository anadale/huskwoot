package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/model"
)

func newTestCommandExtractor(t *testing.T, mock *mockCompleter) *ai.AICommandExtractor {
	t.Helper()
	e, err := ai.NewAICommandExtractor(mock, ai.CommandExtractorConfig{})
	if err != nil {
		t.Fatalf("NewAICommandExtractor: %v", err)
	}
	return e
}

func groupMsg(text string) model.Message {
	return model.Message{
		ID:         "msg42",
		Author:     "user123",
		AuthorName: "Григорий",
		Text:       text,
		Source:     model.Source{Kind: "telegram", ID: "chat99"},
		Kind:       model.MessageKindGroup,
	}
}

// TestAICommandExtractor_SetProjectName verifies extraction of the set_project_name command.
func TestAICommandExtractor_SetProjectName(t *testing.T) {
	resp := `{"type": "set_project_name", "payload": {"name": "Альфа"}}`
	mock := &mockCompleter{response: resp}
	e := newTestCommandExtractor(t, mock)

	msg := groupMsg("это группа проекта Альфа")
	cmd, err := e.Extract(context.Background(), msg)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if cmd.Type != "set_project_name" {
		t.Errorf("cmd.Type = %q, want %q", cmd.Type, "set_project_name")
	}
	if cmd.Payload["name"] != "Альфа" {
		t.Errorf("cmd.Payload[\"name\"] = %q, want %q", cmd.Payload["name"], "Альфа")
	}
	if cmd.Source != msg.Source {
		t.Errorf("cmd.Source = %v, want %v", cmd.Source, msg.Source)
	}
	if cmd.SourceMessage.ID != msg.ID {
		t.Errorf("cmd.SourceMessage.ID = %q, want %q", cmd.SourceMessage.ID, msg.ID)
	}
}

// TestAICommandExtractor_UnknownCommand verifies extraction of an unknown command type.
func TestAICommandExtractor_UnknownCommand(t *testing.T) {
	resp := `{"type": "unknown_action", "payload": {"foo": "bar"}}`
	mock := &mockCompleter{response: resp}
	e := newTestCommandExtractor(t, mock)

	cmd, err := e.Extract(context.Background(), groupMsg("что-то непонятное"))
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if cmd.Type != "unknown_action" {
		t.Errorf("cmd.Type = %q, want %q", cmd.Type, "unknown_action")
	}
}

// TestAICommandExtractor_EmptyPayload verifies a command with an empty payload.
func TestAICommandExtractor_EmptyPayload(t *testing.T) {
	resp := `{"type": "set_project_name", "payload": {}}`
	mock := &mockCompleter{response: resp}
	e := newTestCommandExtractor(t, mock)

	cmd, err := e.Extract(context.Background(), groupMsg("назови эту группу"))
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if cmd.Type != "set_project_name" {
		t.Errorf("cmd.Type = %q, want %q", cmd.Type, "set_project_name")
	}
	if len(cmd.Payload) != 0 {
		t.Errorf("cmd.Payload must be empty, got: %v", cmd.Payload)
	}
}

// TestAICommandExtractor_InvalidJSON verifies handling of invalid JSON.
func TestAICommandExtractor_InvalidJSON(t *testing.T) {
	mock := &mockCompleter{response: "это не json вообще"}
	e := newTestCommandExtractor(t, mock)

	_, err := e.Extract(context.Background(), groupMsg("команда"))
	if err == nil {
		t.Error("Extract() must return an error for invalid JSON")
	}
}

// TestAICommandExtractor_MarkdownWrappedJSON verifies JSON wrapped in a markdown block.
func TestAICommandExtractor_MarkdownWrappedJSON(t *testing.T) {
	resp := "```json\n{\"type\": \"set_project_name\", \"payload\": {\"name\": \"Бета\"}}\n```"
	mock := &mockCompleter{response: resp}
	e := newTestCommandExtractor(t, mock)

	cmd, err := e.Extract(context.Background(), groupMsg("это группа проекта Бета"))
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if cmd.Type != "set_project_name" {
		t.Errorf("cmd.Type = %q, want %q", cmd.Type, "set_project_name")
	}
	if cmd.Payload["name"] != "Бета" {
		t.Errorf("cmd.Payload[\"name\"] = %q, want %q", cmd.Payload["name"], "Бета")
	}
}

// TestAICommandExtractor_AIError verifies handling of an AI client error.
func TestAICommandExtractor_AIError(t *testing.T) {
	mock := &mockCompleter{err: errors.New("сеть недоступна")}
	e := newTestCommandExtractor(t, mock)

	_, err := e.Extract(context.Background(), groupMsg("команда"))
	if err == nil {
		t.Error("Extract() must return an error when AI client errors")
	}
}

// TestAICommandExtractor_ContextTimeout verifies handling of a cancelled context.
func TestAICommandExtractor_ContextTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock := &mockCompleter{err: context.Canceled}
	e := newTestCommandExtractor(t, mock)

	_, err := e.Extract(ctx, groupMsg("команда"))
	if err == nil {
		t.Error("Extract() must return an error for a cancelled context")
	}
}

// TestAICommandExtractor_SystemPromptContents verifies the contents of the default system prompt.
func TestAICommandExtractor_SystemPromptContents(t *testing.T) {
	resp := `{"type": "set_project_name", "payload": {"name": "Тест"}}`
	cap := &capturingCompleter{response: resp}
	e, err := ai.NewAICommandExtractor(cap, ai.CommandExtractorConfig{})
	if err != nil {
		t.Fatalf("NewAICommandExtractor: %v", err)
	}

	_, _ = e.Extract(context.Background(), groupMsg("это группа проекта Тест"))

	checks := []string{"set_project_name", "JSON", "type", "payload"}
	for _, s := range checks {
		if !strings.Contains(cap.systemPrompt, s) {
			t.Errorf("CommandExtractor system prompt does not contain %q", s)
		}
	}
}

// TestAICommandExtractor_CustomSystemTemplate verifies that a custom system template is applied.
func TestAICommandExtractor_CustomSystemTemplate(t *testing.T) {
	resp := `{"type": "set_project_name", "payload": {}}`
	cap := &capturingCompleter{response: resp}
	custom := `Кастомный экстрактор команд`
	e, err := ai.NewAICommandExtractor(cap, ai.CommandExtractorConfig{
		SystemTemplate: custom,
	})
	if err != nil {
		t.Fatalf("NewAICommandExtractor: %v", err)
	}

	_, _ = e.Extract(context.Background(), groupMsg("тест"))

	if !strings.Contains(cap.systemPrompt, "Кастомный экстрактор команд") {
		t.Errorf("custom template was not applied, prompt: %s", cap.systemPrompt)
	}
}

// TestAICommandExtractor_UserPromptContainsText verifies that the message text is included in the prompt.
func TestAICommandExtractor_UserPromptContainsText(t *testing.T) {
	resp := `{"type": "set_project_name", "payload": {"name": "Гамма"}}`
	cap := &capturingCompleter{response: resp}
	e, err := ai.NewAICommandExtractor(cap, ai.CommandExtractorConfig{})
	if err != nil {
		t.Fatalf("NewAICommandExtractor: %v", err)
	}

	_, _ = e.Extract(context.Background(), groupMsg("это группа проекта Гамма"))

	if !strings.Contains(cap.userPrompt, "это группа проекта Гамма") {
		t.Errorf("user prompt does not contain message text, prompt: %s", cap.userPrompt)
	}
}
