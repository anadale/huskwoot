package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/anadale/huskwoot/internal/dateparse"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type snoozeTaskTool struct {
	tasks model.TaskService
	dp    *dateparse.Dateparser
	loc   *goI18n.Localizer
}

// NewSnoozeTaskTool creates the snooze_task tool.
func NewSnoozeTaskTool(tasks model.TaskService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool {
	return &snoozeTaskTool{tasks: tasks, dp: dp, loc: loc}
}

func (t *snoozeTaskTool) Name() string { return "snooze_task" }
func (t *snoozeTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_snooze_task_desc", nil)
}
func (t *snoozeTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_snooze_task_param_task_id", nil)},
			"until":   map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_snooze_task_param_until", nil)},
		},
		"required": []string{"task_id", "until"},
	}
}
func (t *snoozeTaskTool) DMOnly() bool { return false }

func (t *snoozeTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID string `json:"task_id"`
		Until  string `json:"until"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing snooze_task args: %w", err)
	}

	if params.Until == "" {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_snooze_until_required", nil))
	}

	task, err := resolveTask(ctx, t.tasks, t.loc, params.TaskID)
	if err != nil {
		return "", err
	}

	now := time.Now()
	if val, ok := ctx.Value(nowKey).(time.Time); ok {
		now = val
	}

	parsed, err := t.dp.Parse(params.Until, now)
	if err != nil {
		return "", fmt.Errorf("%s: %w", huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil), err)
	}
	if parsed == nil {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil))
	}

	updated, err := t.tasks.UpdateTask(ctx, task.ID, model.TaskUpdate{Deadline: &parsed})
	if err != nil {
		return "", fmt.Errorf("snoozing task: %w", err)
	}

	data := map[string]any{
		"id":         updated.ID,
		"display_id": updated.DisplayID(),
	}
	if updated.Deadline != nil {
		data["deadline"] = updated.Deadline.Format(time.RFC3339)
	}

	result, _ := json.Marshal(data)
	return string(result), nil
}
