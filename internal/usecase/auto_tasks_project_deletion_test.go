package usecase_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

type autoDeletionHarness struct {
	*autoHarness
	projects model.ProjectService
	devices  model.DeviceStore
	broker   model.Broker
}

func newAutoDeletionHarness(t *testing.T) *autoDeletionHarness {
	t.Helper()
	h := &autoDeletionHarness{autoHarness: newAutoHarness(t, autoCheckerFunc(differentAutoChecker))}
	h.devices = devices.NewSQLiteDeviceStore(h.db)
	h.broker = events.NewBroker(events.BrokerConfig{})
	h.projects = usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: h.db, Tasks: h.tasks, Meta: storage.NewSQLiteMetaStore(h.db), AutoTasks: h.store,
		Events: h.events, Devices: h.devices, Queue: h.queue, Broker: h.broker,
	})
	return h
}

func (h *autoDeletionHarness) autoService(tasks model.TaskStore, eventStore model.EventStore, deviceStore model.DeviceStore, store model.AutoTaskStore, checker model.TaskDuplicateChecker) model.AutoTaskService {
	return usecase.NewAutoTaskService(usecase.AutoTaskServiceDeps{
		DB: h.db, Tasks: tasks, Events: eventStore, Devices: deviceStore, Queue: h.queue,
		Broker: h.broker, Store: store, Checker: checker,
	})
}

