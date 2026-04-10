package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
			"task_id":  map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_complete_task_param_task_id", nil)},
			"task_ref": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_complete_task_param_task_ref", nil)},
		},
	}
}
func (t *completeTaskTool) DMOnly() bool { return false }

func (t *completeTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID  string `json:"task_id"`
		TaskRef string `json:"task_ref"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing complete_task args: %w", err)
	}

	id := params.TaskID
	if id == "" && params.TaskRef != "" {
		task, err := t.resolveRef(ctx, params.TaskRef)
		if err != nil {
			return "", err
		}
		id = task.ID
	}
	if id == "" {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_task_id_or_ref_required", nil))
	}

	task, err := t.tasks.CompleteTask(ctx, id)
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

func (t *completeTaskTool) resolveRef(ctx context.Context, ref string) (*model.Task, error) {
	slug, number, ok := parseTaskRef(ref)
	if !ok {
		return nil, errors.New(huskwootI18n.Translate(t.loc, "agent_invalid_ref_format", map[string]any{"Ref": ref}))
	}
	task, err := t.tasks.GetTaskByRef(ctx, slug, number)
	if err != nil {
		return nil, fmt.Errorf("looking up task by ref: %w", err)
	}
	if task == nil {
		return nil, errors.New(huskwootI18n.Translate(t.loc, "agent_task_not_found", map[string]any{"Ref": ref}))
	}
	return task, nil
}

// parseTaskRef parses a string of the form "<slug>#<number>" into its components.
func parseTaskRef(ref string) (slug string, number int, ok bool) {
	idx := strings.LastIndex(ref, "#")
	if idx < 0 {
		return
	}
	n, err := strconv.Atoi(ref[idx+1:])
	if err != nil || n <= 0 {
		return
	}
	return ref[:idx], n, true
}
