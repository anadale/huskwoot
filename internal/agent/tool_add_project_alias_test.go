package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

func TestAddProjectAliasTool_DMOnly(t *testing.T) {
	tool := agent.NewAddProjectAliasTool(&mockProjectService{}, newTestLocalizer("ru"))
	if !tool.DMOnly() {
		t.Error("DMOnly() must return true for add_project_alias")
	}
}

func TestAddProjectAliasTool_Execute(t *testing.T) {
	baseProject := &model.Project{ID: "proj-uuid-1", Name: "Букинист", Slug: "bukinist"}
	updatedProject := &model.Project{
		ID:      "proj-uuid-1",
		Name:    "Букинист",
		Slug:    "bukinist",
		Aliases: []string{"букинист"},
	}
	inboxProject := &model.Project{ID: "inbox-id", Name: "Inbox", Slug: "inbox"}

	cases := []struct {
		name                    string
		args                    string
		resolveProjectRefResult *model.Project
		resolveProjectRefErr    error
		addAliasResult          *model.Project
		addAliasErr             error
		wantErr                 bool
		wantAliases             []string
		wantNotePresent         bool
		wantProjectID           string
		wantAlias               string
	}{
		{
			name:                    "success — alias added",
			args:                    `{"ref":"proj-uuid-1","alias":"букинист"}`,
			resolveProjectRefResult: baseProject,
			addAliasResult:          updatedProject,
			wantAliases:             []string{"букинист"},
			wantNotePresent:         true,
			wantProjectID:           "proj-uuid-1",
			wantAlias:               "букинист",
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
			name:                    "error — alias invalid",
			args:                    `{"ref":"proj-uuid-1","alias":"bad alias"}`,
			resolveProjectRefResult: baseProject,
			addAliasErr:             usecase.ErrAliasInvalid,
			wantErr:                 true,
		},
		{
			name:                    "error — alias taken",
			args:                    `{"ref":"proj-uuid-1","alias":"taken"}`,
			resolveProjectRefResult: baseProject,
			addAliasErr:             usecase.ErrAliasTaken,
			wantErr:                 true,
		},
		{
			name:                    "error — alias conflicts with name",
			args:                    `{"ref":"proj-uuid-1","alias":"inbox"}`,
			resolveProjectRefResult: baseProject,
			addAliasErr:             usecase.ErrAliasConflictsWithName,
			wantErr:                 true,
		},
		{
			name:                    "error — alias limit reached",
			args:                    `{"ref":"proj-uuid-1","alias":"eleventh"}`,
			resolveProjectRefResult: baseProject,
			addAliasErr:             usecase.ErrAliasLimitReached,
			wantErr:                 true,
		},
		{
			name:                    "error — forbidden for inbox",
			args:                    `{"ref":"inbox","alias":"test"}`,
			resolveProjectRefResult: inboxProject,
			addAliasErr:             usecase.ErrAliasForbiddenForInbox,
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
				addAliasResult:          tc.addAliasResult,
				addAliasErr:             tc.addAliasErr,
			}
			tool := agent.NewAddProjectAliasTool(svc, newTestLocalizer("ru"))

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
			if tc.wantProjectID != "" && svc.lastAddProjectID != tc.wantProjectID {
				t.Errorf("AddProjectAlias called with projectID = %q, want %q", svc.lastAddProjectID, tc.wantProjectID)
			}
			if tc.wantAlias != "" && svc.lastAddAlias != tc.wantAlias {
				t.Errorf("AddProjectAlias called with alias = %q, want %q", svc.lastAddAlias, tc.wantAlias)
			}
		})
	}
}
