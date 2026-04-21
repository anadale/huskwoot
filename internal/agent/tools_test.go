package agent_test

import (
	"context"
	"encoding/json"
	"errors"
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

	lastCreateReq     model.CreateTaskRequest
	lastCompleteID    string
	lastMoveID        string
	lastMoveProjID    string
	lastListProjectID string
	lastListFilter    model.TaskFilter
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

func (m *mockTaskService) UpdateTask(_ context.Context, _ string, _ model.TaskUpdate) (*model.Task, error) {
	return nil, nil
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
