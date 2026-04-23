package agent

import (
	"context"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type moveTaskTool struct {
	tasks    model.TaskService
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewMoveTaskTool creates the move_task tool.
func NewMoveTaskTool(tasks model.TaskService, projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &moveTaskTool{tasks: tasks, projects: projects, loc: loc}
}

func (t *moveTaskTool) Name() string { return "move_task" }
func (t *moveTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_move_task_desc", nil)
}
func (t *moveTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id":    map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_move_task_param_task_id", nil)},
			"project_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_move_task_param_project_id", nil)},
			"project":    map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_move_task_param_project", nil)},
		},
		"required": []string{"task_id"},
	}
}
func (t *moveTaskTool) DMOnly() bool { return false }

func (t *moveTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		TaskID    string `json:"task_id"`
		ProjectID string `json:"project_id"`
		Project   string `json:"project"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing move_task args: %w", err)
	}

	resolved, err := resolveTask(ctx, t.tasks, t.loc, params.TaskID)
	if err != nil {
		return "", err
	}

	projectID := params.ProjectID
	if projectID == "" && params.Project != "" {
		proj, err := t.projects.FindProjectByName(ctx, params.Project)
		if err != nil {
			return "", fmt.Errorf("finding project: %w", err)
		}
		if proj == nil {
			return "", fmt.Errorf("project not found: %q", params.Project)
		}
		projectID = proj.ID
	}
	if projectID == "" {
		return "", fmt.Errorf("project_id or project is required")
	}

	task, err := t.tasks.MoveTask(ctx, resolved.ID, projectID)
	if err != nil {
		return "", fmt.Errorf("moving task: %w", err)
	}

	result, _ := json.Marshal(map[string]any{
		"id":         task.ID,
		"display_id": task.DisplayID(),
		"project_id": task.ProjectID,
		"message":    huskwootI18n.Translate(t.loc, "agent_task_moved", map[string]any{"DisplayID": task.DisplayID()}),
	})
	return string(result), nil
}
