package handler_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/anadale/huskwoot/internal/handler"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// mockProjectService is a manual mock of model.ProjectService for tests.
type mockProjectService struct {
	ensureResult *model.Project
	ensureErr    error
	ensureCalls  []ensureCall

	listResult []model.Project
	listErr    error

	findResult *model.Project
	findErr    error
}

type ensureCall struct {
	channelID string
	name      string
}

func (m *mockProjectService) EnsureChannelProject(_ context.Context, channelID, name string) (*model.Project, error) {
	m.ensureCalls = append(m.ensureCalls, ensureCall{channelID: channelID, name: name})
	return m.ensureResult, m.ensureErr
}

func (m *mockProjectService) CreateProject(_ context.Context, req model.CreateProjectRequest) (*model.Project, error) {
	return &model.Project{Name: req.Name, Slug: req.Slug}, nil
}

func (m *mockProjectService) UpdateProject(_ context.Context, _ string, _ model.ProjectUpdate) (*model.Project, error) {
	return nil, nil
}

func (m *mockProjectService) ListProjects(_ context.Context) ([]model.Project, error) {
	return m.listResult, m.listErr
}

func (m *mockProjectService) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return m.findResult, m.findErr
}

func (m *mockProjectService) ResolveProjectForChannel(_ context.Context, _ string) (string, error) {
	return "", nil
}

func newTestLocalizer(t *testing.T, lang string) *goI18n.Localizer {
	t.Helper()
	bundle, err := huskwootI18n.NewBundle(lang)
	if err != nil {
		t.Fatalf("NewBundle(%q): %v", lang, err)
	}
	return huskwootI18n.NewLocalizer(bundle, lang)
}

func TestSetProjectHandler_Localization_Russian(t *testing.T) {
	svc := &mockProjectService{
		ensureResult: &model.Project{ID: "uuid-ru", Name: "Мой проект", Slug: "moy-proekt"},
	}
	loc := newTestLocalizer(t, "ru")

	var gotReply string
	cmd := model.Command{
		Type:    "set_project_name",
		Payload: map[string]string{"name": "Мой проект"},
		Source:  model.Source{ID: "-100"},
		SourceMessage: model.Message{
			ReplyFn: func(_ context.Context, text string) error {
				gotReply = text
				return nil
			},
		},
	}

	h := handler.NewSetProjectHandler(svc, loc)
	if err := h.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotReply, "Чат привязан к проекту") {
		t.Errorf("Russian reply missing expected phrase, got: %q", gotReply)
	}
}

func TestSetProjectHandler_Localization_English(t *testing.T) {
	svc := &mockProjectService{
		ensureResult: &model.Project{ID: "uuid-en", Name: "My project", Slug: "my-project"},
	}
	loc := newTestLocalizer(t, "en")

	var gotReply string
	cmd := model.Command{
		Type:    "set_project_name",
		Payload: map[string]string{"name": "My project"},
		Source:  model.Source{ID: "-200"},
		SourceMessage: model.Message{
			ReplyFn: func(_ context.Context, text string) error {
				gotReply = text
				return nil
			},
		},
	}

	h := handler.NewSetProjectHandler(svc, loc)
	if err := h.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotReply, "Chat linked to project") {
		t.Errorf("English reply missing expected phrase, got: %q", gotReply)
	}
}

func TestSetProjectHandler_Name(t *testing.T) {
	loc := newTestLocalizer(t, "ru")
	h := handler.NewSetProjectHandler(&mockProjectService{}, loc)
	if h.Name() == "" {
		t.Error("Name() must not return empty string")
	}
}

func TestSetProjectHandler_Handle_FindsExistingProject(t *testing.T) {
	svc := &mockProjectService{
		ensureResult: &model.Project{ID: "uuid-7", Name: "Основной проект", Slug: "osnovnoy-proekt"},
	}
	loc := newTestLocalizer(t, "ru")

	var gotReply string
	cmd := model.Command{
		Type:    "set_project_name",
		Payload: map[string]string{"name": "Основной проект"},
		Source:  model.Source{ID: "-100123456"},
		SourceMessage: model.Message{
			ReplyFn: func(_ context.Context, text string) error {
				gotReply = text
				return nil
			},
		},
	}

	h := handler.NewSetProjectHandler(svc, loc)
	err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(svc.ensureCalls) == 0 {
		t.Fatal("EnsureChannelProject was not called")
	}
	got := svc.ensureCalls[0]
	if got.channelID != "-100123456" {
		t.Errorf("channelID: want %q, got %q", "-100123456", got.channelID)
	}
	if got.name != "Основной проект" {
		t.Errorf("name: want %q, got %q", "Основной проект", got.name)
	}

	if !strings.Contains(gotReply, "Основной проект") || !strings.Contains(gotReply, "osnovnoy-proekt") {
		t.Errorf("reply missing project info, got: %q", gotReply)
	}
}

