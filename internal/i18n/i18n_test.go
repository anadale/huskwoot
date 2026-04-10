package i18n_test

import (
	"testing"

	"github.com/anadale/huskwoot/internal/i18n"
)

func TestNewBundle_RussianHeader(t *testing.T) {
	bundle, err := i18n.NewBundle("ru")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := i18n.NewLocalizer(bundle, "ru")
	got := i18n.Translate(loc, "tasks_created_header", nil)
	want := "✍️ Новые задачи записаны!"
	if got != want {
		t.Errorf("tasks_created_header (ru): got %q, want %q", got, want)
	}
}

func TestNewBundle_EnglishHeader(t *testing.T) {
	bundle, err := i18n.NewBundle("en")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := i18n.NewLocalizer(bundle, "en")
	got := i18n.Translate(loc, "tasks_created_header", nil)
	want := "✍️ New tasks recorded!"
	if got != want {
		t.Errorf("tasks_created_header (en): got %q, want %q", got, want)
	}
}

func TestTasksTruncated_Russian(t *testing.T) {
	bundle, err := i18n.NewBundle("ru")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := i18n.NewLocalizer(bundle, "ru")

	tests := []struct {
		count int
		want  string
	}{
		{1, "… и ещё 1 задача"},
		{3, "… и ещё 3 задачи"},
		{5, "… и ещё 5 задач"},
	}

	for _, tc := range tests {
		got := i18n.Translate(loc, "tasks_truncated", nil, tc.count)
		if got != tc.want {
			t.Errorf("tasks_truncated (ru, count=%d): got %q, want %q", tc.count, got, tc.want)
		}
	}
}

func TestTasksTruncated_English(t *testing.T) {
	bundle, err := i18n.NewBundle("en")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := i18n.NewLocalizer(bundle, "en")

	tests := []struct {
		count int
		want  string
	}{
		{1, "… and 1 more task"},
		{3, "… and 3 more tasks"},
		{5, "… and 5 more tasks"},
	}

	for _, tc := range tests {
		got := i18n.Translate(loc, "tasks_truncated", nil, tc.count)
		if got != tc.want {
			t.Errorf("tasks_truncated (en, count=%d): got %q, want %q", tc.count, got, tc.want)
		}
	}
}

func TestTasksMore_Russian(t *testing.T) {
	bundle, err := i18n.NewBundle("ru")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := i18n.NewLocalizer(bundle, "ru")

	tests := []struct {
		count int
		want  string
	}{
		{1, "1 задача"},
		{3, "3 задачи"},
		{5, "5 задач"},
	}

	for _, tc := range tests {
		got := i18n.Translate(loc, "tasks_more", nil, tc.count)
		if got != tc.want {
			t.Errorf("tasks_more (ru, count=%d): got %q, want %q", tc.count, got, tc.want)
		}
	}
}

func TestTasksMore_English(t *testing.T) {
	bundle, err := i18n.NewBundle("en")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := i18n.NewLocalizer(bundle, "en")

	tests := []struct {
		count int
		want  string
	}{
		{1, "1 task"},
		{3, "3 tasks"},
		{5, "5 tasks"},
	}

	for _, tc := range tests {
		got := i18n.Translate(loc, "tasks_more", nil, tc.count)
		if got != tc.want {
			t.Errorf("tasks_more (en, count=%d): got %q, want %q", tc.count, got, tc.want)
		}
	}
}
