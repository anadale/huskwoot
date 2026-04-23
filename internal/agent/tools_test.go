package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/dateparse"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

func newTestLocalizer(lang string) *goI18n.Localizer {
	bundle, err := huskwootI18n.NewBundle(lang)
	if err != nil {
		panic(err)
	}
	return huskwootI18n.NewLocalizer(bundle, lang)
}

// newTestDateParseConfig creates a dateparse config for tests with default clock settings.
func newTestDateParseConfig() dateparse.Config {
	return dateparse.Config{
		TimeOfDay: dateparse.TimeOfDay{
			Morning:   11,
			Lunch:     12,
			Afternoon: 14,
			Evening:   20,
		},
		Weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
	}
}

// newTestDateparser creates a Dateparser for the given language for use in tests.
func newTestDateparser(lang string) *dateparse.Dateparser {
	return dateparse.New(newTestDateParseConfig(), dateparse.NewDateLanguage(lang))
}

// ────────────────────────────────────────────────────────────────────────────
// mockTaskService

type mockTaskService struct {
	createTaskResult *model.Task
	createTaskErr    error

	createTasksResult []model.Task
	createTasksErr    error

	listTasksResult []model.Task
	listTasksErr    error

	completeTaskResult *model.Task
	completeTaskErr    error

	reopenTaskResult *model.Task
	reopenTaskErr    error

	getTaskResult *model.Task
	getTaskErr    error

	getTaskByRefResult *model.Task
	getTaskByRefErr    error

	moveTaskResult *model.Task
	moveTaskErr    error

	updateTaskResult *model.Task
	updateTaskErr    error

	lastCreateReq     model.CreateTaskRequest
	lastCompleteID    string
	lastReopenID      string
	lastMoveID        string
	lastMoveProjID    string
	lastListProjectID string
	lastListFilter    model.TaskFilter
	lastUpdateID      string
	lastUpdateReq     model.TaskUpdate
}

