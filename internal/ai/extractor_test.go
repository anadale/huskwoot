package ai_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/dateparse"
	"github.com/anadale/huskwoot/internal/model"
)

// fixedNow is a fixed timestamp for deterministic tests.
var fixedNow = time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)

func newTestExtractor(t *testing.T, mock *mockCompleter) *ai.TaskExtractor {
	t.Helper()
	e, err := ai.NewTaskExtractor(mock, ai.ExtractorConfig{
		UserName:            "Григорий",
		ConfidenceThreshold: 0.5,
		Now:                 func() time.Time { return fixedNow },
		DateParse: dateparse.Config{
			TimeOfDay: dateparse.TimeOfDay{
				Morning:   11,
				Lunch:     12,
				Afternoon: 14,
				Evening:   20,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}
	return e
}

func TestTaskExtractor_SingleTask(t *testing.T) {
	resp := `[{"summary":"Написать отчёт","deadline":null,"confidence":0.9}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("напишу отчёт"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	if tasks[0].Summary != "Написать отчёт" {
		t.Errorf("Summary = %q, want %q", tasks[0].Summary, "Написать отчёт")
	}
	if tasks[0].ID != "" {
		t.Errorf("ID = %q, want empty string (assigned by TaskStore)", tasks[0].ID)
	}
}

func TestTaskExtractor_MultipleTasks(t *testing.T) {
	resp := `[
		{"summary":"Загрузить данные","deadline":"2026-04-12T00:00:00Z","confidence":0.95},
		{"summary":"Отправить отчёт","deadline":null,"confidence":0.85},
		{"summary":"Обновить документацию","deadline":"2026-04-15","confidence":0.7}
	]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("обещаю всё сделать"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("len(tasks) = %d, want 3", len(tasks))
	}

	expectedSummaries := []string{"Загрузить данные", "Отправить отчёт", "Обновить документацию"}
	for i, want := range expectedSummaries {
		if tasks[i].Summary != want {
			t.Errorf("tasks[%d].Summary = %q, want %q", i, tasks[i].Summary, want)
		}
		if tasks[i].ID != "" {
			t.Errorf("tasks[%d].ID = %q, want empty string (assigned by TaskStore)", i, tasks[i].ID)
		}
	}

	// First task must have a deadline
	if tasks[0].Deadline == nil {
		t.Error("tasks[0].Deadline = nil, want a deadline")
	}
	// Second — no deadline
	if tasks[1].Deadline != nil {
		t.Errorf("tasks[1].Deadline = %v, want nil", tasks[1].Deadline)
	}
}

func TestTaskExtractor_EmptyArray(t *testing.T) {
	mock := &mockCompleter{response: `[]`}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("ничего не обещал"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("len(tasks) = %d, want 0", len(tasks))
	}
}

func TestTaskExtractor_MixedConfidence(t *testing.T) {
	resp := `[
		{"summary":"Высокая уверенность","deadline":null,"confidence":0.9},
		{"summary":"Низкая уверенность","deadline":null,"confidence":0.3},
		{"summary":"На пороге","deadline":null,"confidence":0.5}
	]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("разные обещания"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	// Only tasks with confidence >= 0.5 (threshold) should remain
	if len(tasks) != 2 {
		t.Fatalf("len(tasks) = %d, want 2 (task with confidence 0.3 filtered out)", len(tasks))
	}
	if tasks[0].Summary != "Высокая уверенность" {
		t.Errorf("tasks[0].Summary = %q, want %q", tasks[0].Summary, "Высокая уверенность")
	}
	if tasks[1].Summary != "На пороге" {
		t.Errorf("tasks[1].Summary = %q, want %q", tasks[1].Summary, "На пороге")
	}
	// ID is not assigned by the extractor — TaskStore generates it.
	if tasks[0].ID != "" {
		t.Errorf("tasks[0].ID = %q, want empty string", tasks[0].ID)
	}
	if tasks[1].ID != "" {
		t.Errorf("tasks[1].ID = %q, want empty string", tasks[1].ID)
	}
}

func TestTaskExtractor_WithDeadline(t *testing.T) {
	resp := `[{"summary":"Написать отчёт по проекту","deadline":"2026-04-12T00:00:00Z","confidence":0.9}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	msg := ownerMsg("сделаю отчёт завтра")
	tasks, err := e.Extract(context.Background(), msg, nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice, want a task")
	}
	task := tasks[0]
	if task.Summary != "Написать отчёт по проекту" {
		t.Errorf("Summary = %q, want %q", task.Summary, "Написать отчёт по проекту")
	}
	if task.Deadline == nil {
		t.Error("Deadline = nil, want a deadline")
	} else {
		expected := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
		if !task.Deadline.Equal(expected) {
			t.Errorf("Deadline = %v, want %v", task.Deadline, expected)
		}
	}
	if task.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", task.Confidence)
	}
}

func TestTaskExtractor_WithoutDeadline(t *testing.T) {
	resp := `[{"summary":"Посмотреть на запрос","deadline":null,"confidence":0.8}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("посмотрю"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice, want a task")
	}
	task := tasks[0]
	if task.Summary != "Посмотреть на запрос" {
		t.Errorf("Summary = %q, want %q", task.Summary, "Посмотреть на запрос")
	}
	if task.Deadline != nil {
		t.Errorf("Deadline = %v, want nil", task.Deadline)
	}
}

func TestTaskExtractor_FromMeetingTranscript(t *testing.T) {
	resp := `[{"summary":"Реализовать интеграцию с внешним API","deadline":null,"confidence":0.95}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	history := []model.HistoryEntry{
		{
			AuthorName: "Менеджер",
			Text:       "Нужно сделать интеграцию с внешним API до конца недели",
			Timestamp:  fixedNow.Add(-10 * time.Minute),
		},
		{
			AuthorName: "Григорий",
			Text:       "Григорий обещал реализовать интеграцию с внешним API",
			Timestamp:  fixedNow.Add(-5 * time.Minute),
		},
	}

	msg := model.Message{
		ID:         "msg10",
		Author:     "user123",
		AuthorName: "Григорий",
		Text:       "Григорий обещал реализовать интеграцию с внешним API",
		Timestamp:  fixedNow,
		Source:     model.Source{Kind: "telegram", ID: "chat1"},
	}

	tasks, err := e.Extract(context.Background(), msg, history)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice, want a task")
	}
	task := tasks[0]
	if task.Summary != "Реализовать интеграцию с внешним API" {
		t.Errorf("Summary = %q, want %q", task.Summary, "Реализовать интеграцию с внешним API")
	}
	if task.Confidence != 0.95 {
		t.Errorf("Confidence = %v, want 0.95", task.Confidence)
	}
	if mock.calls != 1 {
		t.Errorf("want 1 AI call, got %d", mock.calls)
	}
}

func TestTaskExtractor_LowConfidence(t *testing.T) {
	resp := `[{"summary":"Что-то сделать","deadline":null,"confidence":0.3}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("может, посмотрю потом"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("Extract() returned %d tasks at low confidence, want 0", len(tasks))
	}
}

func TestTaskExtractor_AIError(t *testing.T) {
	mock := &mockCompleter{err: errors.New("сервис недоступен")}
	e := newTestExtractor(t, mock)

	_, err := e.Extract(context.Background(), ownerMsg("сделаю завтра"), nil)
	if err == nil {
		t.Error("Extract() must return an error when AI client errors")
	}
}

func TestTaskExtractor_InvalidJSON(t *testing.T) {
	mock := &mockCompleter{response: "Задача извлечена: написать отчёт"}
	e := newTestExtractor(t, mock)

	_, err := e.Extract(context.Background(), ownerMsg("сделаю отчёт"), nil)
	if err == nil {
		t.Error("Extract() must return an error for invalid JSON")
	}
}

func TestTaskExtractor_RelativeDeadlineFromModel(t *testing.T) {
	resp := `[{"summary":"Сделать что-то","deadline":"завтра","confidence":0.85}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("сделаю завтра"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice, want a task")
	}
	task := tasks[0]
	if task.Deadline == nil {
		t.Fatal("Deadline = nil, want tomorrow's date")
	}
	expectedDay := fixedNow.Day() + 1
	if task.Deadline.Day() != expectedDay {
		t.Errorf("Deadline.Day() = %d, want %d", task.Deadline.Day(), expectedDay)
	}
}

func TestTaskExtractor_TaskSourceAndOrigin(t *testing.T) {
	resp := `[{"summary":"Отправить отчёт","deadline":null,"confidence":0.9}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	src := model.Source{Kind: "telegram", ID: "chat42", Name: "Рабочий чат"}
	msg := model.Message{
		ID:         "msg99",
		Author:     "user123",
		AuthorName: "Григорий",
		Text:       "отправлю отчёт",
		Timestamp:  fixedNow,
		Source:     src,
	}

	tasks, err := e.Extract(context.Background(), msg, nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice")
	}
	task := tasks[0]
	if task.Source != src {
		t.Errorf("Source = %+v, want %+v", task.Source, src)
	}
	if task.SourceMessage.ID != "msg99" {
		t.Errorf("SourceMessage.ID = %q, want %q", task.SourceMessage.ID, "msg99")
	}
	if !task.CreatedAt.Equal(fixedNow) {
		t.Errorf("CreatedAt = %v, want %v", task.CreatedAt, fixedNow)
	}
}

func TestTaskExtractor_ReactionMessage(t *testing.T) {
	resp := `[{"summary":"Сделать отчёт","deadline":null,"confidence":0.88}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	msg := model.Message{
		ID:         "msg5",
		Author:     "user123",
		AuthorName: "Григорий",
		Timestamp:  fixedNow,
		Source:     model.Source{Kind: "telegram", ID: "chat1"},
		ReplyTo: &model.Message{
			Author:     "other",
			AuthorName: "Другой",
			Text:       "Напишешь отчёт?",
		},
		Reaction: &model.Reaction{Emoji: "👍", UserID: "user123"},
	}

	tasks, err := e.Extract(context.Background(), msg, nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice for a reaction promise")
	}
}

// TestParseDeadline verifies parseDeadline via TaskExtractor.
// Indirect testing — via Extract with various deadlines in the model response.
func TestParseDeadline_Variants(t *testing.T) {
	cases := []struct {
		name        string
		deadline    string
		expectNil   bool
		expectHour  int
		expectDay   int
		expectDelta time.Duration
	}{
		{
			name:      "ISO 8601",
			deadline:  "2026-05-01T10:00:00Z",
			expectNil: false,
		},
		{
			name:      "дата без времени",
			deadline:  "2026-05-01",
			expectNil: false,
		},
		{
			name:      "null строка",
			deadline:  "null",
			expectNil: true,
		},
		{
			name:      "пустая строка не приходит через JSON",
			deadline:  "2026-04-12T00:00:00Z",
			expectNil: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jsonResp := `[{"summary":"задача","deadline":"` + tc.deadline + `","confidence":0.9}]`
			if tc.expectNil && tc.deadline == "null" {
				jsonResp = `[{"summary":"задача","deadline":null,"confidence":0.9}]`
			}
			mock := &mockCompleter{response: jsonResp}
			e := newTestExtractor(t, mock)

			tasks, err := e.Extract(context.Background(), ownerMsg("тест"), nil)
			if err != nil {
				t.Fatalf("Extract() error: %v", err)
			}
			if len(tasks) == 0 {
				t.Fatal("tasks is empty")
			}
			task := tasks[0]
			if tc.expectNil && task.Deadline != nil {
				t.Errorf("Deadline = %v, want nil", task.Deadline)
			}
			if !tc.expectNil && task.Deadline == nil {
				t.Error("Deadline = nil, want a value")
			}
		})
	}
}

func TestTaskExtractor_ConfidenceAtThreshold(t *testing.T) {
	// Exactly at threshold — the task must be created
	resp := `[{"summary":"Задача на пороге","deadline":null,"confidence":0.5}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("что-то сделаю"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Error("Extract() returned empty slice when confidence == threshold, want a task")
	}
}

func TestTaskExtractor_MarkdownFenceStripped(t *testing.T) {
	// LLM may wrap JSON in a markdown block — it must be stripped.
	resp := "```json\n[{\"summary\":\"Купить билеты\",\"deadline\":null,\"confidence\":0.9}]\n```"
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("куплю билеты"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error with markdown-fence: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice, want a task")
	}
	if tasks[0].Summary != "Купить билеты" {
		t.Errorf("Summary = %q, want %q", tasks[0].Summary, "Купить билеты")
	}
}

func TestTaskExtractor_UnknownDeadlineFormatDoesNotLoseTask(t *testing.T) {
	// An unrecognised deadline format must not cause the task to be lost.
	resp := `[{"summary":"Сделать что-нибудь","deadline":"конец недели","confidence":0.85}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("сделаю на этой неделе"), nil)
	if err != nil {
		t.Fatalf("Extract() must not return an error for unknown deadline: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice, task must be saved without deadline")
	}
	if tasks[0].Deadline != nil {
		t.Errorf("Deadline = %v, want nil for unrecognised format", tasks[0].Deadline)
	}
}

// TestParseDeadline_Extended verifies extended date formats via Extract.
// fixedNow = 2026-04-11 12:00:00 UTC.
// TimeOfDay defaults: Morning=11, Afternoon=14, Evening=20.
func TestParseDeadline_Extended(t *testing.T) {
	cases := []struct {
		deadline string
		wantTime time.Time
	}{
		// Relative offsets.
		{"через 2 часа", fixedNow.Add(2 * time.Hour)},
		{"через 1 час", fixedNow.Add(time.Hour)},
		{"через 3 часа", fixedNow.Add(3 * time.Hour)},
		{"через 5 часов", fixedNow.Add(5 * time.Hour)},
		{"через 30 минут", fixedNow.Add(30 * time.Minute)},
		{"через 1 минуту", fixedNow.Add(time.Minute)},
		{"через 45 минут", fixedNow.Add(45 * time.Minute)},
		{"через 3 дня", fixedNow.AddDate(0, 0, 3)},
		{"через 1 день", fixedNow.AddDate(0, 0, 1)},
		{"через 7 дней", fixedNow.AddDate(0, 0, 7)},
		// day after tomorrow
		{"послезавтра", time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)},
		// combinations of day + time of day
		{"сегодня утром", time.Date(2026, 4, 11, 11, 0, 0, 0, time.UTC)},
		{"сегодня днём", time.Date(2026, 4, 11, 14, 0, 0, 0, time.UTC)},
		{"сегодня вечером", time.Date(2026, 4, 11, 20, 0, 0, 0, time.UTC)},
		// "tonight" = the next midnight (not earlier today)
		{"сегодня ночью", time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)},
		{"завтра утром", time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC)},
		{"завтра днём", time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)},
		{"завтра вечером", time.Date(2026, 4, 12, 20, 0, 0, 0, time.UTC)},
		// "tomorrow night" = the night after tomorrow
		{"завтра ночью", time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)},
		{"послезавтра утром", time.Date(2026, 4, 13, 11, 0, 0, 0, time.UTC)},
		{"послезавтра вечером", time.Date(2026, 4, 13, 20, 0, 0, 0, time.UTC)},
		// preposition "by"
		{"к вечеру", time.Date(2026, 4, 11, 20, 0, 0, 0, time.UTC)},
		{"к утру", time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC)},
		{"к обеду", time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)},
		// after lunch
		{"после обеда", time.Date(2026, 4, 11, 14, 0, 0, 0, time.UTC)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.deadline, func(t *testing.T) {
			resp := fmt.Sprintf(`[{"summary":"задача","deadline":"%s","confidence":0.9}]`, tc.deadline)
			mock := &mockCompleter{response: resp}
			e := newTestExtractor(t, mock)

			tasks, err := e.Extract(context.Background(), ownerMsg("тест"), nil)
			if err != nil {
				t.Fatalf("Extract() error: %v", err)
			}
			if len(tasks) == 0 || tasks[0].Deadline == nil {
				t.Fatalf("tasks is empty or deadline is nil for %q", tc.deadline)
			}
			if !tasks[0].Deadline.Equal(tc.wantTime) {
				t.Errorf("Deadline = %v, want %v", tasks[0].Deadline, tc.wantTime)
			}
		})
	}
}

// TestParseDeadline_CustomTimeOfDay verifies that custom TimeOfDay values
// are used during parsing.
func TestParseDeadline_CustomTimeOfDay(t *testing.T) {
	cases := []struct {
		deadline string
		wantHour int
	}{
		{"завтра утром", 9},
		{"завтра днём", 13},
		{"завтра вечером", 21},
		{"к вечеру", 21},
		{"к утру", 9},
		{"после обеда", 13},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.deadline, func(t *testing.T) {
			resp := fmt.Sprintf(`[{"summary":"задача","deadline":"%s","confidence":0.9}]`, tc.deadline)
			extractor, _ := ai.NewTaskExtractor(&mockCompleter{response: resp}, ai.ExtractorConfig{
				UserName:            "Григорий",
				ConfidenceThreshold: 0.5,
				Now:                 func() time.Time { return fixedNow },
				DateParse: dateparse.Config{
					TimeOfDay: dateparse.TimeOfDay{
						Morning:   9,
						Afternoon: 13,
						Evening:   21,
					},
				},
			})

			tasks, err := extractor.Extract(context.Background(), ownerMsg("тест"), nil)
			if err != nil {
				t.Fatalf("Extract() error: %v", err)
			}
			if len(tasks) == 0 || tasks[0].Deadline == nil {
				t.Fatalf("deadline is nil for %q", tc.deadline)
			}
			if tasks[0].Deadline.Hour() != tc.wantHour {
				t.Errorf("Hour() = %d, want %d for %q", tasks[0].Deadline.Hour(), tc.wantHour, tc.deadline)
			}
		})
	}
}

func TestTaskExtractor_EmptyHistory(t *testing.T) {
	resp := `[{"summary":"Принести кофе","deadline":null,"confidence":0.7}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	// Empty history — there must be no template rendering errors
	tasks, err := e.Extract(context.Background(), ownerMsg("принесу"), []model.HistoryEntry{})
	if err != nil {
		t.Fatalf("Extract() with empty history returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Error("Extract() returned empty slice")
	}
}

func TestTaskExtractor_AliasesInSystemPrompt(t *testing.T) {
	mock := &mockCompleter{response: `[{"summary":"задача","deadline":null,"confidence":0.9}]`}
	e, err := ai.NewTaskExtractor(mock, ai.ExtractorConfig{
		UserName:            "Григорий",
		Aliases:             []string{"Гриша", "Greg"},
		ConfidenceThreshold: 0.5,
		Now:                 func() time.Time { return fixedNow },
		DateParse: dateparse.Config{
			TimeOfDay: dateparse.TimeOfDay{
				Morning:   11,
				Lunch:     12,
				Afternoon: 14,
				Evening:   20,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}

	_, err = e.Extract(context.Background(), ownerMsg("тест"), nil)
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	// Verify the call went through (a prompt with aliases did not break the template)
	if mock.calls != 1 {
		t.Errorf("want 1 AI call, got %d", mock.calls)
	}
}

func TestTaskExtractor_ContextAndTopicParsed(t *testing.T) {
	resp := `[{
		"summary": "исправить ошибку авторизации",
		"context": "Иван сообщил о проблемах с OAuth-токенами в мобильном приложении",
		"topic": "Аутентификация",
		"deadline": null,
		"confidence": 0.9
	}]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("исправлю ошибку"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Details != "Иван сообщил о проблемах с OAuth-токенами в мобильном приложении" {
		t.Errorf("Details = %q, want value from model", task.Details)
	}
	if task.Topic != "Аутентификация" {
		t.Errorf("Topic = %q, want %q", task.Topic, "Аутентификация")
	}
}

func TestTaskExtractor_EmptyContextAndTopic(t *testing.T) {
	// Response without context and topic — fields are absent or empty
	cases := []struct {
		name string
		resp string
	}{
		{
			name: "поля отсутствуют",
			resp: `[{"summary":"Написать отчёт","deadline":null,"confidence":0.9}]`,
		},
		{
			name: "поля пустые строки",
			resp: `[{"summary":"Написать отчёт","context":"","topic":"","deadline":null,"confidence":0.9}]`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockCompleter{response: tc.resp}
			e := newTestExtractor(t, mock)

			tasks, err := e.Extract(context.Background(), ownerMsg("напишу"), nil)
			if err != nil {
				t.Fatalf("Extract() returned error: %v", err)
			}
			if len(tasks) != 1 {
				t.Fatalf("len(tasks) = %d, want 1", len(tasks))
			}
			task := tasks[0]
			if task.Details != "" {
				t.Errorf("Details = %q, want empty string", task.Details)
			}
			if task.Topic != "" {
				t.Errorf("Topic = %q, want empty string", task.Topic)
			}
			if task.Summary != "Написать отчёт" {
				t.Errorf("Summary = %q, want %q", task.Summary, "Написать отчёт")
			}
		})
	}
}

func TestNewTaskExtractor_DefaultTemplates(t *testing.T) {
	// NewTaskExtractor with empty SystemTemplate/UserTemplate uses default templates.
	mock := &mockCompleter{response: `[{"summary":"задача","deadline":null,"confidence":0.9}]`}
	e, err := ai.NewTaskExtractor(mock, ai.ExtractorConfig{
		UserName:            "Григорий",
		ConfidenceThreshold: 0.5,
		Now:                 func() time.Time { return fixedNow },
		DateParse: dateparse.Config{
			TimeOfDay: dateparse.TimeOfDay{
				Morning:   11,
				Lunch:     12,
				Afternoon: 14,
				Evening:   20,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}

	_, err = e.Extract(context.Background(), ownerMsg("напишу отчёт"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}

	// The system prompt must contain text from the default template
	if !strings.Contains(mock.lastSystem, "Извлеки обещания пользователя Григорий") {
		t.Errorf("system prompt does not contain default text, got: %s", mock.lastSystem)
	}
}

func TestNewTaskExtractor_CustomTemplates(t *testing.T) {
	// NewTaskExtractor with custom templates uses the provided templates.
	customSystem := "Кастомный системный промпт для {{.UserName}}"
	customUser := "Кастомный: {{.Text}}"

	mock := &mockCompleter{response: `[{"summary":"задача","deadline":null,"confidence":0.9}]`}
	e, err := ai.NewTaskExtractor(mock, ai.ExtractorConfig{
		UserName:            "Григорий",
		ConfidenceThreshold: 0.5,
		Now:                 func() time.Time { return fixedNow },
		SystemTemplate:      customSystem,
		UserTemplate:        customUser,
		DateParse: dateparse.Config{
			TimeOfDay: dateparse.TimeOfDay{
				Morning:   11,
				Lunch:     12,
				Afternoon: 14,
				Evening:   20,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}

	_, err = e.Extract(context.Background(), ownerMsg("напишу отчёт"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}

	if !strings.Contains(mock.lastSystem, "Кастомный системный промпт для Григорий") {
		t.Errorf("system prompt does not contain custom text, got: %s", mock.lastSystem)
	}
	if !strings.Contains(mock.lastUser, "Кастомный:") {
		t.Errorf("user prompt does not contain custom text, got: %s", mock.lastUser)
	}
}

func TestTaskExtractor_ContextTopicWithLowConfidenceFiltered(t *testing.T) {
	// The presence of context and topic does not affect confidence filtering
	resp := `[
		{"summary":"Высокий confidence","context":"важный контекст","topic":"Тема А","deadline":null,"confidence":0.9},
		{"summary":"Низкий confidence","context":"неважный контекст","topic":"Тема Б","deadline":null,"confidence":0.2}
	]`
	mock := &mockCompleter{response: resp}
	e := newTestExtractor(t, mock)

	tasks, err := e.Extract(context.Background(), ownerMsg("разные обещания"), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1 (task with low confidence filtered out)", len(tasks))
	}
	if tasks[0].Summary != "Высокий confidence" {
		t.Errorf("tasks[0].Summary = %q, want %q", tasks[0].Summary, "Высокий confidence")
	}
	if tasks[0].Details != "важный контекст" {
		t.Errorf("tasks[0].Details = %q, want %q", tasks[0].Details, "важный контекст")
	}
	if tasks[0].Topic != "Тема А" {
		t.Errorf("tasks[0].Topic = %q, want %q", tasks[0].Topic, "Тема А")
	}
}

// msgWithTimestamp returns a test message with the given timestamp.
func msgWithTimestamp(text string, ts time.Time) model.Message {
	return model.Message{
		ID:         "msg1",
		Author:     "user123",
		AuthorName: "Григорий",
		Text:       text,
		Timestamp:  ts,
		Source:     model.Source{Kind: "telegram", ID: "chat1"},
	}
}

func TestTaskExtractor_CreatedAtUsesMessageTimestamp(t *testing.T) {
	// CreatedAt must use msg.Timestamp, not cfg.Now().
	msgTime := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	mock := &mockCompleter{response: `[{"summary":"Сделать задачу","deadline":null,"confidence":0.9}]`}
	e := newTestExtractor(t, mock) // cfg.Now = fixedNow (2026-04-11)

	tasks, err := e.Extract(context.Background(), msgWithTimestamp("сделаю задачу", msgTime), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("Extract() returned empty slice")
	}
	if !tasks[0].CreatedAt.Equal(msgTime) {
		t.Errorf("CreatedAt = %v, want msg.Timestamp %v", tasks[0].CreatedAt, msgTime)
	}
}

func TestTaskExtractor_RelativeDeadlineFromMessageTimestamp(t *testing.T) {
	// Relative deadline "tomorrow" must be computed from msg.Timestamp, not cfg.Now().
	msgTime := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC) // Jan 15
	mock := &mockCompleter{response: `[{"summary":"Сделать что-то","deadline":"завтра","confidence":0.85}]`}
	e := newTestExtractor(t, mock) // cfg.Now = fixedNow (2026-04-11)

	tasks, err := e.Extract(context.Background(), msgWithTimestamp("сделаю завтра", msgTime), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 || tasks[0].Deadline == nil {
		t.Fatal("Extract() returned empty slice or deadline is nil")
	}
	// "tomorrow" from Jan 15 = Jan 16
	expected := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	if !tasks[0].Deadline.Equal(expected) {
		t.Errorf("Deadline = %v, want %v (tomorrow from message timestamp)", tasks[0].Deadline, expected)
	}
}

func TestTaskExtractor_FallsBackToNowWhenTimestampZero(t *testing.T) {
	// When msg.Timestamp is zero, cfg.Now() is used to compute the deadline.
	mock := &mockCompleter{response: `[{"summary":"Сделать что-то","deadline":"завтра","confidence":0.85}]`}
	e := newTestExtractor(t, mock) // cfg.Now = fixedNow (2026-04-11)

	msg := model.Message{
		ID:         "msg1",
		Author:     "user123",
		AuthorName: "Григорий",
		Text:       "сделаю завтра",
		Timestamp:  time.Time{}, // zero time
		Source:     model.Source{Kind: "telegram", ID: "chat1"},
	}
	tasks, err := e.Extract(context.Background(), msg, nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	if len(tasks) == 0 || tasks[0].Deadline == nil {
		t.Fatal("Extract() returned empty slice or deadline is nil")
	}
	// "tomorrow" from fixedNow (2026-04-11) = 2026-04-12
	expected := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	if !tasks[0].Deadline.Equal(expected) {
		t.Errorf("Deadline = %v, want %v (tomorrow from cfg.Now when Timestamp is zero)", tasks[0].Deadline, expected)
	}
}

func TestTaskExtractor_NowInPromptUsesMessageTimestamp(t *testing.T) {
	// "Current date and time" in the prompt must reflect the message timestamp, not processing time.
	msgTime := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	mock := &mockCompleter{response: `[{"summary":"задача","deadline":null,"confidence":0.9}]`}
	e := newTestExtractor(t, mock) // cfg.Now = fixedNow (2026-04-11)

	_, err := e.Extract(context.Background(), msgWithTimestamp("сделаю", msgTime), nil)
	if err != nil {
		t.Fatalf("Extract() returned error: %v", err)
	}
	wantTime := "2026-01-15 10:30:00 +00:00"
	if !strings.Contains(mock.lastUser, wantTime) {
		t.Errorf("user prompt does not contain message timestamp %q, got:\n%s", wantTime, mock.lastUser)
	}
}

func TestTaskExtractor_KnownProjectsInSystemPrompt(t *testing.T) {
	// Known projects from ProjectsFn must appear in the system prompt.
	cap := &capturingCompleter{
		response: `[{"summary":"тест","project":"","context":"","topic":"","deadline":null,"confidence":0.9}]`,
	}
	projects := []string{"Помощь", "Аналитика"}
	e, err := ai.NewTaskExtractor(cap, ai.ExtractorConfig{
		UserName:            "Григорий",
		ConfidenceThreshold: 0.5,
		Now:                 func() time.Time { return fixedNow },
		ProjectsFn:          func(_ context.Context) ([]string, error) { return projects, nil },
		DateParse: dateparse.Config{
			TimeOfDay: dateparse.TimeOfDay{
				Morning:   11,
				Lunch:     12,
				Afternoon: 14,
				Evening:   20,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}

	_, _ = e.Extract(context.Background(), ownerMsg("тестовое сообщение"), nil)

	if !strings.Contains(cap.systemPrompt, "Помощь") {
		t.Errorf("system prompt does not contain 'Помощь', prompt:\n%s", cap.systemPrompt)
	}
	if !strings.Contains(cap.systemPrompt, "Аналитика") {
		t.Errorf("system prompt does not contain 'Аналитика', prompt:\n%s", cap.systemPrompt)
	}
}

func TestExtractor_SystemPromptHasDeadlineRules(t *testing.T) {
	cap := &capturingCompleter{
		response: `[{"summary":"тест","project":"","context":"","topic":"","deadline":null,"confidence":0.9}]`,
	}
	e, err := ai.NewTaskExtractor(cap, ai.ExtractorConfig{
		UserName:            "Тестовый",
		ConfidenceThreshold: 0.5,
		Now:                 func() time.Time { return fixedNow },
		DateParse: dateparse.Config{
			TimeOfDay: dateparse.TimeOfDay{
				Morning:   11,
				Lunch:     12,
				Afternoon: 14,
				Evening:   20,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}

	_, _ = e.Extract(context.Background(), ownerMsg("тестовое сообщение"), nil)

	if !strings.Contains(cap.systemPrompt, "Расчёт дедлайна:") {
		t.Errorf("system prompt does not contain 'Расчёт дедлайна:', prompt:\n%s", cap.systemPrompt)
	}
	if !strings.Contains(cap.systemPrompt, "ISO 8601") {
		t.Errorf("system prompt does not contain 'ISO 8601', prompt:\n%s", cap.systemPrompt)
	}
	if !strings.Contains(cap.systemPrompt, "русское натуральное выражение") {
		t.Errorf("system prompt does not contain 'русское натуральное выражение', prompt:\n%s", cap.systemPrompt)
	}
	if !strings.Contains(cap.systemPrompt, "завтра") {
		t.Errorf("system prompt does not contain example 'завтра', prompt:\n%s", cap.systemPrompt)
	}
}
