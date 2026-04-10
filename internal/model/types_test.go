package model

import (
	"context"
	"testing"
	"time"
)

func TestMessage_ZeroValue(t *testing.T) {
	var msg Message
	if msg.ID != "" {
		t.Errorf("want empty ID, got %q", msg.ID)
	}
	if msg.ReplyTo != nil {
		t.Error("want nil ReplyTo for zero value")
	}
	if msg.Reaction != nil {
		t.Error("want nil Reaction for zero value")
	}
}

func TestMessage_WithReply(t *testing.T) {
	parent := Message{
		ID:     "1",
		Author: "user1",
		Text:   "Можешь сделать отчёт?",
	}
	reply := Message{
		ID:      "2",
		Author:  "user2",
		Text:    "Сделаю завтра",
		ReplyTo: &parent,
	}

	if reply.ReplyTo == nil {
		t.Fatal("want non-nil ReplyTo")
	}
	if reply.ReplyTo.ID != "1" {
		t.Errorf("want ReplyTo.ID=1, got %q", reply.ReplyTo.ID)
	}
}

func TestMessage_WithReaction(t *testing.T) {
	original := Message{
		ID:   "3",
		Text: "Пришлите отчёт сегодня",
	}
	reaction := Reaction{
		Emoji:  "👍",
		UserID: "owner",
	}
	msg := Message{
		ID:       "4",
		Author:   "owner",
		ReplyTo:  &original,
		Reaction: &reaction,
	}

	if msg.Reaction.Emoji != "👍" {
		t.Errorf("want emoji 👍, got %q", msg.Reaction.Emoji)
	}
}

func TestSource_Fields(t *testing.T) {
	s := Source{
		Kind:      "telegram",
		ID:        "123456",
		Name:      "Рабочий чат",
		AccountID: "work",
	}
	if s.Kind != "telegram" {
		t.Errorf("want Kind=telegram, got %q", s.Kind)
	}
	if s.AccountID != "work" {
		t.Errorf("want AccountID=work, got %q", s.AccountID)
	}
}

func TestTask_WithDeadline(t *testing.T) {
	deadline := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	task := Task{
		ID:         "1",
		Summary:    "Отправить отчёт",
		Deadline:   &deadline,
		Confidence: 0.95,
		CreatedAt:  time.Now(),
	}

	if task.Deadline == nil {
		t.Fatal("want non-nil Deadline")
	}
	if !task.Deadline.Equal(deadline) {
		t.Errorf("want deadline %v, got %v", deadline, *task.Deadline)
	}
	if task.Confidence < 0 || task.Confidence > 1 {
		t.Errorf("Confidence out of range [0,1]: %f", task.Confidence)
	}
}

func TestTask_WithoutDeadline(t *testing.T) {
	task := Task{
		ID:         "2",
		Summary:    "Посмотрю на это",
		Deadline:   nil,
		Confidence: 0.7,
	}
	if task.Deadline != nil {
		t.Error("want nil Deadline")
	}
}

func TestClassification_String(t *testing.T) {
	tests := []struct {
		name  string
		class Classification
		want  string
	}{
		{"skip", ClassSkip, "skip"},
		{"promise", ClassPromise, "promise"},
		{"command", ClassCommand, "command"},
		{"unknown", Classification(99), "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.class.String()
			if got != tc.want {
				t.Errorf("Classification(%d).String() = %q, want %q", int(tc.class), got, tc.want)
			}
		})
	}
}

func TestMessageKind_Constants(t *testing.T) {
	tests := []struct {
		kind MessageKind
		want string
	}{
		{MessageKindDM, "dm"},
		{MessageKindBatch, "batch"},
		{MessageKindGroup, "group"},
	}
	for _, tc := range tests {
		if string(tc.kind) != tc.want {
			t.Errorf("MessageKind=%q, want %q", tc.kind, tc.want)
		}
	}
}

func TestMessage_KindAndCallbacks(t *testing.T) {
	var reactCalled, replyCalled bool
	msg := Message{
		ID:   "5",
		Kind: MessageKindGroup,
		ReactFn: func(_ context.Context, emoji string) error {
			reactCalled = true
			return nil
		},
		ReplyFn: func(_ context.Context, text string) error {
			replyCalled = true
			return nil
		},
	}

	if msg.Kind != MessageKindGroup {
		t.Errorf("Kind = %q, want %q", msg.Kind, MessageKindGroup)
	}
	if msg.ReactFn == nil {
		t.Fatal("ReactFn must not be nil")
	}
	if msg.ReplyFn == nil {
		t.Fatal("ReplyFn must not be nil")
	}
	_ = msg.ReactFn(context.Background(), "👍")
	_ = msg.ReplyFn(context.Background(), "ок")
	if !reactCalled {
		t.Error("ReactFn was not called")
	}
	if !replyCalled {
		t.Error("ReplyFn was not called")
	}
}

func TestMessage_NilCallbacks(t *testing.T) {
	msg := Message{
		ID:   "6",
		Kind: MessageKindBatch,
	}
	if msg.ReactFn != nil {
		t.Error("ReactFn must be nil for Batch messages")
	}
	if msg.ReplyFn != nil {
		t.Error("ReplyFn must be nil for Batch messages")
	}
}

func TestCommand_Fields(t *testing.T) {
	src := Source{Kind: "telegram", ID: "-100123"}
	cmd := Command{
		Type:    "set_project_name",
		Payload: map[string]string{"name": "Проект X"},
		Source:  src,
	}
	if cmd.Type != "set_project_name" {
		t.Errorf("Type = %q, want set_project_name", cmd.Type)
	}
	if cmd.Payload["name"] != "Проект X" {
		t.Errorf("Payload[name] = %q, want %q", cmd.Payload["name"], "Проект X")
	}
}

func TestMessage_HistoryFn_Set(t *testing.T) {
	expected := []HistoryEntry{
		{AuthorName: "Григорий", Text: "Сделаю завтра"},
		{AuthorName: "Менеджер", Text: "Ок, принято"},
	}
	msg := Message{
		ID:   "10",
		Kind: MessageKindGroup,
		HistoryFn: func(_ context.Context) ([]HistoryEntry, error) {
			return expected, nil
		},
	}

	if msg.HistoryFn == nil {
		t.Fatal("HistoryFn must not be nil")
	}
	got, err := msg.HistoryFn(context.Background())
	if err != nil {
		t.Fatalf("HistoryFn returned error: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("HistoryFn returned %d entries, want %d", len(got), len(expected))
	}
	for i, m := range got {
		if m.Text != expected[i].Text {
			t.Errorf("got[%d].Text = %q, want %q", i, m.Text, expected[i].Text)
		}
	}
}

func TestMessage_HistoryFn_Nil(t *testing.T) {
	msg := Message{
		ID:   "11",
		Kind: MessageKindDM,
	}
	if msg.HistoryFn != nil {
		t.Error("HistoryFn must be nil for DM messages (zero value)")
	}
}

func TestCursor_Fields(t *testing.T) {
	now := time.Now()
	c := Cursor{
		MessageID: "99",
		FolderID:  "1234567890",
		UpdatedAt: now,
	}
	if c.MessageID != "99" {
		t.Errorf("want MessageID=99, got %q", c.MessageID)
	}
	if c.UpdatedAt != now {
		t.Error("UpdatedAt does not match")
	}
}
