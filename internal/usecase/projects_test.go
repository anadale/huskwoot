package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

// --- manual mocks ---

type mockTaskStoreForProjects struct {
	projects     []model.Project
	findByName   *model.Project
	findErr      error
	createErr    error
	updateErr    error
	addAliasErr  error
	defaultPID   string
	createCalled int
	updateCalled int
}

func (m *mockTaskStoreForProjects) CreateProjectTx(_ context.Context, _ *sql.Tx, p *model.Project) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.createCalled++
	p.ID = "new-uuid"
	m.projects = append(m.projects, *p)
	return nil
}

func (m *mockTaskStoreForProjects) UpdateProjectTx(_ context.Context, _ *sql.Tx, id string, upd model.ProjectUpdate) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updateCalled++
	for i := range m.projects {
		if m.projects[i].ID != id {
			continue
		}
		if upd.Name != nil {
			m.projects[i].Name = *upd.Name
		}
		if upd.Slug != nil {
			m.projects[i].Slug = *upd.Slug
		}
		if upd.Description != nil {
			m.projects[i].Description = *upd.Description
		}
		return nil
	}
	return nil
}

func (m *mockTaskStoreForProjects) GetProject(_ context.Context, id string) (*model.Project, error) {
	for _, p := range m.projects {
		if p.ID == id {
			cp := p
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockTaskStoreForProjects) GetProjectTx(ctx context.Context, _ *sql.Tx, id string) (*model.Project, error) {
	return m.GetProject(ctx, id)
}

func (m *mockTaskStoreForProjects) ListProjects(_ context.Context) ([]model.Project, error) {
	return m.projects, nil
}

func (m *mockTaskStoreForProjects) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return m.findByName, m.findErr
}

func (m *mockTaskStoreForProjects) CreateTaskTx(_ context.Context, _ *sql.Tx, _ *model.Task) error {
	return nil
}
func (m *mockTaskStoreForProjects) GetTask(_ context.Context, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskStoreForProjects) GetTaskTx(_ context.Context, _ *sql.Tx, _ string) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskStoreForProjects) GetTaskByRef(_ context.Context, _ string, _ int) (*model.Task, error) {
	return nil, nil
}
func (m *mockTaskStoreForProjects) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	return nil, nil
}
func (m *mockTaskStoreForProjects) UpdateTaskTx(_ context.Context, _ *sql.Tx, _ string, _ model.TaskUpdate) error {
	return nil
}
func (m *mockTaskStoreForProjects) MoveTaskTx(_ context.Context, _ *sql.Tx, _, _ string) error {
	return nil
}
func (m *mockTaskStoreForProjects) DefaultProjectID() string { return m.defaultPID }
func (m *mockTaskStoreForProjects) AddProjectAliasTx(_ context.Context, _ *sql.Tx, projectID, alias string) error {
	if m.addAliasErr != nil {
		return m.addAliasErr
	}
	for i := range m.projects {
		if m.projects[i].ID == projectID {
			m.projects[i].Aliases = append(m.projects[i].Aliases, alias)
			return nil
		}
	}
	return nil
}
func (m *mockTaskStoreForProjects) RemoveProjectAliasTx(_ context.Context, _ *sql.Tx, projectID, alias string) error {
	for i := range m.projects {
		if m.projects[i].ID != projectID {
			continue
		}
		for j, a := range m.projects[i].Aliases {
			if a == alias {
				m.projects[i].Aliases = append(m.projects[i].Aliases[:j], m.projects[i].Aliases[j+1:]...)
				return nil
			}
		}
		return nil
	}
	return nil
}
func (m *mockTaskStoreForProjects) ListAliasesForProject(_ context.Context, projectID string) ([]string, error) {
	for _, p := range m.projects {
		if p.ID == projectID {
			return p.Aliases, nil
		}
	}
	return []string{}, nil
}

type mockMetaStoreForProjects struct {
	data   map[string]string
	getErr error
	setErr error
}

func newMockMeta() *mockMetaStoreForProjects {
	return &mockMetaStoreForProjects{data: make(map[string]string)}
}

func (m *mockMetaStoreForProjects) Get(_ context.Context, key string) (string, error) {
	return m.data[key], m.getErr
}

func (m *mockMetaStoreForProjects) SetTx(_ context.Context, _ *sql.Tx, key, value string) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.data[key] = value
	return nil
}

