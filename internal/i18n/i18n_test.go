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

func TestProjectAliasKeysPresent(t *testing.T) {
	keys := []string{
		"tool_get_project_desc",
		"tool_get_project_param_ref",
		"tool_update_project_desc",
		"tool_update_project_param_ref",
		"tool_update_project_param_name",
		"tool_update_project_param_description",
		"tool_update_project_param_slug",
		"agent_project_updated",
		"agent_project_slug_conflict",
		"tool_add_project_alias_desc",
		"tool_add_project_alias_param_ref",
		"tool_add_project_alias_param_alias",
		"tool_remove_project_alias_desc",
		"tool_remove_project_alias_param_ref",
		"tool_remove_project_alias_param_alias",
		"agent_alias_added",
		"agent_alias_removed",
		"agent_alias_required",
		"tool_create_project_param_aliases",
		"err_alias_invalid",
		"err_alias_taken",
		"err_alias_conflicts_with_name",
		"err_alias_limit_reached",
		"err_alias_not_found",
		"err_alias_forbidden_for_inbox",
	}

	for _, lang := range []string{"ru", "en"} {
		bundle, err := i18n.NewBundle(lang)
		if err != nil {
			t.Fatalf("NewBundle(%s): %v", lang, err)
		}
		loc := i18n.NewLocalizer(bundle, lang)
		for _, key := range keys {
			got := i18n.Translate(loc, key, nil)
			if got == key {
				t.Errorf("locale %s: key %q is missing or untranslated", lang, key)
			}
		}
	}
}
