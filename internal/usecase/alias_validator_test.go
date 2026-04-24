package usecase

import (
	"strings"
	"testing"
)

func TestValidateAlias(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// valid
		{name: "latin two chars", input: "ab", wantErr: false},
		{name: "latin 32 chars", input: strings.Repeat("a", 32), wantErr: false},
		{name: "cyrillic", input: "букинист", wantErr: false},
		{name: "hyphen in middle", input: "book-store", wantErr: false},
		{name: "cyrillic hyphen latin", input: "книга-book", wantErr: false},
		{name: "digits", input: "ab1", wantErr: false},
		{name: "digit start", input: "1ab", wantErr: false},
		{name: "exactly two chars cyrillic", input: "аб", wantErr: false},
		{name: "hyphen between digits", input: "1-2", wantErr: false},

		// invalid — length
		{name: "empty", input: "", wantErr: true},
		{name: "one char", input: "a", wantErr: true},
		{name: "33 chars", input: strings.Repeat("a", 33), wantErr: true},

		// invalid — characters
		{name: "space inside", input: "book store", wantErr: true},
		{name: "dot", input: "book.store", wantErr: true},
		{name: "underscore", input: "book_store", wantErr: true},
		{name: "at sign", input: "book@store", wantErr: true},
		{name: "emoji", input: "📚book", wantErr: true},
		{name: "slash", input: "book/store", wantErr: true},

		// invalid — hyphen position
		{name: "starts with hyphen", input: "-book", wantErr: true},
		{name: "ends with hyphen", input: "book-", wantErr: true},
		{name: "only hyphens", input: "--", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateAlias(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for input %q, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for input %q: %v", tc.input, err)
			}
		})
	}
}

func TestValidateAliasNormalizesCase(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "cyrillic uppercase", input: "Букинист", want: "букинист"},
		{name: "latin uppercase with spaces", input: "  TEST  ", want: "test"},
		{name: "mixed case", input: "BookStore", want: "bookstore"},
		{name: "already lowercase", input: "test", want: "test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateAlias(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("validateAlias(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestValidateAliasReturnsErrAliasInvalid(t *testing.T) {
	_, err := validateAlias("a")
	if err != ErrAliasInvalid {
		t.Errorf("expected ErrAliasInvalid, got %v", err)
	}
}
