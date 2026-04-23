package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/model"
)

func newTestSimpleClassifier(t *testing.T, mock *mockCompleter) *ai.SimpleClassifier {
	t.Helper()
	c, err := ai.NewSimpleClassifier(mock, ai.ClassifierConfig{
		UserName: "Григорий",
		Aliases:  []string{"Гриша", "Greg"},
	})
	if err != nil {
		t.Fatalf("NewSimpleClassifier: %v", err)
	}
	return c
}

func newTestGroupClassifier(t *testing.T, mock *mockCompleter) *ai.GroupClassifier {
	t.Helper()
	c, err := ai.NewGroupClassifier(mock, ai.ClassifierConfig{
		UserName: "Григорий",
		Aliases:  []string{"Гриша", "Greg"},
	})
	if err != nil {
		t.Fatalf("NewGroupClassifier: %v", err)
	}
	return c
}

// --- SimpleClassifier tests ---

func TestSimpleClassifier_ReturnsPromise(t *testing.T) {
	mock := &mockCompleter{response: "promise"}
	c := newTestSimpleClassifier(t, mock)

	got, err := c.Classify(context.Background(), ownerMsg("сделаю завтра"))
	if err != nil {
		t.Fatalf("Classify() returned error: %v", err)
	}
	if got != model.ClassPromise {
		t.Errorf("Classify() = %v, want ClassPromise", got)
	}
}

func TestSimpleClassifier_ReturnsSkip(t *testing.T) {
	mock := &mockCompleter{response: "skip"}
	c := newTestSimpleClassifier(t, mock)

	got, err := c.Classify(context.Background(), ownerMsg("привет, как дела?"))
	if err != nil {
		t.Fatalf("Classify() returned error: %v", err)
	}
	if got != model.ClassSkip {
		t.Errorf("Classify() = %v, want ClassSkip", got)
	}
}

func TestSimpleClassifier_PromiseVariants(t *testing.T) {
	cases := []string{"promise", "Promise", "PROMISE", "promise."}
	for _, resp := range cases {
		mock := &mockCompleter{response: resp}
		c := newTestSimpleClassifier(t, mock)

		got, err := c.Classify(context.Background(), ownerMsg("сделаю"))
		if err != nil {
			t.Errorf("Classify() with response %q returned error: %v", resp, err)
			continue
		}
		if got != model.ClassPromise {
			t.Errorf("Classify() with response %q = %v, want ClassPromise", resp, got)
		}
	}
}

func TestSimpleClassifier_SkipVariants(t *testing.T) {
	cases := []string{"skip", "Skip", "SKIP", "skip."}
	for _, resp := range cases {
		mock := &mockCompleter{response: resp}
		c := newTestSimpleClassifier(t, mock)

		got, err := c.Classify(context.Background(), ownerMsg("ничего особенного"))
		if err != nil {
			t.Errorf("Classify() with response %q returned error: %v", resp, err)
			continue
		}
		if got != model.ClassSkip {
			t.Errorf("Classify() with response %q = %v, want ClassSkip", resp, got)
		}
	}
}

func TestSimpleClassifier_UnexpectedResponse(t *testing.T) {
	mock := &mockCompleter{response: "maybe"}
	c := newTestSimpleClassifier(t, mock)

	_, err := c.Classify(context.Background(), ownerMsg("что-то"))
	if err == nil {
		t.Error("Classify() must return an error for unexpected model response")
	}
}

func TestSimpleClassifier_EmptyResponse(t *testing.T) {
	mock := &mockCompleter{response: ""}
	c := newTestSimpleClassifier(t, mock)

	_, err := c.Classify(context.Background(), ownerMsg("что-то"))
	if err == nil {
		t.Error("Classify() must return an error for empty model response")
	}
}

func TestSimpleClassifier_AIError(t *testing.T) {
	mock := &mockCompleter{err: errors.New("сеть недоступна")}
	c := newTestSimpleClassifier(t, mock)

	_, err := c.Classify(context.Background(), ownerMsg("сделаю завтра"))
	if err == nil {
		t.Error("Classify() must return an error when AI client errors")
	}
}

func TestSimpleClassifier_SystemPromptContents(t *testing.T) {
	cap := &capturingCompleter{response: "skip"}
	c, err := ai.NewSimpleClassifier(cap, ai.ClassifierConfig{
		UserName: "Григорий",
		Aliases:  []string{"Гриша"},
	})
	if err != nil {
		t.Fatalf("NewSimpleClassifier: %v", err)
	}

	_, _ = c.Classify(context.Background(), ownerMsg("тест"))

	checks := []string{"Григорий", "Гриша", "promise", "skip"}
	for _, s := range checks {
		if !strings.Contains(cap.systemPrompt, s) {
			t.Errorf("SimpleClassifier system prompt does not contain %q", s)
		}
	}
	// SimpleClassifier must NOT mention "command" in the prompt
	if strings.Contains(cap.systemPrompt, "command") {
		t.Error("SimpleClassifier system prompt must not contain 'command'")
	}
}

