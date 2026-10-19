package model_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anadale/huskwoot/internal/model"
)

func TestTaskDisplayID(t *testing.T) {
	task := model.Task{Number: 42, ProjectSlug: "inbox"}
	if got := task.DisplayID(); got != "inbox#42" {
		t.Fatalf("DisplayID = %q, want %q", got, "inbox#42")
	}
}

func TestTaskPriorityValid(t *testing.T) {
	tests := []struct {
		name  string
		input model.TaskPriority
		want  bool
	}{
		{name: "negative", input: -1, want: false},
		{name: "none", input: model.TaskPriorityNone, want: true},
		{name: "low", input: model.TaskPriorityLow, want: true},
		{name: "medium", input: model.TaskPriorityMedium, want: true},
		{name: "high", input: model.TaskPriorityHigh, want: true},
		{name: "above high", input: 4, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("TaskPriority(%d).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTaskSourceDetailsCappedNil(t *testing.T) {
	var d *model.TaskSourceDetails
	if got := d.Capped(); got != nil {
		t.Fatalf("Capped() on nil = %+v, want nil", got)
	}
}

func TestTaskSourceDetailsCapped(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name          string
		text          string
		replyText     string
		wantTextRunes int
		wantReplyRune int
		wantTextCut   bool
		wantReplyCut  bool
	}{
		{
			name:          "short texts unchanged",
			text:          "Сделаю отчёт к пятнице",
			replyText:     "Когда будет отчёт?",
			wantTextRunes: utf8.RuneCountInString("Сделаю отчёт к пятнице"),
			wantReplyRune: utf8.RuneCountInString("Когда будет отчёт?"),
		},
		{
			name:          "texts exactly at the limit unchanged",
			text:          strings.Repeat("я", model.MaxSourceTextRunes),
			replyText:     strings.Repeat("ю", model.MaxSourceReplyTextRunes),
			wantTextRunes: model.MaxSourceTextRunes,
			wantReplyRune: model.MaxSourceReplyTextRunes,
		},
		{
			name:          "long multibyte texts capped",
			text:          strings.Repeat("я", model.MaxSourceTextRunes+500),
			replyText:     strings.Repeat("ю", model.MaxSourceReplyTextRunes+1),
			wantTextRunes: model.MaxSourceTextRunes,
			wantReplyRune: model.MaxSourceReplyTextRunes,
			wantTextCut:   true,
			wantReplyCut:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := &model.TaskSourceDetails{
				Name:    "Команда",
				Author:  "Алиса",
				Text:    tt.text,
				At:      &at,
				ReplyTo: &model.TaskSourceQuote{Author: "Боб", Text: tt.replyText},
				Link:    "https://t.me/c/123/45",
				Via:     "message",
			}

			got := orig.Capped()
			if got == orig || got.ReplyTo == orig.ReplyTo || got.At == orig.At {
				t.Fatal("Capped() must return a deep copy")
			}
			if !utf8.ValidString(got.Text) || !utf8.ValidString(got.ReplyTo.Text) {
				t.Fatal("Capped() produced invalid UTF-8")
			}
			if n := utf8.RuneCountInString(got.Text); n != tt.wantTextRunes {
				t.Errorf("Text runes = %d, want %d", n, tt.wantTextRunes)
			}
			if n := utf8.RuneCountInString(got.ReplyTo.Text); n != tt.wantReplyRune {
				t.Errorf("ReplyTo.Text runes = %d, want %d", n, tt.wantReplyRune)
			}
			if cut := strings.HasSuffix(got.Text, "…"); cut != tt.wantTextCut {
				t.Errorf("Text ends with ellipsis = %v, want %v", cut, tt.wantTextCut)
			}
			if cut := strings.HasSuffix(got.ReplyTo.Text, "…"); cut != tt.wantReplyCut {
				t.Errorf("ReplyTo.Text ends with ellipsis = %v, want %v", cut, tt.wantReplyCut)
			}
			if got.Name != orig.Name || got.Author != orig.Author || got.Link != orig.Link ||
				got.Via != orig.Via || got.ReplyTo.Author != orig.ReplyTo.Author || !got.At.Equal(at) {
				t.Errorf("Capped() changed non-text fields: %+v", got)
			}
			if orig.Text != tt.text || orig.ReplyTo.Text != tt.replyText {
				t.Error("Capped() mutated the original details")
			}
		})
	}
}

func TestTaskSourceDetailsCappedWithoutReplyTo(t *testing.T) {
	d := &model.TaskSourceDetails{Text: strings.Repeat("a", model.MaxSourceTextRunes+1), Via: "api"}
	got := d.Capped()
	if got.ReplyTo != nil {
		t.Errorf("ReplyTo = %+v, want nil", got.ReplyTo)
	}
	if got.At != nil {
		t.Errorf("At = %v, want nil", got.At)
	}
	if n := utf8.RuneCountInString(got.Text); n != model.MaxSourceTextRunes {
		t.Errorf("Text runes = %d, want %d", n, model.MaxSourceTextRunes)
	}
}
