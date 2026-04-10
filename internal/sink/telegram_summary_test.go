package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anadale/huskwoot/internal/model"
)

func makeFullSummary(t *testing.T) model.Summary {
	t.Helper()
	now := time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC)
	dl1 := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC) // overdue
	dl2 := time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC) // today
	dl3 := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC) // upcoming

	overdue := model.ProjectGroup{
		ProjectID:   "2",
		ProjectName: "work",
		Tasks:       []model.Task{{ID: "1", Summary: "подготовить отчёт", Deadline: &dl1, Topic: "отчётность"}},
	}
	today := model.ProjectGroup{
		ProjectID:   "2",
		ProjectName: "work",
		Tasks:       []model.Task{{ID: "2", Summary: "созвон с клиентом", Deadline: &dl2, Topic: "клиент"}},
	}
	upcoming := model.ProjectGroup{
		ProjectID:   "2",
		ProjectName: "work",
		Tasks:       []model.Task{{ID: "3", Summary: "релиз 1.4", Deadline: &dl3}},
	}
	undated := model.ProjectGroup{
		ProjectID:   "1",
		ProjectName: "inbox",
		Tasks:       []model.Task{{ID: "4", Summary: "уточнить требования"}},
	}

	return model.Summary{
		GeneratedAt:  now,
		Slot:         "morning",
		Overdue:      []model.ProjectGroup{overdue},
		Today:        []model.ProjectGroup{today},
		Upcoming:     []model.ProjectGroup{upcoming},
		Undated:      []model.ProjectGroup{undated},
		UndatedTotal: 12,
	}
}

func TestTelegramSummaryDeliverer_RussianLocalizer(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	d := NewTelegramSummaryDeliverer(newTestBot(t, srv), 123, loc)
	summary := model.Summary{
		GeneratedAt: time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC),
		Slot:        "morning",
		IsEmpty:     true,
	}

	if err := d.Deliver(context.Background(), summary); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if !strings.Contains(capturedText, "Утренняя сводка") {
		t.Errorf("Russian summary must contain 'Утренняя сводка':\n%s", capturedText)
	}
}

func TestTelegramSummaryDeliverer_EnglishLocalizer(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "en")
	d := NewTelegramSummaryDeliverer(newTestBot(t, srv), 123, loc)
	summary := model.Summary{
		GeneratedAt: time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC),
		Slot:        "morning",
		IsEmpty:     true,
	}

	if err := d.Deliver(context.Background(), summary); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if !strings.Contains(capturedText, "Morning summary") {
		t.Errorf("English summary must contain 'Morning summary':\n%s", capturedText)
	}
}

func TestTelegramSummaryDeliverer_Deliver_Success(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	d := NewTelegramSummaryDeliverer(newTestBot(t, srv), 123, loc)
	summary := makeFullSummary(t)

	if err := d.Deliver(context.Background(), summary); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	checks := []struct{ name, contain string }{
		{"emoji утренней сводки", "🌅"},
		{"заголовок", "Утренняя сводка"},
		{"дата", "17.04.2026"},
		{"заголовок просроченных", "🔴 Пропущенные"},
		{"заголовок сегодня", "📋 Нужно выполнить"},
		{"заголовок планов", "🗓 Планы"},
		{"заголовок без срока", "📦 Без срока"},
		{"название проекта", "[work]"},
		{"просроченная задача", "подготовить отчёт"},
		{"дедлайн просроченной", "просрочено с 16.04"},
		{"тема задачи", "#отчётность"},
		{"время сегодняшней задачи", "15:00"},
		{"дата плановой задачи", "20.04"},
		{"показано N из M", "показано 1 из 12"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(capturedText, c.contain) {
				t.Errorf("message does not contain %q:\n%s", c.contain, capturedText)
			}
		})
	}
}

