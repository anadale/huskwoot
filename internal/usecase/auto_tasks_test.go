package usecase_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

type autoCheckerFunc func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error)

func (f autoCheckerFunc) Compare(ctx context.Context, task model.CreateTaskRequest, text string, refs []model.TaskReference) (model.TaskAssessment, error) {
	return f(ctx, task, text, refs)
}

type autoHarness struct {
	db      *sql.DB
	store   *storage.SQLiteAutoTaskStore
	tasks   *storage.CachedTaskStore
	events  *events.SQLiteEventStore
	queue   *push.SQLitePushQueue
	service model.AutoTaskService
	manual  model.TaskService
}

func newAutoHarness(t *testing.T, checker model.TaskDuplicateChecker) *autoHarness {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "auto.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rawTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	h := &autoHarness{db: db, store: storage.NewSQLiteAutoTaskStore(db), tasks: storage.NewCachedTaskStore(rawTasks), events: events.NewSQLiteEventStore(db), queue: push.NewSQLitePushQueue(db)}
	deviceStore := devices.NewSQLiteDeviceStore(db)
	broker := events.NewBroker(events.BrokerConfig{})
	h.manual = usecase.NewTaskService(usecase.TaskServiceDeps{DB: db, Tasks: h.tasks, Events: h.events, Devices: deviceStore, Queue: h.queue, Broker: broker})
	h.service = usecase.NewAutoTaskService(usecase.AutoTaskServiceDeps{DB: db, Tasks: h.tasks, Events: h.events, Devices: deviceStore, Queue: h.queue, Broker: broker, Store: h.store, Checker: checker})
	return h
}

