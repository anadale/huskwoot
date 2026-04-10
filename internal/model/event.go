package model

import (
	"encoding/json"
	"time"
)

// EventKind classifies the type of event raised in the domain model.
type EventKind string

const (
	// EventTaskCreated — a task was created.
	EventTaskCreated EventKind = "task_created"
	// EventTaskUpdated — task fields changed (details, deadline, etc.).
	EventTaskUpdated EventKind = "task_updated"
	// EventTaskCompleted — a task was marked done.
	EventTaskCompleted EventKind = "task_completed"
	// EventTaskReopened — a task was returned to open status.
	EventTaskReopened EventKind = "task_reopened"
	// EventTaskMoved — a task was moved to another project.
	EventTaskMoved EventKind = "task_moved"
	// EventProjectCreated — a project was created.
	EventProjectCreated EventKind = "project_created"
	// EventProjectUpdated — project fields changed (name, description, slug).
	EventProjectUpdated EventKind = "project_updated"
	// EventChatReply — an agent reply in a chat session (DM/GroupDirect).
	EventChatReply EventKind = "chat_reply"
	// EventReminderSummary — a periodic digest was generated.
	EventReminderSummary EventKind = "reminder_summary"
	// EventReset — the client must perform a cold resync (fell behind the retention window).
	EventReset EventKind = "reset"
)

// Event describes a single event published by the domain model.
// Populated by the use case within a transaction and saved to the EventStore.
type Event struct {
	// Seq is the monotonic sequence number assigned by EventStore.Insert.
	Seq int64
	// Kind is the event type (see EventKind constants).
	Kind EventKind
	// EntityID is the entity identifier (task.ID, project.ID, etc.).
	EntityID string
	// Payload is a JSON snapshot of the entity at the time of the event.
	Payload json.RawMessage
	// CreatedAt is the time the event was published.
	CreatedAt time.Time
}