func (h *autoDeletionHarness) project(t *testing.T) *model.Project {
	t.Helper()
	project, err := h.projects.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Reports"})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

type autoDeletionBarrier struct {
	started chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func newAutoDeletionBarrier() *autoDeletionBarrier {
	return &autoDeletionBarrier{started: make(chan struct{}), resume: make(chan struct{})}
}

func (b *autoDeletionBarrier) pause(ctx context.Context) error {
	var err error
	b.once.Do(func() {
		close(b.started)
		select {
		case <-b.resume:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return err
}

func (b *autoDeletionBarrier) wait(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-b.started:
	case <-ctx.Done():
		t.Fatal("submission did not reach deletion barrier")
	}
}

type autoDeletionRevisionBarrier struct {
	model.EventStore
	barrier *autoDeletionBarrier
}

func (s *autoDeletionRevisionBarrier) MaxSeq(ctx context.Context) (int64, error) {
	revision, err := s.EventStore.MaxSeq(ctx)
	if err == nil {
		err = s.barrier.pause(ctx)
	}
	return revision, err
}

type autoDeletionDeviceBarrier struct {
	model.DeviceStore
	barrier *autoDeletionBarrier
}

func (s *autoDeletionDeviceBarrier) ListActiveIDs(ctx context.Context) ([]string, error) {
	if err := s.barrier.pause(ctx); err != nil {
		return nil, err
	}
	return s.DeviceStore.ListActiveIDs(ctx)
}

func TestAutoTasks_ProjectDeletionReassessesInboxAtEveryBoundary(t *testing.T) {
	for _, boundary := range []string{"before read", "during assessment", "before commit"} {
		t.Run(boundary, func(t *testing.T) {
			h := newAutoDeletionHarness(t)
			project := h.project(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, req := range []model.CreateTaskRequest{
				{ProjectID: project.ID, Summary: "Unrelated original work"},
				{Summary: "Send report"},
			} {
				if _, err := h.manual.CreateTask(ctx, req); err != nil {
					t.Fatal(err)
				}
			}
			barrier := newAutoDeletionBarrier()
			var eventStore model.EventStore = h.events
			var deviceStore model.DeviceStore = h.devices
			if boundary == "before read" {
				eventStore = &autoDeletionRevisionBarrier{EventStore: eventStore, barrier: barrier}
			}
			if boundary == "before commit" {
				// Only Submit receives this wrapper; project deletion must remain unblocked.
				deviceStore = &autoDeletionDeviceBarrier{DeviceStore: deviceStore, barrier: barrier}
			}
			var destinations []string
			checker := autoCheckerFunc(func(ctx context.Context, task model.CreateTaskRequest, text string, refs []model.TaskReference) (model.TaskAssessment, error) {
				destinations = append(destinations, task.ProjectID)
				if text != "I will send the report" || len(refs) == 0 {
					t.Error("reassessment lost extraction text or seeded references")
				}
				if boundary == "during assessment" {
					if err := barrier.pause(ctx); err != nil {
						return model.TaskAssessment{}, err
					}
				}
				for _, ref := range refs {
					if ref.Task.Summary == task.Summary {
						return model.TaskAssessment{Verdict: "same", MatchID: ref.ID, Score: 1}, nil
					}
				}
				return model.TaskAssessment{Verdict: "different", Score: 0}, nil
			})
			h.service = h.autoService(h.tasks, eventStore, deviceStore, h.store, checker)
			type outcome struct {
				result model.AutoTaskResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := h.service.Submit(ctx, model.AutoTaskSubmission{
					ProjectID: project.ID, InputKeys: []string{"original-message-key"},
					Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}, SourceText: "I will send the report",
				})
				done <- outcome{result: result, err: err}
			}()
			barrier.wait(t, ctx)
			_, deleteErr := h.projects.DeleteProject(ctx, project.ID)
			close(barrier.resume)
			if deleteErr != nil {
				t.Fatal(deleteErr)
			}
			got := <-done
			if got.err != nil || got.result.Duplicates != 1 || len(got.result.Created) != 0 || len(got.result.Pending) != 0 {
				t.Fatalf("deleted destination result = %+v, err = %v", got.result, got.err)
			}
			wantDestinations := []string{project.ID, h.tasks.DefaultProjectID()}
			if boundary == "before read" {
				wantDestinations = wantDestinations[1:]
			}
			if !reflect.DeepEqual(destinations, wantDestinations) {
				t.Fatalf("assessment destinations = %v, want %v", destinations, wantDestinations)
			}
			tasks, err := h.tasks.ListTasks(ctx, h.tasks.DefaultProjectID(), model.TaskFilter{})
			if err != nil || len(tasks) != 2 {
				t.Fatalf("Inbox tasks = %+v, err = %v", tasks, err)
			}
			processed, err := h.service.Processed(ctx, "original-message-key")
			if err != nil || !processed {
				t.Fatalf("original receipt = %v, err = %v", processed, err)
			}
		})
	}
}

type autoDeletionCandidateStore struct {
	model.AutoTaskStore
	candidates []model.TaskCandidate
}

func (s *autoDeletionCandidateStore) InsertCandidateTx(ctx context.Context, tx *sql.Tx, candidate model.TaskCandidate) error {
	if err := s.AutoTaskStore.InsertCandidateTx(ctx, tx, candidate); err != nil {
		return err
	}
	s.candidates = append(s.candidates, candidate)
	return nil
}

func TestAutoTasks_ProjectDeletionRebuildsWithinBatchReferences(t *testing.T) {
	h := newAutoDeletionHarness(t)
	project := h.project(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, req := range []model.CreateTaskRequest{
		{ProjectID: project.ID, Summary: "Unrelated original work"},
		{Summary: "Send report"},
	} {
		if _, err := h.manual.CreateTask(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	barrier := newAutoDeletionBarrier()
	var originalRefs, inboxRefs []string
	checker := autoCheckerFunc(func(_ context.Context, task model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		for _, ref := range refs {
			if !strings.HasPrefix(ref.ID, "candidate:") {
				continue
			}
			if task.ProjectID == project.ID {
				originalRefs = append(originalRefs, ref.ID)
			} else {
				inboxRefs = append(inboxRefs, ref.ID)
			}
		}
		for _, ref := range refs {
			if ref.Task.Summary == task.Summary || task.Summary == "Repeat follow-up" && ref.Task.Summary == "New follow-up" {
				return model.TaskAssessment{Verdict: "same", MatchID: ref.ID, Score: 1}, nil
			}
		}
		return model.TaskAssessment{Verdict: "different", Score: 0}, nil
	})
	store := &autoDeletionCandidateStore{AutoTaskStore: h.store}
	h.service = h.autoService(h.tasks, h.events, &autoDeletionDeviceBarrier{DeviceStore: h.devices, barrier: barrier}, store, checker)
	type outcome struct {
		result model.AutoTaskResult
		err    error
	}
	done := make(chan outcome, 1)
	finalized := 0
	requests := []model.CreateTaskRequest{{Summary: "Send report"}, {Summary: "New follow-up"}, {Summary: "Repeat follow-up"}}
	go func() {
		result, err := h.service.Submit(ctx, model.AutoTaskSubmission{
			ProjectID: project.ID, InputKeys: []string{"within-batch-original-key"}, SourceText: "Original extracted batch", Tasks: requests,
			Finalize: func(ctx context.Context, tx *sql.Tx) error {
				finalized++
				processed, err := h.store.ProcessedTx(ctx, tx, "within-batch-original-key")
				if err != nil {
					return err
				}
				if !processed {
					return errors.New("Finalize did not see the original input receipt")
				}
				return nil
			},
		})
		done <- outcome{result: result, err: err}
	}()
	barrier.wait(t, ctx)
	_, deleteErr := h.projects.DeleteProject(ctx, project.ID)
	close(barrier.resume)
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	got := <-done
	if got.err != nil || len(got.result.Created) != 1 || got.result.Created[0].Summary != "New follow-up" || got.result.Created[0].ProjectID != h.tasks.DefaultProjectID() || got.result.Duplicates != 2 {
		t.Fatalf("rebuilt batch = %+v, err = %v", got.result, got.err)
	}
	if len(originalRefs) == 0 || len(inboxRefs) != 1 {
		t.Fatalf("within-batch references before = %v, after = %v", originalRefs, inboxRefs)
	}
	for _, old := range originalRefs {
		if old == inboxRefs[0] {
			t.Fatalf("reassessment reused old candidate %q", old)
		}
	}
	if len(store.candidates) != 3 {
		t.Fatalf("persisted candidates = %+v", store.candidates)
	}
	for _, candidate := range store.candidates {
		if candidate.Task.ProjectID != h.tasks.DefaultProjectID() || candidate.SourceText != "Original extracted batch" {
			t.Fatalf("candidate lost effective destination or extraction: %+v", candidate)
		}
	}
	last := store.candidates[2]
	if last.Status != "duplicate" || last.MatchID != "task:"+got.result.Created[0].ID {
		t.Fatalf("same-batch match did not resolve to new Inbox task: %+v", last)
	}
	if finalized != 1 {
		t.Fatalf("Finalize calls = %d, want one committed attempt", finalized)
	}
	for _, request := range requests {
		if request.ProjectID != "" {
			t.Fatalf("Submit mutated the original extraction: %+v", requests)
		}
	}
}

type autoDeletionTaskStore struct {
	model.TaskStore
	projectRead func(context.Context, *sql.Tx, string) (*model.Project, error)
	createErr   error
}

func (s *autoDeletionTaskStore) GetProjectTx(ctx context.Context, tx *sql.Tx, id string) (*model.Project, error) {
	return s.projectRead(ctx, tx, id)
}

func (s *autoDeletionTaskStore) CreateTaskTx(ctx context.Context, tx *sql.Tx, task *model.Task) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.TaskStore.CreateTaskTx(ctx, tx, task)
}

type autoDeletionReferencesStore struct {
	model.AutoTaskStore
	calls int
	err   error
}

func (s *autoDeletionReferencesStore) ReferencesTx(ctx context.Context, tx *sql.Tx, projectID string, closedSince time.Time) ([]model.TaskReference, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.AutoTaskStore.ReferencesTx(ctx, tx, projectID, closedSince)
}

func TestAutoTasks_MissingDestinationAtCommitReassessesInbox(t *testing.T) {
	h := newAutoDeletionHarness(t)
	project := h.project(t)
	ctx := context.Background()
	for _, req := range []model.CreateTaskRequest{{ProjectID: project.ID, Summary: "Unrelated work"}, {Summary: "Send report"}} {
		if _, err := h.manual.CreateTask(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var reads, comparisons []string
	tasks := &autoDeletionTaskStore{TaskStore: h.tasks}
	tasks.projectRead = func(ctx context.Context, tx *sql.Tx, id string) (*model.Project, error) {
		reads = append(reads, id)
		if len(reads) == 2 {
			return nil, nil
		}
		return h.tasks.GetProjectTx(ctx, tx, id)
	}
	checker := autoCheckerFunc(func(_ context.Context, task model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		comparisons = append(comparisons, task.ProjectID)
		for _, ref := range refs {
			if ref.Task.Summary == task.Summary {
				return model.TaskAssessment{Verdict: "same", MatchID: ref.ID, Score: 1}, nil
			}
		}
		return model.TaskAssessment{Verdict: "different", Score: 0}, nil
	})
	service := h.autoService(tasks, h.events, h.devices, h.store, checker)
	result, err := service.Submit(ctx, model.AutoTaskSubmission{ProjectID: project.ID, InputKeys: []string{"commit-missing"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}})
	if err != nil || result.Duplicates != 1 || len(result.Created) != 0 {
		t.Fatalf("missing at commit = %+v, err = %v", result, err)
	}
	inboxID := h.tasks.DefaultProjectID()
	if !reflect.DeepEqual(reads, []string{project.ID, project.ID, inboxID, inboxID}) || !reflect.DeepEqual(comparisons, []string{project.ID, inboxID}) {
		t.Fatalf("project reads = %v, assessments = %v", reads, comparisons)
	}
}

func TestAutoTasks_ChangedRevisionRepeatsCurrentDestination(t *testing.T) {
	h := newAutoDeletionHarness(t)
	project := h.project(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: project.ID, Summary: "Unrelated work"}); err != nil {
		t.Fatal(err)
	}
	barrier := newAutoDeletionBarrier()
	var destinations []string
	checker := autoCheckerFunc(func(_ context.Context, task model.CreateTaskRequest, _ string, _ []model.TaskReference) (model.TaskAssessment, error) {
		destinations = append(destinations, task.ProjectID)
		return model.TaskAssessment{Verdict: "different", Score: 0}, nil
	})
	service := h.autoService(h.tasks, h.events, &autoDeletionDeviceBarrier{DeviceStore: h.devices, barrier: barrier}, h.store, checker)
	type outcome struct {
		result model.AutoTaskResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.Submit(ctx, model.AutoTaskSubmission{ProjectID: project.ID, InputKeys: []string{"revision-only"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}})
		done <- outcome{result: result, err: err}
	}()
	barrier.wait(t, ctx)
	_, createErr := h.manual.CreateTask(ctx, model.CreateTaskRequest{Summary: "Other project changes revision"})
	close(barrier.resume)
	if createErr != nil {
		t.Fatal(createErr)
	}
	got := <-done
	if got.err != nil || len(got.result.Created) != 1 || got.result.Created[0].ProjectID != project.ID || !reflect.DeepEqual(destinations, []string{project.ID, project.ID}) {
		t.Fatalf("revision retry = %+v, err = %v, destinations = %v", got.result, got.err, destinations)
	}
}

func TestAutoTasks_DestinationFailuresAreTerminal(t *testing.T) {
	for _, stage := range []string{"missing Inbox at read", "missing Inbox at commit", "project read", "project commit", "references", "devices", "foreign key write", "canceled comparison"} {
		t.Run(stage, func(t *testing.T) {
			h := newAutoDeletionHarness(t)
			project := h.project(t)
			projectID := project.ID
			if strings.HasPrefix(stage, "missing Inbox") {
				projectID = h.tasks.DefaultProjectID()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: projectID, Summary: "Unrelated work"}); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("unrelated destination operation failed")
			if stage == "foreign key write" {
				failure = errors.New("FOREIGN KEY constraint failed")
			}
			var reads []string
			tasks := &autoDeletionTaskStore{TaskStore: h.tasks}
			tasks.projectRead = func(ctx context.Context, tx *sql.Tx, id string) (*model.Project, error) {
				reads = append(reads, id)
				switch {
				case stage == "missing Inbox at read", stage == "missing Inbox at commit" && len(reads) == 2:
					return nil, nil
				case stage == "project read", stage == "project commit" && len(reads) == 2:
					return nil, failure
				}
				return h.tasks.GetProjectTx(ctx, tx, id)
			}
			if stage == "foreign key write" {
				tasks.createErr = failure
			}
			store := &autoDeletionReferencesStore{AutoTaskStore: h.store}
			if stage == "references" {
				store.err = failure
			}
			var deviceStore model.DeviceStore = h.devices
			if stage == "devices" {
				deviceStore = &mockDeviceStore{listErr: failure}
			}
			comparisons, finalized := 0, 0
			checker := autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
				comparisons++
				if stage == "canceled comparison" {
					cancel()
					return model.TaskAssessment{}, context.Canceled
				}
				return model.TaskAssessment{Verdict: "different", Score: 0}, nil
			})
			service := h.autoService(tasks, h.events, deviceStore, store, checker)
			result, err := service.Submit(ctx, model.AutoTaskSubmission{
				ProjectID: projectID, InputKeys: []string{"terminal-failure"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}},
				Finalize: func(context.Context, *sql.Tx) error { finalized++; return nil },
			})
			if err == nil || len(result.Created) != 0 || len(result.Pending) != 0 || finalized != 0 {
				t.Fatalf("terminal result = %+v, err = %v, finalized = %d", result, err, finalized)
			}
			wantReads, wantComparisons, wantReferences := 2, 1, 1
			switch stage {
			case "missing Inbox at read", "project read":
				wantReads, wantComparisons, wantReferences = 1, 0, 0
			case "references":
				wantReads, wantComparisons = 1, 0
			case "devices", "canceled comparison":
				wantReads = 1
			}
			if len(reads) != wantReads || comparisons != wantComparisons || store.calls != wantReferences {
				t.Fatalf("reads = %v, comparisons = %d, references = %d", reads, comparisons, store.calls)
			}
			for _, id := range reads {
				if id != projectID {
					t.Fatalf("unrelated failure redirected to %q", id)
				}
			}
			switch {
			case stage == "canceled comparison":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
			case !strings.HasPrefix(stage, "missing Inbox"):
				if !errors.Is(err, failure) {
					t.Fatalf("underlying error = %v, want %v", err, failure)
				}
			}
			processed, receiptErr := service.Processed(context.Background(), "terminal-failure")
			if receiptErr != nil || processed {
				t.Fatalf("failed receipt = %v, err = %v", processed, receiptErr)
			}
		})
	}
}

func TestAutoTasks_ReplayAfterDeletionUsesOriginalReceiptsAndFinalize(t *testing.T) {
	for _, failFinalize := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalize failure %v", failFinalize), func(t *testing.T) {
			h := newAutoDeletionHarness(t)
			project := h.project(t)
			ctx := context.Background()
			keys := []string{"telegram:original-first", "telegram:original-second"}
			first, err := h.service.Submit(ctx, model.AutoTaskSubmission{
				ProjectID: project.ID, InputKeys: keys, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}},
			})
			if err != nil || len(first.Created) != 1 {
				t.Fatalf("initial submission = %+v, err = %v", first, err)
			}
			if _, err := h.projects.DeleteProject(ctx, project.ID); err != nil {
				t.Fatal(err)
			}
			buffer := storage.NewSQLiteForwardBuffer(h.db)
			for _, id := range []string{"captured-first", "captured-second", "new-forward"} {
				if err := buffer.Append(ctx, model.ForwardEntry{ChatID: "synthetic-chat", MessageID: id, Text: id, OriginalDate: time.Now(), ReceivedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			reads, comparisons, finalized := 0, 0, 0
			tasks := &autoDeletionTaskStore{TaskStore: h.tasks, projectRead: func(context.Context, *sql.Tx, string) (*model.Project, error) {
				reads++
				return nil, errors.New("replay must not read a destination, even Inbox")
			}}
			store := &autoDeletionReferencesStore{AutoTaskStore: h.store}
			service := h.autoService(tasks, h.events, h.devices, store, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
				comparisons++
				return model.TaskAssessment{}, errors.New("replay must not compare")
			}))
			finalizeErr := errors.New("replay snapshot finalization failed")
			submission := model.AutoTaskSubmission{
				ProjectID: project.ID, InputKeys: keys, Tasks: []model.CreateTaskRequest{{Summary: "Do not reassess"}},
				Finalize: func(ctx context.Context, tx *sql.Tx) error {
					finalized++
					if err := buffer.DeleteEntriesTx(ctx, tx, "synthetic-chat", []string{"captured-first", "captured-second"}); err != nil {
						return err
					}
					if failFinalize {
						return finalizeErr
					}
					return nil
				},
			}
			revision, err := h.events.MaxSeq(ctx)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Submit(ctx, submission)
			if failFinalize {
				if !errors.Is(err, finalizeErr) {
					t.Fatalf("replay Finalize error = %v", err)
				}
				batch, batchErr := buffer.LoadBatch(ctx, "synthetic-chat")
				if batchErr != nil || len(batch) != 3 {
					t.Fatalf("failed replay deleted snapshot: %+v, err = %v", batch, batchErr)
				}
				failFinalize = false
				result, err = service.Submit(ctx, submission)
			}
			if err != nil || !result.Replay || len(result.Created) != 0 || len(result.Pending) != 0 || result.Duplicates != 0 {
				t.Fatalf("deleted-project replay = %+v, err = %v", result, err)
			}
			batch, err := buffer.LoadBatch(ctx, "synthetic-chat")
			if err != nil || len(batch) != 1 || batch[0].MessageID != "new-forward" {
				t.Fatalf("remaining forwards = %+v, err = %v", batch, err)
			}
			if reads != 0 || comparisons != 0 || store.calls != 0 || finalized == 0 {
				t.Fatalf("replay read destination %d / references %d / compared %d / finalized %d", reads, store.calls, comparisons, finalized)
			}
			for _, key := range keys {
				processed, err := service.Processed(ctx, key)
				if err != nil || !processed {
					t.Fatalf("original receipt %q = %v, err = %v", key, processed, err)
				}
			}
			after, err := h.events.MaxSeq(ctx)
			if err != nil || after != revision {
				t.Fatalf("replay events revision = %d, want %d, err = %v", after, revision, err)
			}
		})
	}
}

