package model

import (
	"context"
	"strconv"
	"time"
)

// MessageKind describes the type/context of a message.
type MessageKind string

const (
	// MessageKindDM is a direct message from the owner to the bot.
	MessageKindDM MessageKind = "dm"
	// MessageKindBatch is a message from a batch source (IMAP).
	MessageKindBatch MessageKind = "batch"
	// MessageKindGroup is a message from a group chat.
	MessageKindGroup MessageKind = "group"
	// MessageKindGroupDirect is a bot-directed message in a group chat (mention or reply to bot).
	MessageKindGroupDirect MessageKind = "group_direct"
)

// Classification is the result of message classification.
type Classification int

const (
	// ClassSkip — the message contains no promise and is not a command.
	ClassSkip Classification = iota
	// ClassPromise — the message contains a user promise.
	ClassPromise
	// ClassCommand — the message is a configuration command to the bot.
	ClassCommand
)

// String returns the string representation of a classification.
func (c Classification) String() string {
	switch c {
	case ClassSkip:
		return "skip"
	case ClassPromise:
		return "promise"
	case ClassCommand:
		return "command"
	default:
		return "unknown"
	}
}

// Command describes a configuration command extracted from a message.
type Command struct {
	// Type is the command type (e.g. "set_project_name").
	Type string
	// Payload holds the command parameters.
	Payload map[string]string
	// Source is the channel the command came from.
	Source Source
	// SourceMessage is the original message containing the command.
	SourceMessage Message
}

// Source describes the origin of a message (the channel).
type Source struct {
	// Kind is the channel type: "telegram" or "imap".
	Kind string
	// ID is the unique channel identifier (e.g. chat_id or email address).
	ID string
	// Name is the human-readable channel name.
	Name string
	// AccountID is the identifier of the watcher that created the message (e.g. "work", "personal").
	// Empty string for IMAP sources.
	AccountID string
}

// Reaction describes an emoji reaction to a message.
type Reaction struct {
	// Emoji is the reaction symbol (e.g. "👍").
	Emoji string
	// UserID is the identifier of the user who left the reaction.
	UserID string
}

// Message describes a single message from a channel.
type Message struct {
	// ID is the unique message identifier within the channel.
	ID string
	// Source is the channel the message came from.
	Source Source
	// Author is the message author's identifier.
	Author string
	// AuthorName is the author's display name.
	AuthorName string
	// Subject is the message subject (populated for IMAP, empty for Telegram).
	Subject string
	// Text is the message body.
	Text string
	// Timestamp is the time the message was created.
	Timestamp time.Time
	// ReplyTo is the message being replied to (if any).
	ReplyTo *Message
	// Reaction is the user's reaction to the message (if any).
	Reaction *Reaction
	// Kind is the message type/context (DM, Batch, Group).
	Kind MessageKind
	// ReactFn is a callback for sending a reaction to the message (nil for non-Telegram sources).
	ReactFn func(ctx context.Context, emoji string) error
	// ReplyFn is a callback for sending a reply to the message (nil for non-Telegram sources).
	ReplyFn func(ctx context.Context, text string) error
	// HistoryFn is a callback for fetching the chat message history (nil if history is not needed).
	// Set by the channel for group messages; nil for DM and IMAP.
	HistoryFn func(ctx context.Context) ([]HistoryEntry, error)
}

// Task describes a task extracted from a user promise.
type Task struct {
	// ID is the task UUID.
	ID string
	// Number is the per-project monotonic task number.
	Number int
	// ProjectID is the UUID of the project the task belongs to.
	ProjectID string
	// ProjectSlug is the project slug; populated by the store via JOIN on SELECT.
	ProjectSlug string
	// Summary is a short description of what needs to be done.
	Summary string
	// Details is the task context or details (populated by the extractor).
	Details string
	// Topic is the task's thematic group (e.g. "Deploy", "iOS Client").
	// Populated by the extractor; cleared for group chats in the pipeline.
	Topic string
	// Status is the task status: "open", "done", or "cancelled".
	Status string
	// Deadline is the task due date (nil if not specified).
	Deadline *time.Time
	// Confidence is the model's confidence in the extracted task (0.0–1.0).
	Confidence float64
	// Source is the technical identifier of the source channel.
	Source Source
	// SourceMessage is the original message containing the promise.
	// Contains Subject (email subject for IMAP) and ReactFn/ReplyFn callbacks.
	SourceMessage Message
	// CreatedAt is the time the task record was created.
	CreatedAt time.Time
	// UpdatedAt is the time the task was last updated.
	UpdatedAt time.Time
	// ClosedAt is the time the task was moved to done or cancelled (nil for open tasks).
	ClosedAt *time.Time
}

