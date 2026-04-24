package agent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

func TestGetProjectTool_DMOnly(t *testing.T) {
	tool := agent.NewGetProjectTool(&mockProjectService{}, newTestLocalizer("ru"))
	if !tool.DMOnly() {
		t.Error("DMOnly() must return true for get_project")
	}
}

func TestGetProjectTool_Execute(t *testing.T) {
	created := time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)
	project := &model.Project{
		ID:          "proj-uuid-1",
		Name:        "Букинист",
		Slug:        "bukinist",
		Description: "Магазин старых книг",
		Aliases:     []string{"букинист", "книги"},
		TaskCounter: 5,
		CreatedAt:   created,
	}

	cases := []struct {
		name                    string
		args                    string
		resolveProjectRefResult *model.Project
		resolveProjectRefErr    error
		wantErr                 bool
		wantID                  string
		wantSlug                string
		wantName                string
		wantDescription         string
		wantAliases             []string
		wantTaskCounter         float64
	}{
		{
			name:                    "success — all fields present",
			args:                    `{"ref":"proj-uuid-1"}`,
			resolveProjectRefResult: project,
			wantID:                  "proj-uuid-1",
			wantSlug:                "bukinist",
			wantName:                "Букинист",
			wantDescription:         "Магазин старых книг",
			wantAliases:             []string{"букинист", "книги"},
			wantTaskCounter:         5,
		},
		{
			name:                    "success — empty aliases returns empty array",
			args:                    `{"ref":"inbox"}`,
			resolveProjectRefResult: &model.Project{ID: "inbox-id", Name: "Inbox", Slug: "inbox", Aliases: []string{}},
			wantID:                  "inbox-id",
			wantAliases:             []string{},
		},
		{
			name:                 "project not found returns error",
			args:                 `{"ref":"nonexistent"}`,
			resolveProjectRefErr: usecase.ErrProjectNotFound,
			wantErr:              true,
		},
		{
			name:    "empty ref returns error",
			args:    `{"ref":""}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockProjectService{
				resolveProjectRefResult: tc.resolveProjectRefResult,
				resolveProjectRefErr:    tc.resolveProjectRefErr,
			}
			tool := agent.NewGetProjectTool(svc, newTestLocalizer("ru"))

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

			if tc.wantID != "" {
				if got["id"] != tc.wantID {
					t.Errorf("id = %v, want %q", got["id"], tc.wantID)
				}
			}
			if tc.wantSlug != "" {
				if got["slug"] != tc.wantSlug {
					t.Errorf("slug = %v, want %q", got["slug"], tc.wantSlug)
				}
			}
			if tc.wantName != "" {
				if got["name"] != tc.wantName {
					t.Errorf("name = %v, want %q", got["name"], tc.wantName)
				}
			}
			if tc.wantDescription != "" {
				if got["description"] != tc.wantDescription {
					t.Errorf("description = %v, want %q", got["description"], tc.wantDescription)
				}
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
			if tc.wantTaskCounter != 0 {
				if got["task_counter"] != tc.wantTaskCounter {
					t.Errorf("task_counter = %v, want %v", got["task_counter"], tc.wantTaskCounter)
				}
			}
			if _, ok := got["created_at"]; !ok {
				t.Error("created_at field must be present in response")
			}
		})
	}
}