func (m *mockMetaStoreForProjects) Values(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

// projectFixture assembles mocks and a new ProjectService for a series of tests.
type projectFixture struct {
	db      *sql.DB
	tasks   *mockTaskStoreForProjects
	meta    *mockMetaStoreForProjects
	events  *mockEventStore
	devices *mockDeviceStore
	queue   *mockPushQueue
	broker  *mockBroker
	svc     model.ProjectService
}

func newProjectFixture(t *testing.T) *projectFixture {
	f := &projectFixture{
		db:      openTestDB(t),
		tasks:   &mockTaskStoreForProjects{},
		meta:    newMockMeta(),
		events:  newMockEventStore(),
		devices: &mockDeviceStore{},
		queue:   &mockPushQueue{},
		broker:  newMockBroker(),
	}
	f.svc = usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB:      f.db,
		Tasks:   f.tasks,
		Meta:    f.meta,
		Events:  f.events,
		Devices: f.devices,
		Queue:   f.queue,
		Broker:  f.broker,
	})
	return f
}

// --- tests ---

func TestProjectServiceCreateAutoSlug(t *testing.T) {
	f := newProjectFixture(t)

	p, err := f.svc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Новый Проект"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Slug != "novyy-proekt" {
		t.Fatalf("slug=%q, want %q", p.Slug, "novyy-proekt")
	}
	if f.tasks.createCalled != 1 {
		t.Fatalf("store.CreateProjectTx called %d times, want 1", f.tasks.createCalled)
	}
}

func TestProjectServiceCreateExplicitSlug(t *testing.T) {
	f := newProjectFixture(t)

	p, err := f.svc.CreateProject(context.Background(), model.CreateProjectRequest{
		Name: "Работа",
		Slug: "work",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Slug != "work" {
		t.Fatalf("slug=%q, want %q", p.Slug, "work")
	}
}

func TestProjectServiceCreateProjectEmitsEvent(t *testing.T) {
	f := newProjectFixture(t)
	f.devices.activeIDs = []string{"device-A", "device-B"}
	f.broker.active["device-A"] = true

	p, err := f.svc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Новый"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	events := f.events.recorded()
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	if events[0].Kind != model.EventProjectCreated {
		t.Fatalf("kind=%q, want %q", events[0].Kind, model.EventProjectCreated)
	}
	if events[0].EntityID != p.ID {
		t.Fatalf("EntityID=%q, want %q", events[0].EntityID, p.ID)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("payload does not parse: %v", err)
	}
	if payload["id"] != p.ID {
		t.Fatalf("payload.id=%v, want %q", payload["id"], p.ID)
	}
	if payload["slug"] != p.Slug {
		t.Fatalf("payload.slug=%v, want %q", payload["slug"], p.Slug)
	}

	enq := f.queue.snapshot()
	if len(enq) != 1 || enq[0].DeviceID != "device-B" {
		t.Fatalf("enqueued=%+v, want one for device-B", enq)
	}

	notified := f.broker.notifiedEvents()
	if len(notified) != 1 || notified[0].Seq != 1 {
		t.Fatalf("notified=%+v, want 1 event with seq=1", notified)
	}
}

func TestProjectServiceCreateProjectRollbackOnEventError(t *testing.T) {
	f := newProjectFixture(t)
	f.events.insertErr = errors.New("events insert сломан")

	_, err := f.svc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Упадёт"})
	if err == nil {
		t.Fatal("want error")
	}

	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times, want 0 (rollback)", n)
	}
}

func TestProjectServiceUpdateProjectEmitsEvent(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.projects = []model.Project{{ID: "p1", Name: "Old", Slug: "old"}}
	f.devices.activeIDs = []string{"device-X"}

	newName := "New"
	p, err := f.svc.UpdateProject(context.Background(), "p1", model.ProjectUpdate{Name: &newName})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if p == nil || p.Name != "New" {
		t.Fatalf("UpdateProject returned %+v, want Name=New", p)
	}
	if f.tasks.updateCalled != 1 {
		t.Fatalf("store.UpdateProjectTx called %d times", f.tasks.updateCalled)
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventProjectUpdated {
		t.Fatalf("want 1 event project_updated, got %+v", events)
	}
	enq := f.queue.snapshot()
	if len(enq) != 1 || enq[0].DeviceID != "device-X" {
		t.Fatalf("enqueued=%+v", enq)
	}
}

func TestProjectServiceEnsureChannelProjectCreatesAndSetsMapping(t *testing.T) {
	f := newProjectFixture(t)
	f.devices.activeIDs = []string{"device-A"}

	p, err := f.svc.EnsureChannelProject(context.Background(), "chat:42", "Новый Проект")
	if err != nil {
		t.Fatalf("EnsureChannelProject: %v", err)
	}
	if p.Slug != "novyy-proekt" {
		t.Fatalf("slug=%q, want %q", p.Slug, "novyy-proekt")
	}
	if f.meta.data["project:chat:42"] != p.ID {
		t.Fatalf("mapping not recorded: got %q, want %q", f.meta.data["project:chat:42"], p.ID)
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventProjectCreated {
		t.Fatalf("want 1 event project_created, got %+v", events)
	}
}

func TestProjectServiceEnsureChannelProjectReturnsExistingWithoutEvent(t *testing.T) {
	existing := &model.Project{ID: "existing-uuid", Name: "Проект", Slug: "proekt"}
	f := newProjectFixture(t)
	f.tasks.findByName = existing

	p, err := f.svc.EnsureChannelProject(context.Background(), "chat:99", "Проект")
	if err != nil {
		t.Fatalf("EnsureChannelProject: %v", err)
	}
	if p.ID != "existing-uuid" {
		t.Fatalf("ID=%q, want %q", p.ID, "existing-uuid")
	}
	if f.tasks.createCalled != 0 {
		t.Fatalf("store.CreateProjectTx must not be called for existing project")
	}
	if f.meta.data["project:chat:99"] != "existing-uuid" {
		t.Fatalf("mapping not recorded")
	}

	// For an existing project there is no event: only the meta mapping is written.
	if n := len(f.events.recorded()); n != 0 {
		t.Fatalf("events=%d, want 0 (existing project)", n)
	}
	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times, want 0", n)
	}
}

func TestProjectServiceEnsureChannelProjectAtomicRollback(t *testing.T) {
	f := newProjectFixture(t)
	f.meta.setErr = errors.New("meta set сломан")

	_, err := f.svc.EnsureChannelProject(context.Background(), "chat:42", "Новый Проект")
	if err == nil {
		t.Fatal("want error on meta.SetTx failure")
	}

	// Since the tx was rolled back, the event must not be published.
	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times on rollback", n)
	}
}

func TestProjectServiceResolveForChannelDoesNotOpenTx(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-uuid"
	f.meta.data["project:chat:5"] = "mapped-uuid"

	pid, err := f.svc.ResolveProjectForChannel(context.Background(), "chat:5")
	if err != nil {
		t.Fatalf("ResolveProjectForChannel: %v", err)
	}
	if pid != "mapped-uuid" {
		t.Fatalf("pid=%q, want %q", pid, "mapped-uuid")
	}

	// Read-only path must not write events.
	if n := len(f.events.recorded()); n != 0 {
		t.Fatalf("events=%d, want 0 (read-only path)", n)
	}
}

func TestProjectServiceResolveFallbackInbox(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-uuid"

	pid, err := f.svc.ResolveProjectForChannel(context.Background(), "chat:1")
	if err != nil {
		t.Fatalf("ResolveProjectForChannel: %v", err)
	}
	if pid != "inbox-uuid" {
		t.Fatalf("pid=%q, want %q", pid, "inbox-uuid")
	}
}

func TestProjectServiceEnsureChannelProjectFindError(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.findErr = errors.New("DB error")

	_, err := f.svc.EnsureChannelProject(context.Background(), "chat:42", "Проект")
	if err == nil {
		t.Fatal("want error on FindProjectByName failure")
	}
}

func TestProjectServiceListProjects(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.projects = []model.Project{
		{ID: "a", Name: "A", Slug: "a"},
		{ID: "b", Name: "B", Slug: "b"},
	}

	list, err := f.svc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d, want 2", len(list))
	}
}

