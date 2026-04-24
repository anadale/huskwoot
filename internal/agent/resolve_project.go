package agent

import (
	"context"
	"errors"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

// resolveProjectRef resolves a project by UUID, slug, or alias.
// Returns an i18n error if the reference is empty or the project does not exist.
func resolveProjectRef(ctx context.Context, svc model.ProjectService, loc *goI18n.Localizer, ref string) (*model.Project, error) {
	if ref == "" {
		return nil, errors.New(huskwootI18n.Translate(loc, "agent_project_ref_required", nil))
	}
	p, err := svc.ResolveProjectRef(ctx, ref)
	if err != nil {
		if errors.Is(err, usecase.ErrProjectNotFound) {
			return nil, errors.New(huskwootI18n.Translate(loc, "agent_project_not_found", map[string]any{"Ref": ref}))
		}
		return nil, fmt.Errorf("resolving project ref: %w", err)
	}
	return p, nil
}