func TestSetProjectHandler_Handle_CreatesNewProject(t *testing.T) {
	svc := &mockProjectService{
		ensureResult: &model.Project{ID: "uuid-new", Name: "Новый проект", Slug: "novyy-proekt"},
	}
	loc := newTestLocalizer(t, "ru")

	var gotReply string
	cmd := model.Command{
		Type:    "set_project_name",
		Payload: map[string]string{"name": "Новый проект"},
		Source:  model.Source{ID: "-777"},
		SourceMessage: model.Message{
			ReplyFn: func(_ context.Context, text string) error {
				gotReply = text
				return nil
			},
		},
	}

	h := handler.NewSetProjectHandler(svc, loc)
	err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(svc.ensureCalls) == 0 {
		t.Fatal("EnsureChannelProject was not called")
	}
	if svc.ensureCalls[0].channelID != "-777" {
		t.Errorf("channelID: want %q, got %q", "-777", svc.ensureCalls[0].channelID)
	}

	if !strings.Contains(gotReply, "Новый проект") {
		t.Errorf("reply does not contain project name: %q", gotReply)
	}
}

func TestSetProjectHandler_Handle(t *testing.T) {
	tests := []struct {
		name         string
		cmd          model.Command
		ensureResult *model.Project
		ensureErr    error
		replyErr     error
		replyFnNil   bool
		wantErr      bool
		wantNoEnsure bool
	}{
		{
			name: "ReplyFn = nil — no panic",
			cmd: model.Command{
				Type:          "set_project_name",
				Payload:       map[string]string{"name": "Test"},
				Source:        model.Source{ID: "-999"},
				SourceMessage: model.Message{ReplyFn: nil},
			},
			ensureResult: &model.Project{ID: "uuid-1", Name: "Test", Slug: "test"},
			replyFnNil:   true,
		},
		{
			name: "EnsureChannelProject error is returned",
			cmd: model.Command{
				Type:          "set_project_name",
				Payload:       map[string]string{"name": "Project"},
				Source:        model.Source{ID: "-1"},
				SourceMessage: model.Message{},
			},
			ensureErr:  errors.New("bind error"),
			replyFnNil: true,
			wantErr:    true,
		},
		{
			name: "ReplyFn error is returned",
			cmd: model.Command{
				Type:    "set_project_name",
				Payload: map[string]string{"name": "Project"},
				Source:  model.Source{ID: "-4"},
			},
			ensureResult: &model.Project{ID: "uuid-2", Name: "Project", Slug: "project"},
			replyErr:     errors.New("send error"),
			wantErr:      true,
		},
		{
			name: "unknown command type is ignored",
			cmd: model.Command{
				Type:          "unknown",
				Payload:       map[string]string{"name": "Should not be saved"},
				Source:        model.Source{ID: "-5"},
				SourceMessage: model.Message{},
			},
			replyFnNil:   true,
			wantNoEnsure: true,
		},
		{
			name: "empty project name is ignored",
			cmd: model.Command{
				Type:          "set_project_name",
				Payload:       map[string]string{"name": ""},
				Source:        model.Source{ID: "-6"},
				SourceMessage: model.Message{},
			},
			replyFnNil:   true,
			wantNoEnsure: true,
		},
	}

	loc := newTestLocalizer(t, "ru")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &mockProjectService{
				ensureResult: tt.ensureResult,
				ensureErr:    tt.ensureErr,
			}

			if !tt.replyFnNil {
				replyErr := tt.replyErr
				tt.cmd.SourceMessage.ReplyFn = func(_ context.Context, _ string) error {
					return replyErr
				}
			}

			h := handler.NewSetProjectHandler(svc, loc)
			err := h.Handle(context.Background(), tt.cmd)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNoEnsure {
				if len(svc.ensureCalls) != 0 {
					t.Errorf("EnsureChannelProject should not have been called, but was called %d time(s)", len(svc.ensureCalls))
				}
			}
		})
	}
}
