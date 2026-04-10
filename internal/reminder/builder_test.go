package reminder

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// --- mocks ---

type mockTaskStore struct {
	tasks     []model.Task
	projects  []model.Project
	tasksErr  error
	projErr   error
	defaultID string
}

func (m *mockTaskStore) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	return m.tasks, m.tasksErr
}

func (m *mockTaskStore) ListProjects(_ context.Context) ([]model.Project, error) {
	return m.projects, m.projErr
}

func (m *mockTaskStore) DefaultProjectID() string { return m.defaultID }

func (m *mockTaskStore) CreateProjectTx(_ context.Context, _ *sql.Tx, _ *model.Project) error {
	return nil
}
func (m *mockTaskStore) UpdateProjectTx(_ context.Context, _ *sql.Tx, _ string, _ model.ProjectUpdate) error {
	return nil
}
func (m *mockTaskStore) GetProject(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockTaskStore) GetProjectTx(_ context.Context, _ *sql.Tx, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockTaskStore) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}
func (m *mockTaskStore) CreateTaskTx(_ context.Context, _ *sql.Tx, _ *model.Task) error {
	return nil
}
func (m *mockTaskStore) GetTask(_ context.Context, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskStore) GetTaskTx(_ context.Context, _ *sql.Tx, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskStore) GetTaskByRef(_ context.Context, _ string, _ int) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskStore) UpdateTaskTx(_ context.Context, _ *sql.Tx, _ string, _ model.TaskUpdate) error {
	return nil
}
func (m *mockTaskStore) MoveTaskTx(_ context.Context, _ *sql.Tx, _, _ string) error { return nil }

// --- helpers ---

func ptr(t time.Time) *time.Time { return &t }

var moscow = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return loc
}()

func msk(year, month, day, hour, min int) time.Time {
	return time.Date(year, time.Month(month), day, hour, min, 0, 0, moscow)
}

// --- tests ---

func TestBuilder_Build_Buckets(t *testing.T) {
	// at = 2026-04-17 14:00 Europe/Moscow
	at := msk(2026, 4, 17, 14, 0)

	inbox := model.Project{ID: "1", Name: "Inbox"}
	work := model.Project{ID: "2", Name: "work"}

	tasks := []model.Task{
		// overdue: deadline 16.04 10:00 < at
		{ID: "1", ProjectID: "1", Summary: "задача 1", Deadline: ptr(msk(2026, 4, 16, 10, 0)), CreatedAt: msk(2026, 4, 1, 9, 0)},
		// overdue: deadline 17.04 11:00 < at=14:00 → Overdue, not Today
		{ID: "2", ProjectID: "1", Summary: "задача 2", Deadline: ptr(msk(2026, 4, 17, 11, 0)), CreatedAt: msk(2026, 4, 2, 9, 0)},
		// today: deadline 17.04 18:00 — within [startOfDay, endOfDay)
		{ID: "3", ProjectID: "2", Summary: "задача 3", Deadline: ptr(msk(2026, 4, 17, 18, 0)), CreatedAt: msk(2026, 4, 3, 9, 0)},
		// upcoming: deadline 20.04 12:00 — within 168h horizon
		{ID: "4", ProjectID: "2", Summary: "задача 4", Deadline: ptr(msk(2026, 4, 20, 12, 0)), CreatedAt: msk(2026, 4, 4, 9, 0)},
		// beyond horizon (168h = 7 days): 28.04 — discarded
		{ID: "5", ProjectID: "1", Summary: "задача 5", Deadline: ptr(msk(2026, 4, 28, 12, 0)), CreatedAt: msk(2026, 4, 5, 9, 0)},
		// undated
		{ID: "6", ProjectID: "1", Summary: "задача 6", Deadline: nil, CreatedAt: msk(2026, 4, 6, 9, 0)},
	}

	store := &mockTaskStore{
		tasks:     tasks,
		projects:  []model.Project{inbox, work},
		defaultID: "1",
	}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour, UndatedLimit: 10})

	summary, err := b.Build(context.Background(), "morning", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// task 1 (16.04 10:00) and task 2 (17.04 11:00) — both before at=14:00, in Overdue
	overdueIDs := taskIDs(summary.Overdue)
	if !contains(overdueIDs, "1") || !contains(overdueIDs, "2") {
		t.Errorf("Overdue should contain tasks 1 and 2, got: %v", overdueIDs)
	}

	// task 3 (17.04 18:00) — after at=14:00, but within the current day
	todayIDs := taskIDs(summary.Today)
	if !contains(todayIDs, "3") {
		t.Errorf("Today should contain task 3, got: %v", todayIDs)
	}
	if contains(todayIDs, "2") {
		t.Error("task 2 (deadline passed) should not be in Today")
	}

	upcomingIDs := taskIDs(summary.Upcoming)
	if !contains(upcomingIDs, "4") {
		t.Errorf("Upcoming should contain task 4, got: %v", upcomingIDs)
	}
	if contains(upcomingIDs, "5") {
		t.Error("task 5 is beyond the horizon — should not be in Upcoming")
	}

	undatedIDs := taskIDs(summary.Undated)
	if !contains(undatedIDs, "6") {
		t.Errorf("Undated should contain task 6, got: %v", undatedIDs)
	}

	if summary.IsEmpty {
		t.Error("summary should not be empty")
	}
}