func (h *autoHarness) submit(t *testing.T, key string) model.AutoTaskResult {
	t.Helper()
	result, err := h.service.Submit(context.Background(), model.AutoTaskSubmission{InputKeys: []string{key}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}, SourceText: "I will send the report"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func differentAutoChecker(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
	return model.TaskAssessment{Verdict: "different", Score: 0}, nil
}

func TestAutoTasks_InputReplayAndFinalize(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(differentAutoChecker))
	first := h.submit(t, "message-1")
	if len(first.Created) != 1 {
		t.Fatalf("created = %v", first)
	}
	finalized := false
	second, err := h.service.Submit(context.Background(), model.AutoTaskSubmission{InputKeys: []string{"message-1"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}, Finalize: func(context.Context, *sql.Tx) error { finalized = true; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replay || len(second.Created) != 0 || !finalized {
		t.Fatalf("replay = %+v, finalized=%v", second, finalized)
	}
	tasks, err := h.tasks.ListTasks(context.Background(), "", model.TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	evs, err := h.events.SinceSeq(context.Background(), 0, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events=%v err=%v", evs, err)
	}
}

func TestAutoTasks_HighSameSkipsButDifferentPeriodCreates(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(_ context.Context, task model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		if len(refs) == 0 || task.Details == "October report" {
			return model.TaskAssessment{Verdict: "different", Score: .1}, nil
		}
		return model.TaskAssessment{Verdict: "same", MatchID: refs[0].ID, Score: .99}, nil
	}))
	first := h.submit(t, "first")
	second := h.submit(t, "second")
	if len(first.Created) != 1 || second.Duplicates != 1 || len(second.Pending) != 0 || len(second.Created) != 0 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	third, err := h.service.Submit(context.Background(), model.AutoTaskSubmission{InputKeys: []string{"third"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report", Details: "October report"}}})
	if err != nil || len(third.Created) != 1 {
		t.Fatalf("different period=%+v err=%v", third, err)
	}
}

func TestAutoTasks_UncertainApprovalAndCompetingChoicesOnce(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		return model.TaskAssessment{Verdict: "uncertain", Score: .8, Reason: "The period is not specified"}, nil
	}))
	h.seedReference(t)
	result := h.submit(t, "uncertain")
	if len(result.Created) != 0 || len(result.Pending) != 1 {
		t.Fatalf("result=%+v", result)
	}
	tasks, err := h.tasks.ListTasks(context.Background(), "", model.TaskFilter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("pending created a task: %v %v", tasks, err)
	}
	id := result.Pending[0].ID
	decision, err := h.service.Decide(context.Background(), id, true)
	if err != nil || decision.Task == nil || decision.Candidate.Status != "created" {
		t.Fatalf("approve=%+v err=%v", decision, err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(approve bool) {
			defer wg.Done()
			replay, err := h.service.Decide(context.Background(), id, approve)
			if err != nil || replay.Task == nil || replay.Task.ID != decision.Task.ID || replay.Candidate.Status != "created" {
				t.Errorf("replayed choice=%+v err=%v", replay, err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	tasks, err = h.tasks.ListTasks(context.Background(), "", model.TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	evs, err := h.events.SinceSeq(context.Background(), 0, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events=%v err=%v", evs, err)
	}
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.tasks.DeleteTaskTx(context.Background(), tx, decision.Task.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replay, err := h.service.Decide(context.Background(), id, true)
	if err != nil || replay.Task != nil || replay.Candidate.CreatedTaskID != decision.Task.ID {
		t.Fatalf("retained decision after task purge=%+v err=%v", replay, err)
	}
}

func TestAutoTasks_RejectionAndPendingSuppressRepeatedCommitment(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(_ context.Context, _ model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		for _, ref := range refs {
			if ref.Task.Summary != "Send report" {
				continue
			}
			if ref.SourceText == "" || ref.CreatedAt.IsZero() {
				t.Error("candidate reference lost source context")
			}
			return model.TaskAssessment{Verdict: "same", MatchID: ref.ID, Score: .99}, nil
		}
		return model.TaskAssessment{Verdict: "uncertain", Score: .8}, nil
	}))
	h.seedReference(t)
	first := h.submit(t, "first")
	second := h.submit(t, "second")
	if len(first.Pending) != 1 || second.Duplicates != 1 || len(second.Pending) != 0 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	id := first.Pending[0].ID
	rejected, err := h.service.Decide(context.Background(), id, false)
	if err != nil || rejected.Candidate.Status != "rejected" {
		t.Fatalf("reject=%+v err=%v", rejected, err)
	}
	third := h.submit(t, "third")
	if third.Duplicates != 1 || len(third.Pending) != 0 || len(third.Created) != 0 {
		t.Fatalf("repeat rejection=%+v", third)
	}
	replay, err := h.service.Decide(context.Background(), id, true)
	if err != nil || replay.Candidate.Status != "rejected" || replay.Task != nil {
		t.Fatalf("rejection lost=%+v err=%v", replay, err)
	}
	pending, err := h.service.PendingQuestions(context.Background(), 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("questions=%v err=%v", pending, err)
	}
}

func TestAutoTasks_ClosedMatchRequiresReview(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(_ context.Context, _ model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		if len(refs) != 1 || refs[0].State != "done" || refs[0].ClosedAt == nil {
			t.Fatalf("closed reference missing: %+v", refs)
		}
		return model.TaskAssessment{Verdict: "same", MatchID: refs[0].ID, Score: 1}, nil
	}))
	task, err := h.manual.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "Send report"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manual.CompleteTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	result := h.submit(t, "closed")
	if len(result.Pending) != 1 || len(result.Created) != 0 || result.Duplicates != 0 {
		t.Fatalf("closed match=%+v", result)
	}
}

func TestAutoTasks_SameBatchDuplicatesCreateOnlyOnce(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(_ context.Context, _ model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		if len(refs) == 0 {
			return model.TaskAssessment{Verdict: "different", Score: 0}, nil
		}
		return model.TaskAssessment{Verdict: "same", MatchID: refs[0].ID, Score: 1}, nil
	}))
	result, err := h.service.Submit(context.Background(), model.AutoTaskSubmission{InputKeys: []string{"batch"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}, {Summary: "Deliver the report"}}})
	if err != nil || len(result.Created) != 1 || result.Duplicates != 1 || len(result.Pending) != 0 {
		t.Fatalf("batch=%+v err=%v", result, err)
	}
}

func TestAutoTasks_FinalizeFailureRollsBackTasksEventsPushAndReceipts(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(differentAutoChecker))
	ctx := context.Background()
	deviceStore := devices.NewSQLiteDeviceStore(h.db)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := deviceStore.Create(ctx, tx, &model.Device{ID: "auto-device", Name: "Test", Platform: "linux", TokenHash: "auto-hash"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	finalizeErr := errors.New("cannot delete forward snapshot")
	_, err = h.service.Submit(ctx, model.AutoTaskSubmission{InputKeys: []string{"rollback"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}, Finalize: func(context.Context, *sql.Tx) error { return finalizeErr }})
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("finalize error=%v", err)
	}
	processed, err := h.service.Processed(ctx, "rollback")
	if err != nil || processed {
		t.Fatalf("receipt=%v err=%v", processed, err)
	}
	tasks, err := h.tasks.ListTasks(ctx, "", model.TaskFilter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	evs, err := h.events.SinceSeq(ctx, 0, 0)
	if err != nil || len(evs) != 0 {
		t.Fatalf("events=%v err=%v", evs, err)
	}
	jobs, err := h.queue.NextBatch(ctx, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("push=%v err=%v", jobs, err)
	}
	created := h.submit(t, "rollback")
	if len(created.Created) != 1 || created.Created[0].Number != 1 {
		t.Fatalf("rollback consumed number or input=%+v", created)
	}
	jobs, err = h.queue.NextBatch(ctx, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("committed push=%v err=%v", jobs, err)
	}
}

func TestAutoTasks_CheckerErrorsAndInvalidAssessmentsRequireReview(t *testing.T) {
	cases := []struct {
		name    string
		checker autoCheckerFunc
	}{
		{name: "checker error", checker: func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
			return model.TaskAssessment{}, errors.New("private endpoint error")
		}},
		{name: "missing reference", checker: func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
			return model.TaskAssessment{Verdict: "same", MatchID: "task:unknown", Score: 1}, nil
		}},
		{name: "invalid score", checker: func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
			return model.TaskAssessment{Verdict: "different", Score: -1}, nil
		}},
		{name: "invalid verdict", checker: func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
			return model.TaskAssessment{Verdict: "skip", Score: 0}, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAutoHarness(t, tc.checker)
			h.seedReference(t)
			result := h.submit(t, "unavailable")
			if len(result.Created) != 0 || len(result.Pending) != 1 || result.Pending[0].Reason != "duplicate_check_unavailable" {
				t.Fatalf("unsafe assessment=%+v", result)
			}
			candidate, err := h.service.GetCandidate(context.Background(), result.Pending[0].ID)
			if err != nil || candidate == nil || candidate.Status != "pending" {
				t.Fatalf("pending not durable: %v %v", candidate, err)
			}
			replay := h.submit(t, "unavailable")
			if !replay.Replay || len(replay.Pending) != 0 {
				t.Fatalf("pending replay=%+v", replay)
			}
		})
	}
}

