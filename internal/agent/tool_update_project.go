package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type updateProjectTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewUpdateProjectTool creates the update_project tool.
func NewUpdateProjectTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &updateProjectTool{projects: projects, loc: loc}
}

func (t *updateProjectTool) Name() string { return "update_project" }
func (t *updateProjectTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_update_project_desc", nil)
}
func (t *updateProjectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref":         map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_project_param_ref", nil)},
			"name":        map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_project_param_name", nil)},
			"description": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_project_param_description", nil)},
			"slug":        map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_update_project_param_slug", nil)},
		},
		"required": []string{"ref"},
	}
}
func (t *updateProjectTool) DMOnly() bool { return true }

func (t *updateProjectTool) Execute(ctx context.Context, args string) (string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("parsing update_project args: %w", err)
	}

	var ref string
	if v, ok := raw["ref"]; ok {
		if err := json.Unmarshal(v, &ref); err != nil {
			return "", fmt.Errorf("parsing ref: %w", err)
		}
	}

	_, hasName := raw["name"]
	_, hasDesc := raw["description"]
	_, hasSlug := raw["slug"]
	if !hasName && !hasDesc && !hasSlug {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_no_fields_to_update", nil))
	}

	p, err := resolveProjectRef(ctx, t.projects, t.loc, ref)
	if err != nil {
		return "", err
	}

	var upd model.ProjectUpdate

	if rawName, ok := raw["name"]; ok {
		var s string
		if err := json.Unmarshal(rawName, &s); err != nil {
			return "", fmt.Errorf("parsing name: %w", err)
		}
		upd.Name = &s
	}
	if rawDesc, ok := raw["description"]; ok {
		var s string
		if err := json.Unmarshal(rawDesc, &s); err != nil {
			return "", fmt.Errorf("parsing description: %w", err)
		}
		upd.Description = &s
	}
	if rawSlug, ok := raw["slug"]; ok {
		var s string
		if err := json.Unmarshal(rawSlug, &s); err != nil {
			return "", fmt.Errorf("parsing slug: %w", err)
		}
		upd.Slug = &s
	}

	updated, err := t.projects.UpdateProject(ctx, p.ID, upd)
	if err != nil {
		if isProjectUniqueConstraintErr(err) {
			return "", errors.New(huskwootI18n.Translate(t.loc, "agent_project_slug_conflict", nil))
		}
		return "", fmt.Errorf("updating project: %w", err)
	}

	aliases := updated.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	data := map[string]any{
		"id":          updated.ID,
		"slug":        updated.Slug,
		"name":        updated.Name,
		"description": updated.Description,
		"aliases":     aliases,
		"note":        huskwootI18n.Translate(t.loc, "agent_project_updated", nil),
	}

	result, _ := json.Marshal(data)
	return string(result), nil
}

func isProjectUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
