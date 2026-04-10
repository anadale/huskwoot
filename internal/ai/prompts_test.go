package ai_test

import (
	"strings"
	"testing"

	"github.com/anadale/huskwoot/internal/ai"
)

func TestLoadPrompt_Russian(t *testing.T) {
	got := ai.ExportedLoadPrompt(ai.ExportedPromptsFS, "ru", "classifier_simple_system")
	if got == "" {
		t.Fatal("loadPrompt(ru, classifier_simple_system) returned empty string")
	}
	if !strings.Contains(got, "Always respond in Russian") {
		t.Errorf("Russian prompt does not contain 'Always respond in Russian', got: %q", got[max(0, len(got)-100):])
	}
}

func TestLoadPrompt_English(t *testing.T) {
	got := ai.ExportedLoadPrompt(ai.ExportedPromptsFS, "en", "classifier_simple_system")
	if got == "" {
		t.Fatal("loadPrompt(en, classifier_simple_system) returned empty string")
	}
	if !strings.Contains(got, "Always respond in English") {
		t.Errorf("English prompt does not contain 'Always respond in English', got: %q", got[max(0, len(got)-100):])
	}
}

func TestLoadPrompt_FallbackToRu(t *testing.T) {
	got := ai.ExportedLoadPrompt(ai.ExportedPromptsFS, "xx", "classifier_simple_system")
	if got == "" {
		t.Fatal("loadPrompt fallback returned empty string")
	}
	if !strings.Contains(got, "Always respond in Russian") {
		t.Errorf("fallback prompt does not contain 'Always respond in Russian', got: %q", got[max(0, len(got)-100):])
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
