package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

type removeProjectAliasTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewRemoveProjectAliasTool creates the remove_project_alias tool.
func NewRemoveProjectAliasTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &removeProjectAliasTool{projects: projects, loc: loc}
}

func (t *removeProjectAliasTool) Name() string { return "remove_project_alias" }
func (t *removeProjectAliasTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_remove_project_alias_desc", nil)
}
func (t *removeProjectAliasTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref":   map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_remove_project_alias_param_ref", nil)},
			"alias": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_remove_project_alias_param_alias", nil)},
		},
		"required": []string{"ref", "alias"},
	}
}
func (t *removeProjectAliasTool) DMOnly() bool { return true }

func (t *removeProjectAliasTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Ref   string `json:"ref"`
		Alias string `json:"alias"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing remove_project_alias args: %w", err)
	}
	if params.Alias == "" {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_alias_required", nil))
	}

	p, err := resolveProjectRef(ctx, t.projects, t.loc, params.Ref)
	if err != nil {
		return "", err
	}

	updated, err := t.projects.RemoveProjectAlias(ctx, p.ID, params.Alias)
	if err != nil {
		return "", mapAliasError(t.loc, err)
	}

	aliases := updated.Aliases
	if aliases == nil {
		aliases = []string{}
	}

	data := map[string]any{
		"id":      updated.ID,
		"aliases": aliases,
		"note":    huskwootI18n.Translate(t.loc, "agent_alias_removed", nil),
	}
	result, _ := json.Marshal(data)
	return string(result), nil
}
