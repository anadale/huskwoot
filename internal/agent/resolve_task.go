package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// resolveTask resolves a task by its UUID or "<slug>#<number>" reference.
// Returns an i18n error if the reference is empty, malformed, or the task does not exist.
func resolveTask(ctx context.Context, svc model.TaskService, loc *goI18n.Localizer, ref string) (*model.Task, error) {
	if ref == "" {
		return nil, errors.New(huskwootI18n.Translate(loc, "agent_task_id_or_ref_required", nil))
	}
	if strings.Contains(ref, "#") {
		slug, number, ok := parseTaskRef(ref)
		if !ok {
			return nil, errors.New(huskwootI18n.Translate(loc, "agent_invalid_ref_format", map[string]any{"Ref": ref}))
		}
		task, err := svc.GetTaskByRef(ctx, slug, number)
		if err != nil {
			return nil, fmt.Errorf("looking up task by ref: %w", err)
		}
		if task == nil {
			return nil, errors.New(huskwootI18n.Translate(loc, "agent_task_not_found", map[string]any{"Ref": ref}))
		}
		return task, nil
	}
	task, err := svc.GetTask(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("looking up task: %w", err)
	}
	if task == nil {
		return nil, errors.New(huskwootI18n.Translate(loc, "agent_task_not_found", map[string]any{"Ref": ref}))
	}
	return task, nil
}

// parseTaskRef parses a string of the form "<slug>#<number>" into its components.
func parseTaskRef(ref string) (slug string, number int, ok bool) {
	s, numStr, found := strings.Cut(ref, "#")
	if !found {
		return
	}
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 || s == "" {
		return
	}
	return s, n, true
}
