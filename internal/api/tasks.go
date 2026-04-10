package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anadale/huskwoot/internal/model"
)

const (
	defaultTasksLimit = 50
	maxTasksLimit     = 500
)

// taskDTO is the public task snapshot in JSON responses. Fields and their names
// are kept in sync with the OpenAPI Task schema.
type taskDTO struct {
	ID          string     `json:"id"`
	Number      int        `json:"number"`
	Ref         string     `json:"ref"`
	ProjectID   string     `json:"projectId"`
	ProjectSlug string     `json:"projectSlug"`
	Summary     string     `json:"summary"`
	Details     string     `json:"details,omitempty"`
	Topic       string     `json:"topic,omitempty"`
	Status      string     `json:"status"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
}

func toTaskDTO(t *model.Task) taskDTO {
	return taskDTO{
		ID:          t.ID,
		Number:      t.Number,
		Ref:         httpTaskRef(t.ProjectSlug, t.Number),
		ProjectID:   t.ProjectID,
		ProjectSlug: t.ProjectSlug,
		Summary:     t.Summary,
		Details:     t.Details,
		Topic:       t.Topic,
		Status:      t.Status,
		Deadline:    t.Deadline,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		ClosedAt:    t.ClosedAt,
	}
}

// httpTaskRef builds a URL-safe reference in the form "<slug>-<number>" — the
// format accepted by /v1/tasks/by-ref/{ref} and documented in OpenAPI. For
// agent/bot UI messages, Task.DisplayID() is used with the '#' separator.
func httpTaskRef(slug string, number int) string {
	return slug + "-" + strconv.Itoa(number)
}

// createTaskRequest is the request body for POST /v1/tasks.
type createTaskRequest struct {
	ProjectID string     `json:"projectId,omitempty"`
	Summary   string     `json:"summary"`
	Details   string     `json:"details,omitempty"`
	Topic     string     `json:"topic,omitempty"`
	Deadline  *time.Time `json:"deadline,omitempty"`
}

// moveTaskRequest is the request body for POST /v1/tasks/{id}/move.
type moveTaskRequest struct {
	ProjectID string `json:"projectId"`
}

type tasksHandler struct {
	service model.TaskService
	logger  *slog.Logger
}

func (h *tasksHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	projectID := q.Get("project_id")
	status := q.Get("status")

	tasks, err := h.service.ListTasks(r.Context(), projectID, model.TaskFilter{Status: status})
	if err != nil {
		h.logError(r.Context(), "list tasks", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve tasks")
		return
	}

	if sinceStr := q.Get("since"); sinceStr != "" {
		since, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "since parameter is not in RFC3339 format")
			return
		}
		filtered := tasks[:0]
		for _, t := range tasks {
			if !t.UpdatedAt.Before(since) {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		if !tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
		}
		return tasks[i].ID > tasks[j].ID
	})

	if cursor := q.Get("cursor"); cursor != "" {
		ts, id, ok := decodeTasksCursor(cursor)
		if !ok {
			WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "invalid cursor")
			return
		}
		cut := sort.Search(len(tasks), func(i int) bool {
			t := tasks[i]
			if t.UpdatedAt.Before(ts) {
				return true
			}
			return t.UpdatedAt.Equal(ts) && t.ID < id
		})
		tasks = tasks[cut:]
	}

	limit := defaultTasksLimit
	if l := q.Get("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v <= 0 {
			WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "limit must be a positive number")
			return
		}
		if v > maxTasksLimit {
			v = maxTasksLimit
		}
		limit = v
	}

	var nextCursor string
	if len(tasks) > limit {
		last := tasks[limit-1]
		nextCursor = encodeTasksCursor(last.UpdatedAt, last.ID)
		tasks = tasks[:limit]
	}

	out := make([]taskDTO, 0, len(tasks))
	for i := range tasks {
		out = append(out, toTaskDTO(&tasks[i]))
	}
	resp := map[string]any{"tasks": out}
	if nextCursor != "" {
		resp["nextCursor"] = nextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *tasksHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.service.GetTask(r.Context(), id)
	if err != nil {
		h.logError(r.Context(), "get task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve task")
		return
	}
	if task == nil {
		WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

func (h *tasksHandler) getByRef(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	slug, number, ok := parseTaskRef(ref)
	if !ok {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "ref must be in the format <slug>-<number>")
		return
	}
	task, err := h.service.GetTaskByRef(r.Context(), slug, number)
	if err != nil {
		h.logError(r.Context(), "get task by ref", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve task")
		return
	}
	if task == nil {
		WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

func (h *tasksHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	req.Summary = strings.TrimSpace(req.Summary)
	if req.Summary == "" {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "summary field is required")
		return
	}

	task, err := h.service.CreateTask(r.Context(), model.CreateTaskRequest{
		ProjectID: req.ProjectID,
		Summary:   req.Summary,
		Details:   req.Details,
		Topic:     req.Topic,
		Deadline:  req.Deadline,
	})
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "project not found")
			return
		}
		h.logError(r.Context(), "create task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to create task")
		return
	}
	writeJSON(w, http.StatusCreated, toTaskDTO(task))
}

func (h *tasksHandler) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	raw, err := decodeRawMap(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	if len(raw) == 0 {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "no fields provided")
		return
	}
	upd, err := buildTaskUpdate(raw)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	task, err := h.service.UpdateTask(r.Context(), id, upd)
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task not found")
			return
		}
		h.logError(r.Context(), "update task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to update task")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

func (h *tasksHandler) complete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.service.CompleteTask(r.Context(), id)
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task not found")
			return
		}
		h.logError(r.Context(), "complete task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to complete task")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

func (h *tasksHandler) reopen(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.service.ReopenTask(r.Context(), id)
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task not found")
			return
		}
		h.logError(r.Context(), "reopen task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to reopen task")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

func (h *tasksHandler) move(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req moveTaskRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "projectId field is required")
		return
	}
	task, err := h.service.MoveTask(r.Context(), id, req.ProjectID)
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task or project not found")
			return
		}
		h.logError(r.Context(), "move task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to move task")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

// delete performs a soft-delete by setting the task status to "cancelled".
// Returns the updated task snapshot with 200 OK.
func (h *tasksHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cancelled := "cancelled"
	task, err := h.service.UpdateTask(r.Context(), id, model.TaskUpdate{Status: &cancelled})
	if err != nil {
		if isNotFoundErr(err) {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "task not found")
			return
		}
		h.logError(r.Context(), "delete task", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to cancel task")
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(task))
}

func (h *tasksHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/tasks: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}

// parseTaskRef parses a string of the form "<slug>-<number>" (e.g. "inbox-42").
// Slug may contain hyphens; the separator is the last hyphen.
func parseTaskRef(ref string) (slug string, number int, ok bool) {
	i := strings.LastIndex(ref, "-")
	if i <= 0 || i == len(ref)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(ref[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return ref[:i], n, true
}

// encodeTasksCursor encodes the pagination position: (updated_at, id).
// The cursor is opaque to the client.
func encodeTasksCursor(ts time.Time, id string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "\x00" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeTasksCursor(s string) (time.Time, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", false
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 {
		return time.Time{}, "", false
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}
	return ts, parts[1], true
}

func decodeRawMap(body io.Reader) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(body)
	m := map[string]json.RawMessage{}
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty request body")
		}
		return nil, errors.New("invalid JSON: " + err.Error())
	}
	return m, nil
}

var allowedTaskUpdateFields = map[string]bool{
	"details":  true,
	"status":   true,
	"deadline": true,
}

// buildTaskUpdate assembles a model.TaskUpdate from a raw JSON map, distinguishing
// "field absent" from "field=null" (the latter is used to clear deadline).
func buildTaskUpdate(raw map[string]json.RawMessage) (model.TaskUpdate, error) {
	for k := range raw {
		if !allowedTaskUpdateFields[k] {
			return model.TaskUpdate{}, errors.New("unknown field: " + k)
		}
	}
	var upd model.TaskUpdate
	if v, ok := raw["details"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return model.TaskUpdate{}, errors.New("details must be a string")
		}
		upd.Details = &s
	}
	if v, ok := raw["status"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return model.TaskUpdate{}, errors.New("status must be a string")
		}
		if s != "open" && s != "done" && s != "cancelled" {
			return model.TaskUpdate{}, errors.New("status must be open, done or cancelled")
		}
		upd.Status = &s
	}
	if v, ok := raw["deadline"]; ok {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			var nilTime *time.Time
			upd.Deadline = &nilTime
		} else {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return model.TaskUpdate{}, errors.New("deadline must be an ISO string or null")
			}
			ts, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return model.TaskUpdate{}, errors.New("deadline is not in RFC3339 format")
			}
			ptr := &ts
			upd.Deadline = &ptr
		}
	}
	return upd, nil
}
