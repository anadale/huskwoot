package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type listTasksTool struct {
	tasks model.TaskService
	loc   *goI18n.Localizer
}

// NewListTasksTool creates the list_tasks tool.
func NewListTasksTool(tasks model.TaskService, loc *goI18n.Localizer) Tool {
	return &listTasksTool{tasks: tasks, loc: loc}
}

func (t *listTasksTool) Name() string { return "list_tasks" }
func (t *listTasksTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_list_tasks_desc", nil)
}
func (t *listTasksTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_list_tasks_param_project_id", nil)},
			"status":     map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_list_tasks_param_status", nil)},
			"query":      map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_list_tasks_param_query", nil)},
			"limit":      map[string]any{"type": "integer", "description": huskwootI18n.Translate(t.loc, "tool_list_tasks_param_limit", nil)},
		},
	}
}
func (t *listTasksTool) DMOnly() bool { return false }

func (t *listTasksTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ProjectID string `json:"project_id"`
		Status    string `json:"status"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing list_tasks args: %w", err)
	}

	status := params.Status
	if status == "" {
		status = "open"
	}

	filter := model.TaskFilter{Status: status, Query: params.Query, Limit: params.Limit}
	tasks, err := t.tasks.ListTasks(ctx, params.ProjectID, filter)
	if err != nil {
		return "", err
	}

	type taskItem struct {
		ID          string `json:"id"`
		DisplayID   string `json:"display_id"`
		ProjectID   string `json:"project_id"`
		ProjectSlug string `json:"project_slug"`
		Summary     string `json:"summary"`
		Status      string `json:"status"`
		Deadline    string `json:"deadline,omitempty"`
		ClosedAt    string `json:"closed_at,omitempty"`
	}
	items := make([]taskItem, len(tasks))
	for i, task := range tasks {
		item := taskItem{
			ID:          task.ID,
			DisplayID:   task.DisplayID(),
			ProjectID:   task.ProjectID,
			ProjectSlug: task.ProjectSlug,
			Summary:     task.Summary,
			Status:      task.Status,
		}
		if task.Deadline != nil {
			item.Deadline = task.Deadline.Format(time.RFC3339)
		}
		if task.ClosedAt != nil {
			item.ClosedAt = task.ClosedAt.Format(time.RFC3339)
		}
		items[i] = item
	}

	result, _ := json.Marshal(items)
	return string(result), nil
}