// DisplayID returns a human-readable reference like "inbox#42".
func (t Task) DisplayID() string {
	return t.ProjectSlug + "#" + strconv.Itoa(t.Number)
}

// Project describes a project — a container for tasks.
type Project struct {
	// ID is the project UUID.
	ID string
	// Name is the project name (unique).
	Name string
	// Slug is the lowercase-kebab project identifier (unique).
	Slug string
	// Description is the project description.
	Description string
	// TaskCounter is a monotonic task counter; not rolled back on move.
	TaskCounter int
	// CreatedAt is the time the project was created.
	CreatedAt time.Time
}

// TaskFilter specifies task filtering criteria.
type TaskFilter struct {
	// Status restricts the result set by status: "open", "done", or "cancelled".
	// Empty string means all statuses.
	Status string
	// Query is a substring to search in the task summary (case-insensitive LIKE).
	// Empty string means no text filter.
	Query string
	// Limit is the maximum number of tasks to return (0 means no limit).
	Limit int
}

// TaskUpdate describes changes to apply to a task.
type TaskUpdate struct {
	// Summary is the new short description (nil means no change).
	Summary *string
	// Details is the new task details (nil means no change).
	Details *string
	// Topic is the new thematic group (nil means no change).
	Topic *string
	// Status is the new task status (nil means no change).
	Status *string
	// Deadline is the new due date (nil means no change; pointer to nil clears the deadline).
	Deadline **time.Time
	// ClosedAt is the task close time (nil means no change; pointer to nil clears the field).
	ClosedAt **time.Time
}

// ProjectUpdate describes changes to apply to a project.
type ProjectUpdate struct {
	// Name is the new project name (nil means no change).
	Name *string
	// Description is the new description (nil means no change).
	Description *string
	// Slug is the new project slug (nil means no change).
	Slug *string
}

// HistoryEntry describes a single message history record stored in the store.
type HistoryEntry struct {
	// AuthorName is the author's display name.
	AuthorName string
	// Text is the message body.
	Text string
	// Timestamp is the time the message was created.
	Timestamp time.Time
}

// Summary holds the content of a periodic digest, grouped into sections.
type Summary struct {
	// GeneratedAt is the time the digest was generated.
	GeneratedAt time.Time
	// Slot is the schedule slot: "morning", "afternoon", or "evening".
	Slot string
	// Overdue is the list of overdue tasks, grouped by project.
	Overdue []ProjectGroup
	// Today is the list of tasks due today, grouped by project.
	Today []ProjectGroup
	// Upcoming is the list of tasks due within the planning horizon, grouped by project.
	Upcoming []ProjectGroup
	// Undated is the list of tasks with no deadline, already trimmed to UndatedLimit.
	Undated []ProjectGroup
	// UndatedTotal is the total count of undated tasks (before trimming).
	UndatedTotal int
	// IsEmpty is true when all sections are empty.
	IsEmpty bool
}

// ProjectGroup holds tasks for a single project within a digest section.
type ProjectGroup struct {
	// ProjectID is the project UUID.
	ProjectID string
	// ProjectName is the project name.
	ProjectName string
	// Tasks are the project's tasks in this section.
	Tasks []Task
}

// GuardPending represents a pending guard approval for a Telegram chat.
type GuardPending struct {
	ChatID       int64
	WelcomeMsgID int
	Deadline     time.Time
}

// Cursor describes the read position in a channel, enabling resume from the same point.
type Cursor struct {
	// MessageID is the identifier of the last processed message.
	MessageID string
	// FolderID is the folder identifier (for IMAP: UIDVALIDITY).
	FolderID string
	// UpdatedAt is the time the cursor was last updated.
	UpdatedAt time.Time
}