func (m *mockTaskService) CreateTask(_ context.Context, req model.CreateTaskRequest) (*model.Task, error) {
	m.lastCreateReq = req
	if m.createTaskErr != nil {
		return nil, m.createTaskErr
	}
	if m.createTaskResult != nil {
		return m.createTaskResult, nil
	}
	task := &model.Task{
		ID:          "new-task-uuid",
		Number:      1,
		ProjectID:   req.ProjectID,
		ProjectSlug: "inbox",
		Summary:     req.Summary,
		Details:     req.Details,
		Status:      "open",
		Deadline:    req.Deadline,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	return task, nil
}

func (m *mockTaskService) CreateTasks(_ context.Context, req model.CreateTasksRequest) ([]model.Task, error) {
	if m.createTasksErr != nil {
		return nil, m.createTasksErr
	}
	return m.createTasksResult, nil
}

func (m *mockTaskService) UpdateTask(_ context.Context, id string, upd model.TaskUpdate) (*model.Task, error) {
	m.lastUpdateID = id
	m.lastUpdateReq = upd
	return m.updateTaskResult, m.updateTaskErr
}

func (m *mockTaskService) CompleteTask(_ context.Context, id string) (*model.Task, error) {
	m.lastCompleteID = id
	if m.completeTaskErr != nil {
		return nil, m.completeTaskErr
	}
	if m.completeTaskResult != nil {
		return m.completeTaskResult, nil
	}
	return &model.Task{ID: id, Number: 1, ProjectSlug: "inbox", Status: "done"}, nil
}

func (m *mockTaskService) ReopenTask(_ context.Context, id string) (*model.Task, error) {
	m.lastReopenID = id
	return m.reopenTaskResult, m.reopenTaskErr
}

func (m *mockTaskService) MoveTask(_ context.Context, id, newProjectID string) (*model.Task, error) {
	m.lastMoveID = id
	m.lastMoveProjID = newProjectID
	return m.moveTaskResult, m.moveTaskErr
}

func (m *mockTaskService) ListTasks(_ context.Context, projectID string, filter model.TaskFilter) ([]model.Task, error) {
	m.lastListProjectID = projectID
	m.lastListFilter = filter
	if m.listTasksErr != nil {
		return nil, m.listTasksErr
	}
	var result []model.Task
	for _, t := range m.listTasksResult {
		if projectID != "" && t.ProjectID != projectID {
			continue
		}
		if filter.Status != "" && t.Status != filter.Status {
			continue
		}
		if filter.Query != "" && !strings.Contains(strings.ToLower(t.Summary), strings.ToLower(filter.Query)) {
			continue
		}
		result = append(result, t)
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (m *mockTaskService) GetTask(_ context.Context, id string) (*model.Task, error) {
	if m.getTaskErr != nil {
		return nil, m.getTaskErr
	}
	if m.getTaskResult != nil && m.getTaskResult.ID == id {
		return m.getTaskResult, nil
	}
	return nil, nil
}

func (m *mockTaskService) GetTaskByRef(_ context.Context, _ string, _ int) (*model.Task, error) {
	return m.getTaskByRefResult, m.getTaskByRefErr
}

// ────────────────────────────────────────────────────────────────────────────
// mockProjectService

type mockProjectService struct {
	findByNameResult *model.Project
	findByNameErr    error

	createResult *model.Project
	createErr    error

	listResult []model.Project
	listErr    error

	resolveResult string
	resolveErr    error

	ensureResult *model.Project
	ensureErr    error

	lastEnsureChannelID string
	lastEnsureName      string
	lastCreateReq       model.CreateProjectRequest
}

func (m *mockProjectService) CreateProject(_ context.Context, req model.CreateProjectRequest) (*model.Project, error) {
	m.lastCreateReq = req
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createResult != nil {
		return m.createResult, nil
	}
	slug := strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))
	return &model.Project{ID: "new-proj-uuid", Name: req.Name, Slug: slug, Description: req.Description}, nil
}

func (m *mockProjectService) UpdateProject(_ context.Context, _ string, _ model.ProjectUpdate) (*model.Project, error) {
	return nil, nil
}

func (m *mockProjectService) ListProjects(_ context.Context) ([]model.Project, error) {
	return m.listResult, m.listErr
}

func (m *mockProjectService) FindProjectByName(_ context.Context, name string) (*model.Project, error) {
	if m.findByNameErr != nil {
		return nil, m.findByNameErr
	}
	if m.findByNameResult != nil && m.findByNameResult.Name == name {
		return m.findByNameResult, nil
	}
	return nil, nil
}

func (m *mockProjectService) ResolveProjectForChannel(_ context.Context, _ string) (string, error) {
	return m.resolveResult, m.resolveErr
}

func (m *mockProjectService) EnsureChannelProject(_ context.Context, channelID, name string) (*model.Project, error) {
	m.lastEnsureChannelID = channelID
	m.lastEnsureName = name
	if m.ensureErr != nil {
		return nil, m.ensureErr
	}
	if m.ensureResult != nil {
		return m.ensureResult, nil
	}
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	return &model.Project{ID: "ensure-proj-uuid", Name: name, Slug: slug}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// --- createProjectTool ---

func TestCreateProjectTool_Execute_Success(t *testing.T) {
	projects := &mockProjectService{
		createResult: &model.Project{ID: "proj-1", Slug: "rabota", Name: "Работа"},
	}
	tool := agent.NewCreateProjectTool(projects, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"name":"Работа","description":"Рабочие задачи"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["name"] != "Работа" {
		t.Errorf("name = %v, want %q", got["name"], "Работа")
	}
	if got["id"] == nil || got["id"] == "" {
		t.Error("id must not be empty")
	}
	if got["slug"] != "rabota" {
		t.Errorf("slug = %v, want %q", got["slug"], "rabota")
	}
}

func TestCreateProjectTool_Execute_DuplicateName(t *testing.T) {
	projects := &mockProjectService{
		createErr: errors.New("проект с таким именем уже существует"),
	}
	tool := agent.NewCreateProjectTool(projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"name":"Работа"}`)
	if err == nil {
		t.Fatal("Execute() must return an error on duplicate name")
	}
}

func TestCreateProjectTool_Metadata(t *testing.T) {
	tool := agent.NewCreateProjectTool(&mockProjectService{}, newTestLocalizer("ru"))

	if tool.Name() != "create_project" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if !tool.DMOnly() {
		t.Error("create_project must be DMOnly")
	}
}

func TestCreateProjectTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewCreateProjectTool(&mockProjectService{}, newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

// --- listProjectsTool ---

func TestListProjectsTool_Execute_ReturnsProjects(t *testing.T) {
	projects := &mockProjectService{
		listResult: []model.Project{
			{ID: "uuid-a", Slug: "proekt-a", Name: "Проект А"},
			{ID: "uuid-b", Slug: "proekt-b", Name: "Проект Б", Description: "Описание"},
		},
	}
	tool := agent.NewListProjectsTool(projects, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d projects, want 2", len(got))
	}
	if got[0]["id"] != "uuid-a" || got[0]["name"] != "Проект А" {
		t.Errorf("first project: %v", got[0])
	}
	if got[0]["slug"] != "proekt-a" {
		t.Errorf("first project slug: %v", got[0]["slug"])
	}
}

func TestListProjectsTool_Execute_EmptyList(t *testing.T) {
	tool := agent.NewListProjectsTool(&mockProjectService{}, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %d elements", len(got))
	}
}

func TestListProjectsTool_Metadata(t *testing.T) {
	tool := agent.NewListProjectsTool(&mockProjectService{}, newTestLocalizer("ru"))

	if tool.Name() != "list_projects" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if !tool.DMOnly() {
		t.Error("list_projects must be DMOnly")
	}
}

func TestListProjectsTool_Execute_StoreError(t *testing.T) {
	projects := &mockProjectService{
		listErr: errors.New("база данных недоступна"),
	}
	tool := agent.NewListProjectsTool(projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("Execute() must return an error when store fails")
	}
}

// --- createTaskTool ---

func TestCreateTaskTool_Execute_WithProjectID(t *testing.T) {
	tasks := &mockTaskService{
		createTaskResult: &model.Task{
			ID: "task-uuid-1", Number: 3, ProjectID: "proj-uuid-5",
			ProjectSlug: "test", Summary: "написать отчёт", Status: "open",
		},
	}
	projects := &mockProjectService{}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"summary":"написать отчёт","project_id":"proj-uuid-5"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["summary"] != "написать отчёт" {
		t.Errorf("summary = %v", got["summary"])
	}
	if got["display_id"] != "test#3" {
		t.Errorf("display_id = %v, want test#3", got["display_id"])
	}
	if tasks.lastCreateReq.ProjectID != "proj-uuid-5" {
		t.Errorf("project_id = %q, want proj-uuid-5", tasks.lastCreateReq.ProjectID)
	}
}

func TestCreateTaskTool_Execute_WithProjectName(t *testing.T) {
	tasks := &mockTaskService{}
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "proj-uuid-9", Slug: "na-start", Name: "На Старт"},
	}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"summary":"задача","project":"На Старт"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if tasks.lastCreateReq.ProjectID != "proj-uuid-9" {
		t.Errorf("project_id = %q, want proj-uuid-9", tasks.lastCreateReq.ProjectID)
	}
}

func TestCreateTaskTool_Execute_WithoutProjectID_UsesChannelProject(t *testing.T) {
	tasks := &mockTaskService{}
	projects := &mockProjectService{resolveResult: "resolved-proj-uuid"}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	ctx := context.WithValue(context.Background(), agent.ExportedSourceIDKey, "chat-42")
	_, err := tool.Execute(ctx, `{"summary":"задача"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if tasks.lastCreateReq.ProjectID != "resolved-proj-uuid" {
		t.Errorf("project_id = %q, want resolved-proj-uuid", tasks.lastCreateReq.ProjectID)
	}
}

func TestCreateTaskTool_Execute_WithDeadline(t *testing.T) {
	tasks := &mockTaskService{}
	projects := &mockProjectService{}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	deadline := "2026-12-31T23:59:59Z"
	_, err := tool.Execute(context.Background(), `{"summary":"сдать проект","deadline":"`+deadline+`"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastCreateReq.Deadline == nil {
		t.Fatal("deadline was not set")
	}
	wantDL, _ := time.Parse(time.RFC3339, deadline)
	if !tasks.lastCreateReq.Deadline.Equal(wantDL) {
		t.Errorf("deadline = %v, want %v", tasks.lastCreateReq.Deadline, wantDL)
	}
}

func TestCreateTaskTool_Execute_InvalidDeadline(t *testing.T) {
	tool := agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"summary":"задача","deadline":"не-дата"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid deadline")
	}
}

func TestCreateTaskTool_Metadata(t *testing.T) {
	tool := agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	if tool.Name() != "create_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("create_task must not be DMOnly")
	}
}

func TestCreateTaskTool_Execute_DeduplicatesOpenTask(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "dup-uuid", Number: 42, ProjectID: "proj-1", ProjectSlug: "inbox", Summary: "добавить голосовые сообщения", Status: "open"},
		},
	}
	projects := &mockProjectService{}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"summary":"Добавить голосовые сообщения","project_id":"proj-1"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "dup-uuid" {
		t.Errorf("id = %v, want dup-uuid", got["id"])
	}
	if got["note"] != "задача уже существует" {
		t.Errorf("note = %v, want %q", got["note"], "задача уже существует")
	}
	// No new task should have been created.
	if tasks.lastCreateReq.Summary != "" {
		t.Error("a new task must not be created when a duplicate exists")
	}
}

func TestCreateTaskTool_Execute_AllowsNewTaskIfDoneExists(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "done-uuid", ProjectID: "proj-1", Summary: "добавить голосовые сообщения", Status: "done"},
		},
	}
	projects := &mockProjectService{}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"summary":"Добавить голосовые сообщения","project_id":"proj-1"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["note"] == "задача уже существует" {
		t.Error("must not deduplicate a task with status done")
	}
}

func TestCreateTaskTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

func TestCreateTaskTool_Execute_WithNaturalDeadline_Tomorrow(t *testing.T) {
	tasks := &mockTaskService{}
	projects := &mockProjectService{}
	tool := agent.NewCreateTaskTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"summary":"срочное","deadline":"завтра"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastCreateReq.Deadline == nil {
		t.Fatal("deadline was not set for «завтра»")
	}
	expectedDL := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)
	if !tasks.lastCreateReq.Deadline.Equal(expectedDL) {
		t.Errorf("deadline = %v, want %v", tasks.lastCreateReq.Deadline, expectedDL)
	}
}

func TestCreateTaskTool_Execute_WithNaturalDeadline_TomorrowAtTime(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"summary":"встреча","deadline":"завтра в 10"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastCreateReq.Deadline == nil {
		t.Fatal("deadline was not set for «завтра в 10»")
	}
	expectedDL := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	if !tasks.lastCreateReq.Deadline.Equal(expectedDL) {
		t.Errorf("deadline = %v, want %v", tasks.lastCreateReq.Deadline, expectedDL)
	}
}

func TestCreateTaskTool_Execute_WithNaturalDeadline_Weekday(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC) // Wednesday
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"summary":"отчёт","deadline":"в пятницу"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastCreateReq.Deadline == nil {
		t.Fatal("deadline was not set for «в пятницу»")
	}
	expectedDL := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	if !tasks.lastCreateReq.Deadline.Equal(expectedDL) {
		t.Errorf("deadline = %v, want %v", tasks.lastCreateReq.Deadline, expectedDL)
	}
}

func TestCreateTaskTool_Execute_WithNaturalDeadline_RelativeDays(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"summary":"проект","deadline":"через 2 дня"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastCreateReq.Deadline == nil {
		t.Fatal("deadline was not set for «через 2 дня»")
	}
	expectedDL := time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC)
	if !tasks.lastCreateReq.Deadline.Equal(expectedDL) {
		t.Errorf("deadline = %v, want %v", tasks.lastCreateReq.Deadline, expectedDL)
	}
}

func TestCreateTaskTool_Execute_WithNaturalDeadline_Weekend(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC) // Wednesday
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"summary":"ревью","deadline":"к выходным"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastCreateReq.Deadline == nil {
		t.Fatal("deadline was not set for «к выходным»")
	}
	expectedDL := time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC) // Saturday 00:00
	if !tasks.lastCreateReq.Deadline.Equal(expectedDL) {
		t.Errorf("deadline = %v, want %v", tasks.lastCreateReq.Deadline, expectedDL)
	}
}

// --- listTasksTool ---

func TestListTasksTool_Execute_NoStatus_DefaultsToOpen(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectID: "p1", ProjectSlug: "inbox", Summary: "задача 1", Status: "open"},
			{ID: "t2", Number: 2, ProjectID: "p1", ProjectSlug: "inbox", Summary: "задача 2", Status: "done"},
			{ID: "t3", Number: 1, ProjectID: "p2", ProjectSlug: "work", Summary: "другого проекта", Status: "open"},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"project_id":"p1"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d tasks, want 1 (only open from p1)", len(got))
	}
	if len(got) >= 1 && got[0]["status"] != "open" {
		t.Errorf("task status = %v, want open", got[0]["status"])
	}
}

func TestListTasksTool_Execute_ExplicitStatusDoneOverridesDefault(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectID: "p1", Summary: "открытая", Status: "open"},
			{ID: "t2", Number: 2, ProjectID: "p1", Summary: "закрытая", Status: "done"},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"project_id":"p1","status":"done"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0]["status"] != "done" {
		t.Errorf("want 1 done task, got: %v", got)
	}
}

func TestListTasksTool_Execute_WithLimit(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectID: "p1", Summary: "задача 1", Status: "open"},
			{ID: "t2", Number: 2, ProjectID: "p1", Summary: "задача 2", Status: "open"},
			{ID: "t3", Number: 3, ProjectID: "p1", Summary: "задача 3", Status: "open"},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"limit":2}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d tasks, want 2 (limit=2)", len(got))
	}
}

func TestListTasksTool_Execute_ClosedAtInResult(t *testing.T) {
	closedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "закрытая", Status: "done", ClosedAt: &closedAt},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"status":"done"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d", len(got))
	}
	if got[0]["closed_at"] == nil {
		t.Error("closed_at must be present in result for a closed task")
	}
}

func TestListTasksTool_Execute_ReturnsDisplayID(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 5, ProjectID: "p1", ProjectSlug: "huskwoot", Summary: "задача", Status: "open"},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d", len(got))
	}
	if got[0]["display_id"] != "huskwoot#5" {
		t.Errorf("display_id = %v, want huskwoot#5", got[0]["display_id"])
	}
	if got[0]["project_slug"] != "huskwoot" {
		t.Errorf("project_slug = %v, want huskwoot", got[0]["project_slug"])
	}
}

func TestListTasksTool_Execute_WithoutProject_ReturnsAll(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", ProjectID: "p1", Summary: "в inbox", Status: "open"},
			{ID: "t2", ProjectID: "p2", Summary: "в другом проекте", Status: "open"},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want tasks from all projects (2), got %d", len(got))
	}
}

func TestListTasksTool_Execute_FilterByQuery(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", ProjectID: "p1", Summary: "Переделать идентификаторы на числовые", Status: "open"},
			{ID: "t2", ProjectID: "p1", Summary: "Добавить поиск задач", Status: "open"},
			{ID: "t3", ProjectID: "p1", Summary: "Исправить баг с идентификатором", Status: "open"},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"query":"идентификатор"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 tasks for query «идентификатор», got %d", len(got))
	}
}

func TestListTasksTool_Metadata(t *testing.T) {
	tool := agent.NewListTasksTool(&mockTaskService{}, newTestLocalizer("ru"))

	if tool.Name() != "list_tasks" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("list_tasks must not be DMOnly")
	}
}

func TestListTasksTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewListTasksTool(&mockTaskService{}, newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

func TestListTasksTool_Execute_StoreError(t *testing.T) {
	tasks := &mockTaskService{listTasksErr: errors.New("база данных недоступна")}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"project_id":"p1"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when store fails")
	}
}

// --- completeTaskTool ---

func TestCompleteTaskTool_Execute_ByUUID(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:      &model.Task{ID: "task-uuid-7", Number: 7, ProjectSlug: "inbox", Status: "open"},
		completeTaskResult: &model.Task{ID: "task-uuid-7", Number: 7, ProjectSlug: "inbox", Status: "done"},
	}
	tool := agent.NewCompleteTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-7"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "task-uuid-7" {
		t.Errorf("id = %v, want task-uuid-7", got["id"])
	}
	if got["status"] != "done" {
		t.Errorf("status = %v, want done", got["status"])
	}
	if tasks.lastCompleteID != "task-uuid-7" {
		t.Errorf("CompleteTask called with ID %q, want task-uuid-7", tasks.lastCompleteID)
	}
}

func TestCompleteTaskTool_Execute_ByRef(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "ref-task-uuid", Number: 5, ProjectSlug: "inbox", Status: "open"},
		completeTaskResult: &model.Task{ID: "ref-task-uuid", Number: 5, ProjectSlug: "inbox", Status: "done"},
	}
	tool := agent.NewCompleteTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"inbox#5"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "done" {
		t.Errorf("status = %v, want done", got["status"])
	}
	if tasks.lastCompleteID != "ref-task-uuid" {
		t.Errorf("CompleteTask called with ID %q, want ref-task-uuid", tasks.lastCompleteID)
	}
}

func TestCompleteTaskTool_Execute_ErrorOnNotFound(t *testing.T) {
	tasks := &mockTaskService{completeTaskErr: errors.New("задача не найдена")}
	tool := agent.NewCompleteTaskTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"nonexistent"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for a non-existent ID")
	}
}

func TestCompleteTaskTool_Execute_InvalidRef(t *testing.T) {
	tool := agent.NewCompleteTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"not-a-ref#0"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid ref format")
	}
}

func TestCompleteTaskTool_Execute_MissingID(t *testing.T) {
	tool := agent.NewCompleteTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("Execute() must return an error when task_id and task_ref are missing")
	}
}

func TestCompleteTaskTool_Metadata(t *testing.T) {
	tool := agent.NewCompleteTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	if tool.Name() != "complete_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("complete_task must not be DMOnly")
	}
}

func TestCompleteTaskTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewCompleteTaskTool(&mockTaskService{}, newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

func TestCompleteTaskTool_Execute_AlreadyDone_Idempotent(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-done", Number: 9, ProjectSlug: "inbox", Status: "done"},
	}
	tool := agent.NewCompleteTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-done"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "done" {
		t.Errorf("status = %v, want done", got["status"])
	}
	if tasks.lastCompleteID != "" {
		t.Errorf("CompleteTask must not be called for already-done task, got ID %q", tasks.lastCompleteID)
	}
}

// --- setProjectTool ---

func TestSetProjectTool_Execute_EnsuresChannelProject(t *testing.T) {
	projects := &mockProjectService{
		ensureResult: &model.Project{ID: "proj-abc", Slug: "rabota", Name: "Работа"},
	}
	tool := agent.NewSetProjectTool(projects, newTestLocalizer("ru"))

	ctx := context.WithValue(context.Background(), agent.ExportedSourceIDKey, "chat-42")
	result, err := tool.Execute(ctx, `{"name":"Работа"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["name"] != "Работа" {
		t.Errorf("name = %v, want Работа", got["name"])
	}
	if got["project_id"] != "proj-abc" {
		t.Errorf("project_id = %v, want proj-abc", got["project_id"])
	}
	if projects.lastEnsureChannelID != "chat-42" {
		t.Errorf("EnsureChannelProject called with channelID %q, want chat-42", projects.lastEnsureChannelID)
	}
	if projects.lastEnsureName != "Работа" {
		t.Errorf("EnsureChannelProject called with name %q, want Работа", projects.lastEnsureName)
	}
}

func TestSetProjectTool_Execute_FindsExistingWithoutSourceID(t *testing.T) {
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "proj-5", Slug: "domashnie", Name: "Домашние дела"},
	}
	tool := agent.NewSetProjectTool(projects, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"name":"Домашние дела"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["project_id"] != "proj-5" {
		t.Errorf("project_id = %v, want proj-5", got["project_id"])
	}
	// EnsureChannelProject should not have been called.
	if projects.lastEnsureChannelID != "" {
		t.Error("EnsureChannelProject must not be called without sourceID")
	}
}

func TestSetProjectTool_Execute_CreatesProjectWithoutSourceID(t *testing.T) {
	projects := &mockProjectService{}
	tool := agent.NewSetProjectTool(projects, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"name":"Тест"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result == "" {
		t.Error("result must not be empty")
	}
}

func TestSetProjectTool_Metadata(t *testing.T) {
	tool := agent.NewSetProjectTool(&mockProjectService{}, newTestLocalizer("ru"))

	if tool.Name() != "set_project" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("set_project must not be DMOnly")
	}
}

func TestSetProjectTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewSetProjectTool(&mockProjectService{}, newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

// --- localization tests ---

func TestCreateTaskTool_Description_Russian(t *testing.T) {
	tool := agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))
	desc := tool.Description()
	if !strings.Contains(desc, "задачу") {
		t.Errorf("expected Russian description, got: %q", desc)
	}
}

func TestCreateTaskTool_Description_English(t *testing.T) {
	tool := agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("en"))
	desc := tool.Description()
	if !strings.Contains(desc, "task") {
		t.Errorf("expected English description, got: %q", desc)
	}
}

func TestMoveTaskTool_Description_English(t *testing.T) {
	tool := agent.NewMoveTaskTool(&mockTaskService{}, &mockProjectService{}, newTestLocalizer("en"))
	desc := tool.Description()
	if !strings.Contains(desc, "Move") {
		t.Errorf("expected English description, got: %q", desc)
	}
}

func TestMoveTaskTool_Execute_LocalizedResponse_English(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "uuid-1", Number: 5, ProjectID: "src", ProjectSlug: "inbox"},
		moveTaskResult:     &model.Task{ID: "uuid-1", Number: 3, ProjectID: "dst", ProjectSlug: "work"},
	}
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "dst", Slug: "work", Name: "Work"},
	}
	tool := agent.NewMoveTaskTool(tasks, projects, newTestLocalizer("en"))

	out, err := tool.Execute(context.Background(), `{"task_id":"inbox#5","project":"Work"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "moved") {
		t.Errorf("expected English 'moved' in response, got: %s", out)
	}
}

func TestCompleteTaskTool_Description_English(t *testing.T) {
	tool := agent.NewCompleteTaskTool(&mockTaskService{}, newTestLocalizer("en"))
	desc := tool.Description()
	if !strings.Contains(desc, "done") {
		t.Errorf("expected English description, got: %q", desc)
	}
}

func TestListTasksTool_Description_English(t *testing.T) {
	tool := agent.NewListTasksTool(&mockTaskService{}, newTestLocalizer("en"))
	desc := tool.Description()
	if !strings.Contains(desc, "List") {
		t.Errorf("expected English description, got: %q", desc)
	}
}

func TestCreateTaskTool_Execute_DeduplicatesOpenTask_Localized(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "dup-uuid", Number: 42, ProjectID: "proj-1", ProjectSlug: "inbox", Summary: "write report", Status: "open"},
		},
	}
	tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("en"))

	result, err := tool.Execute(context.Background(), `{"summary":"Write Report","project_id":"proj-1"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["note"] != "task already exists" {
		t.Errorf("note = %v, expected %q", got["note"], "task already exists")
	}
}

// --- getTaskTool ---

func TestGetTaskTool_Execute_ByUUID(t *testing.T) {
	deadline := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		getTaskResult: &model.Task{
			ID: "task-uuid-1", Number: 3, ProjectID: "proj-1", ProjectSlug: "inbox",
			Summary: "написать отчёт", Details: "подробности", Topic: "Работа",
			Status: "open", Deadline: &deadline,
			CreatedAt: time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		},
	}
	tool := agent.NewGetTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-1"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "task-uuid-1" {
		t.Errorf("id = %v, want task-uuid-1", got["id"])
	}
	if got["display_id"] != "inbox#3" {
		t.Errorf("display_id = %v, want inbox#3", got["display_id"])
	}
	if got["summary"] != "написать отчёт" {
		t.Errorf("summary = %v", got["summary"])
	}
	if got["details"] != "подробности" {
		t.Errorf("details = %v", got["details"])
	}
	if got["topic"] != "Работа" {
		t.Errorf("topic = %v", got["topic"])
	}
	if got["deadline"] == nil {
		t.Error("deadline must be present")
	}
	if got["closed_at"] != nil {
		t.Errorf("closed_at must be absent for open task, got: %v", got["closed_at"])
	}
}

