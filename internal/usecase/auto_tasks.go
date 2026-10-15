package usecase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/google/uuid"
)

// ErrTaskCandidateNotFound indicates an unknown automatic task decision.
var ErrTaskCandidateNotFound = errors.New("automatic task candidate not found")

// AutoTaskServiceDeps collects the stores and checker used for automatic tasks.
type AutoTaskServiceDeps struct {
	DB                 *sql.DB
	Tasks              model.TaskStore
	Events             model.EventStore
	Devices            model.DeviceStore
	Queue              model.PushQueue
	Broker             model.Broker
	Store              model.AutoTaskStore
	Checker            model.TaskDuplicateChecker
	DuplicateThreshold float64
	ReviewThreshold    float64
	Logger             *slog.Logger
	Now                func() time.Time
}

type autoTaskService struct {
	mu sync.Mutex
	*taskService
	store     model.AutoTaskStore
	checker   model.TaskDuplicateChecker
	high, low float64
	logger    *slog.Logger
	now       func() time.Time
}

// NewAutoTaskService creates a durable automatic task decision service.
func NewAutoTaskService(deps AutoTaskServiceDeps) model.AutoTaskService {
	if deps.DuplicateThreshold == 0 {
		deps.DuplicateThreshold = .95
	}
	if deps.ReviewThreshold == 0 {
		deps.ReviewThreshold = .70
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &autoTaskService{
		taskService: &taskService{db: deps.DB, tasks: deps.Tasks, events: deps.Events, devices: deps.Devices, queue: deps.Queue, broker: deps.Broker},
		store:       deps.Store, checker: deps.Checker, high: deps.DuplicateThreshold, low: deps.ReviewThreshold, logger: deps.Logger, now: deps.Now,
	}
}

func (s *autoTaskService) Processed(ctx context.Context, key string) (bool, error) {
	return s.store.Processed(ctx, key)
}
func (s *autoTaskService) GetCandidate(ctx context.Context, id string) (*model.TaskCandidate, error) {
	return s.store.GetCandidate(ctx, id)
}
func (s *autoTaskService) PendingQuestions(ctx context.Context, limit int) ([]model.TaskCandidate, error) {
	return s.store.PendingQuestions(ctx, limit)
}
func (s *autoTaskService) MarkQuestionSent(ctx context.Context, id string, chatID int64, messageID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.MarkQuestionSent(ctx, id, chatID, messageID)
}

func (s *autoTaskService) Submit(ctx context.Context, submission model.AutoTaskSubmission) (model.AutoTaskResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(submission.InputKeys) == 0 {
		return model.AutoTaskResult{}, fmt.Errorf("automatic task input identity is required")
	}
	for _, key := range submission.InputKeys {
		if key == "" {
			return model.AutoTaskResult{}, fmt.Errorf("automatic task input identity is empty")
		}
	}
	projectID := submission.ProjectID
	if projectID == "" {
		projectID = s.tasks.DefaultProjectID()
	}

	for {
		if err := ctx.Err(); err != nil {
			return model.AutoTaskResult{}, err
		}
		revision, err := s.events.MaxSeq(ctx)
		if err != nil {
			return model.AutoTaskResult{}, fmt.Errorf("read automatic task revision: %w", err)
		}
		refs, replay, err := s.readSubmission(ctx, submission, projectID)
		if err != nil {
			return model.AutoTaskResult{}, err
		}
		if replay {
			return model.AutoTaskResult{Replay: true}, nil
		}

		candidates, err := s.assess(ctx, submission, projectID, refs)
		if err != nil {
			return model.AutoTaskResult{}, err
		}
		activeIDs, err := s.devices.ListActiveIDs(ctx)
		if err != nil {
			return model.AutoTaskResult{}, fmt.Errorf("list automatic task devices: %w", err)
		}
		result, changed, err := s.commitSubmission(ctx, submission, projectID, candidates, activeIDs, revision)
		if err != nil {
			return model.AutoTaskResult{}, err
		}
		if changed {
			continue
		}
		return result, nil
	}
}

// readSubmission closes its snapshot before any checker call. Replayed batches
// still finalize their exact forward snapshot in a transaction.
func (s *autoTaskService) readSubmission(ctx context.Context, submission model.AutoTaskSubmission, projectID string) ([]model.TaskReference, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin automatic task snapshot: %w", err)
	}
	defer tx.Rollback()
	allProcessed := true
	for _, key := range submission.InputKeys {
		processed, err := s.store.ProcessedTx(ctx, tx, key)
		if err != nil {
			return nil, false, err
		}
		allProcessed = allProcessed && processed
	}
	if allProcessed {
		if submission.Finalize != nil {
			if err := submission.Finalize(ctx, tx); err != nil {
				return nil, false, fmt.Errorf("finalize automatic task replay: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit automatic task replay: %w", err)
		}
		return nil, true, nil
	}
	refs, err := s.store.ReferencesTx(ctx, tx, projectID, s.now().Add(-14*24*time.Hour))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Rollback(); err != nil {
		return nil, false, fmt.Errorf("close automatic task snapshot: %w", err)
	}
	return refs, false, nil
}

func (s *autoTaskService) assess(ctx context.Context, submission model.AutoTaskSubmission, projectID string, refs []model.TaskReference) ([]model.TaskCandidate, error) {
	candidates := make([]model.TaskCandidate, 0, len(submission.Tasks))
	for _, req := range submission.Tasks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req.ProjectID = projectID
		c := model.TaskCandidate{ID: uuid.NewString(), Task: req, SourceText: submission.SourceText, Status: "pending", CreatedAt: s.now().UTC()}
		assessment := model.TaskAssessment{}
		var err error
		if len(refs) == 0 {
			assessment = model.TaskAssessment{Verdict: "different", Score: 0}
		} else if s.checker == nil {
			err = errors.New("automatic task checker unavailable")
		} else {
			assessment, err = s.checker.Compare(ctx, req, submission.SourceText, refs)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		match, valid := validAutoAssessment(assessment, refs)
		if err != nil || !valid {
			// API errors may contain credentials or source text; only the failure
			// category is logged and a localizable reason is persisted.
			s.logger.Warn("automatic task duplicate comparison unavailable", "invalid_assessment", err == nil)
			c.Reason = "duplicate_check_unavailable"
		} else {
			c.Reason, c.MatchID = assessment.Reason, assessment.MatchID
			if match != nil {
				c.MatchSummary, c.MatchDisplayID = match.Task.Summary, match.DisplayID
			}
			switch {
			case assessment.Verdict == "same" && assessment.Score >= s.high && !assessment.Conflict && match != nil && (match.State == "open" || match.State == "pending" || match.State == "rejected"):
				c.Status = "duplicate"
			case assessment.Verdict == "different" && assessment.Score < s.low && !assessment.Conflict:
				c.Status = "created"
			}
		}
		if c.Status != "pending" {
			now := s.now().UTC()
			c.DecidedAt = &now
		}
		candidates = append(candidates, c)
		// Duplicates resolve to a reference already present. Only fresh
		// obligations participate as additional references in this batch.
		if c.Status != "duplicate" {
			state := c.Status
			if state == "created" {
				state = "open"
			}
			refs = append(refs, model.TaskReference{ID: "candidate:" + c.ID, Task: c.Task, State: state, CreatedAt: c.CreatedAt, SourceText: c.SourceText})
		}
	}
	return candidates, nil
}

func validAutoAssessment(a model.TaskAssessment, refs []model.TaskReference) (*model.TaskReference, bool) {
	if math.IsNaN(a.Score) || math.IsInf(a.Score, 0) || a.Score < 0 || a.Score > 1 {
		return nil, false
	}
	if a.Verdict != "same" && a.Verdict != "different" && a.Verdict != "uncertain" {
		return nil, false
	}
	if a.MatchID == "" {
		return nil, a.Verdict != "same"
	}
	for i := range refs {
		if refs[i].ID == a.MatchID {
			return &refs[i], true
		}
	}
	return nil, false
}

func (s *autoTaskService) commitSubmission(ctx context.Context, submission model.AutoTaskSubmission, projectID string, candidates []model.TaskCandidate, activeIDs []string, revision int64) (model.AutoTaskResult, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AutoTaskResult{}, false, fmt.Errorf("begin automatic task decisions: %w", err)
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&current); err != nil {
		return model.AutoTaskResult{}, false, fmt.Errorf("recheck automatic task revision: %w", err)
	}
	if current != revision {
		return model.AutoTaskResult{}, true, nil
	}
	project, err := s.tasks.GetProjectTx(ctx, tx, projectID)
	if err != nil {
		return model.AutoTaskResult{}, false, fmt.Errorf("read automatic task project: %w", err)
	}
	if project == nil {
		return model.AutoTaskResult{}, false, fmt.Errorf("automatic task project %q not found", projectID)
	}
	var result model.AutoTaskResult
	var notifications []model.Event
	// Resolve temporary same-batch candidate references to the durable task.
	createdRefs := make(map[string]*model.Task)
	for i := range candidates {
		c := &candidates[i]
		if matched := createdRefs[c.MatchID]; matched != nil {
			c.MatchID, c.MatchDisplayID = "task:"+matched.ID, matched.DisplayID()
		}
		switch c.Status {
		case "created":
			task, event, err := s.createAutoTaskTx(ctx, tx, c.Task, project.Slug, activeIDs)
			if err != nil {
				return model.AutoTaskResult{}, false, err
			}
			c.CreatedTaskID = task.ID
			createdRefs["candidate:"+c.ID] = task
			result.Created = append(result.Created, *task)
			notifications = append(notifications, event)
		case "pending":
			result.Pending = append(result.Pending, *c)
		case "duplicate":
			result.Duplicates++
		}
		if err := s.store.InsertCandidateTx(ctx, tx, *c); err != nil {
			return model.AutoTaskResult{}, false, err
		}
	}
	for _, key := range submission.InputKeys {
		if err := s.store.RecordInputTx(ctx, tx, key, s.now()); err != nil {
			return model.AutoTaskResult{}, false, err
		}
	}
	if submission.Finalize != nil {
		if err := submission.Finalize(ctx, tx); err != nil {
			return model.AutoTaskResult{}, false, fmt.Errorf("finalize automatic tasks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.AutoTaskResult{}, false, fmt.Errorf("commit automatic tasks: %w", err)
	}
	s.publishAutoTasks(notifications)
	return result, false, nil
}

func (s *autoTaskService) createAutoTaskTx(ctx context.Context, tx *sql.Tx, req model.CreateTaskRequest, slug string, activeIDs []string) (*model.Task, model.Event, error) {
	task := &model.Task{ProjectID: req.ProjectID, ProjectSlug: slug, Summary: req.Summary, Details: req.Details, Topic: req.Topic, Deadline: req.Deadline, Source: req.Source}
	if err := s.tasks.CreateTaskTx(ctx, tx, task); err != nil {
		return nil, model.Event{}, fmt.Errorf("create automatic task: %w", err)
	}
	task.ProjectSlug = slug
	event, err := s.recordEvent(ctx, tx, model.EventTaskCreated, task.ID, wrapTaskPayload(task, nil), activeIDs)
	if err != nil {
		return nil, model.Event{}, err
	}
	return task, event, nil
}

func (s *autoTaskService) publishAutoTasks(notifications []model.Event) {
	if len(notifications) > 0 {
		s.invalidateProjectCache()
	}
	for _, event := range notifications {
		s.broker.Notify(event)
	}
}

func (s *autoTaskService) Decide(ctx context.Context, id string, approve bool) (model.TaskCandidateDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activeIDs, err := s.devices.ListActiveIDs(ctx)
	if err != nil {
		return model.TaskCandidateDecision{}, fmt.Errorf("list automatic task devices: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskCandidateDecision{}, fmt.Errorf("begin automatic task choice: %w", err)
	}
	defer tx.Rollback()
	c, err := s.store.GetCandidateTx(ctx, tx, id)
	if err != nil {
		return model.TaskCandidateDecision{}, err
	}
	if c == nil {
		return model.TaskCandidateDecision{}, fmt.Errorf("candidate %q: %w", id, ErrTaskCandidateNotFound)
	}
	result := model.TaskCandidateDecision{Candidate: *c}
	if c.Status != "pending" {
		if c.CreatedTaskID != "" {
			result.Task, err = s.tasks.GetTaskTx(ctx, tx, c.CreatedTaskID)
			if err != nil {
				return model.TaskCandidateDecision{}, fmt.Errorf("read decided automatic task: %w", err)
			}
		}
		return result, nil
	}
	var notifications []model.Event
	if approve {
		project, err := s.tasks.GetProjectTx(ctx, tx, c.Task.ProjectID)
		if err != nil {
			return model.TaskCandidateDecision{}, fmt.Errorf("read approved task project: %w", err)
		}
		if project == nil {
			return model.TaskCandidateDecision{}, fmt.Errorf("approved task project %q not found", c.Task.ProjectID)
		}
		task, event, err := s.createAutoTaskTx(ctx, tx, c.Task, project.Slug, activeIDs)
		if err != nil {
			return model.TaskCandidateDecision{}, err
		}
		c.Status, c.CreatedTaskID, result.Task = "created", task.ID, task
		notifications = append(notifications, event)
	} else {
		c.Status = "rejected"
	}
	now := s.now().UTC()
	c.DecidedAt = &now
	if err := s.store.UpdateCandidateTx(ctx, tx, *c); err != nil {
		return model.TaskCandidateDecision{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskCandidateDecision{}, fmt.Errorf("commit automatic task choice: %w", err)
	}
	s.publishAutoTasks(notifications)
	result.Candidate = *c
	return result, nil
}
