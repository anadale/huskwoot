package model

import (
	"context"
	"time"
)

// CreateTaskRequest holds parameters for creating a single task via TaskService.
type CreateTaskRequest struct {
	// ProjectID is the project UUID; "" means Inbox.
	ProjectID string
	// Summary is the short task description.
	Summary string
	// Details is the task context or details.
	Details string
	// Topic is the thematic group (optional).
	Topic string
	// Deadline is the due date (nil if not set).
	Deadline *time.Time
	// Source is the channel that initiated task creation.
	Source Source
}

// CreateTasksRequest holds parameters for batch task creation via TaskService.
type CreateTasksRequest struct {
	// ProjectID is the project UUID for all tasks; "" means Inbox.
	ProjectID string
	// Tasks is the list of tasks to create.
	Tasks []CreateTaskRequest
}

// CreateProjectRequest holds parameters for creating a project via ProjectService.
type CreateProjectRequest struct {
	// Name is the project name (required).
	Name string
	// Slug is the project slug; if "", it is auto-generated from Name.
	Slug string
	// Description is the project description (optional).
	Description string
}

// ChatReply is the ChatService response to a message.
type ChatReply struct {
	// Text is the agent's reply text.
	Text string
	// TasksTouched are the UUIDs of tasks touched by agent tools during processing
	// (in order of first access; duplicates excluded).
	TasksTouched []string
	// ProjectsTouched are the UUIDs of projects touched by agent tools during processing
	// (in order of first access; duplicates excluded).
	ProjectsTouched []string
}

// TaskService is the use-case layer for task operations.
type TaskService interface {
	// CreateTask creates a single task; populates ID, Number, CreatedAt, UpdatedAt, Status.
	CreateTask(ctx context.Context, req CreateTaskRequest) (*Task, error)
	// CreateTasks creates a batch of tasks for a single project.
	CreateTasks(ctx context.Context, req CreateTasksRequest) ([]Task, error)
	// UpdateTask applies changes to a task.
	UpdateTask(ctx context.Context, id string, upd TaskUpdate) (*Task, error)
	// CompleteTask moves a task to "done" status.
	CompleteTask(ctx context.Context, id string) (*Task, error)
	// ReopenTask moves a task to "open" status.
	ReopenTask(ctx context.Context, id string) (*Task, error)
	// MoveTask moves a task to another project, reassigning its number.
	MoveTask(ctx context.Context, id, newProjectID string) (*Task, error)
	// ListTasks returns tasks for a project matching the filter.
	// projectID="" means all projects.
	ListTasks(ctx context.Context, projectID string, filter TaskFilter) ([]Task, error)
	// GetTask returns a task by UUID.
	GetTask(ctx context.Context, id string) (*Task, error)
	// GetTaskByRef finds a task by its human-readable reference "<slug>#<number>".
	GetTaskByRef(ctx context.Context, projectSlug string, number int) (*Task, error)
}

// ProjectService is the use-case layer for project operations.
type ProjectService interface {
	// CreateProject creates a new project; generates a slug if not provided.
	CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error)
	// UpdateProject applies changes to a project.
	UpdateProject(ctx context.Context, id string, upd ProjectUpdate) (*Project, error)
	// ListProjects returns all projects.
	ListProjects(ctx context.Context) ([]Project, error)
	// FindProjectByName finds a project by name. Returns nil, nil if not found.
	FindProjectByName(ctx context.Context, name string) (*Project, error)
	// ResolveProjectForChannel returns the UUID of the project bound to the given channel.
	// If no mapping is found, returns the Inbox ID.
	ResolveProjectForChannel(ctx context.Context, channelID string) (string, error)
	// EnsureChannelProject idempotently creates a project and binds it to the channel.
	EnsureChannelProject(ctx context.Context, channelID, name string) (*Project, error)
}

// ChatService is the use-case layer for processing incoming messages via the agent.
type ChatService interface {
	// HandleMessage passes the message to the agent and returns the reply.
	HandleMessage(ctx context.Context, msg Message) (ChatReply, error)
}

// PairingService is the use-case layer for the pairing flow (connecting a device via magic link).
type PairingService interface {
	// RequestPairing creates a new device pairing request, saves it to the store,
	// and sends the owner a DM with the magic link.
	RequestPairing(ctx context.Context, req PairingRequest) (*PendingPairing, error)
	// PollStatus waits for the pairing request result (long-poll up to longPollTTL).
	// Verifies sha256(clientNonce) against the stored NonceHash.
	// Returns PairingResult with Status=="pending" on timeout or Status=="confirmed" on success.
	PollStatus(ctx context.Context, pairID, clientNonce string) (*PairingResult, error)
	// PrepareConfirm generates and saves SHA256(csrfToken) for a pairing request.
	// Used by the GET /pair/confirm/{id} HTML handler before rendering the form.
	PrepareConfirm(ctx context.Context, pairID, csrfToken string) (*PendingPairing, error)
	// ConfirmWithCSRF confirms the pairing: verifies the CSRF token, creates the device,
	// and publishes the bearer token via the broadcaster.
	ConfirmWithCSRF(ctx context.Context, pairID, csrfToken string) (*Device, error)
}