func TestGetTaskTool_Execute_DeadlineNilOmitted(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{
			ID: "task-uuid-2", Number: 1, ProjectSlug: "inbox", Summary: "задача", Status: "open",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	tool := agent.NewGetTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-2"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := got["deadline"]; ok {
		t.Errorf("deadline must be absent when nil, got: %v", got["deadline"])
	}
	if _, ok := got["closed_at"]; ok {
		t.Errorf("closed_at must be absent when nil, got: %v", got["closed_at"])
	}
	if _, ok := got["details"]; ok {
		t.Errorf("details must be absent when empty, got: %v", got["details"])
	}
}

func TestGetTaskTool_Execute_ClosedAtPresent(t *testing.T) {
	closedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		getTaskResult: &model.Task{
			ID: "task-uuid-3", Number: 2, ProjectSlug: "inbox", Summary: "закрытая",
			Status: "done", ClosedAt: &closedAt,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	tool := agent.NewGetTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-3"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["closed_at"] == nil {
		t.Error("closed_at must be present for done task")
	}
}

func TestGetTaskTool_Execute_ByRef(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{
			ID: "ref-uuid", Number: 5, ProjectSlug: "inbox", Summary: "по ссылке",
			Status: "open", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	tool := agent.NewGetTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"inbox#5"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "ref-uuid" {
		t.Errorf("id = %v, want ref-uuid", got["id"])
	}
}

func TestGetTaskTool_Execute_NotFound(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewGetTaskTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"nonexistent-uuid"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for non-existent task")
	}
}

