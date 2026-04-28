package pipeline

import (
	"testing"
)

func TestParseTriggerProjectHint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHint string
		wantRest string
	}{
		{
			name:     "only hash slug",
			input:    "#вася",
			wantHint: "вася",
			wantRest: "",
		},
		{
			name:     "hash slug after text",
			input:    "обработай #вася",
			wantHint: "вася",
			wantRest: "обработай ",
		},
		{
			name:     "multiple hash slugs - takes first",
			input:    "#alpha #beta",
			wantHint: "alpha",
			wantRest: " #beta",
		},
		{
			name:     "empty input",
			input:    "",
			wantHint: "",
			wantRest: "",
		},
		{
			name:     "no hash",
			input:    "привет",
			wantHint: "",
			wantRest: "привет",
		},
		{
			name:     "hash followed by space - no match",
			input:    "# ",
			wantHint: "",
			wantRest: "# ",
		},
		{
			name:     "cyrillic slug with hyphen and digit",
			input:    "#проект-1",
			wantHint: "проект-1",
			wantRest: "",
		},
		{
			name:     "latin slug with underscore",
			input:    "go #my_project now",
			wantHint: "my_project",
			wantRest: "go  now",
		},
		{
			name:     "hash at end",
			input:    "finish #",
			wantHint: "",
			wantRest: "finish #",
		},
		{
			name:     "slug with digits",
			input:    "#task123",
			wantHint: "task123",
			wantRest: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hint, rest := parseTriggerProjectHint(tc.input)
			if hint != tc.wantHint {
				t.Errorf("hint: got %q, want %q", hint, tc.wantHint)
			}
			if rest != tc.wantRest {
				t.Errorf("rest: got %q, want %q", rest, tc.wantRest)
			}
		})
	}
}
