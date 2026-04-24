package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

func TestUpdateProjectTool_DMOnly(t *testing.T) {
	tool := agent.NewUpdateProjectTool(&mockProjectService{}, newTestLocalizer("ru"))
	if !tool.DMOnly() {
		t.Error("DMOnly() must return true for update_project")
	}
}

func TestUpdateProjectTool_Execute(t *testing.T) {
	baseProject := &model.Project{
		ID:          "proj-uuid-1",
		Name:        "Букинист",
		Slug:        "bukinist",
		Description: "Магазин старых книг",
		Aliases:     []string{"книги"},
	}

	newName := "Новый Букинист"
	newDesc := "Обновлённое описание"
	newSlug := "new-bukinist"

	updatedWithName := &model.Project{ID: "proj-uuid-1", Name: newName, Slug: "bukinist", Description: "Магазин старых книг"}
	updatedWithDesc := &model.Project{ID: "proj-uuid-1", Name: "Букинист", Slug: "bukinist", Description: newDesc}
	updatedWithSlug := &model.Project{ID: "proj-uuid-1", Name: "Букинист", Slug: newSlug, Description: "Магазин старых книг"}

	cases := []struct {
		name                    string
		args                    string
		resolveProjectRefResult *model.Project
		resolveProjectRefErr    error
		updateProjectResult     *model.Project
		updateProjectErr        error
		wantErr                 bool
		wantName                string
		wantSlug                string
		wantDescription         string
		wantNotePresent         bool
		wantUpdID               string
		wantUpdNameSet          bool
		wantUpdDescSet          bool
		wantUpdSlugSet          bool
	}{
		{
			name:                    "success — update name",
			args:                    `{"ref":"proj-uuid-1","name":"Новый Букинист"}`,
			resolveProjectRefResult: baseProject,
			updateProjectResult:     updatedWithName,
			wantName:                newName,
			wantNotePresent:         true,
			wantUpdID:               "proj-uuid-1",
			wantUpdNameSet:          true,
		},
		{
			name:                    "success — update description",
			args:                    `{"ref":"bukinist","description":"Обновлённое описание"}`,
			resolveProjectRefResult: baseProject,
			updateProjectResult:     updatedWithDesc,
			wantDescription:         newDesc,
			wantNotePresent:         true,
			wantUpdID:               "proj-uuid-1",
			wantUpdDescSet:          true,
		},
		{
			name:                    "success — update slug",
			args:                    `{"ref":"proj-uuid-1","slug":"new-bukinist"}`,
			resolveProjectRefResult: baseProject,
			updateProjectResult:     updatedWithSlug,
			wantSlug:                newSlug,
			wantNotePresent:         true,
			wantUpdID:               "proj-uuid-1",
			wantUpdSlugSet:          true,
		},
		{
			name:                 "error — no fields provided",
			args:                 `{"ref":"proj-uuid-1"}`,
			resolveProjectRefResult: baseProject,
			wantErr:              true,
		},
		{
			name:                 "error — project not found",
			args:                 `{"ref":"nonexistent","name":"X"}`,
			resolveProjectRefErr: usecase.ErrProjectNotFound,
			wantErr:              true,
		},
		{
			name:                    "error — slug conflict",
			args:                    `{"ref":"proj-uuid-1","slug":"other-slug"}`,
			resolveProjectRefResult: baseProject,
			updateProjectErr:        errors.New("UNIQUE constraint failed: projects.slug"),
			wantErr:                 true,
		},
		{
			name:    "error — empty ref",
			args:    `{"ref":""}`,
			wantErr: true,
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
				updateProjectResult:     tc.updateProjectResult,
				updateProjectErr:        tc.updateProjectErr,
			}
			tool := agent.NewUpdateProjectTool(svc, newTestLocalizer("ru"))

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

			if tc.wantName != "" {
				if got["name"] != tc.wantName {
					t.Errorf("name = %v, want %q", got["name"], tc.wantName)
				}
			}
			if tc.wantSlug != "" {
				if got["slug"] != tc.wantSlug {
					t.Errorf("slug = %v, want %q", got["slug"], tc.wantSlug)
				}
			}
			if tc.wantDescription != "" {
				if got["description"] != tc.wantDescription {
					t.Errorf("description = %v, want %q", got["description"], tc.wantDescription)
				}
			}
			if tc.wantNotePresent {
				if _, ok := got["note"]; !ok {
					t.Error("note field must be present in response")
				}
			}

			if tc.wantUpdID != "" {
				if svc.lastUpdateProjectID != tc.wantUpdID {
					t.Errorf("UpdateProject called with id = %q, want %q", svc.lastUpdateProjectID, tc.wantUpdID)
				}
			}
			if tc.wantUpdNameSet && svc.lastUpdateProjectUpd.Name == nil {
				t.Error("UpdateProject.Name must be set")
			}
			if !tc.wantUpdNameSet && svc.lastUpdateProjectUpd.Name != nil {
				t.Error("UpdateProject.Name must not be set")
			}
			if tc.wantUpdDescSet && svc.lastUpdateProjectUpd.Description == nil {
				t.Error("UpdateProject.Description must be set")
			}
			if !tc.wantUpdDescSet && svc.lastUpdateProjectUpd.Description != nil {
				t.Error("UpdateProject.Description must not be set")
			}
			if tc.wantUpdSlugSet && svc.lastUpdateProjectUpd.Slug == nil {
				t.Error("UpdateProject.Slug must be set")
			}
			if !tc.wantUpdSlugSet && svc.lastUpdateProjectUpd.Slug != nil {
				t.Error("UpdateProject.Slug must not be set")
			}
			if svc.lastUpdateProjectUpd.Aliases != nil {
				t.Error("UpdateProject.Aliases must always be nil (use add/remove tools)")
			}
		})
	}
}
