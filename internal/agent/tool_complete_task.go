package agent

import (
	"context"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type completeTaskTool struct {
	tasks model.TaskService
	loc   *goI18n.Localizer
}

// NewCompleteTaskTool creates the complete_task tool.
func NewCompleteTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool {
	return &completeTaskTool{tasks: tasks, loc: loc}
}

func (t *completeTaskTool) Name() string { return "complete_task" }
func (t *completeTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_complete_task_desc", nil)
}
func (t *completeTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_complete_task_param_task_id", nil)},
		},
		"required": []string{"task_id"},
	}
}
func (t *completeTaskTool) DMOnly() bool { return false }

func (t *completeTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing complete_task args: %w", err)
	}

	resolved, err := resolveTask(ctx, t.tasks, t.loc, params.TaskID)
	if err != nil {
		return "", err
	}

	// Idempotent: already done.
	if resolved.Status == "done" {
		result, _ := json.Marshal(map[string]any{
			"id":         resolved.ID,
			"display_id": resolved.DisplayID(),
			"status":     "done",
		})
		return string(result), nil
	}

	task, err := t.tasks.CompleteTask(ctx, resolved.ID)
	if err != nil {
		return "", err
	}

	result, _ := json.Marshal(map[string]any{
		"id":         task.ID,
		"display_id": task.DisplayID(),
		"status":     task.Status,
	})
	return string(result), nil
}
