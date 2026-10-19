package storage_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
)

// --- helpers ---

func newTaskStore(t *testing.T) (*storage.SQLiteTaskStore, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	store, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	return store, db
}

// withTx opens a transaction, calls fn, and commits on success.
// On error or panic it rolls back and returns the corresponding error.
func withTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// createProjectHelper wraps store.CreateProjectTx in a short transaction for test use.
func createProjectHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, p *model.Project) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateProjectTx(context.Background(), tx, p)
	})
}

// createTaskHelper wraps store.CreateTaskTx in a short transaction for test use.
func createTaskHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, task *model.Task) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.CreateTaskTx(context.Background(), tx, task)
	})
}

// updateTaskHelper wraps store.UpdateTaskTx in a short transaction for test use.
func updateTaskHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, id string, upd model.TaskUpdate) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.UpdateTaskTx(context.Background(), tx, id, upd)
	})
}

// updateProjectHelper wraps store.UpdateProjectTx in a short transaction for test use.
func updateProjectHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, id string, upd model.ProjectUpdate) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.UpdateProjectTx(context.Background(), tx, id, upd)
	})
}

// moveTaskHelper wraps store.MoveTaskTx in a short transaction for test use.
func moveTaskHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, taskID, newProjectID string) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.MoveTaskTx(context.Background(), tx, taskID, newProjectID)
	})
}

func newProject(name string) *model.Project {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	return &model.Project{Name: name, Description: "описание " + name, Slug: slug}
}

func newTask(projectID string, summary string) *model.Task {
	return &model.Task{ProjectID: projectID, Summary: summary, Details: "детали"}
}

// --- Projects ---

func TestSQLiteTaskStore_DefaultProject_CreatedAutomatically(t *testing.T) {
	store, _ := newTaskStore(t)

	id := store.DefaultProjectID()
	if id == "" {
		t.Fatal("DefaultProjectID returned empty string")
	}

	p, err := store.GetProject(context.Background(), id)
	if err != nil {
		t.Fatalf("GetProject(inbox): %v", err)
	}
	if p == nil {
		t.Fatal("GetProject returned nil for default project")
	}
	if p.Name != "Inbox" {
		t.Errorf("want name %q, got %q", "Inbox", p.Name)
	}
	if p.Slug != "inbox" {
		t.Errorf("want slug %q, got %q", "inbox", p.Slug)
	}
}

func TestSQLiteTaskStore_CreateProject_Success(t *testing.T) {
	store, db := newTaskStore(t)

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID == "" {
		t.Error("ID not populated after CreateProject")
	}
	if len(p.ID) != 36 {
		t.Errorf("ID does not look like UUID: %q", p.ID)
	}
	if p.CreatedAt.IsZero() {
		t.Error("CreatedAt not populated after CreateProject")
	}
	if p.Slug == "" {
		t.Error("Slug not populated after CreateProject")
	}
}

func TestSQLiteTaskStore_CreateProject_RequiresSlug(t *testing.T) {
	store, db := newTaskStore(t)

	p := &model.Project{Name: "Без слага", Description: ""}
	err := createProjectHelper(t, db, store, p)
	if err == nil {
		t.Fatal("expected error when slug is absent")
	}
	if !strings.Contains(err.Error(), "slug is required") {
		t.Errorf("expected message about slug, got: %v", err)
	}
}

func TestSQLiteTaskStore_CreateProject_DuplicateName(t *testing.T) {
	store, db := newTaskStore(t)

	p1 := newProject("Дубликат")
	if err := createProjectHelper(t, db, store, p1); err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}

	p2 := newProject("Дубликат")
	err := createProjectHelper(t, db, store, p2)
	if err == nil {
		t.Fatal("expected error when duplicating project name")
	}
}

func TestSQLiteTaskStore_GetProject_NotFound(t *testing.T) {
	store, _ := newTaskStore(t)

	p, err := store.GetProject(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p != nil {
		t.Errorf("want nil for missing project, got %+v", p)
	}
}

func TestSQLiteTaskStore_ListProjects(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	// Inbox is created automatically — must be 1 project.
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects (empty DB): %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("want 1 project (Inbox), got %d", len(projects))
	}

	names := []string{"Альфа", "Бета"}
	for _, name := range names {
		p := newProject(name)
		if err := createProjectHelper(t, db, store, p); err != nil {
			t.Fatalf("CreateProject(%q): %v", name, err)
		}
	}

	projects, err = store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("want 3 projects, got %d", len(projects))
	}
}

func TestSQLiteTaskStore_FindProjectByName_Found(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Поиск")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	found, err := store.FindProjectByName(ctx, "Поиск")
	if err != nil {
		t.Fatalf("FindProjectByName: %v", err)
	}
	if found == nil {
		t.Fatal("FindProjectByName returned nil, want a project")
	}
	if found.Name != "Поиск" {
		t.Errorf("Name: want %q, got %q", "Поиск", found.Name)
	}
}

func TestSQLiteTaskStore_FindProjectByName_NotFound(t *testing.T) {
	store, _ := newTaskStore(t)

	found, err := store.FindProjectByName(context.Background(), "Несуществующий")
	if err != nil {
		t.Fatalf("FindProjectByName: %v", err)
	}
	if found != nil {
		t.Errorf("want nil, got %+v", found)
	}
}

