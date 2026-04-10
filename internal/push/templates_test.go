package push_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/pushproto"
)

func newTestTemplates() *push.Templates {
	return push.NewTemplates("UTC")
}

func makeTaskPayload(t *testing.T, id, slug string, number int, summary string, deadline *time.Time, changedFields []string) []byte {
	t.Helper()
	type snap struct {
		ID          string     `json:"id"`
		Number      int        `json:"number"`
		ProjectID   string     `json:"project_id"`
		ProjectSlug string     `json:"project_slug,omitempty"`
		Summary     string     `json:"summary"`
		Deadline    *time.Time `json:"deadline,omitempty"`
	}
	type payload struct {
		Task          snap     `json:"task"`
		ChangedFields []string `json:"changedFields,omitempty"`
	}
	p := payload{
		Task:          snap{ID: id, Number: number, ProjectSlug: slug, Summary: summary, Deadline: deadline},
		ChangedFields: changedFields,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

func makeReminderPayload(t *testing.T, slot string, todayCount int) []byte {
	t.Helper()
	type payload struct {
		Slot       string `json:"slot"`
		TodayCount int    `json:"todayCount"`
	}
	raw, err := json.Marshal(payload{Slot: slot, TodayCount: todayCount})
	if err != nil {
		t.Fatalf("marshal reminder payload: %v", err)
	}
	return raw
}

func TestTemplates_Resolve_TaskCreated_BuildsHighPriorityRequest(t *testing.T) {
	tmpl := newTestTemplates()

	deadline := time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC)
	ev := &model.Event{
		Seq:      42,
		Kind:     model.EventTaskCreated,
		EntityID: "task-1",
		Payload:  makeTaskPayload(t, "task-1", "inbox", 7, "Написать отчёт", &deadline, nil),
	}

	req, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for task_created")
	}
	if req.Priority != "high" {
		t.Errorf("Priority = %q, want %q", req.Priority, "high")
	}
	if req.CollapseKey != "tasks" {
		t.Errorf("CollapseKey = %q, want %q", req.CollapseKey, "tasks")
	}
	if req.Notification.Title != "Новая задача" {
		t.Errorf("Title = %q", req.Notification.Title)
	}
	wantBody := "inbox#7: Написать отчёт (до 01.05 18:00)"
	if req.Notification.Body != wantBody {
		t.Errorf("Body = %q, want %q", req.Notification.Body, wantBody)
	}
	if req.Data.TaskID != "task-1" {
		t.Errorf("Data.TaskID = %q", req.Data.TaskID)
	}
	if req.Data.DisplayID != "inbox#7" {
		t.Errorf("Data.DisplayID = %q", req.Data.DisplayID)
	}
	if req.Data.EventSeq != 42 {
		t.Errorf("Data.EventSeq = %d", req.Data.EventSeq)
	}
	if req.Data.Kind != string(model.EventTaskCreated) {
		t.Errorf("Data.Kind = %q", req.Data.Kind)
	}
}

func TestTemplates_Resolve_TaskCreated_NoDeadline(t *testing.T) {
	tmpl := newTestTemplates()

	ev := &model.Event{
		Seq:      1,
		Kind:     model.EventTaskCreated,
		EntityID: "t1",
		Payload:  makeTaskPayload(t, "t1", "work", 3, "Позвонить клиенту", nil, nil),
	}

	req, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	wantBody := "work#3: Позвонить клиенту"
	if req.Notification.Body != wantBody {
		t.Errorf("Body = %q, want %q", req.Notification.Body, wantBody)
	}
}

func TestTemplates_Resolve_TaskUpdatedSummary_Included(t *testing.T) {
	tmpl := newTestTemplates()

	ev := &model.Event{
		Seq:     10,
		Kind:    model.EventTaskUpdated,
		Payload: makeTaskPayload(t, "t2", "proj", 5, "Новое описание", nil, []string{"summary"}),
	}

	req, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when changedFields=[summary]")
	}
	if req.Priority != "normal" {
		t.Errorf("Priority = %q, want normal", req.Priority)
	}
	if req.Notification.Title != "Задача обновлена" {
		t.Errorf("Title = %q", req.Notification.Title)
	}
	wantBody := "proj#5: Новое описание"
	if req.Notification.Body != wantBody {
		t.Errorf("Body = %q, want %q", req.Notification.Body, wantBody)
	}
}