func TestProjectServiceUpdateProjectStoreError(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.updateErr = errors.New("DB error")

	newName := "X"
	_, err := f.svc.UpdateProject(context.Background(), "p1", model.ProjectUpdate{Name: &newName})
	if err == nil {
		t.Fatal("want error on store.UpdateProjectTx failure")
	}
}

func TestProjectServiceEnsureChannelProjectCreateError(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.createErr = errors.New("DB error")

	_, err := f.svc.EnsureChannelProject(context.Background(), "chat:42", "Новый Проект")
	if err == nil {
		t.Fatal("want error on store.CreateProjectTx failure")
	}
}

func TestProjectServiceResolveChannelMetaGetError(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-uuid"
	f.meta.getErr = errors.New("meta error")

	_, err := f.svc.ResolveProjectForChannel(context.Background(), "chat:1")
	if err == nil {
		t.Fatal("want error on meta.Get failure")
	}
}

func TestProjectServiceAddAliasHappyPath(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{}},
	}

	p, err := f.svc.AddProjectAlias(context.Background(), "p1", "букинист")
	if err != nil {
		t.Fatalf("AddProjectAlias: %v", err)
	}
	if len(p.Aliases) != 1 || p.Aliases[0] != "букинист" {
		t.Fatalf("Aliases=%v, want [букинист]", p.Aliases)
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventProjectUpdated {
		t.Fatalf("want 1 project_updated event, got %+v", events)
	}

	var payload struct {
		ChangedFields []string `json:"changedFields"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.ChangedFields) != 1 || payload.ChangedFields[0] != "aliases" {
		t.Fatalf("changedFields=%v, want [aliases]", payload.ChangedFields)
	}
}

func TestProjectServiceAddAliasInvalidAlias(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"

	_, err := f.svc.AddProjectAlias(context.Background(), "p1", "invalid alias!")
	if !errors.Is(err, usecase.ErrAliasInvalid) {
		t.Fatalf("want ErrAliasInvalid, got %v", err)
	}
}

func TestProjectServiceAddAliasForbiddenForInbox(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "inbox-id", Name: "Inbox", Slug: "inbox"},
	}

	_, err := f.svc.AddProjectAlias(context.Background(), "inbox-id", "букинист")
	if !errors.Is(err, usecase.ErrAliasForbiddenForInbox) {
		t.Fatalf("want ErrAliasForbiddenForInbox, got %v", err)
	}
}

func TestProjectServiceAddAliasConflictsWithName(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore"},
		{ID: "p2", Name: "Work", Slug: "work"},
	}

	_, err := f.svc.AddProjectAlias(context.Background(), "p1", "work")
	if !errors.Is(err, usecase.ErrAliasConflictsWithName) {
		t.Fatalf("want ErrAliasConflictsWithName, got %v", err)
	}
}

func TestProjectServiceAddAliasLimitReached(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	tenAliases := []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh", "ii", "jj"}
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: tenAliases},
	}

	_, err := f.svc.AddProjectAlias(context.Background(), "p1", "kk")
	if !errors.Is(err, usecase.ErrAliasLimitReached) {
		t.Fatalf("want ErrAliasLimitReached, got %v", err)
	}
}

func TestProjectServiceAddAliasTaken(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{}},
	}
	f.tasks.addAliasErr = fmt.Errorf("UNIQUE constraint failed: project_aliases.alias")

	_, err := f.svc.AddProjectAlias(context.Background(), "p1", "букинист")
	if !errors.Is(err, usecase.ErrAliasTaken) {
		t.Fatalf("want ErrAliasTaken, got %v", err)
	}
}

func TestProjectServiceAddAliasRollbackOnEventError(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{}},
	}
	f.events.insertErr = errors.New("events insert failed")

	_, err := f.svc.AddProjectAlias(context.Background(), "p1", "букинист")
	if err == nil {
		t.Fatal("want error on event insert failure")
	}

	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times, want 0 (rollback)", n)
	}
}

func TestProjectServiceRemoveAliasRollbackOnEventError(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{"букинист"}},
	}
	f.events.insertErr = errors.New("events insert failed")

	_, err := f.svc.RemoveProjectAlias(context.Background(), "p1", "букинист")
	if err == nil {
		t.Fatal("want error on event insert failure")
	}

	if n := len(f.broker.notifiedEvents()); n != 0 {
		t.Fatalf("broker.Notify called %d times, want 0 (rollback)", n)
	}
}

func TestProjectServiceRemoveAliasHappyPath(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{"букинист"}},
	}

	p, err := f.svc.RemoveProjectAlias(context.Background(), "p1", "букинист")
	if err != nil {
		t.Fatalf("RemoveProjectAlias: %v", err)
	}
	if len(p.Aliases) != 0 {
		t.Fatalf("Aliases=%v, want empty", p.Aliases)
	}

	events := f.events.recorded()
	if len(events) != 1 || events[0].Kind != model.EventProjectUpdated {
		t.Fatalf("want 1 project_updated event, got %+v", events)
	}
}

func TestProjectServiceRemoveAliasNotFound(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.defaultPID = "inbox-id"
	f.tasks.projects = []model.Project{
		{ID: "p1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{}},
	}

	_, err := f.svc.RemoveProjectAlias(context.Background(), "p1", "noexist")
	if !errors.Is(err, usecase.ErrAliasNotFound) {
		t.Fatalf("want ErrAliasNotFound, got %v", err)
	}
}

func TestProjectServiceResolveProjectRef(t *testing.T) {
	f := newProjectFixture(t)
	f.tasks.projects = []model.Project{
		{ID: "uuid-1", Name: "Bookstore", Slug: "bookstore", Aliases: []string{"букинист"}},
		{ID: "uuid-2", Name: "Work", Slug: "work", Aliases: []string{"job"}},
	}

	tests := []struct {
		name    string
		ref     string
		wantID  string
		wantErr error
	}{
		{name: "by UUID", ref: "uuid-1", wantID: "uuid-1"},
		{name: "by slug", ref: "bookstore", wantID: "uuid-1"},
		{name: "by alias", ref: "букинист", wantID: "uuid-1"},
		{name: "by alias uppercase", ref: "Букинист", wantID: "uuid-1"},
		{name: "by alias another project", ref: "job", wantID: "uuid-2"},
		{name: "not found", ref: "nonexistent", wantErr: usecase.ErrProjectNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := f.svc.ResolveProjectRef(context.Background(), tc.ref)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.ID != tc.wantID {
				t.Fatalf("ID=%q, want %q", p.ID, tc.wantID)
			}
		})
	}
}