func TestBuilder_Build_UndatedLimit0(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	store := &mockTaskStore{
		tasks: []model.Task{
			{ID: "1", Summary: "a", Deadline: nil, CreatedAt: msk(2026, 4, 1, 9, 0)},
			{ID: "2", Summary: "b", Deadline: nil, CreatedAt: msk(2026, 4, 2, 9, 0)},
			{ID: "3", Summary: "c", Deadline: nil, CreatedAt: msk(2026, 4, 3, 9, 0)},
		},
		projects:  []model.Project{{ID: "1", Name: "Inbox"}},
		defaultID: "1",
	}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour, UndatedLimit: 0})
	summary, err := b.Build(context.Background(), "morning", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(summary.Undated) != 0 {
		t.Errorf("UndatedLimit=0: Undated should be empty, got %d groups", len(summary.Undated))
	}
	if summary.UndatedTotal != 3 {
		t.Errorf("UndatedTotal should be 3, got %d", summary.UndatedTotal)
	}
	if !summary.IsEmpty {
		t.Error("summary should be empty (no Overdue/Today/Upcoming/Undated)")
	}
}

func TestBuilder_Build_UndatedLimit(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	tasks := []model.Task{
		{ID: "1", Summary: "a", Deadline: nil, CreatedAt: msk(2026, 4, 1, 9, 0)},
		{ID: "2", Summary: "b", Deadline: nil, CreatedAt: msk(2026, 4, 3, 9, 0)},
		{ID: "3", Summary: "c", Deadline: nil, CreatedAt: msk(2026, 4, 2, 9, 0)},
		{ID: "4", Summary: "d", Deadline: nil, CreatedAt: msk(2026, 4, 5, 9, 0)},
		{ID: "5", Summary: "e", Deadline: nil, CreatedAt: msk(2026, 4, 4, 9, 0)},
	}
	store := &mockTaskStore{
		tasks:     tasks,
		projects:  []model.Project{{ID: "1", Name: "Inbox"}},
		defaultID: "1",
	}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour, UndatedLimit: 3})
	summary, err := b.Build(context.Background(), "morning", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if summary.UndatedTotal != 5 {
		t.Errorf("UndatedTotal should be 5, got %d", summary.UndatedTotal)
	}

	undated := flatTasks(summary.Undated)
	if len(undated) != 3 {
		t.Fatalf("Undated should contain 3 tasks, got %d", len(undated))
	}
	// Expect 3 oldest: 1, 3, 2 (by CreatedAt: 01, 02, 03)
	if undated[0].ID != "1" || undated[1].ID != "3" || undated[2].ID != "2" {
		t.Errorf("wrong Undated order: %v", taskIDs(flatGroups(summary.Undated)))
	}
}

