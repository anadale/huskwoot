package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
)

func TestMoveTaskTool_Execute_ByRef(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "uuid-1", Number: 5, ProjectID: "src", ProjectSlug: "inbox"},
		moveTaskResult:     &model.Task{ID: "uuid-1", Number: 3, ProjectID: "dst", ProjectSlug: "work"},
	}
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "dst", Slug: "work", Name: "Работа"},
	}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("ru"))

	out, err := tool.Execute(context.Background(), `{"task_id":"inbox#5","project":"Работа"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "перенесена") {
		t.Fatalf("expected text «перенесена» in response, got: %s", out)
	}
	if tasks.lastMoveID != "uuid-1" {
		t.Fatalf("MoveTask called with ID %q, want uuid-1", tasks.lastMoveID)
	}
	if tasks.lastMoveProjID != "dst" {
		t.Fatalf("MoveTask called with project_id %q, want dst", tasks.lastMoveProjID)
	}
}

func TestMoveTaskTool_Execute_ByUUID(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:  &model.Task{ID: "uuid-2", Number: 1, ProjectID: "src", ProjectSlug: "inbox"},
		moveTaskResult: &model.Task{ID: "uuid-2", Number: 1, ProjectID: "dst", ProjectSlug: "rabota"},
	}
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "dst", Slug: "rabota", Name: "Работа"},
	}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("ru"))

	out, err := tool.Execute(context.Background(), `{"task_id":"uuid-2","project":"Работа"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "перенесена") {
		t.Fatalf("expected text «перенесена» in response, got: %s", out)
	}
	if tasks.lastMoveID != "uuid-2" {
		t.Fatalf("MoveTask called with ID %q, want uuid-2", tasks.lastMoveID)
	}
}

func TestMoveTaskTool_Execute_ByProjectID(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:  &model.Task{ID: "uuid-3", Number: 2, ProjectID: "src", ProjectSlug: "inbox"},
		moveTaskResult: &model.Task{ID: "uuid-3", Number: 2, ProjectID: "proj-direct", ProjectSlug: "direct"},
	}
	tool := agent.NewMoveTaskTool(tasks, &mockProjectService{}, newTestLocalizer("ru"))

	out, err := tool.Execute(context.Background(), `{"task_id":"uuid-3","project_id":"proj-direct"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "перенесена") {
		t.Fatalf("expected text «перенесена» in response, got: %s", out)
	}
	if tasks.lastMoveProjID != "proj-direct" {
		t.Fatalf("MoveTask called with project_id %q, want proj-direct", tasks.lastMoveProjID)
	}
}

func TestMoveTaskTool_Execute_ProjectNotFound(t *testing.T) {
	tasks := &mockTaskService{}
	projects := &mockProjectService{}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"uuid-4","project":"Несуществующий"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when project is not found")
	}
}

func TestMoveTaskTool_Execute_InvalidRef(t *testing.T) {
	tool := agent.NewMoveTaskTool(&mockTaskService{}, &mockProjectService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"not-a-ref#0","project_id":"proj-1"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid ref format")
	}
}

func TestMoveTaskTool_Execute_MissingTaskID(t *testing.T) {
	tool := agent.NewMoveTaskTool(&mockTaskService{}, &mockProjectService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"project_id":"proj-1"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when task_id and task_ref are missing")
	}
}

func TestMoveTaskTool_Execute_MissingProject(t *testing.T) {
	tool := agent.NewMoveTaskTool(&mockTaskService{}, &mockProjectService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"uuid-5"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when project_id and project are missing")
	}
}

func TestMoveTaskTool_Metadata(t *testing.T) {
	tool := agent.NewMoveTaskTool(&mockTaskService{}, &mockProjectService{}, newTestLocalizer("ru"))

	if tool.Name() != "move_task" {
		t.Errorf("Name() = %q, want move_task", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("move_task must not be DMOnly")
	}
}

func TestMoveTaskTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewMoveTaskTool(&mockTaskService{}, &mockProjectService{}, newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

func TestMoveTaskTool_Execute_TaskRefNotFound(t *testing.T) {
	tasks := &mockTaskService{} // getTaskByRefResult == nil
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "dst", Slug: "work", Name: "Работа"},
	}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"inbox#99","project":"Работа"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when task is not found by ref")
	}
}

func TestMoveTaskTool_Execute_GetTaskByRefError(t *testing.T) {
	tasks := &mockTaskService{getTaskByRefErr: errors.New("DB error")}
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "dst", Slug: "work", Name: "Работа"},
	}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"inbox#5","project":"Работа"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when GetTaskByRef fails")
	}
}

func TestMoveTaskTool_Execute_MoveTaskError(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "uuid-1", Number: 5, ProjectID: "src", ProjectSlug: "inbox"},
		moveTaskErr:        errors.New("DB error"),
	}
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "dst", Slug: "work", Name: "Работа"},
	}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"inbox#5","project":"Работа"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when MoveTask fails")
	}
}
