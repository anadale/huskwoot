package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/anadale/huskwoot/internal/dateparse"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type createTaskTool struct {
	tasks    model.TaskService
	projects model.ProjectService
	dp       *dateparse.Dateparser
	loc      *goI18n.Localizer
}

// NewCreateTaskTool creates the create_task tool.
func NewCreateTaskTool(tasks model.TaskService, projects model.ProjectService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool {
	return &createTaskTool{tasks: tasks, projects: projects, dp: dp, loc: loc}
}

func (t *createTaskTool) Name() string { return "create_task" }
func (t *createTaskTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_create_task_desc", nil)
}
func (t *createTaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":    map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_task_param_summary", nil)},
			"details":    map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_task_param_details", nil)},
			"project_id": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_task_param_project_id", nil)},
			"project":    map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_task_param_project", nil)},
			"deadline":   map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_task_param_deadline", nil)},
		},
		"required": []string{"summary"},
	}
}
func (t *createTaskTool) DMOnly() bool { return false }

func (t *createTaskTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Summary   string `json:"summary"`
		Details   string `json:"details"`
		ProjectID string `json:"project_id"`
		Project   string `json:"project"`
		Deadline  string `json:"deadline"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing create_task args: %w", err)
	}

	pid := params.ProjectID

	// If project_id is not set, try to resolve by name.
	if pid == "" && params.Project != "" {
		proj, err := t.projects.FindProjectByName(ctx, params.Project)
		if err != nil {
			return "", fmt.Errorf("finding project by name: %w", err)
		}
		if proj != nil {
			pid = proj.ID
		}
	}

	// If project_id is still not set, try to resolve the current channel's project.
	if pid == "" {
		sourceID, _ := ctx.Value(sourceIDKey).(string)
		if sourceID != "" {
			resolved, err := t.projects.ResolveProjectForChannel(ctx, sourceID)
			if err != nil {
				slog.WarnContext(ctx, "resolving channel project", "source_id", sourceID, "error", err)
			} else {
				pid = resolved
			}
		}
	}

	// Deduplication: look for an open task with the same summary.
	dup, err := t.findOpenDuplicate(ctx, pid, params.Summary)
	if err != nil {
		return "", fmt.Errorf("finding duplicate: %w", err)
	}
	if dup != nil {
		result, _ := json.Marshal(map[string]any{
			"id":         dup.ID,
			"display_id": dup.DisplayID(),
			"summary":    dup.Summary,
			"note":       huskwootI18n.Translate(t.loc, "agent_task_already_exists", nil),
		})
		return string(result), nil
	}

	req := model.CreateTaskRequest{
		ProjectID: pid,
		Summary:   params.Summary,
		Details:   params.Details,
	}

	if params.Deadline != "" {
		now := time.Now()
		if val, ok := ctx.Value(nowKey).(time.Time); ok {
			now = val
		}
		dl, err := t.dp.Parse(params.Deadline, now)
		if err != nil {
			return "", fmt.Errorf("parsing deadline: %w", err)
		}
		if dl != nil {
			req.Deadline = dl
		}
	}

	task, err := t.tasks.CreateTask(ctx, req)
	if err != nil {
		return "", err
	}

	result, _ := json.Marshal(map[string]any{
		"id":         task.ID,
		"display_id": task.DisplayID(),
		"summary":    task.Summary,
	})
	return string(result), nil
}

func (t *createTaskTool) findOpenDuplicate(ctx context.Context, projectID, summary string) (*model.Task, error) {
	tasks, err := t.tasks.ListTasks(ctx, projectID, model.TaskFilter{Status: "open"})
	if err != nil {
		return nil, err
	}
	summaryLower := strings.ToLower(summary)
	for i := range tasks {
		if strings.ToLower(tasks[i].Summary) == summaryLower {
			return &tasks[i], nil
		}
	}
	return nil, nil
}