func TestGetTaskTool_Execute_MissingID(t *testing.T) {
	tool := agent.NewGetTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("Execute() must return an error when task_id is missing")
	}
}

func TestGetTaskTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewGetTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

func TestGetTaskTool_Metadata(t *testing.T) {
	tool := agent.NewGetTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	if tool.Name() != "get_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("get_task must not be DMOnly")
	}
}

// --- cancelTaskTool ---

func TestCancelTaskTool_Execute_Normal(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-4", Number: 4, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-4", Number: 4, ProjectSlug: "inbox", Status: "cancelled"},
	}
	tool := agent.NewCancelTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-4"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", got["status"])
	}
	if tasks.lastUpdateID != "task-uuid-4" {
		t.Errorf("UpdateTask called with ID %q, want task-uuid-4", tasks.lastUpdateID)
	}
	if tasks.lastUpdateReq.Status == nil || *tasks.lastUpdateReq.Status != "cancelled" {
		t.Errorf("UpdateTask.Status = %v, want &\"cancelled\"", tasks.lastUpdateReq.Status)
	}
}

func TestCancelTaskTool_Execute_AlreadyCancelled_Idempotent(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-5", Number: 5, ProjectSlug: "inbox", Status: "cancelled"},
	}
	tool := agent.NewCancelTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-5"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", got["status"])
	}
	// UpdateTask must not be called for already-cancelled task.
	if tasks.lastUpdateID != "" {
		t.Errorf("UpdateTask must not be called for already-cancelled task, got ID %q", tasks.lastUpdateID)
	}
}

