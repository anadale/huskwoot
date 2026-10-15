package model

import (
	"context"
	"database/sql"
	"time"
)

// TaskReference identifies an existing obligation supplied to the duplicate checker.
// ID is prefixed with "task:" or "candidate:" and is opaque to the checker.
type TaskReference struct {
	ID         string
	Task       CreateTaskRequest
	State      string
	DisplayID  string
	CreatedAt  time.Time
	ClosedAt   *time.Time
	SourceText string
}

// TaskAssessment describes whether a candidate repeats an existing obligation.
// Score estimates sameness, not confidence that the message contains a promise.
type TaskAssessment struct {
	Verdict  string
	MatchID  string
	Score    float64
	Conflict bool
	Reason   string
}

// TaskCandidate is a durable extracted obligation and its final or pending decision.
// Status is pending, created, rejected, or duplicate.
type TaskCandidate struct {
	ID             string
	Task           CreateTaskRequest
	SourceText     string
	Status         string
	Reason         string
	MatchID        string
	MatchSummary   string
	MatchDisplayID string
	CreatedAt      time.Time
	DecidedAt      *time.Time
	CreatedTaskID  string
	QuestionChatID int64
	QuestionMsgID  int
}

// AutoTaskSubmission groups one extraction and its durable source identities.
// Finalize runs inside the same transaction as decisions, tasks, events, and input
// receipts; forward batches use it to delete only their processed snapshot.
type AutoTaskSubmission struct {
	InputKeys  []string
	ProjectID  string
	Tasks      []CreateTaskRequest
	SourceText string
	Finalize   func(context.Context, *sql.Tx) error
}

// AutoTaskResult reports only newly created tasks and newly pending questions.
type AutoTaskResult struct {
	Created    []Task
	Pending    []TaskCandidate
	Duplicates int
	Replay     bool
}

// TaskCandidateDecision returns the persisted result of an idempotent owner choice.
type TaskCandidateDecision struct {
	Candidate TaskCandidate
	Task      *Task
}
