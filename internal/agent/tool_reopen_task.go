package agent

import (
	"context"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type reopenTaskTool struct {
	tasks model.TaskService
	loc   *goI18n.Localizer
}

// NewReopenTaskTool creates the reopen_task tool.
func NewReopenTaskTool(tasks model.TaskService, loc *goI18n.Localizer) Tool {
	return &reopenTaskTool{tasks: tasks, loc: loc}
}

func (t *reopenTaskTool) Name() string { return "reopen_task" }
func (t *reopenTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_reopen_task_desc", nil)
}
func (t *reopenTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_reopen_task_param_task_id", nil)},
		},
		"required": []string{"task_id"},
	}
}
func (t *reopenTaskTool) DMOnly() bool { return false }

func (t *reopenTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing reopen_task args: %w", err)
	}

	resolved, err := resolveTask(ctx, t.tasks, t.loc, params.TaskID)
	if err != nil {
		return "", err
	}

	// Idempotent: already open.
	if resolved.Status == "open" {
		result, _ := json.Marshal(map[string]any{
			"id":         resolved.ID,
			"display_id": resolved.DisplayID(),
			"status":     "open",
		})
		return string(result), nil
	}

	task, err := t.tasks.ReopenTask(ctx, resolved.ID)
	if err != nil {
		return "", fmt.Errorf("reopening task: %w", err)
	}

	result, _ := json.Marshal(map[string]any{
		"id":         task.ID,
		"display_id": task.DisplayID(),
		"status":     task.Status,
	})
	return string(result), nil
}