func TestCancelTaskTool_Execute_NotFound(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewCancelTaskTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"nonexistent"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for non-existent task")
	}
}

func TestCancelTaskTool_Execute_UpdateTaskError(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-4", Number: 4, ProjectSlug: "inbox", Status: "open"},
		updateTaskErr: errors.New("database error"),
	}
	tool := agent.NewCancelTaskTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-4"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when UpdateTask fails")
	}
}

func TestCancelTaskTool_Metadata(t *testing.T) {
	tool := agent.NewCancelTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	if tool.Name() != "cancel_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("cancel_task must not be DMOnly")
	}
}

// --- reopenTaskTool ---

func TestReopenTaskTool_Execute_FromDone(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-6", Number: 6, ProjectSlug: "inbox", Status: "done"},
		reopenTaskResult: &model.Task{ID: "task-uuid-6", Number: 6, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewReopenTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-6"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "open" {
		t.Errorf("status = %v, want open", got["status"])
	}
	if got["id"] != "task-uuid-6" {
		t.Errorf("id = %v, want task-uuid-6", got["id"])
	}
}

func TestReopenTaskTool_Execute_FromCancelled(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-7", Number: 7, ProjectSlug: "work", Status: "cancelled"},
		reopenTaskResult: &model.Task{ID: "task-uuid-7", Number: 7, ProjectSlug: "work", Status: "open"},
	}
	tool := agent.NewReopenTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-7"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "open" {
		t.Errorf("status = %v, want open", got["status"])
	}
}

