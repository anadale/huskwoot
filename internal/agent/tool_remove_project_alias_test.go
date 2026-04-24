package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

func TestRemoveProjectAliasTool_DMOnly(t *testing.T) {
	tool := agent.NewRemoveProjectAliasTool(&mockProjectService{}, newTestLocalizer("ru"))
	if !tool.DMOnly() {
		t.Error("DMOnly() must return true for remove_project_alias")
	}
}

func TestRemoveProjectAliasTool_Execute(t *testing.T) {
	baseProject := &model.Project{
		ID:      "proj-uuid-1",
		Name:    "Букинист",
		Slug:    "bukinist",
		Aliases: []string{"букинист", "книги"},
	}
	updatedProject := &model.Project{
		ID:      "proj-uuid-1",
		Name:    "Букинист",
		Slug:    "bukinist",
		Aliases: []string{"книги"},
	}
	emptyAliasProject := &model.Project{
		ID:      "proj-uuid-1",
		Name:    "Букинист",
		Slug:    "bukinist",
		Aliases: []string{},
	}

	cases := []struct {
		name                    string
		args                    string
		resolveProjectRefResult *model.Project
		resolveProjectRefErr    error
		removeAliasResult       *model.Project
		removeAliasErr          error
		wantErr                 bool
		wantAliases             []string
		wantNotePresent         bool
		wantProjectID           string
		wantAlias               string
	}{
		{
			name:                    "success — alias removed",
			args:                    `{"ref":"proj-uuid-1","alias":"букинист"}`,
			resolveProjectRefResult: baseProject,
			removeAliasResult:       updatedProject,
			wantAliases:             []string{"книги"},
			wantNotePresent:         true,
			wantProjectID:           "proj-uuid-1",
			wantAlias:               "букинист",
		},
		{
			name:                    "success — last alias removed returns empty list",
			args:                    `{"ref":"proj-uuid-1","alias":"книги"}`,
			resolveProjectRefResult: baseProject,
			removeAliasResult:       emptyAliasProject,
			wantAliases:             []string{},
			wantNotePresent:         true,
		},
		{
			name:    "error — empty ref",
			args:    `{"ref":"","alias":"букинист"}`,
			wantErr: true,
		},
		{
			name:    "error — empty alias",
			args:    `{"ref":"proj-uuid-1","alias":""}`,
			wantErr: true,
		},
		{
			name:                 "error — project not found",
			args:                 `{"ref":"nonexistent","alias":"test"}`,
			resolveProjectRefErr: usecase.ErrProjectNotFound,
			wantErr:              true,
		},
		{
			name:                    "error — alias not found",
			args:                    `{"ref":"proj-uuid-1","alias":"missing"}`,
			resolveProjectRefResult: baseProject,
			removeAliasErr:          usecase.ErrAliasNotFound,
			wantErr:                 true,
		},
		{
			name:    "error — invalid JSON",
			args:    `not json`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockProjectService{
				resolveProjectRefResult: tc.resolveProjectRefResult,
				resolveProjectRefErr:    tc.resolveProjectRefErr,
				removeAliasResult:       tc.removeAliasResult,
				removeAliasErr:          tc.removeAliasErr,
			}
			tool := agent.NewRemoveProjectAliasTool(svc, newTestLocalizer("ru"))

			result, err := tool.Execute(context.Background(), tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Execute(%q) expected error, got nil", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute(%q) returned unexpected error: %v", tc.args, err)
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(result), &got); err != nil {
				t.Fatalf("result is not valid JSON: %v; raw: %s", err, result)
			}

			if tc.wantAliases != nil {
				raw, ok := got["aliases"]
				if !ok {
					t.Fatal("aliases field must be present in response")
				}
				rawSlice, ok := raw.([]any)
				if !ok {
					t.Fatalf("aliases must be an array, got %T", raw)
				}
				if len(rawSlice) != len(tc.wantAliases) {
					t.Fatalf("aliases length = %d, want %d", len(rawSlice), len(tc.wantAliases))
				}
				for i, a := range tc.wantAliases {
					if rawSlice[i] != a {
						t.Errorf("aliases[%d] = %v, want %q", i, rawSlice[i], a)
					}
				}
			}
			if tc.wantNotePresent {
				if _, ok := got["note"]; !ok {
					t.Error("note field must be present in response")
				}
			}
			if tc.wantProjectID != "" && svc.lastRemoveProjectID != tc.wantProjectID {
				t.Errorf("RemoveProjectAlias called with projectID = %q, want %q", svc.lastRemoveProjectID, tc.wantProjectID)
			}
			if tc.wantAlias != "" && svc.lastRemoveAlias != tc.wantAlias {
				t.Errorf("RemoveProjectAlias called with alias = %q, want %q", svc.lastRemoveAlias, tc.wantAlias)
			}
		})
	}
}
