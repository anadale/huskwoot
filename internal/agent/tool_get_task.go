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

type getTaskTool struct {
	tasks model.TaskService
	loc   *goI18n.Localizer
}

// NewGetTaskTool creates the get_task tool.
func NewGetTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool {
	return &getTaskTool{tasks: tasks, loc: loc}
}

func (t *getTaskTool) Name() string { return "get_task" }
func (t *getTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_get_task_desc", nil)
}
func (t *getTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_get_task_param_task_id", nil)},
		},
		"required": []string{"task_id"},
	}
}
func (t *getTaskTool) DMOnly() bool { return false }

func (t *getTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing get_task args: %w", err)
	}

	task, err := resolveTask(ctx, t.tasks, t.loc, params.TaskID)
	if err != nil {
		return "", err
	}

	data := map[string]any{
		"id":           task.ID,
		"display_id":   task.DisplayID(),
		"project_id":   task.ProjectID,
		"project_slug": task.ProjectSlug,
		"summary":      task.Summary,
		"status":       task.Status,
		"created_at":   task.CreatedAt.Format(time.RFC3339),
		"updated_at":   task.UpdatedAt.Format(time.RFC3339),
	}
	if task.Details != "" {
		data["details"] = task.Details
	}
	if task.Topic != "" {
		data["topic"] = task.Topic
	}
	if task.Deadline != nil {
		data["deadline"] = task.Deadline.Format(time.RFC3339)
	}
	if task.ClosedAt != nil {
		data["closed_at"] = task.ClosedAt.Format(time.RFC3339)
	}

	result, _ := json.Marshal(data)
	return string(result), nil
}
