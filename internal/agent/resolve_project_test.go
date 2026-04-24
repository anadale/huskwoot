package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

func TestResolveProjectRef(t *testing.T) {
	fixedTime := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	byUUID := &model.Project{
		ID: "proj-uuid-1", Name: "Букинист", Slug: "bukinist",
		Aliases: []string{"букинист"}, CreatedAt: fixedTime,
	}
	bySlug := &model.Project{
		ID: "proj-uuid-2", Name: "Работа", Slug: "rabota",
		Aliases: []string{}, CreatedAt: fixedTime,
	}
	byAlias := &model.Project{
		ID: "proj-uuid-3", Name: "Магазин", Slug: "magazin",
		Aliases: []string{"маг"}, CreatedAt: fixedTime,
	}
	dbErr := errors.New("database error")

	cases := []struct {
		name                    string
		ref                     string
		resolveProjectRefResult *model.Project
		resolveProjectRefErr    error
		wantID                  string
		wantErr                 bool
	}{
		{
			name:                    "UUID found",
			ref:                     "proj-uuid-1",
			resolveProjectRefResult: byUUID,
			wantID:                  "proj-uuid-1",
		},
		{
			name:                    "slug found",
			ref:                     "rabota",
			resolveProjectRefResult: bySlug,
			wantID:                  "proj-uuid-2",
		},
		{
			name:                    "alias found (lowercase)",
			ref:                     "маг",
			resolveProjectRefResult: byAlias,
			wantID:                  "proj-uuid-3",
		},
		{
			name:                    "alias found (uppercase normalized by service)",
			ref:                     "МАГ",
			resolveProjectRefResult: byAlias,
			wantID:                  "proj-uuid-3",
		},
		{
			name:                 "empty ref returns required error",
			ref:                  "",
			wantErr:              true,
		},
		{
			name:                 "project not found returns i18n error",
			ref:                  "nonexistent",
			resolveProjectRefErr: usecase.ErrProjectNotFound,
			wantErr:              true,
		},
		{
			name:                 "db error is wrapped",
			ref:                  "some-ref",
			resolveProjectRefErr: dbErr,
			wantErr:              true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockProjectService{
				resolveProjectRefResult: tc.resolveProjectRefResult,
				resolveProjectRefErr:    tc.resolveProjectRefErr,
			}
			loc := newTestLocalizer("ru")

			p, err := agent.ResolveProjectRef(context.Background(), svc, loc, tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveProjectRef(%q) expected error, got nil", tc.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveProjectRef(%q) unexpected error: %v", tc.ref, err)
			}
			if p == nil {
				t.Fatalf("ResolveProjectRef(%q) returned nil project", tc.ref)
			}
			if p.ID != tc.wantID {
				t.Errorf("ResolveProjectRef(%q).ID = %q, want %q", tc.ref, p.ID, tc.wantID)
			}
		})
	}
}