func TestTelegramSummaryDeliverer_Deliver_Empty(t *testing.T) {
	var capturedText string
	srv := newTGTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing request form: %v", err)
		}
		capturedText = r.FormValue("text")
		successSendHandler(w, r)
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	d := NewTelegramSummaryDeliverer(newTestBot(t, srv), 123, loc)
	summary := model.Summary{
		GeneratedAt: time.Date(2026, 4, 17, 21, 0, 0, 0, time.UTC),
		Slot:        "evening",
		IsEmpty:     true,
	}

	if err := d.Deliver(context.Background(), summary); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	if !strings.Contains(capturedText, "🌙") {
		t.Errorf("message does not contain evening summary emoji:\n%s", capturedText)
	}
	if !strings.Contains(capturedText, "Всё чисто 👌 задач нет") {
		t.Errorf("message does not contain empty summary phrase:\n%s", capturedText)
	}
	if strings.Contains(capturedText, "🔴") || strings.Contains(capturedText, "📋") {
		t.Errorf("empty summary must not contain task sections:\n%s", capturedText)
	}
}

func TestTruncate_LongSummary(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	tasks := make([]model.Task, 500)
	for i := range tasks {
		tasks[i] = model.Task{
			ID:      fmt.Sprintf("%d", i+1),
			Summary: fmt.Sprintf("задача номер %d с достаточно длинным описанием для заполнения", i+1),
		}
	}
	groups := []model.ProjectGroup{
		{ProjectID: "1", ProjectName: "inbox", Tasks: tasks},
	}
	summary := model.Summary{
		GeneratedAt:  time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC),
		Slot:         "morning",
		Undated:      groups,
		UndatedTotal: 500,
	}

	full := formatSummary(loc, summary)
	truncated := truncate(loc, full, telegramSummaryMaxLen)

	if utf8.RuneCountInString(truncated) > telegramSummaryMaxLen {
		t.Errorf("truncated text length %d > %d", utf8.RuneCountInString(truncated), telegramSummaryMaxLen)
	}
	if !strings.Contains(truncated, "… и ещё") {
		t.Errorf("truncated text does not contain tail:\n%.200s", truncated)
	}

	// Count removed task lines and verify N in the tail.
	originalTaskLines := strings.Count(full, "\n    — ")
	truncatedTaskLines := strings.Count(truncated, "\n    — ")
	removed := originalTaskLines - truncatedTaskLines
	expectedSuffix := fmt.Sprintf("… и ещё %d задач", removed)
	if !strings.Contains(truncated, expectedSuffix) {
		t.Errorf("truncated text does not contain correct tail %q", expectedSuffix)
	}
}

func TestTruncate_FitsWithinLimit(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	text := "короткий текст"
	result := truncate(loc, text, telegramSummaryMaxLen)
	if result != text {
		t.Errorf("truncate modified text that fits within the limit")
	}
}

func TestTelegramSummaryDeliverer_Deliver_APIError(t *testing.T) {
	srv := newTGTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tgResponse{
			OK:          false,
			ErrorCode:   400,
			Description: "Bad Request: chat not found",
		})
	})
	defer srv.Close()

	loc := makeLocalizer(t, "ru")
	d := NewTelegramSummaryDeliverer(newTestBot(t, srv), 999, loc)
	summary := makeFullSummary(t)

	err := d.Deliver(context.Background(), summary)
	if err == nil {
		t.Fatal("want an error when API responds with ok=false")
	}
	if !strings.Contains(err.Error(), "sending summary") {
		t.Errorf("error does not contain operation context: %v", err)
	}
}

func TestFormatSummary_AfternoonSlot(t *testing.T) {
	loc := makeLocalizer(t, "ru")
	now := time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC)
	summary := model.Summary{
		GeneratedAt: now,
		Slot:        "afternoon",
		IsEmpty:     true,
	}
	text := formatEmptySummary(loc, summary)
	if !strings.Contains(text, "☀️") {
		t.Errorf("afternoon summary does not contain emoji ☀️:\n%s", text)
	}
	if !strings.Contains(text, "Дневная сводка") {
		t.Errorf("afternoon summary does not contain header:\n%s", text)
	}
}