func TestTemplates_Resolve_TaskUpdatedDeadline_Included(t *testing.T) {
	tmpl := newTestTemplates()

	ev := &model.Event{
		Seq:     11,
		Kind:    model.EventTaskUpdated,
		Payload: makeTaskPayload(t, "t3", "proj", 6, "Сдать проект", nil, []string{"deadline"}),
	}

	req, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when changedFields=[deadline]")
	}
	if req.Notification.Title != "Задача обновлена" {
		t.Errorf("Title = %q", req.Notification.Title)
	}
}

func TestTemplates_Resolve_TaskUpdatedSummaryAndDeadline_Included(t *testing.T) {
	tmpl := newTestTemplates()

	ev := &model.Event{
		Seq:     12,
		Kind:    model.EventTaskUpdated,
		Payload: makeTaskPayload(t, "t4", "p", 1, "Текст", nil, []string{"summary", "deadline"}),
	}

	_, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want ok=true", ok, err)
	}
}

func TestTemplates_Resolve_TaskUpdatedOther_ReturnsFalse(t *testing.T) {
	tmpl := newTestTemplates()

	cases := []struct {
		name   string
		fields []string
	}{
		{"details only", []string{"details"}},
		{"topic only", []string{"topic"}},
		{"status only", []string{"status"}},
		{"empty", []string{}},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &model.Event{
				Kind:    model.EventTaskUpdated,
				Payload: makeTaskPayload(t, "t5", "p", 2, "Текст", nil, tc.fields),
			}
			_, ok, err := tmpl.Resolve(context.Background(), ev)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if ok {
				t.Errorf("expected ok=false for changedFields=%v", tc.fields)
			}
		})
	}
}

func TestTemplates_Resolve_TaskCompleted_ReturnsFalse(t *testing.T) {
	tmpl := newTestTemplates()

	kinds := []model.EventKind{
		model.EventTaskCompleted,
		model.EventTaskReopened,
		model.EventTaskMoved,
		model.EventProjectCreated,
		model.EventProjectUpdated,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			ev := &model.Event{
				Kind:    kind,
				Payload: makeTaskPayload(t, "t6", "p", 3, "Текст", nil, nil),
			}
			_, ok, err := tmpl.Resolve(context.Background(), ev)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if ok {
				t.Errorf("expected ok=false for %s", kind)
			}
		})
	}
}

func TestTemplates_Resolve_ReminderSummary_BuildsNormalPriority(t *testing.T) {
	tmpl := newTestTemplates()

	ev := &model.Event{
		Seq:     99,
		Kind:    model.EventReminderSummary,
		Payload: makeReminderPayload(t, "morning", 5),
	}

	req, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for reminder_summary")
	}
	if req.Priority != "normal" {
		t.Errorf("Priority = %q, want normal", req.Priority)
	}
	if req.CollapseKey != "reminders" {
		t.Errorf("CollapseKey = %q, want reminders", req.CollapseKey)
	}
	if req.Notification.Title != "Утренняя сводка" {
		t.Errorf("Title = %q", req.Notification.Title)
	}
	wantBody := "5 задач сегодня"
	if req.Notification.Body != wantBody {
		t.Errorf("Body = %q, want %q", req.Notification.Body, wantBody)
	}
	if req.Data.Kind != string(model.EventReminderSummary) {
		t.Errorf("Data.Kind = %q", req.Data.Kind)
	}
	if req.Data.EventSeq != 99 {
		t.Errorf("Data.EventSeq = %d", req.Data.EventSeq)
	}
}

func TestTemplates_Resolve_ReminderSummary_ZeroTasks(t *testing.T) {
	tmpl := newTestTemplates()

	ev := &model.Event{
		Kind:    model.EventReminderSummary,
		Payload: makeReminderPayload(t, "evening", 0),
	}

	req, ok, err := tmpl.Resolve(context.Background(), ev)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	wantBody := "0 задач сегодня"
	if req.Notification.Body != wantBody {
		t.Errorf("Body = %q, want %q", req.Notification.Body, wantBody)
	}
}

func TestTemplates_Resolve_UnknownKind_ReturnsFalse(t *testing.T) {
	tmpl := newTestTemplates()

	kinds := []model.EventKind{
		model.EventChatReply,
		model.EventReset,
		"unknown_event",
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			ev := &model.Event{
				Kind:    kind,
				Payload: json.RawMessage(`{}`),
			}
			req, ok, err := tmpl.Resolve(context.Background(), ev)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if ok || req != nil {
				t.Errorf("expected ok=false, req=nil for %s", kind)
			}
		})
	}
}

// Verify that the result has the correct type pushproto.PushRequest.
var _ *pushproto.PushRequest = (*pushproto.PushRequest)(nil)
