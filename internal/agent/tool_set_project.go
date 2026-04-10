package agent

import (
	"context"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type setProjectTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewSetProjectTool creates the set_project tool.
func NewSetProjectTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &setProjectTool{projects: projects, loc: loc}
}

func (t *setProjectTool) Name() string { return "set_project" }
func (t *setProjectTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_set_project_desc", nil)
}
func (t *setProjectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_set_project_param_name", nil)},
		},
		"required": []string{"name"},
	}
}
func (t *setProjectTool) DMOnly() bool { return false }

func (t *setProjectTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing set_project args: %w", err)
	}

	sourceID, _ := ctx.Value(sourceIDKey).(string)

	var p *model.Project
	var err error
	if sourceID != "" {
		p, err = t.projects.EnsureChannelProject(ctx, sourceID, params.Name)
	} else {
		// No sourceID: just create/find the project without binding it to a channel.
		p, err = t.projects.FindProjectByName(ctx, params.Name)
		if err == nil && p == nil {
			p, err = t.projects.CreateProject(ctx, model.CreateProjectRequest{Name: params.Name})
		}
	}
	if err != nil {
		return "", err
	}

	result, _ := json.Marshal(map[string]any{"project_id": p.ID, "slug": p.Slug, "name": p.Name})
	return string(result), nil
}
