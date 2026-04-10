package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/anadale/huskwoot/internal/model"
)

// snapshotResponse is the response body for GET /v1/sync/snapshot. Used by
// the client for a cold re-sync when the SSE stream has fallen behind the retention horizon.
type snapshotResponse struct {
	Projects  []projectDTO `json:"projects"`
	OpenTasks []taskDTO    `json:"openTasks"`
	LastSeq   int64        `json:"lastSeq"`
}

type syncHandler struct {
	projects model.ProjectService
	tasks    model.TaskService
	events   model.EventStore
	logger   *slog.Logger
}

func newSyncHandler(projects model.ProjectService, tasks model.TaskService, events model.EventStore, logger *slog.Logger) *syncHandler {
	return &syncHandler{projects: projects, tasks: tasks, events: events, logger: logger}
}

// snapshot returns a state snapshot. last_seq is read BEFORE reading the
// collections: if a new event arrives between reading last_seq and reading
// projects/tasks, the client will catch it via SinceSeq(last_seq). This may
// cause duplicates during replay but never gaps.
func (h *syncHandler) snapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	lastSeq, err := h.events.MaxSeq(ctx)
	if err != nil {
		h.logError(ctx, "MaxSeq", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve seq")
		return
	}

	projects, err := h.projects.ListProjects(ctx)
	if err != nil {
		h.logError(ctx, "list projects", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve projects")
		return
	}

	tasks, err := h.tasks.ListTasks(ctx, "", model.TaskFilter{Status: "open"})
	if err != nil {
		h.logError(ctx, "list tasks", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve tasks")
		return
	}

	pp := make([]projectDTO, 0, len(projects))
	for i := range projects {
		pp = append(pp, toProjectDTO(&projects[i]))
	}
	tt := make([]taskDTO, 0, len(tasks))
	for i := range tasks {
		tt = append(tt, toTaskDTO(&tasks[i]))
	}

	writeJSON(w, http.StatusOK, snapshotResponse{
		Projects:  pp,
		OpenTasks: tt,
		LastSeq:   lastSeq,
	})
}

func (h *syncHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/sync: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}
