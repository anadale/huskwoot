package agent

import (
	"context"
	"encoding/json"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type listProjectsTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewListProjectsTool creates the list_projects tool.
func NewListProjectsTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &listProjectsTool{projects: projects, loc: loc}
}

func (t *listProjectsTool) Name() string { return "list_projects" }
func (t *listProjectsTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_list_projects_desc", nil)
}
func (t *listProjectsTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
func (t *listProjectsTool) DMOnly() bool { return true }
func (t *listProjectsTool) Execute(ctx context.Context, _ string) (string, error) {
	projects, err := t.projects.ListProjects(ctx)
	if err != nil {
		return "", err
	}

	type projectItem struct {
		ID          string `json:"id"`
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	items := make([]projectItem, len(projects))
	for i, p := range projects {
		items[i] = projectItem{ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description}
	}

	result, _ := json.Marshal(items)
	return string(result), nil
}