func TestBuilder_Build_ProjectOrdering(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	inbox := model.Project{ID: "1", Name: "Inbox"}
	alpha := model.Project{ID: "2", Name: "alpha"}
	beta := model.Project{ID: "3", Name: "beta"}

	tasks := []model.Task{
		{ID: "1", ProjectID: "3", Summary: "beta-task", Deadline: ptr(msk(2026, 4, 20, 10, 0)), CreatedAt: msk(2026, 4, 1, 9, 0)},
		{ID: "2", ProjectID: "2", Summary: "alpha-task", Deadline: ptr(msk(2026, 4, 20, 11, 0)), CreatedAt: msk(2026, 4, 1, 9, 0)},
		{ID: "3", ProjectID: "1", Summary: "inbox-task", Deadline: ptr(msk(2026, 4, 20, 12, 0)), CreatedAt: msk(2026, 4, 1, 9, 0)},
	}

	store := &mockTaskStore{
		tasks:     tasks,
		projects:  []model.Project{inbox, alpha, beta},
		defaultID: "1",
	}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour, UndatedLimit: 10})
	summary, err := b.Build(context.Background(), "morning", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(summary.Upcoming) != 3 {
		t.Fatalf("expected 3 groups in Upcoming, got %d", len(summary.Upcoming))
	}
	if summary.Upcoming[0].ProjectID != "1" {
		t.Errorf("first group should be Inbox, got: %s", summary.Upcoming[0].ProjectName)
	}
	if summary.Upcoming[1].ProjectName != "alpha" {
		t.Errorf("second group should be alpha, got: %s", summary.Upcoming[1].ProjectName)
	}
	if summary.Upcoming[2].ProjectName != "beta" {
		t.Errorf("third group should be beta, got: %s", summary.Upcoming[2].ProjectName)
	}
}

func TestBuilder_Build_TaskSortingDeadlineAsc(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	store := &mockTaskStore{
		tasks: []model.Task{
			{ID: "2", ProjectID: "1", Summary: "b", Deadline: ptr(msk(2026, 4, 20, 12, 0)), CreatedAt: msk(2026, 4, 2, 9, 0)},
			{ID: "1", ProjectID: "1", Summary: "a", Deadline: ptr(msk(2026, 4, 19, 10, 0)), CreatedAt: msk(2026, 4, 1, 9, 0)},
		},
		projects:  []model.Project{{ID: "1", Name: "Inbox"}},
		defaultID: "1",
	}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour, UndatedLimit: 10})
	summary, err := b.Build(context.Background(), "morning", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	upcoming := flatTasks(summary.Upcoming)
	if len(upcoming) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(upcoming))
	}
	if upcoming[0].ID != "1" || upcoming[1].ID != "2" {
		t.Errorf("wrong order by Deadline: %v", []string{upcoming[0].ID, upcoming[1].ID})
	}
}

func TestBuilder_Build_EmptyTasks(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	store := &mockTaskStore{
		tasks:     []model.Task{},
		projects:  []model.Project{{ID: "1", Name: "Inbox"}},
		defaultID: "1",
	}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour, UndatedLimit: 10})
	summary, err := b.Build(context.Background(), "morning", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !summary.IsEmpty {
		t.Error("summary should be empty")
	}
	if summary.Overdue != nil || summary.Today != nil || summary.Upcoming != nil || summary.Undated != nil {
		t.Error("all sections should be nil")
	}
}

func TestBuilder_Build_ListTasksError(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	wantErr := errors.New("db unavailable")
	store := &mockTaskStore{tasksErr: wantErr, defaultID: "1"}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour})
	_, err := b.Build(context.Background(), "morning", at)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error should wrap the original, got: %v", err)
	}
}

func TestBuilder_Build_ListProjectsError(t *testing.T) {
	at := msk(2026, 4, 17, 14, 0)
	wantErr := errors.New("projects unavailable")
	store := &mockTaskStore{projErr: wantErr, defaultID: "1"}
	b := NewBuilder(store, BuilderConfig{PlansHorizon: 168 * time.Hour})
	_, err := b.Build(context.Background(), "morning", at)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error should wrap the original, got: %v", err)
	}
}

// --- test helpers ---

func taskIDs(groups []model.ProjectGroup) []string {
	var ids []string
	for _, g := range groups {
		for _, t := range g.Tasks {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

func flatTasks(groups []model.ProjectGroup) []model.Task {
	var tasks []model.Task
	for _, g := range groups {
		tasks = append(tasks, g.Tasks...)
	}
	return tasks
}

func flatGroups(groups []model.ProjectGroup) []model.ProjectGroup {
	return groups
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
