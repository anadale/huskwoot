package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
)

func TestResolveTask(t *testing.T) {
	fixedTime := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	existingTask := &model.Task{
		ID: "existing-uuid", Number: 7, ProjectID: "proj-1", ProjectSlug: "inbox",
		Summary: "test task", Status: "open", CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	refTask := &model.Task{
		ID: "ref-uuid", Number: 42, ProjectID: "proj-1", ProjectSlug: "inbox",
		Summary: "ref task", Status: "open", CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	dbErr := errors.New("DB error")

	cases := []struct {
		name               string
		ref                string
		getTaskResult      *model.Task
		getTaskErr         error
		getTaskByRefResult *model.Task
		getTaskByRefErr    error
		wantID             string
		wantErr            bool
	}{
		{
			name:          "UUID found",
			ref:           "existing-uuid",
			getTaskResult: existingTask,
			wantID:        "existing-uuid",
		},
		{
			name:               "slug#number found",
			ref:                "inbox#42",
			getTaskByRefResult: refTask,
			wantID:             "ref-uuid",
		},
		{
			name:    "empty ref returns required error",
			ref:     "",
			wantErr: true,
		},
		{
			name:    "malformed ref slug#0 (number <= 0)",
			ref:     "inbox#0",
			wantErr: true,
		},
		{
			name:    "malformed ref slug# (no number)",
			ref:     "inbox#",
			wantErr: true,
		},
		{
			name:    "malformed ref slug#abc (non-numeric)",
			ref:     "inbox#abc",
			wantErr: true,
		},
		{
			name:    "nonexistent UUID (GetTask returns nil)",
			ref:     "nonexistent-uuid",
			wantErr: true,
		},
		{
			name:    "nonexistent ref (GetTaskByRef returns nil)",
			ref:     "inbox#99",
			wantErr: true,
		},
		{
			name:       "GetTask error is wrapped",
			ref:        "some-uuid",
			getTaskErr: dbErr,
			wantErr:    true,
		},
		{
			name:            "GetTaskByRef error is wrapped",
			ref:             "inbox#5",
			getTaskByRefErr: dbErr,
			wantErr:         true,
		},
		{
			// "#42" has empty slug; parseTaskRef rejects it as malformed
			name:    "hash-only ref with empty slug returns invalid format error",
			ref:     "#42",
			wantErr: true,
		},
		{
			// UUID whose ID does not match mock's getTaskResult
			name:          "UUID not matching mock getTaskResult",
			ref:           "other-uuid",
			getTaskResult: &model.Task{ID: "existing-uuid"},
			wantErr:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockTaskService{
				getTaskResult:      tc.getTaskResult,
				getTaskErr:         tc.getTaskErr,
				getTaskByRefResult: tc.getTaskByRefResult,
				getTaskByRefErr:    tc.getTaskByRefErr,
			}
			loc := newTestLocalizer("ru")

			task, err := agent.ResolveTask(context.Background(), svc, loc, tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveTask(%q) expected error, got nil", tc.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTask(%q) unexpected error: %v", tc.ref, err)
			}
			if task == nil {
				t.Fatalf("ResolveTask(%q) returned nil task", tc.ref)
			}
			if task.ID != tc.wantID {
				t.Errorf("ResolveTask(%q).ID = %q, want %q", tc.ref, task.ID, tc.wantID)
			}
		})
	}
}
