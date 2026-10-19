package model_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anadale/huskwoot/internal/model"
)

func TestSourceDetailsFromMessage(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		msg  model.Message
		via  string
		want model.TaskSourceDetails
	}{
		{
			name: "telegram supergroup gets a deep link",
			msg: model.Message{
				ID:         "42",
				Source:     model.Source{Kind: "telegram", ID: "-1001234567890", Name: "Team chat"},
				Author:     "1001",
				AuthorName: "Alice",
				Text:       "I will send the slides tomorrow",
				Timestamp:  at,
			},
			via: model.SourceViaMessage,
			want: model.TaskSourceDetails{
				Name:   "Team chat",
				Author: "Alice",
				Text:   "I will send the slides tomorrow",
				At:     &at,
				Link:   "https://t.me/c/1234567890/42",
				Via:    model.SourceViaMessage,
			},
		},
		{
			name: "telegram private chat has no link",
			msg: model.Message{
				ID:         "7",
				Source:     model.Source{Kind: "telegram", ID: "dm", Name: "DM"},
				AuthorName: "Owner",
				Text:       "remind me to call Bob",
				Timestamp:  at,
			},
			via: model.SourceViaChat,
			want: model.TaskSourceDetails{
				Name:   "DM",
				Author: "Owner",
				Text:   "remind me to call Bob",
				At:     &at,
				Via:    model.SourceViaChat,
			},
		},
		{
			name: "telegram basic group has no link",
			msg: model.Message{
				ID:         "9",
				Source:     model.Source{Kind: "telegram", ID: "-4567", Name: "Family"},
				AuthorName: "Alice",
				Text:       "I'll buy milk",
			},
			via: model.SourceViaMessage,
			want: model.TaskSourceDetails{
				Name:   "Family",
				Author: "Alice",
				Text:   "I'll buy milk",
				Via:    model.SourceViaMessage,
			},
		},
		{
			name: "IMAP keeps the subject and falls back to the author address",
			msg: model.Message{
				ID:        "100",
				Source:    model.Source{Kind: "imap", ID: "me@example.com:INBOX", Name: "Work mail"},
				Author:    "boss@example.com",
				Subject:   "Q1 report",
				Text:      "please prepare the report",
				Timestamp: at,
			},
			via: model.SourceViaMessage,
			want: model.TaskSourceDetails{
				Name:    "Work mail",
				Subject: "Q1 report",
				Author:  "boss@example.com",
				Text:    "please prepare the report",
				At:      &at,
				Via:     model.SourceViaMessage,
			},
		},
		{
			name: "reply-to is quoted",
			msg: model.Message{
				ID:         "43",
				Source:     model.Source{Kind: "telegram", ID: "-1009", Name: "Ops"},
				AuthorName: "Alice",
				Text:       "sure, will do",
				ReplyTo:    &model.Message{Author: "2002", AuthorName: "Bob", Text: "can you restart the server?"},
			},
			via: model.SourceViaMessage,
			want: model.TaskSourceDetails{
				Name:    "Ops",
				Author:  "Alice",
				Text:    "sure, will do",
				ReplyTo: &model.TaskSourceQuote{Author: "Bob", Text: "can you restart the server?"},
				Link:    "https://t.me/c/9/43",
				Via:     model.SourceViaMessage,
			},
		},
		{
			name: "empty reply-to is dropped",
			msg: model.Message{
				Source:  model.Source{Kind: "telegram", ID: "dm"},
				Text:    "text",
				ReplyTo: &model.Message{},
			},
			via:  model.SourceViaChat,
			want: model.TaskSourceDetails{Text: "text", Via: model.SourceViaChat},
		},
		{
			name: "client source hides name and author",
			msg: model.Message{
				ID:         "device-1:123",
				Source:     model.Source{Kind: "client", ID: "device-1", Name: "ignored", AccountID: "client:device-1"},
				Author:     "device-1",
				AuthorName: "device-1",
				Text:       "add a task to buy tickets",
				Timestamp:  at,
			},
			via: model.SourceViaVoice,
			want: model.TaskSourceDetails{
				Text: "add a task to buy tickets",
				At:   &at,
				Via:  model.SourceViaVoice,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := model.SourceDetailsFromMessage(tc.msg, tc.via)
			if got == nil {
				t.Fatal("SourceDetailsFromMessage returned nil")
			}
			assertSourceDetails(t, *got, tc.want)
		})
	}
}