func TestAutoTasks_ManualCreationDuringCompareReevaluates(t *testing.T) {
	started, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := newAutoHarness(t, autoCheckerFunc(func(ctx context.Context, _ model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-resume:
			case <-ctx.Done():
				return model.TaskAssessment{}, ctx.Err()
			}
		}
		for _, ref := range refs {
			if ref.Task.Summary == "Send report" {
				return model.TaskAssessment{Verdict: "same", MatchID: ref.ID, Score: 1}, nil
			}
		}
		return model.TaskAssessment{Verdict: "different", Score: 0}, nil
	}))
	h.seedReference(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		result model.AutoTaskResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := h.service.Submit(ctx, model.AutoTaskSubmission{InputKeys: []string{"concurrent"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}})
		done <- outcome{result, err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("checker not called")
	}
	if _, err := h.manual.CreateTask(ctx, model.CreateTaskRequest{Summary: "Send report"}); err != nil {
		close(resume)
		t.Fatal(err)
	}
	close(resume)
	got := <-done
	if got.err != nil || got.result.Duplicates != 1 || len(got.result.Created) != 0 || calls.Load() != 2 {
		t.Fatalf("concurrent submission=%+v err=%v comparisons=%d", got.result, got.err, calls.Load())
	}
}

func TestAutoTasks_CanceledComparisonDoesNotPersistPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newAutoHarness(t, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		cancel()
		return model.TaskAssessment{}, context.Canceled
	}))
	h.seedReference(t)
	_, err := h.service.Submit(ctx, model.AutoTaskSubmission{InputKeys: []string{"canceled"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	pending, err := h.service.PendingQuestions(context.Background(), 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("canceled questions=%v err=%v", pending, err)
	}
	processed, err := h.service.Processed(context.Background(), "canceled")
	if err != nil || processed {
		t.Fatalf("canceled receipt=%v err=%v", processed, err)
	}
}

func TestAutoTasks_MixedReceiptsStillProcessNewInputs(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(differentAutoChecker))
	h.submit(t, "old")
	finalized := false
	result, err := h.service.Submit(context.Background(), model.AutoTaskSubmission{InputKeys: []string{"old", "new"}, Tasks: []model.CreateTaskRequest{{Summary: "Another obligation"}}, Finalize: func(context.Context, *sql.Tx) error { finalized = true; return nil }})
	if err != nil || result.Replay || len(result.Created) != 1 || !finalized {
		t.Fatalf("mixed receipts=%+v err=%v finalized=%v", result, err, finalized)
	}
	processed, err := h.service.Processed(context.Background(), "new")
	if err != nil || !processed {
		t.Fatalf("new receipt=%v err=%v", processed, err)
	}
}

func TestAutoTasks_AllOpenReferencesWithoutLimit(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(_ context.Context, _ model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
		if len(refs) != 160 {
			t.Errorf("reference count=%d, want all 160 open tasks", len(refs))
		}
		for _, ref := range refs {
			if ref.Task.Summary == "Send report" {
				return model.TaskAssessment{Verdict: "same", MatchID: ref.ID, Score: 1}, nil
			}
		}
		return model.TaskAssessment{}, errors.New("matching task omitted")
	}))
	reqs := make([]model.CreateTaskRequest, 160)
	for i := range reqs {
		reqs[i].Summary = "Unrelated work"
	}
	reqs[159].Summary = "Send report"
	if _, err := h.manual.CreateTasks(context.Background(), model.CreateTasksRequest{Tasks: reqs}); err != nil {
		t.Fatal(err)
	}
	result := h.submit(t, "all-open")
	if result.Duplicates != 1 || len(result.Created) != 0 || len(result.Pending) != 0 {
		t.Fatalf("all-open result=%+v", result)
	}
}