func TestSimpleClassifier_CustomSystemTemplate(t *testing.T) {
	cap := &capturingCompleter{response: "skip"}
	custom := `Кастомный простой классификатор для {{.UserName}}`
	c, err := ai.NewSimpleClassifier(cap, ai.ClassifierConfig{
		UserName:       "Григорий",
		SystemTemplate: custom,
	})
	if err != nil {
		t.Fatalf("NewSimpleClassifier: %v", err)
	}

	_, _ = c.Classify(context.Background(), ownerMsg("тест"))

	if !strings.Contains(cap.systemPrompt, "Кастомный простой классификатор для Григорий") {
		t.Errorf("custom template was not applied, prompt: %s", cap.systemPrompt)
	}
}

// --- GroupClassifier tests ---

func TestGroupClassifier_ReturnsPromise(t *testing.T) {
	mock := &mockCompleter{response: "promise"}
	c := newTestGroupClassifier(t, mock)

	got, err := c.Classify(context.Background(), ownerMsg("сделаю к пятнице"))
	if err != nil {
		t.Fatalf("Classify() returned error: %v", err)
	}
	if got != model.ClassPromise {
		t.Errorf("Classify() = %v, want ClassPromise", got)
	}
}

func TestGroupClassifier_ReturnsSkip(t *testing.T) {
	mock := &mockCompleter{response: "skip"}
	c := newTestGroupClassifier(t, mock)

	got, err := c.Classify(context.Background(), ownerMsg("хорошая идея"))
	if err != nil {
		t.Fatalf("Classify() returned error: %v", err)
	}
	if got != model.ClassSkip {
		t.Errorf("Classify() = %v, want ClassSkip", got)
	}
}

func TestGroupClassifier_AllVariants(t *testing.T) {
	cases := []struct {
		response string
		want     model.Classification
	}{
		{"promise", model.ClassPromise},
		{"Promise", model.ClassPromise},
		{"PROMISE", model.ClassPromise},
		{"promise.", model.ClassPromise},
		{"skip", model.ClassSkip},
		{"Skip", model.ClassSkip},
		{"skip.", model.ClassSkip},
	}
	for _, tc := range cases {
		mock := &mockCompleter{response: tc.response}
		c := newTestGroupClassifier(t, mock)

		got, err := c.Classify(context.Background(), ownerMsg("тест"))
		if err != nil {
			t.Errorf("Classify() with response %q returned error: %v", tc.response, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Classify() with response %q = %v, want %v", tc.response, got, tc.want)
		}
	}
}

func TestGroupClassifier_UnexpectedResponse(t *testing.T) {
	mock := &mockCompleter{response: "yes"}
	c := newTestGroupClassifier(t, mock)

	_, err := c.Classify(context.Background(), ownerMsg("что-то"))
	if err == nil {
		t.Error("GroupClassifier.Classify() must return an error for response 'yes'")
	}
}

func TestGroupClassifier_AIError(t *testing.T) {
	mock := &mockCompleter{err: errors.New("таймаут")}
	c := newTestGroupClassifier(t, mock)

	_, err := c.Classify(context.Background(), ownerMsg("что-то"))
	if err == nil {
		t.Error("Classify() must return an error when AI client errors")
	}
}

func TestGroupClassifier_SystemPromptContents(t *testing.T) {
	cap := &capturingCompleter{response: "skip"}
	c, err := ai.NewGroupClassifier(cap, ai.ClassifierConfig{
		UserName: "Григорий",
		Aliases:  []string{"Гриша"},
	})
	if err != nil {
		t.Fatalf("NewGroupClassifier: %v", err)
	}

	_, _ = c.Classify(context.Background(), ownerMsg("тест"))

	checks := []string{"Григорий", "Гриша", "promise", "skip"}
	for _, s := range checks {
		if !strings.Contains(cap.systemPrompt, s) {
			t.Errorf("GroupClassifier system prompt does not contain %q", s)
		}
	}
	if strings.Contains(cap.systemPrompt, "command") {
		t.Error("GroupClassifier system prompt must not contain 'command'")
	}
}

func TestGroupClassifier_CustomSystemTemplate(t *testing.T) {
	cap := &capturingCompleter{response: "skip"}
	custom := `Кастомный групповой классификатор для {{.UserName}}`
	c, err := ai.NewGroupClassifier(cap, ai.ClassifierConfig{
		UserName:       "Григорий",
		SystemTemplate: custom,
	})
	if err != nil {
		t.Fatalf("NewGroupClassifier: %v", err)
	}

	_, _ = c.Classify(context.Background(), ownerMsg("тест"))

	if !strings.Contains(cap.systemPrompt, "Кастомный групповой классификатор для Григорий") {
		t.Errorf("custom template was not applied, prompt: %s", cap.systemPrompt)
	}
}

func TestGroupClassifier_ContextTimeout(t *testing.T) {
	// Verify that a cancelled context returns an error
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := &mockCompleter{err: context.Canceled}
	c := newTestGroupClassifier(t, mock)

	_, err := c.Classify(ctx, ownerMsg("тест"))
	if err == nil {
		t.Error("Classify() must return an error for a cancelled context")
	}
}