func TestReopenTaskTool_Execute_ByRef(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "ref-uuid-2", Number: 3, ProjectSlug: "inbox", Status: "done"},
		reopenTaskResult:   &model.Task{ID: "ref-uuid-2", Number: 3, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewReopenTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"inbox#3"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "ref-uuid-2" {
		t.Errorf("id = %v, want ref-uuid-2", got["id"])
	}
	if tasks.lastReopenID != "ref-uuid-2" {
		t.Errorf("ReopenTask called with ID %q, want ref-uuid-2", tasks.lastReopenID)
	}
}

func TestReopenTaskTool_Execute_FromOpen(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-open", Number: 9, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewReopenTaskTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-open"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["status"] != "open" {
		t.Errorf("status = %v, want open", got["status"])
	}
	if got["id"] != "task-uuid-open" {
		t.Errorf("id = %v, want task-uuid-open", got["id"])
	}
	if tasks.lastReopenID != "" {
		t.Errorf("ReopenTask must not be called for already-open task, got ID %q", tasks.lastReopenID)
	}
}

func TestReopenTaskTool_Execute_NotFound(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewReopenTaskTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"nonexistent"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for non-existent task")
	}
}

func TestReopenTaskTool_Execute_ServiceError(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-8", Number: 8, ProjectSlug: "inbox", Status: "done"},
		reopenTaskErr: errors.New("database error"),
	}
	tool := agent.NewReopenTaskTool(tasks, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-8"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when ReopenTask fails")
	}
}

func TestReopenTaskTool_Metadata(t *testing.T) {
	tool := agent.NewReopenTaskTool(&mockTaskService{}, newTestLocalizer("ru"))

	if tool.Name() != "reopen_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("reopen_task must not be DMOnly")
	}
}

// --- updateTaskTool ---

func TestUpdateTaskTool_Execute_SummaryOnly(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-10", Number: 10, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-10", Number: 10, ProjectSlug: "inbox", Summary: "новое описание", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-10","summary":"новое описание"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["summary"] != "новое описание" {
		t.Errorf("summary = %v, want %q", got["summary"], "новое описание")
	}
	if tasks.lastUpdateID != "task-uuid-10" {
		t.Errorf("UpdateTask called with ID %q, want task-uuid-10", tasks.lastUpdateID)
	}
	if tasks.lastUpdateReq.Summary == nil || *tasks.lastUpdateReq.Summary != "новое описание" {
		t.Errorf("UpdateTask.Summary = %v, want &\"новое описание\"", tasks.lastUpdateReq.Summary)
	}
	if tasks.lastUpdateReq.Details != nil {
		t.Error("Details must be nil when not provided")
	}
}

func TestUpdateTaskTool_Execute_DetailsOnly_NonEmpty(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-11", Number: 11, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-11", Number: 11, ProjectSlug: "inbox", Summary: "задача", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-11","details":"новые детали"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastUpdateReq.Details == nil || *tasks.lastUpdateReq.Details != "новые детали" {
		t.Errorf("UpdateTask.Details = %v, want &\"новые детали\"", tasks.lastUpdateReq.Details)
	}
	if tasks.lastUpdateReq.Summary != nil {
		t.Error("Summary must be nil when not provided")
	}
}

func TestUpdateTaskTool_Execute_Details_EmptyStringClears(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-12", Number: 12, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-12", Number: 12, ProjectSlug: "inbox", Summary: "задача", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-12","details":""}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastUpdateReq.Details == nil {
		t.Fatal("Details must be set (pointer to empty string) for explicit empty value")
	}
	if *tasks.lastUpdateReq.Details != "" {
		t.Errorf("Details = %q, want empty string", *tasks.lastUpdateReq.Details)
	}
}

func TestUpdateTaskTool_Execute_Deadline_ClearsOnEmpty(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-13", Number: 13, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-13", Number: 13, ProjectSlug: "inbox", Summary: "задача", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-13","deadline":""}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastUpdateReq.Deadline == nil {
		t.Fatal("Deadline must be set (pointer to nil) to clear it")
	}
	if *tasks.lastUpdateReq.Deadline != nil {
		t.Error("Deadline inner pointer must be nil to clear")
	}
}

func TestUpdateTaskTool_Execute_Deadline_NaturalLanguage(t *testing.T) {
	deadline := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-14", Number: 14, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-14", Number: 14, ProjectSlug: "inbox", Summary: "задача", Status: "open", Deadline: &deadline},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC) // Thursday
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"task_id":"task-uuid-14","deadline":"послезавтра"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastUpdateReq.Deadline == nil {
		t.Fatal("Deadline must be set")
	}
	if *tasks.lastUpdateReq.Deadline == nil {
		t.Fatal("Deadline inner pointer must not be nil when date is provided")
	}
	wantDeadline := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	if !(*tasks.lastUpdateReq.Deadline).Equal(wantDeadline) {
		t.Errorf("Deadline = %v, want %v", *tasks.lastUpdateReq.Deadline, wantDeadline)
	}
}

func TestUpdateTaskTool_Execute_AllFields(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-15", Number: 15, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-15", Number: 15, ProjectSlug: "inbox", Summary: "обновлено", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"task_id":"task-uuid-15","summary":"обновлено","details":"детали","deadline":"завтра"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if tasks.lastUpdateReq.Summary == nil || *tasks.lastUpdateReq.Summary != "обновлено" {
		t.Error("Summary not set correctly")
	}
	if tasks.lastUpdateReq.Details == nil || *tasks.lastUpdateReq.Details != "детали" {
		t.Error("Details not set correctly")
	}
	if tasks.lastUpdateReq.Deadline == nil || *tasks.lastUpdateReq.Deadline == nil {
		t.Error("Deadline not set correctly")
	}
}

func TestUpdateTaskTool_Execute_NoFields_Error(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-16", Number: 16, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-16"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when no fields are provided")
	}
}

