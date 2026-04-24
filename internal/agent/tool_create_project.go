package agent

import (
	"context"
	"encoding/json"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type createProjectTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewCreateProjectTool creates the create_project tool.
func NewCreateProjectTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &createProjectTool{projects: projects, loc: loc}
}

func (t *createProjectTool) Name() string { return "create_project" }
func (t *createProjectTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_create_project_desc", nil)
}
func (t *createProjectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":        map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_project_param_name", nil)},
			"description": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_create_project_param_description", nil)},
			"aliases": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": huskwootI18n.Translate(t.loc, "tool_create_project_param_aliases", nil),
			},
		},
		"required": []string{"name"},
	}
}
func (t *createProjectTool) DMOnly() bool { return true }
func (t *createProjectTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Aliases     []string `json:"aliases"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing create_project args: %w", err)
	}

	p, err := t.projects.CreateProject(ctx, model.CreateProjectRequest{
		Name:        params.Name,
		Description: params.Description,
		Aliases:     params.Aliases,
	})
	if err != nil {
		return "", mapAliasError(t.loc, err)
	}

	aliases := p.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	result, _ := json.Marshal(map[string]any{"id": p.ID, "slug": p.Slug, "name": p.Name, "aliases": aliases})
	return string(result), nil
}
