package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/events"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
	"github.com/sashabaranov/go-openai"
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
		if upd.Color != nil {
			m.projects[i].Color = *upd.Color
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
func (m *mockTaskStoreForProjects) DeleteProjectTx(_ context.Context, _ *sql.Tx, projectID, _ string) ([]string, error) {
	for i := range m.projects {
		if m.projects[i].ID == projectID {
			m.projects = append(m.projects[:i], m.projects[i+1:]...)
			if m.findByName != nil && m.findByName.ID == projectID {
				m.findByName = nil
			}
			return nil, nil
		}
	}
	return nil, fmt.Errorf("deleting project %q: %w", projectID, sql.ErrNoRows)
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
func (m *mockTaskStoreForProjects) ListClosedOlderThan(_ context.Context, _ time.Time) ([]model.Task, error) {
	return nil, nil
}
func (m *mockTaskStoreForProjects) DeleteTaskTx(_ context.Context, _ *sql.Tx, _ string) error {
	return nil
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
func (m *mockMetaStoreForProjects) DeleteProjectBindingsTx(_ context.Context, _ *sql.Tx, projectID string) error {
	if m.setErr != nil {
		return m.setErr
	}
	for key, value := range m.data {
		if strings.HasPrefix(key, "project:") && value == projectID {
			delete(m.data, key)
		}
	}
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
		DB:        f.db,
		Tasks:     f.tasks,
		AutoTasks: storage.NewSQLiteAutoTaskStore(f.db),
		Meta:      f.meta,
		Events:    f.events,
		Devices:   f.devices,
		Queue:     f.queue,
		Broker:    f.broker,
	})
	return f
}

// --- tests ---

func TestProjectServiceCreateProjectColor(t *testing.T) {
	for _, color := range []string{"", "blue", "purple", "teal", "orange", "pink"} {
		t.Run("slot_"+color, func(t *testing.T) {
			f := newProjectFixture(t)
			p, err := f.svc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Palette", Color: color})
			if err != nil || p == nil || p.Color != color {
				t.Fatalf("CreateProject: project=%+v err=%v want color=%q", p, err, color)
			}
			events := f.events.recorded()
			if len(events) != 1 || events[0].Kind != model.EventProjectCreated {
				t.Fatalf("created events=%+v", events)
			}
			var payload map[string]any
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if value, ok := payload["color"]; !ok || value != color {
				t.Fatalf("event color=%v present=%v want=%q", value, ok, color)
			}
		})
	}
}

func TestProjectServiceRejectsInvalidColorWithoutWrites(t *testing.T) {
	for _, color := range []string{"Blue", " blue", "blue ", "#007AFF", "green", "auto"} {
		for _, operation := range []string{"create", "update"} {
			t.Run(operation+"_"+color, func(t *testing.T) {
				f := newProjectFixture(t)
				f.tasks.projects = []model.Project{{ID: "p1", Name: "Palette", Color: "pink"}}
				var err error
				if operation == "create" {
					_, err = f.svc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "New", Color: color})
				} else {
					_, err = f.svc.UpdateProject(context.Background(), "p1", model.ProjectUpdate{Color: &color})
				}
				if !errors.Is(err, usecase.ErrColorInvalid) {
					t.Fatalf("error=%v want ErrColorInvalid", err)
				}
				if f.tasks.createCalled != 0 || f.tasks.updateCalled != 0 || len(f.events.recorded()) != 0 || len(f.broker.notifiedEvents()) != 0 || len(f.queue.snapshot()) != 0 {
					t.Fatal("invalid color must not write or publish")
				}
			})
		}
	}
}