func TestAutoTasks_DatabaseFailureIsNotConvertedToPending(t *testing.T) {
	var calls int
	h := newAutoHarness(t, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		calls++
		return model.TaskAssessment{Verdict: "different", Score: 0}, nil
	}))
	if _, err := h.db.Exec(`DROP TABLE auto_task_candidates`); err != nil {
		t.Fatal(err)
	}
	result, err := h.service.Submit(context.Background(), model.AutoTaskSubmission{InputKeys: []string{"broken-store"}, Tasks: []model.CreateTaskRequest{{Summary: "Send report"}}})
	if err == nil || calls != 0 || len(result.Pending) != 0 {
		t.Fatalf("database failure swallowed: result=%+v err=%v checker calls=%d", result, err, calls)
	}
}

func TestAutoTasks_ConcurrentFirstChoiceWins(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		return model.TaskAssessment{Verdict: "uncertain", Score: .8}, nil
	}))
	h.seedReference(t)
	first := h.submit(t, "competing")
	id := first.Pending[0].ID
	type outcome struct {
		decision model.TaskCandidateDecision
		err      error
	}
	results := make(chan outcome, 8)
	start := make(chan struct{})
	for i := range 8 {
		go func(approve bool) {
			<-start
			decision, err := h.service.Decide(context.Background(), id, approve)
			results <- outcome{decision, err}
		}(i%2 == 0)
	}
	close(start)
	var status, taskID string
	for range 8 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if status == "" {
			status, taskID = got.decision.Candidate.Status, got.decision.Candidate.CreatedTaskID
		}
		if got.decision.Candidate.Status != status || got.decision.Candidate.CreatedTaskID != taskID {
			t.Fatalf("competing terminal decision changed: %+v", got.decision)
		}
	}
	tasks, err := h.tasks.ListTasks(context.Background(), "", model.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	if status == "created" {
		want = 1
	} else if status != "rejected" {
		t.Fatalf("terminal status=%q", status)
	}
	if len(tasks) != want {
		t.Fatalf("tasks=%v winner=%s", tasks, status)
	}
	evs, err := h.events.SinceSeq(context.Background(), 0, 0)
	if err != nil || len(evs) != want {
		t.Fatalf("events=%v err=%v winner=%s", evs, err, status)
	}
}