func TestSQLiteTaskStore_UpdateProject_RenamesSlug(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Старое Имя")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a task so we can verify GetTaskByRef with the new slug.
	task := newTask(p.ID, "Тестовая задача")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	newSlug := "novoe-imya"
	if err := updateProjectHelper(t, db, store, p.ID, model.ProjectUpdate{Slug: &newSlug}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	// GetTaskByRef with the new slug must find the task.
	found, err := store.GetTaskByRef(ctx, newSlug, task.Number)
	if err != nil {
		t.Fatalf("GetTaskByRef after UpdateProject: %v", err)
	}
	if found == nil {
		t.Fatal("task not found by new slug")
	}
	if found.ProjectSlug != newSlug {
		t.Errorf("ProjectSlug = %q, want %q", found.ProjectSlug, newSlug)
	}
}

func TestSQLiteTaskStore_UpdateProject_NotFound(t *testing.T) {
	store, db := newTaskStore(t)
	name := "новое"
	err := updateProjectHelper(t, db, store, "00000000-0000-0000-0000-000000000000", model.ProjectUpdate{Name: &name})
	if err == nil {
		t.Fatal("expected error for missing project")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// --- Tasks ---

func TestSQLiteTaskStore_CreateTask_Success(t *testing.T) {
	store, db := newTaskStore(t)

	task := newTask(store.DefaultProjectID(), "Купить молоко")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ID == "" {
		t.Error("ID not populated after CreateTask")
	}
	if len(task.ID) != 36 {
		t.Errorf("ID does not look like UUID: %q", task.ID)
	}
	if task.Number != 1 {
		t.Errorf("Number: want 1, got %d", task.Number)
	}
	if task.Status != "open" {
		t.Errorf("Status: want %q, got %q", "open", task.Status)
	}
	if task.CreatedAt.IsZero() {
		t.Error("CreatedAt not populated after CreateTask")
	}
	if task.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not populated after CreateTask")
	}
}

func TestSQLiteTaskStore_CreateTask_AssignsMonotonicNumber(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	for i := 1; i <= 3; i++ {
		task := newTask(inboxID, "Задача")
		if err := createTaskHelper(t, db, store, task); err != nil {
			t.Fatalf("CreateTask #%d: %v", i, err)
		}
		if task.Number != i {
			t.Errorf("task #%d: Number = %d, want %d", i, task.Number, i)
		}
	}

	// Verify the project task_counter.
	p, err := store.GetProject(ctx, inboxID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.TaskCounter != 3 {
		t.Errorf("TaskCounter = %d, want 3", p.TaskCounter)
	}
}

func TestSQLiteTaskStore_CreateTask_Concurrent_UniqueNumbers(t *testing.T) {
	store, db := newTaskStore(t)
	inboxID := store.DefaultProjectID()

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	numbers := make(chan int, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := newTask(inboxID, "Параллельная задача")
			if err := createTaskHelper(t, db, store, task); err != nil {
				errs <- err
				return
			}
			numbers <- task.Number
		}()
	}
	wg.Wait()
	close(errs)
	close(numbers)

	for err := range errs {
		t.Fatalf("concurrent CreateTask: %v", err)
	}

	seen := map[int]bool{}
	for num := range numbers {
		if seen[num] {
			t.Errorf("duplicate number: %d", num)
		}
		seen[num] = true
	}
	if len(seen) != n {
		t.Errorf("want %d unique numbers, got %d", n, len(seen))
	}
}

func TestSQLiteTaskStore_CreateTask_InvalidProjectID(t *testing.T) {
	store, db := newTaskStore(t)

	task := newTask("00000000-0000-0000-0000-000000000000", "Задача")
	err := createTaskHelper(t, db, store, task)
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestSQLiteTaskStore_GetTask_NotFound(t *testing.T) {
	store, _ := newTaskStore(t)

	task, err := store.GetTask(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task != nil {
		t.Errorf("want nil for missing task, got %+v", task)
	}
}

func TestSQLiteTaskStore_GetTask_Success(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Позвонить другу")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask returned nil for existing task")
	}
	if got.Summary != task.Summary {
		t.Errorf("Summary: want %q, got %q", task.Summary, got.Summary)
	}
	if got.Status != "open" {
		t.Errorf("Status: want %q, got %q", "open", got.Status)
	}
	if got.Number != 1 {
		t.Errorf("Number: want 1, got %d", got.Number)
	}
	if got.ProjectSlug != "inbox" {
		t.Errorf("ProjectSlug: want %q, got %q", "inbox", got.ProjectSlug)
	}
}

func TestSQLiteTaskStore_GetTaskByRef(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	t1 := newTask(inboxID, "Первая")
	t2 := newTask(inboxID, "Вторая")
	for _, task := range []*model.Task{t1, t2} {
		if err := createTaskHelper(t, db, store, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	found, err := store.GetTaskByRef(ctx, "inbox", 2)
	if err != nil {
		t.Fatalf("GetTaskByRef: %v", err)
	}
	if found == nil {
		t.Fatal("GetTaskByRef returned nil, want a task")
	}
	if found.Summary != "Вторая" {
		t.Errorf("Summary: want %q, got %q", "Вторая", found.Summary)
	}
	if found.Number != 2 {
		t.Errorf("Number: want 2, got %d", found.Number)
	}
	if found.ProjectSlug != "inbox" {
		t.Errorf("ProjectSlug: want %q, got %q", "inbox", found.ProjectSlug)
	}

	// Non-existent ref.
	notFound, err := store.GetTaskByRef(ctx, "inbox", 999)
	if err != nil {
		t.Fatalf("GetTaskByRef(notFound): %v", err)
	}
	if notFound != nil {
		t.Errorf("want nil for missing ref, got %+v", notFound)
	}
}

func TestSQLiteTaskStore_MoveTask_ReassignsNumber(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	// Create the target project.
	work := newProject("Работа")
	if err := createProjectHelper(t, db, store, work); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a task in Inbox.
	task := newTask(inboxID, "Перенести меня")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	origNumber := task.Number // must be 1
	origInboxID := task.ProjectID

	// Create another task in Inbox so the counter does not roll back.
	other := newTask(inboxID, "Осталась")
	if err := createTaskHelper(t, db, store, other); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Move the first task to Work.
	if err := moveTaskHelper(t, db, store, task.ID, work.ID); err != nil {
		t.Fatalf("MoveTask: %v", err)
	}

	// Verify that the task received a new number in the Work project.
	moved, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask after MoveTask: %v", err)
	}
	if moved.ProjectID != work.ID {
		t.Errorf("ProjectID: want %q, got %q", work.ID, moved.ProjectID)
	}
	if moved.Number != 1 {
		t.Errorf("Number in new project: want 1, got %d", moved.Number)
	}
	if moved.Number == origNumber && moved.ProjectID == origInboxID {
		t.Error("task was not moved")
	}

	// Verify that the Inbox task_counter did not roll back.
	inbox, err := store.GetProject(ctx, inboxID)
	if err != nil {
		t.Fatalf("GetProject(inbox): %v", err)
	}
	if inbox.TaskCounter != 2 {
		t.Errorf("inbox.TaskCounter = %d, want 2 (not rolled back)", inbox.TaskCounter)
	}

	// Verify the Work project task_counter.
	workProject, err := store.GetProject(ctx, work.ID)
	if err != nil {
		t.Fatalf("GetProject(work): %v", err)
	}
	if workProject.TaskCounter != 1 {
		t.Errorf("work.TaskCounter = %d, want 1", workProject.TaskCounter)
	}
}

func TestSQLiteTaskStore_ListTasks_ByProject(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	inboxID := store.DefaultProjectID()

	p := newProject("Другой")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for _, summary := range []string{"Задача 1", "Задача 2"} {
		if err := createTaskHelper(t, db, store, newTask(inboxID, summary)); err != nil {
			t.Fatalf("CreateTask(inbox): %v", err)
		}
	}
	if err := createTaskHelper(t, db, store, newTask(p.ID, "Задача 3")); err != nil {
		t.Fatalf("CreateTask(other): %v", err)
	}

	inbox, err := store.ListTasks(ctx, inboxID, model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks(inbox): %v", err)
	}
	if len(inbox) != 2 {
		t.Errorf("want 2 tasks in Inbox, got %d", len(inbox))
	}

	other, err := store.ListTasks(ctx, p.ID, model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks(other): %v", err)
	}
	if len(other) != 1 {
		t.Errorf("want 1 task in %q, got %d", p.Name, len(other))
	}
}

func TestSQLiteTaskStore_ListTasks_FilterByStatus(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	inboxID := store.DefaultProjectID()

	for _, summary := range []string{"Открытая 1", "Открытая 2"} {
		if err := createTaskHelper(t, db, store, newTask(inboxID, summary)); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	done := newTask(inboxID, "Завершённая")
	if err := createTaskHelper(t, db, store, done); err != nil {
		t.Fatalf("CreateTask(done): %v", err)
	}
	statusDone := "done"
	if err := updateTaskHelper(t, db, store, done.ID, model.TaskUpdate{Status: &statusDone}); err != nil {
		t.Fatalf("UpdateTask(done): %v", err)
	}

	openTasks, err := store.ListTasks(ctx, inboxID, model.TaskFilter{Status: "open"})
	if err != nil {
		t.Fatalf("ListTasks(open): %v", err)
	}
	if len(openTasks) != 2 {
		t.Errorf("want 2 open tasks, got %d", len(openTasks))
	}

	doneTasks, err := store.ListTasks(ctx, inboxID, model.TaskFilter{Status: "done"})
	if err != nil {
		t.Fatalf("ListTasks(done): %v", err)
	}
	if len(doneTasks) != 1 {
		t.Errorf("want 1 done task, got %d", len(doneTasks))
	}

	allTasks, err := store.ListTasks(ctx, inboxID, model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks(all): %v", err)
	}
	if len(allTasks) != 3 {
		t.Errorf("want 3 tasks total, got %d", len(allTasks))
	}
}

func TestSQLiteTaskStore_UpdateTask_Status(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Обновляемая задача")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	status := "done"
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{Status: &status}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask after UpdateTask: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("Status: want %q, got %q", "done", got.Status)
	}
}

func TestSQLiteTaskStore_UpdateTask_Details(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Задача с деталями")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	newDetails := "Обновлённые детали"
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{Details: &newDetails}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Details != newDetails {
		t.Errorf("Details: want %q, got %q", newDetails, got.Details)
	}
}

func TestSQLiteTaskStore_UpdateTask_Deadline(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Дедлайн")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	deadline := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	deadlinePtr := &deadline
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{Deadline: &deadlinePtr}); err != nil {
		t.Fatalf("UpdateTask(set deadline): %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Deadline == nil {
		t.Fatal("Deadline not set")
	}
	if !got.Deadline.Equal(deadline) {
		t.Errorf("Deadline: want %v, got %v", deadline, *got.Deadline)
	}

	// Clear the deadline.
	var nilTime *time.Time
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{Deadline: &nilTime}); err != nil {
		t.Fatalf("UpdateTask(clear deadline): %v", err)
	}

	got2, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask after clearing deadline: %v", err)
	}
	if got2.Deadline != nil {
		t.Errorf("Deadline must be nil after clearing, got %v", got2.Deadline)
	}
}

func TestSQLiteTaskStore_UpdateTask_NotFound(t *testing.T) {
	store, db := newTaskStore(t)
	status := "done"
	err := updateTaskHelper(t, db, store, "00000000-0000-0000-0000-000000000000", model.TaskUpdate{Status: &status})
	if err == nil {
		t.Fatal("expected error for missing task")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestSQLiteTaskStore_CreateTask_WithDeadline(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	deadline := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	task := newTask(store.DefaultProjectID(), "Задача с дедлайном")
	task.Deadline = &deadline

	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Deadline == nil {
		t.Fatal("Deadline is nil after CreateTask")
	}
	if !got.Deadline.Equal(deadline) {
		t.Errorf("Deadline: want %v, got %v", deadline, *got.Deadline)
	}
}

func TestSQLiteTaskStore_DefaultProject_IdempotentCreation(t *testing.T) {
	db := openTestDB(t)

	store1, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("first NewSQLiteTaskStore: %v", err)
	}
	store2, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("second NewSQLiteTaskStore: %v", err)
	}

	if store1.DefaultProjectID() != store2.DefaultProjectID() {
		t.Errorf("DefaultProjectID: %q != %q", store1.DefaultProjectID(), store2.DefaultProjectID())
	}

	projects, err := store1.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	inboxCount := 0
	for _, p := range projects {
		if p.Name == "Inbox" {
			inboxCount++
		}
	}
	if inboxCount != 1 {
		t.Errorf("want exactly 1 Inbox project, found %d", inboxCount)
	}
}

func TestSQLiteTaskStore_ListTasks_FilterByQuery(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	for _, summary := range []string{
		"Переделать идентификаторы на числовые",
		"Добавить поиск задач по названию",
		"Исправить баг с идентификатором пользователя",
	} {
		if err := createTaskHelper(t, db, store, newTask(inboxID, summary)); err != nil {
			t.Fatalf("CreateTask(%q): %v", summary, err)
		}
	}

	tasks, err := store.ListTasks(ctx, "", model.TaskFilter{Query: "идентификатор"})
	if err != nil {
		t.Fatalf("ListTasks(query): %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("want 2 tasks for query «идентификатор», got %d", len(tasks))
	}

	tasks, err = store.ListTasks(ctx, "", model.TaskFilter{Query: "ИДЕНТИФИКАТОР"})
	if err != nil {
		t.Fatalf("ListTasks(query uppercase): %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("want 2 tasks for query «ИДЕНТИФИКАТОР», got %d", len(tasks))
	}

	tasks, err = store.ListTasks(ctx, inboxID, model.TaskFilter{Query: "поиск"})
	if err != nil {
		t.Fatalf("ListTasks(query+project): %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("want 1 task for query «поиск» in Inbox, got %d", len(tasks))
	}

	tasks, err = store.ListTasks(ctx, "", model.TaskFilter{Query: "несуществующий"})
	if err != nil {
		t.Fatalf("ListTasks(no match): %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("want 0 tasks, got %d", len(tasks))
	}

	tasks, err = store.ListTasks(ctx, "", model.TaskFilter{Query: "идентификаторы у huskwoot"})
	if err != nil {
		t.Fatalf("ListTasks(word-based query): %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("want 1 task for word-based query, got %d", len(tasks))
	}
}

func TestSQLiteTaskStore_UpdateTask_SetsClosedAtWhenDone(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Завершаемая задача")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	before := time.Now().Add(-time.Second)
	status := "done"
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{Status: &status}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	after := time.Now().Add(time.Second)

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ClosedAt == nil {
		t.Fatal("ClosedAt must be set when transitioning to done")
	}
	if got.ClosedAt.Before(before) || got.ClosedAt.After(after) {
		t.Errorf("ClosedAt = %v, want between %v and %v", got.ClosedAt, before, after)
	}
}

func TestSQLiteTaskStore_UpdateTask_SetsClosedAtWhenCancelled(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Отменяемая задача")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	status := "cancelled"
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{Status: &status}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ClosedAt == nil {
		t.Fatal("ClosedAt must be set when transitioning to cancelled")
	}
}

func TestSQLiteTaskStore_GetTask_ClosedAtNilForOpenTask(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Открытая задача")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ClosedAt != nil {
		t.Errorf("ClosedAt must be nil for open task, got %v", got.ClosedAt)
	}
}

func TestSQLiteTaskStore_ListTasks_WithLimit(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	inboxID := store.DefaultProjectID()
	for _, summary := range []string{"Задача 1", "Задача 2", "Задача 3", "Задача 4", "Задача 5"} {
		if err := createTaskHelper(t, db, store, newTask(inboxID, summary)); err != nil {
			t.Fatalf("CreateTask(%q): %v", summary, err)
		}
	}

	tasks, err := store.ListTasks(ctx, "", model.TaskFilter{Limit: 3})
	if err != nil {
		t.Fatalf("ListTasks(limit=3): %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("want 3 tasks with limit=3, got %d", len(tasks))
	}
}

func TestSQLiteTaskStore_ListTasks_ClosedTasksOrderedByClosedAtDesc(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	inboxID := store.DefaultProjectID()
	t1 := newTask(inboxID, "Первая закрытая")
	t2 := newTask(inboxID, "Вторая закрытая")
	t3 := newTask(inboxID, "Третья закрытая")
	for _, task := range []*model.Task{t1, t2, t3} {
		if err := createTaskHelper(t, db, store, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	statusDone := "done"
	for _, id := range []string{t3.ID, t1.ID, t2.ID} {
		time.Sleep(2 * time.Millisecond)
		if err := updateTaskHelper(t, db, store, id, model.TaskUpdate{Status: &statusDone}); err != nil {
			t.Fatalf("UpdateTask(%s): %v", id, err)
		}
	}

	tasks, err := store.ListTasks(ctx, "", model.TaskFilter{Status: "done", Limit: 2})
	if err != nil {
		t.Fatalf("ListTasks(done, limit=2): %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(tasks))
	}
	// The most recently closed one is t2.
	if tasks[0].ID != t2.ID {
		t.Errorf("first task must be t2 (most recently closed), got id=%s", tasks[0].ID)
	}
	if tasks[1].ID != t1.ID {
		t.Errorf("second task must be t1, got id=%s", tasks[1].ID)
	}
}

func TestSQLiteTaskStore_ListTasks_PopulatesProjectSlug(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Huskwoot")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := createTaskHelper(t, db, store, newTask(p.ID, "Добавить поиск")); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := createTaskHelper(t, db, store, newTask(store.DefaultProjectID(), "Задача в Inbox")); err != nil {
		t.Fatalf("CreateTask(inbox): %v", err)
	}

	tasks, err := store.ListTasks(ctx, "", model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		if task.ProjectSlug == "" {
			t.Errorf("task %s: ProjectSlug field not populated", task.ID)
		}
	}
	for _, task := range tasks {
		if task.Summary == "Добавить поиск" && task.ProjectSlug != "huskwoot" {
			t.Errorf("task «Добавить поиск»: ProjectSlug = %q, want %q", task.ProjectSlug, "huskwoot")
		}
		if task.Summary == "Задача в Inbox" && task.ProjectSlug != "inbox" {
			t.Errorf("task «Задача в Inbox»: ProjectSlug = %q, want %q", task.ProjectSlug, "inbox")
		}
	}
}

func TestSQLiteTaskStore_UpdateTask_MultipleFields(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "многополевая задача")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	deadline := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	deadlinePtr := &deadline
	details := "подробности задачи"
	status := "done"
	if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{
		Status:   &status,
		Details:  &details,
		Deadline: &deadlinePtr,
	}); err != nil {
		t.Fatalf("UpdateTask(all fields): %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("Status: want %q, got %q", "done", got.Status)
	}
	if got.Details != details {
		t.Errorf("Details: want %q, got %q", details, got.Details)
	}
	if got.Deadline == nil {
		t.Fatal("Deadline not set")
	}
	if !got.Deadline.Equal(deadline) {
		t.Errorf("Deadline: want %v, got %v", deadline, *got.Deadline)
	}
}

// --- Tx semantics of write methods ---

// TestCreateTaskTxAtomicRollback verifies that on transaction rollback
// the task does not remain in the DB and the project task_counter is not incremented.
func TestCreateTaskTxAtomicRollback(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	before, err := store.GetProject(ctx, inboxID)
	if err != nil {
		t.Fatalf("GetProject(before): %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	task := newTask(inboxID, "Откатная задача")
	if err := store.CreateTaskTx(ctx, tx, task); err != nil {
		_ = tx.Rollback()
		t.Fatalf("CreateTaskTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got != nil {
		t.Errorf("after Rollback task still in DB: %+v", got)
	}

	after, err := store.GetProject(ctx, inboxID)
	if err != nil {
		t.Fatalf("GetProject(after): %v", err)
	}
	if after.TaskCounter != before.TaskCounter {
		t.Errorf("TaskCounter changed after Rollback: before %d, after %d", before.TaskCounter, after.TaskCounter)
	}
}

// TestCreateProjectTxRollback verifies that on transaction rollback
// the project does not appear in the DB.
func TestCreateProjectTxRollback(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	p := newProject("Откатный проект")
	if err := store.CreateProjectTx(ctx, tx, p); err != nil {
		_ = tx.Rollback()
		t.Fatalf("CreateProjectTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := store.FindProjectByName(ctx, "Откатный проект")
	if err != nil {
		t.Fatalf("FindProjectByName: %v", err)
	}
	if got != nil {
		t.Errorf("after Rollback project still in DB: %+v", got)
	}
}

// TestMoveTaskTxReassignsNumber verifies that within a single transaction
// MoveTaskTx correctly assigns number = task_counter+1 in the target project
// and does not roll back the source project's counter.
func TestMoveTaskTxReassignsNumber(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	work := newProject("Рабочий")
	if err := createProjectHelper(t, db, store, work); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a task in Work to set the initial counter.
	existing := newTask(work.ID, "уже была")
	if err := createTaskHelper(t, db, store, existing); err != nil {
		t.Fatalf("CreateTask(work): %v", err)
	}

	// Task in Inbox that will be moved.
	moved := newTask(inboxID, "переносимая")
	if err := createTaskHelper(t, db, store, moved); err != nil {
		t.Fatalf("CreateTask(inbox): %v", err)
	}

	if err := moveTaskHelper(t, db, store, moved.ID, work.ID); err != nil {
		t.Fatalf("MoveTaskTx: %v", err)
	}

	got, err := store.GetTask(ctx, moved.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ProjectID != work.ID {
		t.Errorf("ProjectID: want %q, got %q", work.ID, got.ProjectID)
	}
	if got.Number != 2 {
		t.Errorf("Number in Work: want 2 (counter+1), got %d", got.Number)
	}

	// Verify that the Inbox task_counter did not roll back — Inbox is not touched,
	// but within this test a task was created and its number must remain.
	inbox, err := store.GetProject(ctx, inboxID)
	if err != nil {
		t.Fatalf("GetProject(inbox): %v", err)
	}
	if inbox.TaskCounter != 1 {
		t.Errorf("inbox.TaskCounter = %d, want 1 (not rolled back)", inbox.TaskCounter)
	}
}

// TestMoveTaskTxRollback verifies that on transaction rollback the changes are not persisted:
// the task stays in the source project and the target project task_counter is not incremented.
func TestMoveTaskTxRollback(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()
	inboxID := store.DefaultProjectID()

	work := newProject("Target")
	if err := createProjectHelper(t, db, store, work); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	task := newTask(inboxID, "rollback move")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	workBefore, err := store.GetProject(ctx, work.ID)
	if err != nil {
		t.Fatalf("GetProject(work before): %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := store.MoveTaskTx(ctx, tx, task.ID, work.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("MoveTaskTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ProjectID != inboxID {
		t.Errorf("after Rollback ProjectID = %q, want %q", got.ProjectID, inboxID)
	}

	workAfter, err := store.GetProject(ctx, work.ID)
	if err != nil {
		t.Fatalf("GetProject(work after): %v", err)
	}
	if workAfter.TaskCounter != workBefore.TaskCounter {
		t.Errorf("work.TaskCounter changed after Rollback: before %d, after %d", workBefore.TaskCounter, workAfter.TaskCounter)
	}
}

// addAliasHelper wraps store.AddProjectAliasTx in a short transaction for test use.
func addAliasHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, projectID, alias string) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.AddProjectAliasTx(context.Background(), tx, projectID, alias)
	})
}

// removeAliasHelper wraps store.RemoveProjectAliasTx in a short transaction for test use.
func removeAliasHelper(t *testing.T, db *sql.DB, store *storage.SQLiteTaskStore, projectID, alias string) error {
	t.Helper()
	return withTx(t, db, func(tx *sql.Tx) error {
		return store.RemoveProjectAliasTx(context.Background(), tx, projectID, alias)
	})
}

// --- Alias tests ---

func TestTaskStoreAddProjectAliasSuccess(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := addAliasHelper(t, db, store, p.ID, "work"); err != nil {
		t.Fatalf("AddProjectAliasTx: %v", err)
	}

	aliases, err := store.ListAliasesForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAliasesForProject: %v", err)
	}
	if len(aliases) != 1 || aliases[0] != "work" {
		t.Errorf("want [work], got %v", aliases)
	}
}

func TestTaskStoreAddProjectAliasDuplicatePrimaryKey(t *testing.T) {
	store, db := newTaskStore(t)

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := addAliasHelper(t, db, store, p.ID, "work"); err != nil {
		t.Fatalf("first AddProjectAliasTx: %v", err)
	}

	err := addAliasHelper(t, db, store, p.ID, "work")
	if err == nil {
		t.Fatal("expected error on duplicate alias, got nil")
	}
}

func TestTaskStoreAddProjectAliasForeignKeyMissing(t *testing.T) {
	store, db := newTaskStore(t)

	err := addAliasHelper(t, db, store, "00000000-0000-0000-0000-000000000000", "ghost")
	if err == nil {
		t.Fatal("expected FK error for non-existent project, got nil")
	}
}

func TestTaskStoreRemoveProjectAliasSuccess(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := addAliasHelper(t, db, store, p.ID, "work"); err != nil {
		t.Fatalf("AddProjectAliasTx: %v", err)
	}

	if err := removeAliasHelper(t, db, store, p.ID, "work"); err != nil {
		t.Fatalf("RemoveProjectAliasTx: %v", err)
	}

	aliases, err := store.ListAliasesForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAliasesForProject: %v", err)
	}
	if len(aliases) != 0 {
		t.Errorf("want empty aliases after removal, got %v", aliases)
	}
}

func TestTaskStoreRemoveProjectAliasNotFound(t *testing.T) {
	store, db := newTaskStore(t)

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Callers pre-validate alias membership; the store returns nil when the row is absent.
	if err := removeAliasHelper(t, db, store, p.ID, "nonexistent"); err != nil {
		t.Fatalf("expected nil when removing non-existent alias, got: %v", err)
	}
}

func TestTaskStoreListAliasesForProjectEmpty(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	aliases, err := store.ListAliasesForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAliasesForProject: %v", err)
	}
	if len(aliases) != 0 {
		t.Errorf("want empty slice, got %v", aliases)
	}
}

func TestTaskStoreListAliasesForProjectMultiple(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, a := range []string{"beta", "alpha", "gamma"} {
		if err := addAliasHelper(t, db, store, p.ID, a); err != nil {
			t.Fatalf("AddProjectAliasTx(%q): %v", a, err)
		}
	}

	aliases, err := store.ListAliasesForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAliasesForProject: %v", err)
	}
	if len(aliases) != 3 {
		t.Fatalf("want 3 aliases, got %d: %v", len(aliases), aliases)
	}
}

func TestTaskStoreListAliasesForProjectSorted(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, a := range []string{"zebra", "apple", "mango"} {
		if err := addAliasHelper(t, db, store, p.ID, a); err != nil {
			t.Fatalf("AddProjectAliasTx(%q): %v", a, err)
		}
	}

	aliases, err := store.ListAliasesForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAliasesForProject: %v", err)
	}
	want := []string{"apple", "mango", "zebra"}
	for i, w := range want {
		if aliases[i] != w {
			t.Errorf("aliases[%d] = %q, want %q", i, aliases[i], w)
		}
	}
}

func TestSQLiteTaskStore_GetProject_IncludesAliases(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Букинист")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, a := range []string{"books", "букинист"} {
		if err := addAliasHelper(t, db, store, p.ID, a); err != nil {
			t.Fatalf("AddProjectAliasTx(%q): %v", a, err)
		}
	}

	got, err := store.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got == nil {
		t.Fatal("GetProject returned nil")
	}
	if len(got.Aliases) != 2 {
		t.Fatalf("want 2 aliases, got %d: %v", len(got.Aliases), got.Aliases)
	}
	if got.Aliases[0] != "books" || got.Aliases[1] != "букинист" {
		t.Errorf("want [books, букинист], got %v", got.Aliases)
	}
}

func TestSQLiteTaskStore_GetProject_NoAliasesReturnsEmptySlice(t *testing.T) {
	store, _ := newTaskStore(t)
	ctx := context.Background()

	got, err := store.GetProject(ctx, store.DefaultProjectID())
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got == nil {
		t.Fatal("GetProject returned nil")
	}
	if got.Aliases == nil {
		t.Error("Aliases must be empty slice, not nil")
	}
	if len(got.Aliases) != 0 {
		t.Errorf("want empty aliases, got %v", got.Aliases)
	}
}

func TestSQLiteTaskStore_GetProjectTx_IncludesAliases(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Тест")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add alias inside a transaction and verify GetProjectTx sees it within same tx.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	if err := store.AddProjectAliasTx(ctx, tx, p.ID, "testalias"); err != nil {
		t.Fatalf("AddProjectAliasTx: %v", err)
	}

	got, err := store.GetProjectTx(ctx, tx, p.ID)
	if err != nil {
		t.Fatalf("GetProjectTx: %v", err)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "testalias" {
		t.Errorf("GetProjectTx inside tx: want [testalias], got %v", got.Aliases)
	}
}

func TestSQLiteTaskStore_ListProjects_IncludesAliases(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	p := newProject("Работа")
	if err := createProjectHelper(t, db, store, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, a := range []string{"работа", "job"} {
		if err := addAliasHelper(t, db, store, p.ID, a); err != nil {
			t.Fatalf("AddProjectAliasTx(%q): %v", a, err)
		}
	}

	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	var found *model.Project
	for i := range projects {
		if projects[i].ID == p.ID {
			found = &projects[i]
			break
		}
	}
	if found == nil {
		t.Fatal("project not found in ListProjects")
	}
	if len(found.Aliases) != 2 {
		t.Fatalf("want 2 aliases, got %d: %v", len(found.Aliases), found.Aliases)
	}
	if found.Aliases[0] != "job" || found.Aliases[1] != "работа" {
		t.Errorf("want [job, работа] (sorted), got %v", found.Aliases)
	}
}

func TestSQLiteTaskStore_ListProjects_NoAliasesReturnsEmptySlice(t *testing.T) {
	store, _ := newTaskStore(t)
	ctx := context.Background()

	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("want at least Inbox in ListProjects")
	}
	for _, p := range projects {
		if p.Aliases == nil {
			t.Errorf("project %q: Aliases must be empty slice, not nil", p.Name)
		}
	}
}

// TestUpdateTaskTxRollback verifies that task status changes are rolled back.
func TestUpdateTaskTxRollback(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "откатный апдейт")
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	status := "done"
	if err := store.UpdateTaskTx(ctx, tx, task.ID, model.TaskUpdate{Status: &status}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("UpdateTaskTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "open" {
		t.Errorf("after Rollback Status = %q, want %q", got.Status, "open")
	}
}

// TestListClosedOlderThan verifies that only done/cancelled tasks closed
// before the cutoff are listed; open tasks and recently closed tasks are
// excluded.
func TestListClosedOlderThan(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	openTask := newTask(store.DefaultProjectID(), "открытая")
	if err := createTaskHelper(t, db, store, openTask); err != nil {
		t.Fatalf("CreateTask open: %v", err)
	}

	doneOld := newTask(store.DefaultProjectID(), "давно завершённая")
	if err := createTaskHelper(t, db, store, doneOld); err != nil {
		t.Fatalf("CreateTask doneOld: %v", err)
	}
	statusDone := "done"
	if err := updateTaskHelper(t, db, store, doneOld.ID, model.TaskUpdate{
		Status: &statusDone, ClosedAt: ptrToPtrTime(old),
	}); err != nil {
		t.Fatalf("UpdateTask doneOld: %v", err)
	}

	cancelledOld := newTask(store.DefaultProjectID(), "давно отменённая")
	if err := createTaskHelper(t, db, store, cancelledOld); err != nil {
		t.Fatalf("CreateTask cancelledOld: %v", err)
	}
	statusCancelled := "cancelled"
	if err := updateTaskHelper(t, db, store, cancelledOld.ID, model.TaskUpdate{
		Status: &statusCancelled, ClosedAt: ptrToPtrTime(old),
	}); err != nil {
		t.Fatalf("UpdateTask cancelledOld: %v", err)
	}

	doneRecent := newTask(store.DefaultProjectID(), "недавно завершённая")
	if err := createTaskHelper(t, db, store, doneRecent); err != nil {
		t.Fatalf("CreateTask doneRecent: %v", err)
	}
	if err := updateTaskHelper(t, db, store, doneRecent.ID, model.TaskUpdate{
		Status: &statusDone, ClosedAt: ptrToPtrTime(recent),
	}); err != nil {
		t.Fatalf("UpdateTask doneRecent: %v", err)
	}

	cutoff := now.Add(-14 * 24 * time.Hour)
	listed, err := store.ListClosedOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListClosedOlderThan: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d tasks, want 2", len(listed))
	}

	gotIDs := map[string]bool{listed[0].ID: true, listed[1].ID: true}
	if !gotIDs[doneOld.ID] {
		t.Errorf("doneOld %s not listed", doneOld.ID)
	}
	if !gotIDs[cancelledOld.ID] {
		t.Errorf("cancelledOld %s not listed", cancelledOld.ID)
	}

	if err := withTx(t, db, func(tx *sql.Tx) error {
		for _, task := range listed {
			if err := store.DeleteTaskTx(ctx, tx, task.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("DeleteTaskTx: %v", err)
	}

	if got, _ := store.GetTask(ctx, openTask.ID); got == nil {
		t.Error("open task was deleted, must remain")
	}
	if got, _ := store.GetTask(ctx, doneRecent.ID); got == nil {
		t.Error("recently closed task was deleted, must remain")
	}
	if got, _ := store.GetTask(ctx, doneOld.ID); got != nil {
		t.Error("old done task was not deleted")
	}
	if got, _ := store.GetTask(ctx, cancelledOld.ID); got != nil {
		t.Error("old cancelled task was not deleted")
	}
}

// TestListClosedOlderThan_EmptyTable returns an empty slice with no error
// when the tasks table contains no closed tasks.
func TestListClosedOlderThan_EmptyTable(t *testing.T) {
	store, _ := newTaskStore(t)

	listed, err := store.ListClosedOlderThan(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("ListClosedOlderThan: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("listed %d tasks, want 0", len(listed))
	}
}

// ptrToPtrTime returns a non-nil **time.Time wrapping the given time,
// for use with model.TaskUpdate.ClosedAt (a pointer-to-pointer field).
func ptrToPtrTime(t time.Time) **time.Time {
	tp := &t
	return &tp
}

// --- Priority and source details ---

// equalSourceDetails compares source details, using time.Equal for At.
func equalSourceDetails(a, b *model.TaskSourceDetails) bool {
	if a == nil || b == nil {
		return a == b
	}
	ac, bc := *a, *b
	if (ac.At == nil) != (bc.At == nil) {
		return false
	}
	if ac.At != nil && !ac.At.Equal(*bc.At) {
		return false
	}
	ac.At, bc.At = nil, nil
	return reflect.DeepEqual(ac, bc)
}

func TestSQLiteTaskStore_PrioritySourceDetails_RoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name          string
		priority      model.TaskPriority
		details       *model.TaskSourceDetails
		wantRawDetail string
	}{
		{
			name:     "with details",
			priority: model.TaskPriorityMedium,
			details: &model.TaskSourceDetails{
				Name:    "Команда разработки",
				Subject: "Отчёт",
				Author:  "Алиса",
				Text:    "Сделаю отчёт к пятнице",
				At:      &at,
				ReplyTo: &model.TaskSourceQuote{Author: "Боб", Text: "Когда будет отчёт?"},
				Link:    "https://t.me/c/1234567890/42",
				Via:     "message",
			},
		},
		{
			name:     "minimal details",
			priority: model.TaskPriorityHigh,
			details:  &model.TaskSourceDetails{Via: "api"},
		},
		{
			name:          "without details",
			priority:      model.TaskPriorityNone,
			details:       nil,
			wantRawDetail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, db := newTaskStore(t)
			ctx := context.Background()

			task := newTask(store.DefaultProjectID(), "Подготовить отчёт")
			task.Priority = tt.priority
			task.Source = model.Source{Kind: "telegram", ID: "-1001234567890"}
			task.SourceDetails = tt.details
			if err := createTaskHelper(t, db, store, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}

			var rawPriority int
			var rawDetails string
			if err := db.QueryRowContext(ctx,
				`SELECT priority, source_details FROM tasks WHERE id = ?`, task.ID,
			).Scan(&rawPriority, &rawDetails); err != nil {
				t.Fatalf("reading raw columns: %v", err)
			}
			if rawPriority != int(tt.priority) {
				t.Errorf("raw priority = %d, want %d", rawPriority, tt.priority)
			}
			if tt.details == nil && rawDetails != tt.wantRawDetail {
				t.Errorf("raw source_details = %q, want %q", rawDetails, tt.wantRawDetail)
			}
			if tt.details != nil && rawDetails == "" {
				t.Error("raw source_details is empty, want JSON")
			}

			readers := map[string]func() (*model.Task, error){
				"GetTask": func() (*model.Task, error) { return store.GetTask(ctx, task.ID) },
				"GetTaskTx": func() (*model.Task, error) {
					var got *model.Task
					err := withTx(t, db, func(tx *sql.Tx) error {
						var err error
						got, err = store.GetTaskTx(ctx, tx, task.ID)
						return err
					})
					return got, err
				},
				"GetTaskByRef": func() (*model.Task, error) { return store.GetTaskByRef(ctx, "inbox", task.Number) },
				"ListTasks": func() (*model.Task, error) {
					tasks, err := store.ListTasks(ctx, "", model.TaskFilter{})
					if err != nil || len(tasks) != 1 {
						return nil, err
					}
					return &tasks[0], nil
				},
			}
			for name, read := range readers {
				got, err := read()
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got == nil {
					t.Fatalf("%s: task not found", name)
				}
				if got.Priority != tt.priority {
					t.Errorf("%s: Priority = %d, want %d", name, got.Priority, tt.priority)
				}
				if !equalSourceDetails(got.SourceDetails, tt.details) {
					t.Errorf("%s: SourceDetails = %+v, want %+v", name, got.SourceDetails, tt.details)
				}
				if got.Source.Kind != "telegram" || got.Source.ID != "-1001234567890" {
					t.Errorf("%s: Source = %+v, want telegram/-1001234567890", name, got.Source)
				}
			}

			statusDone := "done"
			if err := updateTaskHelper(t, db, store, task.ID, model.TaskUpdate{
				Status: &statusDone, ClosedAt: ptrToPtrTime(time.Now().Add(-30 * 24 * time.Hour)),
			}); err != nil {
				t.Fatalf("UpdateTask: %v", err)
			}
			closed, err := store.ListClosedOlderThan(ctx, time.Now())
			if err != nil {
				t.Fatalf("ListClosedOlderThan: %v", err)
			}
			if len(closed) != 1 {
				t.Fatalf("ListClosedOlderThan returned %d tasks, want 1", len(closed))
			}
			if closed[0].Priority != tt.priority {
				t.Errorf("ListClosedOlderThan: Priority = %d, want %d", closed[0].Priority, tt.priority)
			}
			if !equalSourceDetails(closed[0].SourceDetails, tt.details) {
				t.Errorf("ListClosedOlderThan: SourceDetails = %+v, want %+v", closed[0].SourceDetails, tt.details)
			}
		})
	}
}

func TestSQLiteTaskStore_UpdateTask_Priority(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Позвонить клиенту")
	task.Priority = model.TaskPriorityLow
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	high := model.TaskPriorityHigh
	none := model.TaskPriorityNone
	summary := "Позвонить клиенту сегодня"
	steps := []struct {
		name string
		upd  model.TaskUpdate
		want model.TaskPriority
	}{
		{name: "raise to high", upd: model.TaskUpdate{Priority: &high}, want: model.TaskPriorityHigh},
		{name: "other field keeps priority", upd: model.TaskUpdate{Summary: &summary}, want: model.TaskPriorityHigh},
		{name: "reset to none", upd: model.TaskUpdate{Priority: &none}, want: model.TaskPriorityNone},
	}
	for _, step := range steps {
		if err := updateTaskHelper(t, db, store, task.ID, step.upd); err != nil {
			t.Fatalf("%s: UpdateTask: %v", step.name, err)
		}
		got, err := store.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("%s: GetTask: %v", step.name, err)
		}
		if got.Priority != step.want {
			t.Errorf("%s: Priority = %d, want %d", step.name, got.Priority, step.want)
		}
	}
}

func TestSQLiteTaskStore_CreateTask_CapsSourceTexts(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	longText := strings.Repeat("я", model.MaxSourceTextRunes+250)
	longReply := strings.Repeat("ю", model.MaxSourceReplyTextRunes+250)
	orig := &model.TaskSourceDetails{
		Author:  "Алиса",
		Text:    longText,
		ReplyTo: &model.TaskSourceQuote{Author: "Боб", Text: longReply},
		Via:     "message",
	}
	task := newTask(store.DefaultProjectID(), "Длинное обещание")
	task.SourceDetails = orig
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if orig.Text != longText || orig.ReplyTo.Text != longReply {
		t.Error("CreateTaskTx mutated the caller's source details")
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	for name, d := range map[string]*model.TaskSourceDetails{"created": task.SourceDetails, "stored": got.SourceDetails} {
		if d == nil {
			t.Fatalf("%s: SourceDetails is nil", name)
		}
		if n := utf8.RuneCountInString(d.Text); n != model.MaxSourceTextRunes {
			t.Errorf("%s: Text runes = %d, want %d", name, n, model.MaxSourceTextRunes)
		}
		if n := utf8.RuneCountInString(d.ReplyTo.Text); n != model.MaxSourceReplyTextRunes {
			t.Errorf("%s: ReplyTo.Text runes = %d, want %d", name, n, model.MaxSourceReplyTextRunes)
		}
		if d.Author != "Алиса" || d.ReplyTo.Author != "Боб" {
			t.Errorf("%s: authors changed: %+v", name, d)
		}
	}
}

func TestSQLiteTaskStore_InvalidSourceDetailsJSON_YieldsNil(t *testing.T) {
	store, db := newTaskStore(t)
	ctx := context.Background()

	task := newTask(store.DefaultProjectID(), "Повреждённый снимок")
	task.Priority = model.TaskPriorityMedium
	task.SourceDetails = &model.TaskSourceDetails{Text: "текст", Via: "message"}
	if err := createTaskHelper(t, db, store, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tasks SET source_details = ? WHERE id = ?`, `{"text": broken`, task.ID,
	); err != nil {
		t.Fatalf("corrupting source_details: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.SourceDetails != nil {
		t.Errorf("GetTask: SourceDetails = %+v, want nil", got.SourceDetails)
	}
	if got.Priority != model.TaskPriorityMedium {
		t.Errorf("GetTask: Priority = %d, want %d", got.Priority, model.TaskPriorityMedium)
	}

	listed, err := store.ListTasks(ctx, "", model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(listed) != 1 || listed[0].SourceDetails != nil {
		t.Errorf("ListTasks: got %+v, want one task with nil SourceDetails", listed)
	}
}