func TestUpdateTaskTool_Execute_EmptySummary_Error(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-17", Number: 17, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-17","summary":""}`)
	if err == nil {
		t.Fatal("Execute() must return an error for empty summary")
	}
}

func TestUpdateTaskTool_Execute_InvalidDeadline_Error(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-18", Number: 18, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-18","deadline":"не-дата-вообще"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid deadline")
	}
}

func TestUpdateTaskTool_Execute_NotFound(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"nonexistent-uuid","summary":"новое"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for non-existent task")
	}
}

func TestUpdateTaskTool_Execute_ByRef(t *testing.T) {
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "ref-uuid-3", Number: 7, ProjectSlug: "work", Status: "open"},
		updateTaskResult:   &model.Task{ID: "ref-uuid-3", Number: 7, ProjectSlug: "work", Summary: "обновлено по ссылке", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"work#7","summary":"обновлено по ссылке"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "ref-uuid-3" {
		t.Errorf("id = %v, want ref-uuid-3", got["id"])
	}
}

func TestUpdateTaskTool_Execute_ResponseContainsNote(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-19", Number: 19, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-19", Number: 19, ProjectSlug: "inbox", Summary: "задача", Status: "open"},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-19","summary":"задача"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["note"] == nil || got["note"] == "" {
		t.Error("response must contain a note field")
	}
}

func TestUpdateTaskTool_Execute_UpdateTaskServiceError(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-20", Number: 20, ProjectSlug: "inbox", Status: "open"},
		updateTaskErr: errors.New("database error"),
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-20","summary":"новое название"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when UpdateTask fails")
	}
}

func TestUpdateTaskTool_Metadata(t *testing.T) {
	tool := agent.NewUpdateTaskTool(&mockTaskService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	if tool.Name() != "update_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("update_task must not be DMOnly")
	}
}

// --- snoozeTaskTool ---

func TestSnoozeTaskTool_Execute_NormalSnooze(t *testing.T) {
	deadline := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-20", Number: 20, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-20", Number: 20, ProjectSlug: "inbox", Status: "open", Deadline: &deadline},
	}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC) // Thursday
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	result, err := tool.Execute(ctx, `{"task_id":"task-uuid-20","until":"завтра"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "task-uuid-20" {
		t.Errorf("id = %v, want task-uuid-20", got["id"])
	}
	if got["display_id"] != "inbox#20" {
		t.Errorf("display_id = %v, want inbox#20", got["display_id"])
	}
	if got["deadline"] == nil {
		t.Error("deadline must be present in response")
	}
	if tasks.lastUpdateID != "task-uuid-20" {
		t.Errorf("UpdateTask called with ID %q, want task-uuid-20", tasks.lastUpdateID)
	}
	if tasks.lastUpdateReq.Deadline == nil || *tasks.lastUpdateReq.Deadline == nil {
		t.Error("Deadline must be set to parsed time")
	}
}

func TestSnoozeTaskTool_Execute_MissingUntil_Error(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-21", Number: 21, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-21"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when until is missing")
	}
}

func TestSnoozeTaskTool_Execute_EmptyUntil_Error(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-22", Number: 22, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-22","until":""}`)
	if err == nil {
		t.Fatal("Execute() must return an error when until is empty string")
	}
}

func TestSnoozeTaskTool_Execute_InvalidUntil_Error(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-23", Number: 23, ProjectSlug: "inbox", Status: "open"},
	}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-23","until":"не-дата-вообще"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid until expression")
	}
}

func TestSnoozeTaskTool_Execute_NotFound(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"task_id":"nonexistent-uuid","until":"завтра"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for non-existent task")
	}
}

func TestSnoozeTaskTool_Execute_ByRef(t *testing.T) {
	deadline := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		getTaskByRefResult: &model.Task{ID: "ref-uuid-4", Number: 9, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult:   &model.Task{ID: "ref-uuid-4", Number: 9, ProjectSlug: "inbox", Status: "open", Deadline: &deadline},
	}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC) // Thursday
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	result, err := tool.Execute(ctx, `{"task_id":"inbox#9","until":"в понедельник"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "ref-uuid-4" {
		t.Errorf("id = %v, want ref-uuid-4", got["id"])
	}
}

func TestSnoozeTaskTool_Execute_UpdateTaskError(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-snooze", Number: 1, ProjectSlug: "inbox", Status: "open"},
		updateTaskErr: errors.New("database error"),
	}
	tool := agent.NewSnoozeTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	fixedNow := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), agent.NowKey, fixedNow)
	_, err := tool.Execute(ctx, `{"task_id":"task-uuid-snooze","until":"завтра"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when UpdateTask fails")
	}
}

func TestSnoozeTaskTool_Metadata(t *testing.T) {
	tool := agent.NewSnoozeTaskTool(&mockTaskService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	if tool.Name() != "snooze_task" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("snooze_task must not be DMOnly")
	}
}

// --- searchTasksTool ---

func TestSearchTasksTool_Execute_ByQuery(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "написать отчёт", Status: "open"},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "созвониться с командой", Status: "open"},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "написать тесты", Status: "open"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"query":"написать"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d results, want 2 (matching «написать»)", len(got))
	}
}

func TestSearchTasksTool_Execute_StatusOpen_Default(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "открытая", Status: "open"},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "закрытая", Status: "done"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0]["status"] != "open" {
		t.Errorf("default status must be open, got: %v", got)
	}
	if tasks.lastListFilter.Status != "open" {
		t.Errorf("ListTasks filter.Status = %q, want open", tasks.lastListFilter.Status)
	}
}

func TestSearchTasksTool_Execute_StatusDone(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "открытая", Status: "open"},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "выполненная", Status: "done"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"status":"done"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0]["status"] != "done" {
		t.Errorf("want 1 done task, got: %v", got)
	}
}

func TestSearchTasksTool_Execute_StatusAll(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "открытая", Status: "open"},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "выполненная", Status: "done"},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "отменённая", Status: "cancelled"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"status":"all"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("want 3 tasks for status=all, got %d", len(got))
	}
	if tasks.lastListFilter.Status != "" {
		t.Errorf("ListTasks filter.Status must be empty for «all», got %q", tasks.lastListFilter.Status)
	}
}

func TestSearchTasksTool_Execute_StatusCancelled(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "открытая", Status: "open"},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "отменённая", Status: "cancelled"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"status":"cancelled"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0]["status"] != "cancelled" {
		t.Errorf("want 1 cancelled task, got: %v", got)
	}
}

func TestSearchTasksTool_Execute_ByProjectName(t *testing.T) {
	projects := &mockProjectService{
		findByNameResult: &model.Project{ID: "proj-work", Slug: "work", Name: "Работа"},
	}
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectID: "proj-work", ProjectSlug: "work", Summary: "задача работы", Status: "open"},
			{ID: "t2", Number: 2, ProjectID: "proj-inbox", ProjectSlug: "inbox", Summary: "задача инбокса", Status: "open"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"project":"Работа"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if tasks.lastListProjectID != "proj-work" {
		t.Errorf("ListTasks projectID = %q, want proj-work", tasks.lastListProjectID)
	}
	if len(got) != 1 {
		t.Errorf("got %d tasks, want 1 (from project Работа)", len(got))
	}
}

func TestSearchTasksTool_Execute_ByProjectUUID(t *testing.T) {
	projects := &mockProjectService{}
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectID: "proj-uuid-99", ProjectSlug: "test", Summary: "задача", Status: "open"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"project":"proj-uuid-99"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if tasks.lastListProjectID != "proj-uuid-99" {
		t.Errorf("ListTasks projectID = %q, want proj-uuid-99", tasks.lastListProjectID)
	}
	if len(got) != 1 {
		t.Errorf("got %d tasks, want 1", len(got))
	}
}

func TestSearchTasksTool_Execute_ByProjectNotFound_EmptyResult(t *testing.T) {
	projects := &mockProjectService{}
	tasks := &mockTaskService{
		listTasksResult: []model.Task{},
	}
	tool := agent.NewSearchTasksTool(tasks, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"project":"несуществующий"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty result for unknown project, got %d tasks", len(got))
	}
}

func TestSearchTasksTool_Execute_DueBefore(t *testing.T) {
	d1 := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "ранняя", Status: "open", Deadline: &d1},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "средняя", Status: "open", Deadline: &d2},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "поздняя", Status: "open", Deadline: &d3},
			{ID: "t4", Number: 4, ProjectSlug: "inbox", Summary: "без срока", Status: "open"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	cutoff := "2026-05-01T00:00:00Z"
	result, err := tool.Execute(context.Background(), `{"status":"open","due_before":"`+cutoff+`"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("due_before: got %d tasks, want 2 (d1 and d2)", len(got))
	}
}

