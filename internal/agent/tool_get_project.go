package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type getProjectTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewGetProjectTool creates the get_project tool.
func NewGetProjectTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &getProjectTool{projects: projects, loc: loc}
}

func (t *getProjectTool) Name() string { return "get_project" }
func (t *getProjectTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_get_project_desc", nil)
}
func (t *getProjectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_get_project_param_ref", nil)},
		},
		"required": []string{"ref"},
	}
}
func (t *getProjectTool) DMOnly() bool { return true }

func (t *getProjectTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing get_project args: %w", err)
	}

	p, err := resolveProjectRef(ctx, t.projects, t.loc, params.Ref)
	if err != nil {
		return "", err
	}

	aliases := p.Aliases
	if aliases == nil {
		aliases = []string{}
	}

	data := map[string]any{
		"id":           p.ID,
		"slug":         p.Slug,
		"name":         p.Name,
		"description":  p.Description,
		"aliases":      aliases,
		"task_counter": p.TaskCounter,
		"created_at":   p.CreatedAt.Format(time.RFC3339),
	}

	result, _ := json.Marshal(data)
	return string(result), nil
}
