package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
)

// mockTaskStore is a partial implementation of model.TaskStore for cache tests.
// The embedded interface stubs all other methods; calling any of them panics,
// which tests rely on to guarantee only the intended methods are touched.
type mockTaskStore struct {
	model.TaskStore

	mu              sync.Mutex
	projects        []model.Project
	listCalls       int
	listErr         error
	createCalls     int
	createErr       error
	updateProjCalls int
	updateProjErr   error
	defaultID       string
	defaultCalls    int
	listTasksCalls  int
	listTasksReturn []model.Task
}

func (m *mockTaskStore) ListProjects(_ context.Context) ([]model.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listCalls++
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]model.Project, len(m.projects))
	copy(out, m.projects)
	return out, nil
}

func (m *mockTaskStore) CreateProjectTx(_ context.Context, _ *sql.Tx, p *model.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if m.createErr != nil {
		return m.createErr
	}
	p.ID = "proj-" + p.Name
	m.projects = append(m.projects, *p)
	return nil
}

func (m *mockTaskStore) UpdateProjectTx(_ context.Context, _ *sql.Tx, _ string, _ model.ProjectUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateProjCalls++
	return m.updateProjErr
}

func (m *mockTaskStore) DefaultProjectID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultCalls++
	return m.defaultID
}

func (m *mockTaskStore) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listTasksCalls++
	return m.listTasksReturn, nil
}

func TestCachedTaskStore_ListProjects_CachesAfterFirstCall(t *testing.T) {
	mock := &mockTaskStore{
		projects: []model.Project{
			{ID: "p1", Name: "Inbox"},
			{ID: "p2", Name: "На Старт"},
		},
	}
	cached := storage.NewCachedTaskStore(mock)

	ctx := context.Background()
	first, err := cached.ListProjects(ctx)
	if err != nil {
		t.Fatalf("first ListProjects: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("want 2 projects, got %d", len(first))
	}

	second, err := cached.ListProjects(ctx)
	if err != nil {
		t.Fatalf("second ListProjects: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second call returned %d projects, want 2", len(second))
	}

	if mock.listCalls != 1 {
		t.Errorf("delegate ListProjects called %d times, want 1 (cache)", mock.listCalls)
	}
}

func TestCachedTaskStore_CreateProject_DoesNotInvalidate(t *testing.T) {
	// Write methods inside a transaction must not touch the cache: invalidation is
	// the responsibility of the use-case layer after tx.Commit().
	mock := &mockTaskStore{
		projects: []model.Project{{ID: "p1", Name: "Inbox"}},
	}
	cached := storage.NewCachedTaskStore(mock)

	ctx := context.Background()
	if _, err := cached.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects #1: %v", err)
	}

	if err := cached.CreateProjectTx(ctx, nil, &model.Project{Name: "Новый", Slug: "novyy"}); err != nil {
		t.Fatalf("CreateProjectTx: %v", err)
	}

	if _, err := cached.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects #2: %v", err)
	}

	if mock.listCalls != 1 {
		t.Errorf("CreateProjectTx must not invalidate cache, want 1 ListProjects call, got %d", mock.listCalls)
	}
}

func TestCachedTaskStore_UpdateProject_DoesNotInvalidate(t *testing.T) {
	mock := &mockTaskStore{
		projects: []model.Project{{ID: "p1", Name: "Inbox", Slug: "inbox"}},
	}
	cached := storage.NewCachedTaskStore(mock)

	ctx := context.Background()
	if _, err := cached.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects #1: %v", err)
	}

	newSlug := "inbox-new"
	if err := cached.UpdateProjectTx(ctx, nil, "p1", model.ProjectUpdate{Slug: &newSlug}); err != nil {
		t.Fatalf("UpdateProjectTx: %v", err)
	}

	if _, err := cached.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects #2: %v", err)
	}

	if mock.listCalls != 1 {
		t.Errorf("UpdateProjectTx must not invalidate cache, want 1 ListProjects call, got %d", mock.listCalls)
	}
}

func TestCachedTaskStore_Invalidate_ResetsCache(t *testing.T) {
	mock := &mockTaskStore{
		projects: []model.Project{{ID: "p1", Name: "Inbox"}},
	}
	cached := storage.NewCachedTaskStore(mock)

	ctx := context.Background()
	if _, err := cached.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects #1: %v", err)
	}

	cached.Invalidate()

	if _, err := cached.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects #2: %v", err)
	}

	if mock.listCalls != 2 {
		t.Errorf("after Invalidate expected repeated ListProjects call (2 calls), got %d", mock.listCalls)
	}
}

func TestCachedTaskStore_ListProjects_ErrorNotCached(t *testing.T) {
	mock := &mockTaskStore{listErr: errors.New("бд недоступна")}
	cached := storage.NewCachedTaskStore(mock)

	ctx := context.Background()
	if _, err := cached.ListProjects(ctx); err == nil {
		t.Fatal("first ListProjects should return an error")
	}
	if _, err := cached.ListProjects(ctx); err == nil {
		t.Fatal("second ListProjects should return an error")
	}

	if mock.listCalls != 2 {
		t.Errorf("ListProjects error must not be cached, want 2 calls, got %d", mock.listCalls)
	}
}

func TestCachedTaskStore_ListProjects_ReturnsDefensiveCopy(t *testing.T) {
	mock := &mockTaskStore{
		projects: []model.Project{{ID: "p1", Name: "Inbox"}},
	}
	cached := storage.NewCachedTaskStore(mock)

	ctx := context.Background()
	first, err := cached.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	first[0].Name = "Модификация"

	second, err := cached.ListProjects(ctx)
	if err != nil {
		t.Fatalf("second ListProjects: %v", err)
	}
	if second[0].Name != "Inbox" {
		t.Errorf("cache must be protected from external mutation: want Inbox, got %q", second[0].Name)
	}
}

func TestCachedTaskStore_OtherMethodsDelegated(t *testing.T) {
	mock := &mockTaskStore{defaultID: "inbox-uuid"}
	cached := storage.NewCachedTaskStore(mock)

	if got := cached.DefaultProjectID(); got != "inbox-uuid" {
		t.Errorf("DefaultProjectID = %q, want %q", got, "inbox-uuid")
	}
	if mock.defaultCalls != 1 {
		t.Errorf("delegate DefaultProjectID called %d times, want 1", mock.defaultCalls)
	}

	_, err := cached.ListTasks(context.Background(), "", model.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if mock.listTasksCalls != 1 {
		t.Errorf("delegate ListTasks called %d times, want 1", mock.listTasksCalls)
	}
}