func TestSourceDetailsFromMessageCapsTexts(t *testing.T) {
	msg := model.Message{
		Source:  model.Source{Kind: "telegram", ID: "-1001"},
		Text:    strings.Repeat("я", model.MaxSourceTextRunes+50),
		ReplyTo: &model.Message{AuthorName: "Bob", Text: strings.Repeat("ж", model.MaxSourceReplyTextRunes+50)},
	}
	got := model.SourceDetailsFromMessage(msg, model.SourceViaMessage)
	if n := utf8.RuneCountInString(got.Text); n != model.MaxSourceTextRunes {
		t.Errorf("text runes = %d, want %d", n, model.MaxSourceTextRunes)
	}
	if got.ReplyTo == nil {
		t.Fatal("reply-to dropped")
	}
	if n := utf8.RuneCountInString(got.ReplyTo.Text); n != model.MaxSourceReplyTextRunes {
		t.Errorf("reply-to text runes = %d, want %d", n, model.MaxSourceReplyTextRunes)
	}
	if utf8.RuneCountInString(msg.Text) != model.MaxSourceTextRunes+50 {
		t.Error("builder modified the message text")
	}
}

func TestTelegramMessageLink(t *testing.T) {
	tests := []struct {
		name      string
		source    model.Source
		messageID string
		want      string
	}{
		{name: "supergroup", source: model.Source{Kind: "telegram", ID: "-1001234"}, messageID: "5", want: "https://t.me/c/1234/5"},
		{name: "private chat", source: model.Source{Kind: "telegram", ID: "123456"}, messageID: "5"},
		{name: "dm source", source: model.Source{Kind: "telegram", ID: "dm"}, messageID: "5"},
		{name: "basic group", source: model.Source{Kind: "telegram", ID: "-123456"}, messageID: "5"},
		{name: "bare prefix", source: model.Source{Kind: "telegram", ID: "-100"}, messageID: "5"},
		{name: "missing message id", source: model.Source{Kind: "telegram", ID: "-1001234"}},
		{name: "non-numeric message id", source: model.Source{Kind: "telegram", ID: "-1001234"}, messageID: "abc"},
		{name: "non-telegram source", source: model.Source{Kind: "imap", ID: "-1001234"}, messageID: "5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := model.TelegramMessageLink(tc.source, tc.messageID); got != tc.want {
				t.Errorf("TelegramMessageLink = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSourceTime(t *testing.T) {
	if got := model.SourceTime(time.Time{}); got != nil {
		t.Errorf("SourceTime(zero) = %v, want nil", got)
	}
	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	got := model.SourceTime(at)
	if got == nil || !got.Equal(at) {
		t.Errorf("SourceTime = %v, want %v", got, at)
	}
}

// assertSourceDetails compares source details field by field, using Equal for times.
func assertSourceDetails(t *testing.T, got, want model.TaskSourceDetails) {
	t.Helper()
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Subject != want.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, want.Subject)
	}
	if got.Author != want.Author {
		t.Errorf("Author = %q, want %q", got.Author, want.Author)
	}
	if got.Text != want.Text {
		t.Errorf("Text = %q, want %q", got.Text, want.Text)
	}
	switch {
	case got.At == nil && want.At != nil:
		t.Errorf("At = nil, want %v", want.At)
	case got.At != nil && want.At == nil:
		t.Errorf("At = %v, want nil", got.At)
	case got.At != nil && !got.At.Equal(*want.At):
		t.Errorf("At = %v, want %v", got.At, want.At)
	}
	switch {
	case got.ReplyTo == nil && want.ReplyTo != nil:
		t.Errorf("ReplyTo = nil, want %+v", want.ReplyTo)
	case got.ReplyTo != nil && want.ReplyTo == nil:
		t.Errorf("ReplyTo = %+v, want nil", got.ReplyTo)
	case got.ReplyTo != nil && *got.ReplyTo != *want.ReplyTo:
		t.Errorf("ReplyTo = %+v, want %+v", got.ReplyTo, want.ReplyTo)
	}
	if got.Link != want.Link {
		t.Errorf("Link = %q, want %q", got.Link, want.Link)
	}
	if got.Via != want.Via {
		t.Errorf("Via = %q, want %q", got.Via, want.Via)
	}
}