func TestAutoTasks_InboxReassessmentFinalizeFailureRollsBackEverything(t *testing.T) {
	h := newAutoDeletionHarness(t)
	project := h.project(t)
	ctx := context.Background()
	if _, err := h.projects.DeleteProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	buffer := storage.NewSQLiteForwardBuffer(h.db)
	if err := buffer.Append(ctx, model.ForwardEntry{ChatID: "synthetic-chat", MessageID: "captured", Text: "Send report", OriginalDate: time.Now(), ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	revision, err := h.events.MaxSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store := &autoDeletionCandidateStore{AutoTaskStore: h.store}
	service := h.autoService(h.tasks, h.events, h.devices, store, autoCheckerFunc(differentAutoChecker))
	finalizeErr := errors.New("forward snapshot failed")
	submission := model.AutoTaskSubmission{
		ProjectID: project.ID, InputKeys: []string{"original-rollback-first", "original-rollback-second"},
		Tasks: []model.CreateTaskRequest{{Summary: "Send report"}, {Summary: "Send another report"}}, SourceText: "Original extracted text",
		Finalize: func(ctx context.Context, tx *sql.Tx) error {
			if err := buffer.DeleteEntriesTx(ctx, tx, "synthetic-chat", []string{"captured"}); err != nil {
				return err
			}
			return finalizeErr
		},
	}
	_, err = service.Submit(ctx, submission)
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("Finalize error = %v", err)
	}
	for _, key := range submission.InputKeys {
		processed, err := service.Processed(ctx, key)
		if err != nil || processed {
			t.Fatalf("rolled-back receipt %q = %v, err = %v", key, processed, err)
		}
	}
	if len(store.candidates) != 2 {
		t.Fatalf("Finalize did not follow candidate writes: %+v", store.candidates)
	}
	for _, candidate := range store.candidates {
		persisted, err := service.GetCandidate(ctx, candidate.ID)
		if err != nil || persisted != nil {
			t.Fatalf("rolled-back candidate = %+v, err = %v", persisted, err)
		}
	}
	tasks, err := h.tasks.ListTasks(ctx, h.tasks.DefaultProjectID(), model.TaskFilter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("rolled-back Inbox tasks = %+v, err = %v", tasks, err)
	}
	after, err := h.events.MaxSeq(ctx)
	if err != nil || after != revision {
		t.Fatalf("rolled-back event revision = %d, want %d, err = %v", after, revision, err)
	}
	batch, err := buffer.LoadBatch(ctx, "synthetic-chat")
	if err != nil || len(batch) != 1 {
		t.Fatalf("rolled-back forward snapshot = %+v, err = %v", batch, err)
	}
	submission.Finalize = func(ctx context.Context, tx *sql.Tx) error {
		return buffer.DeleteEntriesTx(ctx, tx, "synthetic-chat", []string{"captured"})
	}
	result, err := service.Submit(ctx, submission)
	if err != nil || len(result.Created) != 2 || result.Created[0].Number != 1 || result.Created[1].Number != 2 {
		t.Fatalf("retry consumed no original receipts or Inbox numbers = %+v, err = %v", result, err)
	}
	for _, key := range submission.InputKeys {
		processed, err := service.Processed(ctx, key)
		if err != nil || !processed {
			t.Fatalf("retry original receipt %q = %v, err = %v", key, processed, err)
		}
	}
	batch, err = buffer.LoadBatch(ctx, "synthetic-chat")
	if err != nil || len(batch) != 0 {
		t.Fatalf("successful retry left captured forwards: %+v, err = %v", batch, err)
	}
}

func TestAutoTasks_ProjectDeletionPreservesEveryCandidateStatusAndQuestion(t *testing.T) {
	h := newAutoDeletionHarness(t)
	project := h.project(t)
	ctx := context.Background()
	task, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: project.ID, Summary: "Existing obligation"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var original []model.TaskCandidate
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, status := range []string{"pending", "created", "rejected", "duplicate"} {
		candidate := model.TaskCandidate{
			ID: "all-statuses-" + status, Task: model.CreateTaskRequest{ProjectID: project.ID, Summary: status, Priority: model.TaskPriorityHigh, SourceDetails: &model.TaskSourceDetails{Via: "forward", Text: "Extracted context"}},
			Status: status, SourceText: "Original batch", Reason: "Original assessment", MatchID: "task:" + task.ID, MatchSummary: task.Summary, MatchDisplayID: task.DisplayID(), CreatedAt: now,
		}
		if status != "pending" {
			candidate.DecidedAt = &now
			candidate.QuestionChatID, candidate.QuestionMsgID = 42, i+100
		}
		if status == "created" {
			candidate.CreatedTaskID = task.ID
		}
		if err := h.store.InsertCandidateTx(ctx, tx, candidate); err != nil {
			t.Fatal(err)
		}
		original = append(original, candidate)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := range original {
		if original[i].Status != "pending" {
			continue
		}
		if err := h.service.MarkQuestionSent(ctx, original[i].ID, 42, i+100); err != nil {
			t.Fatal(err)
		}
		original[i].QuestionChatID, original[i].QuestionMsgID = 42, i+100
	}
	if _, err := h.projects.DeleteProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	for _, want := range original {
		want.Task.ProjectID = h.tasks.DefaultProjectID()
		got, err := h.service.GetCandidate(ctx, want.ID)
		if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
			t.Fatalf("candidate %q after deletion = %+v, want %+v, err = %v", want.ID, got, want, err)
		}
	}
	tx, err = h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	refs, err := h.store.ReferencesTx(ctx, tx, h.tasks.DefaultProjectID(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending", "rejected"} {
		found := false
		for _, ref := range refs {
			found = found || ref.ID == "candidate:all-statuses-"+status
		}
		if !found {
			t.Fatalf("Inbox references omitted reassigned %s candidate: %+v", status, refs)
		}
	}
}

func TestAutoTasks_ProjectDeletionAndApprovalUseFreshCandidateOnce(t *testing.T) {
	for _, approveFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("approval before deletion %v", approveFirst), func(t *testing.T) {
			h := newAutoDeletionHarness(t)
			project := h.project(t)
			ctx := context.Background()
			if _, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{ProjectID: project.ID, Summary: "Seed comparison"}); err != nil {
				t.Fatal(err)
			}
			service := h.autoService(h.tasks, h.events, h.devices, h.store, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
				return model.TaskAssessment{Verdict: "uncertain", Score: .8, Reason: "Needs confirmation"}, nil
			}))
			result, err := service.Submit(ctx, model.AutoTaskSubmission{ProjectID: project.ID, InputKeys: []string{"approval-original"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report", Priority: model.TaskPriorityHigh, SourceDetails: &model.TaskSourceDetails{Via: "forward", Text: "Original promise"}}}})
			if err != nil || len(result.Pending) != 1 {
				t.Fatalf("pending = %+v, err = %v", result, err)
			}
			id := result.Pending[0].ID
			if err := service.MarkQuestionSent(ctx, id, 42, 17); err != nil {
				t.Fatal(err)
			}
			stale, err := service.GetCandidate(ctx, id)
			if err != nil || stale == nil || stale.Task.ProjectID != project.ID {
				t.Fatalf("pre-deletion candidate = %+v, err = %v", stale, err)
			}
			taskID := ""
			if approveFirst {
				decision, err := service.Decide(ctx, id, true)
				if err != nil || decision.Task == nil || decision.Task.ProjectID != project.ID {
					t.Fatalf("approval before deletion = %+v, err = %v", decision, err)
				}
				taskID = decision.Task.ID
			}
			if _, err := h.projects.DeleteProject(ctx, project.ID); err != nil {
				t.Fatal(err)
			}
			for _, approve := range []bool{true, true, false} {
				decision, err := service.Decide(ctx, id, approve)
				if err != nil || decision.Task == nil || decision.Task.ProjectID != h.tasks.DefaultProjectID() || decision.Candidate.Task.ProjectID != h.tasks.DefaultProjectID() || decision.Candidate.Status != "created" {
					t.Fatalf("fresh approval = %+v, err = %v", decision, err)
				}
				if taskID == "" {
					taskID = decision.Task.ID
				}
				if decision.Task.ID != taskID || decision.Candidate.QuestionChatID != 42 || decision.Candidate.QuestionMsgID != 17 || decision.Task.Priority != model.TaskPriorityHigh || decision.Task.SourceDetails == nil || decision.Task.SourceDetails.Text != "Original promise" {
					t.Fatalf("repeated approval changed identity or metadata: %+v", decision)
				}
			}
			tasks, err := h.tasks.ListTasks(ctx, h.tasks.DefaultProjectID(), model.TaskFilter{})
			if err != nil || len(tasks) != 2 {
				t.Fatalf("Inbox has more than seed + one approval: %+v, err = %v", tasks, err)
			}
		})
	}
}