func TestAutoTasks_ConflictAndThresholdsStayPending(t *testing.T) {
	cases := []struct {
		name       string
		assessment model.TaskAssessment
	}{
		{name: "same with conflicting period", assessment: model.TaskAssessment{Verdict: "same", Score: 1, Conflict: true}},
		{name: "same below skip threshold", assessment: model.TaskAssessment{Verdict: "same", Score: .94}},
		{name: "different at review threshold", assessment: model.TaskAssessment{Verdict: "different", Score: .70}},
		{name: "different with conflicting period", assessment: model.TaskAssessment{Verdict: "different", Score: .1, Conflict: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAutoHarness(t, autoCheckerFunc(func(_ context.Context, _ model.CreateTaskRequest, _ string, refs []model.TaskReference) (model.TaskAssessment, error) {
				assessment := tc.assessment
				assessment.MatchID = refs[0].ID
				return assessment, nil
			}))
			if _, err := h.manual.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "Send report"}); err != nil {
				t.Fatal(err)
			}
			result := h.submit(t, "conflict")
			if len(result.Pending) != 1 || len(result.Created) != 0 || result.Duplicates != 0 {
				t.Fatalf("conflict result=%+v", result)
			}
			// An explicit approval is not blocked again by semantic matching.
			decision, err := h.service.Decide(context.Background(), result.Pending[0].ID, true)
			if err != nil || decision.Task == nil {
				t.Fatalf("explicit approval=%+v err=%v", decision, err)
			}
		})
	}
}

func (h *autoHarness) seedReference(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	candidate := model.TaskCandidate{ID: "unrelated-reference", Task: model.CreateTaskRequest{ProjectID: h.tasks.DefaultProjectID(), Summary: "Unrelated obligation"}, Status: "rejected", SourceText: "An unrelated previous commitment", CreatedAt: time.Now().UTC()}
	if err := h.store.InsertCandidateTx(ctx, tx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAutoTasks_EmptyReferenceSetCreatesWithoutReview(t *testing.T) {
	h := newAutoHarness(t, autoCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		return model.TaskAssessment{}, errors.New("comparison unavailable")
	}))
	result := h.submit(t, "first-obligation")
	if len(result.Created) != 1 || len(result.Pending) != 0 || result.Duplicates != 0 {
		t.Fatalf("empty-project result=%+v", result)
	}
}
