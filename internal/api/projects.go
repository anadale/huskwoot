package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anadale/huskwoot/internal/model"
)

// projectDTO is the public project snapshot in JSON responses. Fields and their names
// are kept in sync with the OpenAPI Project schema.
type projectDTO struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	TaskCounter int       `json:"taskCounter"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toProjectDTO(p *model.Project) projectDTO {
	return projectDTO{
		ID:          p.ID,
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		TaskCounter: p.TaskCounter,
		CreatedAt:   p.CreatedAt,
	}
}

// createProjectRequest is the request body for POST /v1/projects.
type createProjectRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug,omitempty"`
	Description string `json:"description,omitempty"`
}

// updateProjectRequest is the request body for PATCH /v1/projects/{id}. Omitted fields are
// left unchanged; an empty string is treated as an explicit clear (for Description).
type updateProjectRequest struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Description *string `json:"description,omitempty"`
}

type projectsHandler struct {
	service model.ProjectService
	logger  *slog.Logger
}

func (h *projectsHandler) list(w http.ResponseWriter, r *http.Request) {
	projects, err := h.service.ListProjects(r.Context())
	if err != nil {
		h.logError(r.Context(), "list projects", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve projects")
		return
	}
	out := make([]projectDTO, 0, len(projects))
	for i := range projects {
		out = append(out, toProjectDTO(&projects[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (h *projectsHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	projects, err := h.service.ListProjects(r.Context())
	if err != nil {
		h.logError(r.Context(), "list projects", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve projects")
		return
	}
	for i := range projects {
		if projects[i].ID == id {
			writeJSON(w, http.StatusOK, toProjectDTO(&projects[i]))
			return
		}
	}
	WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "project not found")
}

func (h *projectsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "name field is required")
		return
	}

	existing, err := h.service.FindProjectByName(r.Context(), req.Name)
	if err != nil {
		h.logError(r.Context(), "find project by name", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "internal server error")
		return
	}
	if existing != nil {
		WriteError(w, http.StatusConflict, ErrorCodeConflict, "project with this name already exists")
		return
	}

	p, err := h.service.CreateProject(r.Context(), model.CreateProjectRequest{
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
	})
	if err != nil {
		if isUniqueConstraintErr(err) {
			WriteError(w, http.StatusConflict, ErrorCodeConflict, "project with this name or slug already exists")
			return
		}
		h.logError(r.Context(), "create project", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to create project")
		return
	}
	writeJSON(w, http.StatusCreated, toProjectDTO(p))
}

func (h *projectsHandler) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateProjectRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	if req.Name == nil && req.Slug == nil && req.Description == nil {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "no fields provided")
		return
	}

	upd := model.ProjectUpdate{Name: req.Name, Slug: req.Slug, Description: req.Description}
	p, err := h.service.UpdateProject(r.Context(), id, upd)
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "project not found")
			return
		}
		if isUniqueConstraintErr(err) {
			WriteError(w, http.StatusConflict, ErrorCodeConflict, "project with this name or slug already exists")
			return
		}
		h.logError(r.Context(), "update project", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to update project")
		return
	}
	writeJSON(w, http.StatusOK, toProjectDTO(p))
}

func (h *projectsHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/projects: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}

// decodeJSON strictly parses the request body, disallowing unknown fields.
func decodeJSON(body io.Reader, dst any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("empty request body")
		}
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

// isUniqueConstraintErr detects a SQLite uniqueness error by substring matching.
// modernc.org/sqlite exposes no public sentinel, so we compare the message text.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// isNotFoundErr is a heuristic to distinguish "not found" errors from others.
// Error messages are produced in storage/task_store.go and usecase/projects.go.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found")
}