func TestProjectServiceColorUpdatesAndChangedFieldsOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		color      *string
		allFields  bool
		wantColor  string
		wantFields []string
	}{
		{name: "absent preserves", wantColor: "purple", wantFields: []string{"description"}},
		{name: "color only", color: new("teal"), wantColor: "teal", wantFields: []string{"color"}},
		{name: "empty resets", color: new(""), wantColor: "", wantFields: []string{"color"}},
		{name: "all fields ordered", color: new("orange"), allFields: true, wantColor: "orange", wantFields: []string{"name", "description", "slug", "color", "aliases"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProjectFixture(t)
			f.tasks.projects = []model.Project{{ID: "p1", Name: "Palette", Slug: "palette", Color: "purple"}}
			upd := model.ProjectUpdate{Color: tc.color}
			if tc.color == nil || tc.allFields {
				upd.Description = new("Updated description")
			}
			if tc.allFields {
				upd.Name = new("Renamed")
				upd.Slug = new("renamed")
				aliases := []string{"palette-alias"}
				upd.Aliases = &aliases
			}
			p, err := f.svc.UpdateProject(context.Background(), "p1", upd)
			if err != nil || p == nil || p.Color != tc.wantColor {
				t.Fatalf("UpdateProject: project=%+v err=%v want color=%q", p, err, tc.wantColor)
			}
			events := f.events.recorded()
			if len(events) != 1 || events[0].Kind != model.EventProjectUpdated {
				t.Fatalf("updated events=%+v", events)
			}
			var payload struct {
				Project       map[string]any `json:"project"`
				ChangedFields []string       `json:"changedFields"`
			}
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if value, ok := payload.Project["color"]; !ok || value != tc.wantColor {
				t.Fatalf("event color=%v present=%v want=%q", value, ok, tc.wantColor)
			}
			if !reflect.DeepEqual(payload.ChangedFields, tc.wantFields) {
				t.Fatalf("changedFields=%v want=%v", payload.ChangedFields, tc.wantFields)
			}
			notified := f.broker.notifiedEvents()
			if len(notified) != 1 || string(notified[0].Payload) != string(events[0].Payload) {
				t.Fatal("broker must receive the recorded color event")
			}
		})
	}
}

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
	f.tasks.projects = []model.Project{*existing}

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
	f.tasks.projects = []model.Project{{ID: "mapped-uuid", Name: "Mapped", Slug: "mapped"}}

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

func TestProjectServiceInboxProtection(t *testing.T) {
	for _, upd := range []model.ProjectUpdate{
		{Name: new("Inbox")}, {Name: new("")}, {Slug: new("inbox")}, {Slug: new("")},
		{Name: new("Other"), Color: new("teal"), Description: new("changed")},
	} {
		f := newProjectFixture(t)
		f.tasks.defaultPID = "inbox"
		f.tasks.projects = []model.Project{{ID: "inbox", Name: "Inbox", Slug: "inbox"}}
		_, err := f.svc.UpdateProject(context.Background(), "inbox", upd)
		if !errors.Is(err, usecase.ErrInboxProtected) {
			t.Fatalf("update=%+v error=%v want ErrInboxProtected", upd, err)
		}
		if f.tasks.updateCalled != 0 || len(f.events.recorded()) != 0 || len(f.broker.notifiedEvents()) != 0 {
			t.Fatal("protected update must not write or publish")
		}
	}
}