func TestSearchTasksTool_Execute_DueAfter(t *testing.T) {
	d1 := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "ранняя", Status: "open", Deadline: &d1},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "средняя", Status: "open", Deadline: &d2},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "поздняя", Status: "open", Deadline: &d3},
			{ID: "t4", Number: 4, ProjectSlug: "inbox", Summary: "без срока", Status: "open"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	cutoff := "2026-04-25T00:00:00Z"
	result, err := tool.Execute(context.Background(), `{"status":"open","due_after":"`+cutoff+`"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("due_after: got %d tasks, want 2 (d2 and d3)", len(got))
	}
}

func TestSearchTasksTool_Execute_DueBeforeAndDueAfter(t *testing.T) {
	d1 := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "ранняя", Status: "open", Deadline: &d1},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "средняя", Status: "open", Deadline: &d2},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "поздняя", Status: "open", Deadline: &d3},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"status":"open","due_after":"2026-04-25T00:00:00Z","due_before":"2026-05-05T00:00:00Z"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0]["id"] != "t2" {
		t.Errorf("due_before+due_after: want only t2, got: %v", got)
	}
}

func TestSearchTasksTool_Execute_LimitTruncates(t *testing.T) {
	var taskList []model.Task
	for i := 1; i <= 10; i++ {
		taskList = append(taskList, model.Task{
			ID: fmt.Sprintf("t%d", i), Number: i,
			ProjectSlug: "inbox", Summary: fmt.Sprintf("задача %d", i), Status: "open",
		})
	}
	tasks := &mockTaskService{listTasksResult: taskList}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"limit":3}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d tasks, want 3 (limit=3)", len(got))
	}
}

func TestSearchTasksTool_Execute_LimitOver50_Clamps(t *testing.T) {
	tasks := &mockTaskService{}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"limit":51}`)
	if err != nil {
		t.Fatalf("Execute() must not return an error when limit > 50, got: %v", err)
	}
	if tasks.lastListFilter.Limit != 100 {
		t.Errorf("fetchLimit = %d, want 100 (clamped limit 50 * 2)", tasks.lastListFilter.Limit)
	}
}

func TestSearchTasksTool_Execute_InvalidDueBefore_Error(t *testing.T) {
	tool := agent.NewSearchTasksTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"due_before":"не-дата-вообще"}`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid due_before")
	}
}

func TestSearchTasksTool_Execute_ResponseFields(t *testing.T) {
	deadline := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{
				ID: "t1", Number: 7, ProjectID: "p1", ProjectSlug: "work",
				Summary: "задача с дедлайном", Status: "open", Deadline: &deadline,
			},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d", len(got))
	}
	item := got[0]
	for _, key := range []string{"id", "display_id", "project_slug", "summary", "status"} {
		if item[key] == nil || item[key] == "" {
			t.Errorf("field %q must be present and non-empty, got: %v", key, item[key])
		}
	}
	if item["display_id"] != "work#7" {
		t.Errorf("display_id = %v, want work#7", item["display_id"])
	}
	if item["deadline"] == nil {
		t.Error("deadline must be present when task has a deadline")
	}
	if _, ok := item["details"]; ok {
		t.Error("details must not be present in search results")
	}
}

func TestSearchTasksTool_Execute_DeadlineAbsentWhenNil(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "без срока", Status: "open"},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d", len(got))
	}
	if _, ok := got[0]["deadline"]; ok {
		t.Error("deadline must be absent when nil")
	}
}

func TestSearchTasksTool_Execute_EmptyResult(t *testing.T) {
	tasks := &mockTaskService{listTasksResult: []model.Task{}}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"query":"несуществующая задача"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	var got []any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty array, got %d items", len(got))
	}
}

func TestSearchTasksTool_Execute_StoreError(t *testing.T) {
	tasks := &mockTaskService{listTasksErr: errors.New("database error")}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("Execute() must return an error when store fails")
	}
}

func TestSearchTasksTool_Execute_InvalidJSON(t *testing.T) {
	tool := agent.NewSearchTasksTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))
	_, err := tool.Execute(context.Background(), `{not valid json`)
	if err == nil {
		t.Fatal("Execute() must return an error for invalid JSON")
	}
}

func TestSearchTasksTool_Execute_FindProjectByNameError(t *testing.T) {
	projects := &mockProjectService{findByNameErr: errors.New("database error")}
	tool := agent.NewSearchTasksTool(&mockTaskService{}, projects, newTestDateparser("ru"), newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"project":"work"}`)
	if err == nil {
		t.Fatal("Execute() must return an error when FindProjectByName fails")
	}
}

func TestSearchTasksTool_Metadata(t *testing.T) {
	tool := agent.NewSearchTasksTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	if tool.Name() != "search_tasks" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.DMOnly() {
		t.Error("search_tasks must not be DMOnly")
	}
}
