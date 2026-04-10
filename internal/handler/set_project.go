package handler

import (
	"context"
	"fmt"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// SetProjectHandler handles the set_project_name command:
// idempotently creates a project and binds the chat to it via ProjectService.
type SetProjectHandler struct {
	projects model.ProjectService
	loc      *goI18n.Localizer
}

// NewSetProjectHandler creates a handler for the set_project_name command.
func NewSetProjectHandler(projects model.ProjectService, loc *goI18n.Localizer) *SetProjectHandler {
	return &SetProjectHandler{projects: projects, loc: loc}
}

// Name returns the handler name.
func (h *SetProjectHandler) Name() string {
	return "set_project_name"
}

// Handle binds the chat to a project via ProjectService.EnsureChannelProject.
func (h *SetProjectHandler) Handle(ctx context.Context, cmd model.Command) error {
	if cmd.Type != h.Name() {
		return nil
	}

	name := cmd.Payload["name"]
	if name == "" {
		return nil
	}

	p, err := h.projects.EnsureChannelProject(ctx, cmd.Source.ID, name)
	if err != nil {
		return fmt.Errorf("binding project: %w", err)
	}

	if cmd.SourceMessage.ReplyFn != nil {
		reply := huskwootI18n.Translate(h.loc, "project_bound_confirmation", map[string]any{
			"Name": p.Name,
			"Slug": p.Slug,
		})
		if err := cmd.SourceMessage.ReplyFn(ctx, reply); err != nil {
			return fmt.Errorf("sending confirmation: %w", err)
		}
	}

	return nil
}
