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

type searchTasksTool struct {
	tasks    model.TaskService
	projects model.ProjectService
	dp       *dateparse.Dateparser
	loc      *goI18n.Localizer
}

// NewSearchTasksTool creates the search_tasks tool.
func NewSearchTasksTool(tasks model.TaskService, projects model.ProjectService, dp *dateparse.Dateparser, loc *goI18n.Localizer) Tool {
	return &searchTasksTool{tasks: tasks, projects: projects, dp: dp, loc: loc}
}

func (t *searchTasksTool) Name() string { return "search_tasks" }
func (t *searchTasksTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_search_tasks_desc", nil)
}
func (t *searchTasksTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":      map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_search_tasks_param_query", nil)},
			"status":     map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_search_tasks_param_status", nil)},
			"project":    map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_search_tasks_param_project", nil)},
			"due_before": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_search_tasks_param_due_before", nil)},
			"due_after":  map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_search_tasks_param_due_after", nil)},
			"limit":      map[string]any{"type": "integer", "description": huskwootI18n.Translate(t.loc, "tool_search_tasks_param_limit", nil)},
		},
	}
}
func (t *searchTasksTool) DMOnly() bool { return false }

func (t *searchTasksTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Query     string `json:"query"`
		Status    string `json:"status"`
		Project   string `json:"project"`
		DueBefore string `json:"due_before"`
		DueAfter  string `json:"due_after"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing search_tasks args: %w", err)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 50 {
		limit = 50
	}

	status := params.Status
	if status == "" {
		status = "open"
	} else if status == "all" {
		status = ""
	}

	now := time.Now()
	if val, ok := ctx.Value(nowKey).(time.Time); ok {
		now = val
	}

	var dueBefore, dueAfter *time.Time
	if params.DueBefore != "" {
		parsed, err := t.dp.Parse(params.DueBefore, now)
		if err != nil {
			return "", fmt.Errorf("%s: %w", huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil), err)
		}
		if parsed == nil {
			return "", errors.New(huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil))
		}
		dueBefore = parsed
	}
	if params.DueAfter != "" {
		parsed, err := t.dp.Parse(params.DueAfter, now)
		if err != nil {
			return "", fmt.Errorf("%s: %w", huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil), err)
		}
		if parsed == nil {
			return "", errors.New(huskwootI18n.Translate(t.loc, "agent_deadline_parse_failed", nil))
		}
		dueAfter = parsed
	}

	projectID := ""
	if params.Project != "" {
		proj, err := t.projects.FindProjectByName(ctx, params.Project)
		if err != nil {
			return "", fmt.Errorf("finding project by name: %w", err)
		}
		if proj != nil {
			projectID = proj.ID
		} else {
			// Treat params.Project as a UUID directly.
			projectID = params.Project
		}
	}

	fetchLimit := limit * 2
	if dueBefore != nil || dueAfter != nil {
		// No DB-level limit when date post-filtering: the multiplier heuristic
		// silently drops results when matching tasks are sparse in the full set.
		fetchLimit = 0
	}

	tasks, err := t.tasks.ListTasks(ctx, projectID, model.TaskFilter{
		Query:  params.Query,
		Status: status,
		Limit:  fetchLimit,
	})
	if err != nil {
		return "", err
	}

	filtered := make([]model.Task, 0, len(tasks))
	for _, task := range tasks {
		if dueBefore != nil {
			if task.Deadline == nil || !task.Deadline.Before(*dueBefore) {
				continue
			}
		}
		if dueAfter != nil {
			if task.Deadline == nil || !task.Deadline.After(*dueAfter) {
				continue
			}
		}
		filtered = append(filtered, task)
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	result := make([]map[string]any, 0, len(filtered))
	for _, task := range filtered {
		item := map[string]any{
			"id":           task.ID,
			"display_id":   task.DisplayID(),
			"project_slug": task.ProjectSlug,
			"summary":      task.Summary,
			"status":       task.Status,
		}
		if task.Deadline != nil {
			item["deadline"] = task.Deadline.Format(time.RFC3339)
		}
		result = append(result, item)
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