func TestProjectServiceGeneratedSlugs(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(fmt.Sprintf("channel_%v", channel), func(t *testing.T) {
			db, err := storage.OpenDB(filepath.Join(t.TempDir(), "projects.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store, err := storage.NewSQLiteTaskStore(db)
			if err != nil {
				t.Fatal(err)
			}
			svc := usecase.NewProjectService(usecase.ProjectServiceDeps{DB: db, Tasks: store, AutoTasks: storage.NewSQLiteAutoTaskStore(db), Meta: storage.NewSQLiteMetaStore(db)})
			ctx := context.Background()
			create := func(name string) *model.Project {
				t.Helper()
				var p *model.Project
				var err error
				if channel {
					p, err = svc.EnsureChannelProject(ctx, name, name)
				} else {
					p, err = svc.CreateProject(ctx, model.CreateProjectRequest{Name: name})
				}
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			first := create("Работа")
			renamed, err := svc.UpdateProject(ctx, first.ID, model.ProjectUpdate{Name: new("Дом")})
			if err != nil || renamed.Slug != "rabota" {
				t.Fatalf("rename=%+v err=%v", renamed, err)
			}
			if got := create("Работа").Slug; got != "rabota-2" {
				t.Fatalf("recreated slug=%q", got)
			}
			for i, name := range []string{"X", "X!", "X?"} {
				want := []string{"x", "x-2", "x-3"}[i]
				if got := create(name).Slug; got != want {
					t.Fatalf("name=%q slug=%q want=%q", name, got, want)
				}
			}
			if _, err := svc.CreateProject(ctx, model.CreateProjectRequest{Name: "Explicit", Slug: "x"}); err == nil {
				t.Fatal("explicit occupied slug must fail")
			}
		})
	}
}

// singleAttemptProjectStore bounds retries so a regression fails without hanging.
type singleAttemptProjectStore struct {
	model.TaskStore
	attempts int
	firstErr error
	terminal error
	cancel   context.CancelFunc
}

func (s *singleAttemptProjectStore) CreateProjectTx(ctx context.Context, tx *sql.Tx, p *model.Project) error {
	s.attempts++
	if s.attempts > 1 {
		return s.terminal
	}
	s.firstErr = s.TaskStore.CreateProjectTx(ctx, tx, p)
	if s.cancel != nil {
		s.cancel()
	}
	return s.firstErr
}

func TestProjectServiceCreateNameConflictDoesNotRetrySlug(t *testing.T) {
	for _, cancelAfterConflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_after_conflict_%v", cancelAfterConflict), func(t *testing.T) {
			db, err := storage.OpenDB(filepath.Join(t.TempDir(), "projects.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store, err := storage.NewSQLiteTaskStore(db)
			if err != nil {
				t.Fatal(err)
			}
			svc := usecase.NewProjectService(usecase.ProjectServiceDeps{DB: db, Tasks: store, AutoTasks: storage.NewSQLiteAutoTaskStore(db)})
			const name = "UNIQUE constraint failed: projects.slug"
			// A distinct explicit slug ensures the second creation conflicts only on name.
			if _, err := svc.CreateProject(context.Background(), model.CreateProjectRequest{
				Name: name,
				Slug: "existing-project",
			}); err != nil {
				t.Fatalf("seed project: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			terminal := errors.New("unexpected project insertion retry")
			bounded := &singleAttemptProjectStore{TaskStore: store, terminal: terminal}
			if cancelAfterConflict {
				bounded.cancel = cancel
			}
			svc = usecase.NewProjectService(usecase.ProjectServiceDeps{DB: db, Tasks: bounded, AutoTasks: storage.NewSQLiteAutoTaskStore(db)})
			p, err := svc.CreateProject(ctx, model.CreateProjectRequest{Name: name})

			if bounded.firstErr == nil || !strings.Contains(bounded.firstErr.Error(), "UNIQUE constraint failed: projects.name") {
				t.Fatalf("first insertion error=%v, want SQLite project name conflict", bounded.firstErr)
			}
			if bounded.attempts != 1 {
				t.Errorf("CreateProjectTx called %d times, want 1 for a name conflict", bounded.attempts)
			}
			if p != nil {
				t.Errorf("CreateProject returned project %+v on name conflict", p)
			}
			if errors.Is(err, terminal) {
				t.Errorf("CreateProject retried instead of returning the name conflict: %v", err)
			}
			if !errors.Is(err, bounded.firstErr) {
				t.Errorf("CreateProject error=%v, want original name conflict %v", err, bounded.firstErr)
			}
		})
	}
}

func TestUpdateProjectToolInboxProtectedLocalized(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{
		{"en", "Inbox name and slug cannot be changed; you can still edit its description"},
		{"ru", "Название и slug проекта Inbox нельзя изменить; описание по-прежнему можно редактировать"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			db, err := storage.OpenDB(filepath.Join(t.TempDir(), "agent-projects.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store, err := storage.NewSQLiteTaskStore(db)
			if err != nil {
				t.Fatal(err)
			}
			svc := usecase.NewProjectService(usecase.ProjectServiceDeps{DB: db, Tasks: store, AutoTasks: storage.NewSQLiteAutoTaskStore(db)})
			bundle, err := huskwootI18n.NewBundle(tc.lang)
			if err != nil {
				t.Fatal(err)
			}
			tool := agent.NewUpdateProjectTool(svc, huskwootI18n.NewLocalizer(bundle, tc.lang))
			for _, field := range []string{"name", "slug"} {
				args, err := json.Marshal(map[string]string{"ref": store.DefaultProjectID(), field: "changed"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = tool.Execute(context.Background(), string(args))
				if err == nil || err.Error() != tc.want {
					t.Fatalf("field=%s error=%v want=%q", field, err, tc.want)
				}
			}
		})
	}
}

type projectDeletionHarness struct {
	db     *sql.DB
	raw    *storage.SQLiteTaskStore
	tasks  *storage.CachedTaskStore
	meta   *storage.SQLiteMetaStore
	auto   *storage.SQLiteAutoTaskStore
	events *events.SQLiteEventStore
	broker *mockBroker
	svc    model.ProjectService
	manual model.TaskService
}

func newProjectDeletionHarness(t *testing.T) *projectDeletionHarness {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "deletion.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	h := &projectDeletionHarness{db: db, raw: raw, tasks: storage.NewCachedTaskStore(raw), meta: storage.NewSQLiteMetaStore(db), auto: storage.NewSQLiteAutoTaskStore(db), events: events.NewSQLiteEventStore(db), broker: newMockBroker()}
	h.svc = usecase.NewProjectService(h.deps())
	h.manual = usecase.NewTaskService(usecase.TaskServiceDeps{DB: db, Tasks: h.tasks, Events: h.events, Devices: &mockDeviceStore{}, Broker: h.broker})
	return h
}

func (h *projectDeletionHarness) deps() usecase.ProjectServiceDeps {
	return usecase.ProjectServiceDeps{DB: h.db, Tasks: h.tasks, Meta: h.meta, AutoTasks: h.auto, Events: h.events, Devices: &mockDeviceStore{}, Broker: h.broker}
}

func TestProjectServiceDeleteMovesAllStatusesInOrderAndPublishesAfterCommit(t *testing.T) {
	h := newProjectDeletionHarness(t)
	ctx := context.Background()
	p, err := h.svc.EnsureChannelProject(ctx, "deleted-channel", "Deleted")
	if err != nil {
		t.Fatal(err)
	}
	q, err := h.svc.EnsureChannelProject(ctx, "retained-channel", "Retained")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.AddProjectAlias(ctx, p.ID, "alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{Summary: "Existing Inbox"}); err != nil {
		t.Fatal(err)
	}
	var original []model.Task
	for _, status := range []string{"cancelled", "open", "done"} {
		task, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: p.ID, Summary: status, Details: "Preserved", Priority: 2})
		if err != nil {
			t.Fatal(err)
		}
		if status != "open" {
			task, err = h.manual.UpdateTask(ctx, task.ID, model.TaskUpdate{Status: &status})
			if err != nil {
				t.Fatal(err)
			}
		}
		original = append(original, *task)
	}
	if _, err := h.tasks.ListProjects(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := h.events.MaxSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	notifiedBefore := len(h.broker.notifiedEvents())
	moved, err := h.svc.DeleteProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != len(original) {
		t.Fatalf("moved=%+v", moved)
	}
	evs, err := h.events.SinceSeq(ctx, before, 0)
	if err != nil || len(evs) != len(original)+1 {
		t.Fatalf("events=%+v err=%v", evs, err)
	}
	notified := h.broker.notifiedEvents()[notifiedBefore:]
	if !reflect.DeepEqual(evs, notified) {
		// Insert assigns CreatedAt in SQL; Notify only needs the same ordered identity and payload.
		for i, ev := range evs {
			if i >= len(notified) || notified[i].Seq != ev.Seq || notified[i].Kind != ev.Kind || !reflect.DeepEqual(notified[i].Payload, ev.Payload) {
				t.Fatalf("notify=%+v durable=%+v", notified, evs)
			}
		}
		if len(notified) != len(evs) {
			t.Fatalf("notify count=%d", len(notified))
		}
	}
	for i, task := range moved {
		if task.ID != original[i].ID || task.ProjectID != h.raw.DefaultProjectID() || task.Number != i+2 || task.Status != original[i].Status || task.Details != original[i].Details || task.Priority != original[i].Priority || !reflect.DeepEqual(task.ClosedAt, original[i].ClosedAt) {
			t.Fatalf("moved[%d]=%+v original=%+v", i, task, original[i])
		}
		var payload struct {
			Task struct {
				ID        string `json:"id"`
				ProjectID string `json:"project_id"`
				Number    int    `json:"number"`
				Status    string `json:"status"`
			} `json:"task"`
		}
		if err := json.Unmarshal(evs[i].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if evs[i].Kind != model.EventTaskMoved || evs[i].EntityID != task.ID || evs[i].Seq != before+int64(i)+1 || payload.Task.ID != task.ID || payload.Task.ProjectID != task.ProjectID || payload.Task.Number != task.Number || payload.Task.Status != task.Status {
			t.Fatalf("event=%+v payload=%+v", evs[i], payload)
		}
	}
	last := evs[len(evs)-1]
	var deleted map[string]string
	if err := json.Unmarshal(last.Payload, &deleted); err != nil {
		t.Fatal(err)
	}
	if last.Kind != model.EventProjectDeleted || last.EntityID != p.ID || len(deleted) != 1 || deleted["id"] != p.ID {
		t.Fatalf("deleted=%+v payload=%v", last, deleted)
	}
	projects, err := h.tasks.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if project.ID == p.ID {
			t.Fatal("deleted project remains cached")
		}
	}
	if binding, err := h.meta.Get(ctx, "project:deleted-channel"); err != nil || binding != "" {
		t.Fatalf("deleted binding=%q err=%v", binding, err)
	}
	if binding, err := h.meta.Get(ctx, "project:retained-channel"); err != nil || binding != q.ID {
		t.Fatalf("retained binding=%q err=%v", binding, err)
	}
	if aliases, err := h.raw.ListAliasesForProject(ctx, p.ID); err != nil || len(aliases) != 0 {
		t.Fatalf("aliases=%v err=%v", aliases, err)
	}
}

func TestProjectServiceDeleteEmptyMissingAndInbox(t *testing.T) {
	h := newProjectDeletionHarness(t)
	ctx := context.Background()
	p, err := h.svc.CreateProject(ctx, model.CreateProjectRequest{Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := h.svc.DeleteProject(ctx, p.ID)
	if err != nil || len(moved) != 0 {
		t.Fatalf("empty deletion=%v err=%v", moved, err)
	}
	before, _ := h.events.MaxSeq(ctx)
	for _, tc := range []struct {
		id   string
		want error
	}{{p.ID, usecase.ErrProjectNotFound}, {"missing", usecase.ErrProjectNotFound}, {h.raw.DefaultProjectID(), usecase.ErrInboxProtected}} {
		if _, err := h.svc.DeleteProject(ctx, tc.id); !errors.Is(err, tc.want) {
			t.Fatalf("id=%q error=%v want=%v", tc.id, err, tc.want)
		}
	}
	after, _ := h.events.MaxSeq(ctx)
	if before != after {
		t.Fatalf("failed deletions emitted events: %d -> %d", before, after)
	}
}

type deletionTaskFailure struct {
	model.TaskStore
	stage         string
	err           error
	invalidations int
}

func (s *deletionTaskFailure) DeleteProjectTx(ctx context.Context, tx *sql.Tx, id, inbox string) ([]string, error) {
	ids, err := s.TaskStore.DeleteProjectTx(ctx, tx, id, inbox)
	if err == nil && s.stage == "delete" {
		return nil, s.err
	}
	return ids, err
}
func (s *deletionTaskFailure) GetTaskTx(ctx context.Context, tx *sql.Tx, id string) (*model.Task, error) {
	if s.stage == "snapshot" {
		return nil, s.err
	}
	if s.stage == "nil_snapshot" {
		return nil, nil
	}
	return s.TaskStore.GetTaskTx(ctx, tx, id)
}
func (s *deletionTaskFailure) Invalidate() { s.invalidations++ }

type deletionAutoFailure struct {
	model.AutoTaskStore
	err error
}

func (s deletionAutoFailure) ReassignProjectTx(ctx context.Context, tx *sql.Tx, from, to string) error {
	if err := s.AutoTaskStore.ReassignProjectTx(ctx, tx, from, to); err != nil {
		return err
	}
	return s.err
}

type deletionMetaFailure struct {
	model.MetaStore
	err error
}

func (s deletionMetaFailure) DeleteProjectBindingsTx(ctx context.Context, tx *sql.Tx, id string) error {
	if err := s.MetaStore.DeleteProjectBindingsTx(ctx, tx, id); err != nil {
		return err
	}
	return s.err
}

type deletionEventFailure struct {
	model.EventStore
	failKind model.EventKind
	err      error
	cancel   context.CancelFunc
}

func (s deletionEventFailure) Insert(ctx context.Context, tx *sql.Tx, ev model.Event) (int64, error) {
	seq, err := s.EventStore.Insert(ctx, tx, ev)
	if err != nil {
		return 0, err
	}
	if ev.Kind == s.failKind {
		if s.cancel != nil {
			s.cancel()
		} else {
			return 0, s.err
		}
	}
	return seq, nil
}

func TestProjectServiceDeleteRollsBackEveryMutationOnFailure(t *testing.T) {
	for _, stage := range []string{"candidates", "bindings", "delete", "snapshot", "nil_snapshot", "task_event", "project_event", "push", "commit"} {
		t.Run(stage, func(t *testing.T) {
			h := newProjectDeletionHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p, err := h.svc.EnsureChannelProject(ctx, "channel", "Rollback")
			if err != nil {
				t.Fatal(err)
			}
			task, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: p.ID, Summary: "Retained"})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := h.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			candidate := model.TaskCandidate{ID: "candidate", Status: "pending", Task: model.CreateTaskRequest{ProjectID: p.ID, Summary: "Pending"}, CreatedAt: time.Now()}
			if err := h.auto.InsertCandidateTx(ctx, tx, candidate); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			before, _ := h.events.MaxSeq(ctx)
			notifiedBefore := len(h.broker.notifiedEvents())
			failure := errors.New("injected deletion failure")
			store := &deletionTaskFailure{TaskStore: h.tasks, stage: stage, err: failure}
			deps := h.deps()
			deps.Tasks = store
			switch stage {
			case "candidates":
				deps.AutoTasks = deletionAutoFailure{AutoTaskStore: h.auto, err: failure}
			case "bindings":
				deps.Meta = deletionMetaFailure{MetaStore: h.meta, err: failure}
			case "task_event":
				deps.Events = deletionEventFailure{EventStore: h.events, failKind: model.EventTaskMoved, err: failure}
			case "project_event":
				deps.Events = deletionEventFailure{EventStore: h.events, failKind: model.EventProjectDeleted, err: failure}
			case "commit":
				deps.Events = deletionEventFailure{EventStore: h.events, failKind: model.EventProjectDeleted, cancel: cancel}
			case "push":
				deps.Devices = &mockDeviceStore{activeIDs: []string{"offline"}}
				deps.Queue = &mockPushQueue{enqueueErr: failure}
			}
			if _, err := usecase.NewProjectService(deps).DeleteProject(ctx, p.ID); err == nil {
				t.Fatal("deletion unexpectedly succeeded")
			}
			readCtx := context.Background()
			if project, err := h.raw.GetProject(readCtx, p.ID); err != nil || project == nil {
				t.Fatalf("project=%+v err=%v", project, err)
			}
			if got, err := h.raw.GetTask(readCtx, task.ID); err != nil || got == nil || got.ProjectID != p.ID || got.Number != task.Number {
				t.Fatalf("task=%+v err=%v", got, err)
			}
			if got, err := h.auto.GetCandidate(readCtx, candidate.ID); err != nil || got == nil || got.Task.ProjectID != p.ID {
				t.Fatalf("candidate=%+v err=%v", got, err)
			}
			if binding, err := h.meta.Get(readCtx, "project:channel"); err != nil || binding != p.ID {
				t.Fatalf("binding=%q err=%v", binding, err)
			}
			if after, err := h.events.MaxSeq(readCtx); err != nil || after != before {
				t.Fatalf("event seq=%d want=%d err=%v", after, before, err)
			}
			if store.invalidations != 0 || len(h.broker.notifiedEvents()) != notifiedBefore {
				t.Fatal("rollback invalidated cache or notified events")
			}
			next, err := h.manual.CreateTask(readCtx, model.CreateTaskRequest{Summary: "Counter rollback"})
			if err != nil || next.Number != 1 {
				t.Fatalf("Inbox counter=%+v err=%v", next, err)
			}
		})
	}
}

type channelProjectStore struct {
	model.TaskStore
	find  func(context.Context, string) (*model.Project, error)
	get   func(context.Context, string) (*model.Project, error)
	getTx func(context.Context, *sql.Tx, string) (*model.Project, error)
}

func (s channelProjectStore) FindProjectByName(ctx context.Context, name string) (*model.Project, error) {
	if s.find != nil {
		return s.find(ctx, name)
	}
	return s.TaskStore.FindProjectByName(ctx, name)
}
func (s channelProjectStore) GetProject(ctx context.Context, id string) (*model.Project, error) {
	if s.get != nil {
		return s.get(ctx, id)
	}
	return s.TaskStore.GetProject(ctx, id)
}
func (s channelProjectStore) GetProjectTx(ctx context.Context, tx *sql.Tx, id string) (*model.Project, error) {
	if s.getTx != nil {
		return s.getTx(ctx, tx, id)
	}
	return s.TaskStore.GetProjectTx(ctx, tx, id)
}

type channelBindingObserver struct {
	model.MetaStore
	writes int
}

func (s *channelBindingObserver) SetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	s.writes++
	return s.MetaStore.SetTx(ctx, tx, key, value)
}

func TestProjectServiceEnsureChannelProjectRechecksFoundProject(t *testing.T) {
	for _, scenario := range []string{"missing", "error", "fresh"} {
		t.Run(scenario, func(t *testing.T) {
			h := newProjectDeletionHarness(t)
			ctx := context.Background()
			p, err := h.svc.CreateProject(ctx, model.CreateProjectRequest{Name: "Found"})
			if err != nil {
				t.Fatal(err)
			}
			readErr := errors.New("transactional project read failed")
			fresh := *p
			fresh.Name = "Fresh snapshot"
			deps := h.deps()
			meta := &channelBindingObserver{MetaStore: h.meta}
			deps.Meta = meta
			deps.Tasks = channelProjectStore{TaskStore: h.tasks, getTx: func(context.Context, *sql.Tx, string) (*model.Project, error) {
				switch scenario {
				case "missing":
					return nil, nil
				case "error":
					return nil, readErr
				default:
					return &fresh, nil
				}
			}}
			before, _ := h.events.MaxSeq(ctx)
			notifiedBefore := len(h.broker.notifiedEvents())
			got, err := usecase.NewProjectService(deps).EnsureChannelProject(ctx, "channel", p.Name)
			switch scenario {
			case "missing":
				if got != nil || !errors.Is(err, usecase.ErrProjectNotFound) {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			case "error":
				if got != nil || !errors.Is(err, readErr) {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			case "fresh":
				if err != nil || !reflect.DeepEqual(got, &fresh) {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			}
			binding, bindingErr := h.meta.Get(ctx, "project:channel")
			want := ""
			if scenario == "fresh" {
				want = p.ID
			}
			if bindingErr != nil || binding != want {
				t.Fatalf("binding=%q err=%v want=%q", binding, bindingErr, want)
			}
			wantWrites := 0
			if scenario == "fresh" {
				wantWrites = 1
			}
			if meta.writes != wantWrites {
				t.Fatalf("SetTx calls=%d want=%d", meta.writes, wantWrites)
			}
			projects, err := h.raw.ListProjects(ctx)
			if err != nil || len(projects) != 2 {
				t.Fatalf("projects=%+v err=%v", projects, err)
			}
			if after, _ := h.events.MaxSeq(ctx); after != before || len(h.broker.notifiedEvents()) != notifiedBefore {
				t.Fatal("existing-project bind emitted an event")
			}
		})
	}
}

func TestProjectServiceEnsureChannelProjectSearchDeleteBindRace(t *testing.T) {
	h := newProjectDeletionHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := h.svc.CreateProject(ctx, model.CreateProjectRequest{Name: "Race"})
	if err != nil {
		t.Fatal(err)
	}
	found, resume := make(chan struct{}), make(chan struct{})
	deps := h.deps()
	deps.Tasks = channelProjectStore{TaskStore: h.tasks, find: func(ctx context.Context, name string) (*model.Project, error) {
		project, err := h.raw.FindProjectByName(ctx, name)
		close(found)
		select {
		case <-resume:
			return project, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	type result struct {
		project *model.Project
		err     error
	}
	done := make(chan result, 1)
	go func() {
		project, err := usecase.NewProjectService(deps).EnsureChannelProject(ctx, "channel", p.Name)
		done <- result{project, err}
	}()
	select {
	case <-found:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := h.svc.DeleteProject(ctx, p.ID); err != nil {
		close(resume)
		t.Fatal(err)
	}
	before, _ := h.events.MaxSeq(ctx)
	notifiedBefore := len(h.broker.notifiedEvents())
	close(resume)
	select {
	case got := <-done:
		if got.project != nil || !errors.Is(got.err, usecase.ErrProjectNotFound) {
			t.Fatalf("bind=%+v err=%v", got.project, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if binding, err := h.meta.Get(ctx, "project:channel"); err != nil || binding != "" {
		t.Fatalf("binding=%q err=%v", binding, err)
	}
	if projects, err := h.raw.ListProjects(ctx); err != nil || len(projects) != 1 {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
	if after, _ := h.events.MaxSeq(ctx); before != after || len(h.broker.notifiedEvents()) != notifiedBefore {
		t.Fatal("failed bind published an event")
	}
}

func TestProjectServiceBindBeforeDeleteCleansBinding(t *testing.T) {
	h := newProjectDeletionHarness(t)
	ctx := context.Background()
	p, err := h.svc.CreateProject(ctx, model.CreateProjectRequest{Name: "Bound first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.EnsureChannelProject(ctx, "channel", p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if binding, err := h.meta.Get(ctx, "project:channel"); err != nil || binding != "" {
		t.Fatalf("binding=%q err=%v", binding, err)
	}
}

func setProjectBinding(t *testing.T, h *projectDeletionHarness, channelID, projectID string) {
	t.Helper()
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := h.meta.SetTx(context.Background(), tx, "project:"+channelID, projectID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectServiceResolveStaleBindingReadOnlyAndPropagatesErrors(t *testing.T) {
	for _, scenario := range []string{"missing", "error", "rebound"} {
		t.Run(scenario, func(t *testing.T) {
			h := newProjectDeletionHarness(t)
			ctx := context.Background()
			setProjectBinding(t, h, "channel", "deleted-project")
			readErr := errors.New("uncached project read failed")
			deps := h.deps()
			deps.Tasks = channelProjectStore{TaskStore: h.tasks, get: func(context.Context, string) (*model.Project, error) {
				if scenario == "error" {
					return nil, readErr
				}
				if scenario == "rebound" {
					setProjectBinding(t, h, "channel", h.raw.DefaultProjectID())
				}
				return nil, nil
			}}
			before, _ := h.events.MaxSeq(ctx)
			id, err := usecase.NewProjectService(deps).ResolveProjectForChannel(ctx, "channel")
			if scenario == "error" {
				if id != "" || !errors.Is(err, readErr) {
					t.Fatalf("id=%q err=%v", id, err)
				}
			} else if err != nil || id != h.raw.DefaultProjectID() {
				t.Fatalf("id=%q err=%v", id, err)
			}
			wantBinding := "deleted-project"
			if scenario == "rebound" {
				wantBinding = h.raw.DefaultProjectID()
			}
			if binding, err := h.meta.Get(ctx, "project:channel"); err != nil || binding != wantBinding {
				t.Fatalf("resolver mutated binding=%q err=%v", binding, err)
			}
			if after, _ := h.events.MaxSeq(ctx); after != before {
				t.Fatal("resolver wrote an event")
			}
		})
	}
}

type ordinaryProjectResolver struct {
	model.ProjectService
	calls     int
	channelID string
}

func (s *ordinaryProjectResolver) ResolveProjectForChannel(ctx context.Context, channelID string) (string, error) {
	s.calls++
	s.channelID = channelID
	return s.ProjectService.ResolveProjectForChannel(ctx, channelID)
}

type ordinaryCreateTaskAI struct {
	calls  int
	result string
}

func (a *ordinaryCreateTaskAI) CreateChatCompletion(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	a.calls++
	message := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "Created"}
	if a.calls == 1 {
		message.ToolCalls = []openai.ToolCall{{ID: "create", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "create_task", Arguments: `{"summary":"Ordinary channel task"}`}}}
	} else {
		a.result = req.Messages[len(req.Messages)-1].Content
	}
	return openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{Message: message}}}, nil
}

func TestOrdinaryAgentCreateTaskUsesRealStaleResolverToInbox(t *testing.T) {
	h := newProjectDeletionHarness(t)
	ctx := context.Background()
	p, err := h.svc.EnsureChannelProject(ctx, "channel", "Deleted")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	// A historical binding can outlive its project; the resolver must not write it.
	setProjectBinding(t, h, "channel", p.ID)
	bundle, err := huskwootI18n.NewBundle("en")
	if err != nil {
		t.Fatal(err)
	}
	client := &ordinaryCreateTaskAI{}
	resolver := &ordinaryProjectResolver{ProjectService: h.svc}
	tool := agent.NewCreateTaskTool(h.manual, resolver, nil, huskwootI18n.NewLocalizer(bundle, "en"))
	a, err := agent.New(client, []agent.Tool{tool}, agent.Config{Language: "en"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Handle(ctx, model.Message{Kind: model.MessageKindGroupDirect, Source: model.Source{Kind: "telegram", ID: "channel"}, Text: "Create an ordinary task"}); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || resolver.channelID != "channel" {
		t.Fatalf("real resolver calls=%d channel=%q", resolver.calls, resolver.channelID)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(client.result), &result); err != nil {
		t.Fatalf("tool result=%s err=%v", client.result, err)
	}
	task, err := h.raw.GetTask(ctx, result.ID)
	if err != nil || task == nil || task.ProjectID != h.raw.DefaultProjectID() || task.Summary != "Ordinary channel task" || task.Source.ID != "channel" {
		t.Fatalf("ordinary create_task=%+v err=%v result=%s", task, err, client.result)
	}
	if binding, err := h.meta.Get(ctx, "project:channel"); err != nil || binding != p.ID {
		t.Fatalf("resolver changed stale binding=%q err=%v", binding, err)
	}
}

type deletionCommitObserver struct {
	model.TaskStore
	invalidate func()
}

func (s deletionCommitObserver) Invalidate() { s.invalidate() }

type deletionNotifyObserver struct {
	model.Broker
	notify func(model.Event)
}

func (b deletionNotifyObserver) Notify(ev model.Event) { b.notify(ev) }

func TestProjectServiceDeleteInvalidatesAndNotifiesOnlyAfterCommit(t *testing.T) {
	h := newProjectDeletionHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := h.svc.CreateProject(ctx, model.CreateProjectRequest{Name: "Committed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: p.ID, Summary: "Moved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.tasks.ListProjects(ctx); err != nil {
		t.Fatal(err)
	}
	invalidated := false
	deps := h.deps()
	deps.Tasks = deletionCommitObserver{TaskStore: h.tasks, invalidate: func() {
		got, err := h.raw.GetProject(ctx, p.ID)
		if err != nil || got != nil {
			t.Errorf("invalidate before delete commit: project=%+v err=%v", got, err)
		}
		h.tasks.Invalidate()
		invalidated = true
	}}
	deps.Broker = deletionNotifyObserver{Broker: h.broker, notify: func(ev model.Event) {
		if !invalidated {
			t.Error("Notify happened before invalidation")
		}
		durable, err := h.events.GetBySeq(ctx, ev.Seq)
		if err != nil || durable == nil || durable.Kind != ev.Kind {
			t.Errorf("Notify before event commit: event=%+v err=%v", durable, err)
		}
		h.broker.Notify(ev)
	}}
	if _, err := usecase.NewProjectService(deps).DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if !invalidated {
		t.Fatal("project cache was not invalidated")
	}
}
