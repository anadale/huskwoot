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

type updateTaskTool struct {
	tasks model.TaskService
	dp    *dateparse.Dateparser
	loc   *goI18n.Localizer
}

// NewUpdateTaskTool creates the update_task tool.
func NewUpdateTaskTool(tasks model.TaskService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool {
	return &updateTaskTool{tasks: tasks, dp: dp, loc: loc}
}

func (t *updateTaskTool) Name() string { return "update_task" }
func (t *updateTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_update_task_desc", nil)
}
func (t *updateTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id":  map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_task_param_task_id", nil)},
			"summary":  map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_task_param_summary", nil)},
			"details":  map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_task_param_details", nil)},
			"deadline": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_task_param_deadline", nil)},
		},
		"required": []string{"task_id"},
	}
}
func (t *updateTaskTool) DMOnly() bool { return false }

func (t *updateTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("parsing update_task args: %w", err)
	}

	var taskID string
	if v, ok := raw["task_id"]; ok {
		if err := json.Unmarshal(v, &taskID); err != nil {
			return "", fmt.Errorf("parsing task_id: %w", err)
		}
	}

	_, hasSummary := raw["summary"]
	_, hasDetails := raw["details"]
	_, hasDeadline := raw["deadline"]
	if !hasSummary && !hasDetails && !hasDeadline {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_no_fields_to_update", nil))
	}

	task, err := resolveTask(ctx, t.tasks, t.loc, taskID)
	if err != nil {
		return "", err
	}

	var upd model.TaskUpdate

	if rawSummary, ok := raw["summary"]; ok {
		var s string
		if err := json.Unmarshal(rawSummary, &s); err != nil {
			return "", fmt.Errorf("parsing summary: %w", err)
		}
		if s == "" {
			return "", errors.New(huskwootI18n.Translate(t.loc, "agent_summary_empty", nil))
		}
		upd.Summary = &s
	}

	if rawDetails, ok := raw["details"]; ok {
		var s string
		if err := json.Unmarshal(rawDetails, &s); err != nil {
			return "", fmt.Errorf("parsing details: %w", err)
		}
		upd.Details = &s
	}

	if rawDeadline, ok := raw["deadline"]; ok {
		var s string
		if err := json.Unmarshal(rawDeadline, &s); err != nil {
			return "", fmt.Errorf("parsing deadline: %w", err)
		}
		if s == "" {
			var nilTime *time.Time
			upd.Deadline = &nilTime
		} else {
			now := time.Now()
			if val, ok := ctx.Value(nowKey).(time.Time); ok {
				now = val
			}
			parsed, err := t.dp.Parse(s, now)
			if err != nil {
				return "", fmt.Errorf("%s: %w", huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil), err)
			}
			if parsed == nil {
				return "", errors.New(huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil))
			}
			upd.Deadline = &parsed
		}
	}

	updated, err := t.tasks.UpdateTask(ctx, task.ID, upd)
	if err != nil {
		return "", fmt.Errorf("updating task: %w", err)
	}

	data := map[string]any{
		"id":         updated.ID,
		"display_id": updated.DisplayID(),
		"summary":    updated.Summary,
		"note":       huskwootI18n.Translate(t.loc, "agent_task_updated", nil),
	}
	if updated.Deadline != nil {
		data["deadline"] = updated.Deadline.Format(time.RFC3339)
	}

	result, _ := json.Marshal(data)
	return string(result), nil
}
