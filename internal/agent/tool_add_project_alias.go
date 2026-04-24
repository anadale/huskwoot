package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

type addProjectAliasTool struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewAddProjectAliasTool creates the add_project_alias tool.
func NewAddProjectAliasTool(projects model.ProjectService, loc *goI18n.Localizer) Tool {
	return &addProjectAliasTool{projects: projects, loc: loc}
}

func (t *addProjectAliasTool) Name() string { return "add_project_alias" }
func (t *addProjectAliasTool) Description() string {
	return huskwootI18n.Translate(t.loc, "tool_add_project_alias_desc", nil)
}
func (t *addProjectAliasTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref":   map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_add_project_alias_param_ref", nil)},
			"alias": map[string]any{"type": "string", "description": huskwootI18n.Translate(t.loc, "tool_add_project_alias_param_alias", nil)},
		},
		"required": []string{"ref", "alias"},
	}
}
func (t *addProjectAliasTool) DMOnly() bool { return true }

func (t *addProjectAliasTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Ref   string `json:"ref"`
		Alias string `json:"alias"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parsing add_project_alias args: %w", err)
	}
	if params.Alias == "" {
		return "", errors.New(huskwootI18n.Translate(t.loc, "agent_alias_required", nil))
	}

	p, err := resolveProjectRef(ctx, t.projects, t.loc, params.Ref)
	if err != nil {
		return "", err
	}

	updated, err := t.projects.AddProjectAlias(ctx, p.ID, params.Alias)
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
		"note":    huskwootI18n.Translate(t.loc, "agent_alias_added", nil),
	}
	result, _ := json.Marshal(data)
	return string(result), nil
}

// mapAliasError maps alias sentinel errors to i18n messages.
func mapAliasError(loc *goI18n.Localizer, err error) error {
	switch {
	case errors.Is(err, usecase.ErrAliasInvalid):
		return errors.New(huskwootI18n.Translate(loc, "err_alias_invalid", nil))
	case errors.Is(err, usecase.ErrAliasTaken):
		return errors.New(huskwootI18n.Translate(loc, "err_alias_taken", nil))
	case errors.Is(err, usecase.ErrAliasConflictsWithName):
		return errors.New(huskwootI18n.Translate(loc, "err_alias_conflicts_with_name", nil))
	case errors.Is(err, usecase.ErrAliasLimitReached):
		return errors.New(huskwootI18n.Translate(loc, "err_alias_limit_reached", nil))
	case errors.Is(err, usecase.ErrAliasForbiddenForInbox):
		return errors.New(huskwootI18n.Translate(loc, "err_alias_forbidden_for_inbox", nil))
	case errors.Is(err, usecase.ErrAliasNotFound):
		return errors.New(huskwootI18n.Translate(loc, "err_alias_not_found", nil))
	default:
		return fmt.Errorf("alias operation failed: %w", err)
	}
}
