package agent

import (
	"context"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type cancelTaskTool struct {
	tasks model.TaskService
	loc   *goI18n.Localizer
}

// NewCancelTaskTool creates the cancel_task tool.
func NewCancelTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool {
	return &cancelTaskTool{tasks: tasks, loc: loc}
}

func (t *cancelTaskTool) Name() string { return "cancel_task" }
func (t *cancelTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_cancel_task_desc", nil)
}
func (t *cancelTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_cancel_task_param_task_id", nil)},
		},
		"required": []string{"task_id"},
	}
}
func (t *cancelTaskTool) DMOnly() bool { return false }

func (t *cancelTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing cancel_task args: %w", err)
	}

	resolved, err := resolveTask(ctx, t.tasks, t.loc, params.TaskID)
	if err != nil {
		return "", err
	}

	// Idempotent: already cancelled.
	if resolved.Status == "cancelled" {
		result, _ := json.Marshal(map[string]any{
			"id":         resolved.ID,
			"display_id": resolved.DisplayID(),
			"status":     "cancelled",
		})
		return string(result), nil
	}

	status := "cancelled"
	task, err := t.tasks.UpdateTask(ctx, resolved.ID, model.TaskUpdate{Status: &status})
	if err != nil {
		return "", fmt.Errorf("cancelling task: %w", err)
	}

	result, _ := json.Marshal(map[string]any{
		"id":         task.ID,
		"display_id": task.DisplayID(),
		"status":     task.Status,
	})
	return string(result), nil
}
