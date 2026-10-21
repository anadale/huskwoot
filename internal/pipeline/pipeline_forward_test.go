package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

type forwardExtractorFunc func(context.Context, model.Message, []model.HistoryEntry) ([]model.Task, error)

func (f forwardExtractorFunc) Extract(ctx context.Context, msg model.Message, h []model.HistoryEntry) ([]model.Task, error) {
	return f(ctx, msg, h)
}

type mockForwardProjectService struct {
	model.ProjectService
	resolveRefResult *model.Project
	resolveRefErr    error
}

func (s *mockForwardProjectService) ResolveProjectRef(context.Context, string) (*model.Project, error) {
	return s.resolveRefResult, s.resolveRefErr
}

func makeTestForwards(chatID string, n int) []model.ForwardEntry {
	now := time.Now().UTC()
	entries := make([]model.ForwardEntry, n)
	for i := range entries {
		entries[i] = model.ForwardEntry{ChatID: chatID, MessageID: fmt.Sprintf("msg-%d", i+1), ForwardFromName: "Alice", IsSelfAuthored: true, OriginalDate: now.Add(time.Duration(i) * time.Minute), ReceivedAt: now, Text: fmt.Sprintf("promise %d", i+1)}
	}
	return entries
}

func forwardFixture(t *testing.T) (Config, *pipelineAutoTasks, *storage.SQLiteForwardBuffer, model.Message) {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "forward.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	buf := storage.NewSQLiteForwardBuffer(db)
	entries := makeTestForwards("owner-chat", 2)
	for _, entry := range entries {
		if err := buf.Append(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	auto := &pipelineAutoTasks{db: db}
	cfg := Config{Logger: discardLogger, AutoTasks: auto, ForwardBuffer: buf, TaskStore: tasks, ForwardClassifier: &mockClassifier{class: model.ClassPromise}, ForwardExtractor: &mockExtractor{tasks: []model.Task{{Summary: "send slides"}, {Summary: "send report"}}}}
	msg := model.Message{Kind: model.MessageKindForwardBatch, Source: model.Source{Kind: "telegram", ID: "owner-chat", AccountID: "personal"}, ForwardBatch: &model.ForwardBatchPayload{ChatID: "owner-chat", Forwards: entries, Counterpart: "Alice", TriggerID: "trigger"}}
	return cfg, auto, buf, msg
}

func TestProcessForwardBatch_HappyPath(t *testing.T) {
	cfg, _, buf, msg := forwardFixture(t)
	var deleted []string
	msg.ForwardBatch.DeleteFn = func(_ context.Context, ids []string) error { deleted = ids; return nil }
	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	remaining, err := buf.LoadBatch(context.Background(), "owner-chat")
	if err != nil || len(remaining) != 0 {
		t.Fatalf("snapshot not cleared: %v %v", remaining, err)
	}
	if len(deleted) != 3 || deleted[2] != "trigger" {
		t.Fatalf("wrong Telegram deletion: %v", deleted)
	}
}

func TestProcessForwardBatch_SkipAndEmptyExtractionPersistReceipts(t *testing.T) {
	for _, skip := range []bool{true, false} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			cfg, auto, buf, msg := forwardFixture(t)
			if skip {
				cfg.ForwardClassifier = &mockClassifier{class: model.ClassSkip}
			} else {
				cfg.ForwardExtractor = &mockExtractor{}
			}
			deleted := false
			msg.ForwardBatch.DeleteFn = func(context.Context, []string) error { deleted = true; return nil }
			if err := New(cfg).Process(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			batch, err := buf.LoadBatch(context.Background(), "owner-chat")
			if err != nil || len(batch) != 0 || len(auto.processed) != 2 {
				t.Fatalf("skip not committed: batch=%v receipts=%v err=%v", batch, auto.processed, err)
			}
			if deleted {
				t.Fatal("skipped extraction deleted Telegram messages")
			}
		})
	}
}

func TestProcessForwardBatch_SubmitFailurePreservesSnapshot(t *testing.T) {
	for _, skip := range []bool{true, false} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			cfg, auto, buf, msg := forwardFixture(t)
			sentinel := errors.New("decision persistence failed")
			auto.err = sentinel
			if skip {
				cfg.ForwardClassifier = &mockClassifier{class: model.ClassSkip}
			}
			replied := false
			msg.ForwardBatch.ReplyFn = func(context.Context, string) error { replied = true; return nil }
			if err := New(cfg).Process(context.Background(), msg); !errors.Is(err, sentinel) {
				t.Fatalf("error=%v", err)
			}
			batch, err := buf.LoadBatch(context.Background(), "owner-chat")
			if err != nil || len(batch) != 2 || replied {
				t.Fatalf("failed submission lost snapshot/replied: %v %v %v", batch, err, replied)
			}
		})
	}
}

func TestProcessForwardBatch_RealFinalizeFailureRollsBackBufferTasksCandidatesEventsAndReceipts(t *testing.T) {
	h := newRealForwardHarness(t, forwardCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		return model.TaskAssessment{Verdict: "uncertain", Score: .8}, nil
	}))
	ctx := context.Background()
	newEntry := makeTestForwards("owner-chat", 1)[0]
	newEntry.MessageID = "arrived-during-inference"
	h.cfg.ForwardExtractor = forwardExtractorFunc(func(context.Context, model.Message, []model.HistoryEntry) ([]model.Task, error) {
		if err := h.buffer.Append(ctx, newEntry); err != nil {
			return nil, err
		}
		return []model.Task{{Summary: "send slides"}, {Summary: "send report"}}, nil
	})
	sentinel := errors.New("finalize failed")
	finalizations := 0
	h.cfg.ForwardBuffer = forwardFinalizeProbe{ForwardBuffer: h.buffer, check: func(ctx context.Context, tx *sql.Tx) error {
		finalizations++
		for table, want := range map[string]int{"forward_buffer": 1, "auto_task_inputs": 2, "auto_task_candidates": 2, "tasks": 1, "events": 1} {
			var got int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
				return err
			}
			if got != want {
				t.Fatalf("before Finalize failure %s count=%d want %d", table, got, want)
			}
		}
		return sentinel
	}}
	h.msg.ForwardBatch.ReplyFn = func(context.Context, string) error {
		t.Fatal("failed Finalize sent a success reply")
		return nil
	}
	h.msg.ForwardBatch.DeleteFn = func(context.Context, []string) error {
		t.Fatal("failed Finalize deleted Telegram messages")
		return nil
	}
	if err := New(h.cfg).Process(ctx, h.msg); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	if finalizations != 1 {
		t.Fatalf("Finalize calls=%d want 1", finalizations)
	}
	for table, want := range map[string]int{"forward_buffer": 3, "auto_task_inputs": 0, "auto_task_candidates": 0, "tasks": 0, "events": 0} {
		var got int
		if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("after rollback %s count=%d want %d", table, got, want)
		}
	}
	batch, err := h.buffer.LoadBatch(ctx, "owner-chat")
	if err != nil || len(batch) != 3 {
		t.Fatalf("failed Finalize lost buffered forwards: batch=%+v err=%v", batch, err)
	}
	for _, entry := range batch {
		processed, err := h.auto.Processed(ctx, autoInputKey(h.msg.Source, entry.MessageID, entry.Text, 0))
		if err != nil || processed {
			t.Fatalf("failed Finalize persisted receipt for %q: processed=%v err=%v", entry.MessageID, processed, err)
		}
	}
}

func TestProcessForwardBatch_NewArrivalDuringInferenceIsPreserved(t *testing.T) {
	for _, skip := range []bool{true, false} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			cfg, _, buf, msg := forwardFixture(t)
			appendNew := func() {
				entry := makeTestForwards("owner-chat", 1)[0]
				entry.MessageID = "arrived-during-inference"
				if err := buf.Append(context.Background(), entry); err != nil {
					t.Fatal(err)
				}
			}
			if skip {
				cfg.ForwardClassifier = forwardClassifierFunc(func(context.Context, model.Message) (model.Classification, error) {
					appendNew()
					return model.ClassSkip, nil
				})
			} else {
				cfg.ForwardExtractor = forwardExtractorFunc(func(context.Context, model.Message, []model.HistoryEntry) ([]model.Task, error) {
					appendNew()
					return []model.Task{{Summary: "slides"}}, nil
				})
			}
			if err := New(cfg).Process(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			batch, err := buf.LoadBatch(context.Background(), "owner-chat")
			if err != nil || len(batch) != 1 || batch[0].MessageID != "arrived-during-inference" {
				t.Fatalf("new arrival deleted: %v %v", batch, err)
			}
		})
	}
}

type forwardClassifierFunc func(context.Context, model.Message) (model.Classification, error)

func (f forwardClassifierFunc) Classify(ctx context.Context, msg model.Message) (model.Classification, error) {
	return f(ctx, msg)
}

func TestProcessForwardBatch_ReplayFiltersBeforeInference(t *testing.T) {
	cfg, auto, buf, msg := forwardFixture(t)
	first := msg.ForwardBatch.Forwards[0]
	auto.processed = map[string]bool{autoInputKey(msg.Source, first.MessageID, first.Text, 0): true}
	cfg.ForwardExtractor = forwardExtractorFunc(func(_ context.Context, in model.Message, _ []model.HistoryEntry) ([]model.Task, error) {
		if strings.Contains(in.Text, first.Text) {
			t.Fatal("processed forward leaked into extraction")
		}
		return []model.Task{{Summary: "new promise"}}, nil
	})
	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(auto.submissions[0].InputKeys) != 2 {
		t.Fatal("snapshot submission omitted processed input keys")
	}
	if batch, err := buf.LoadBatch(context.Background(), "owner-chat"); err != nil || len(batch) != 0 {
		t.Fatalf("snapshot not cleared: %v %v", batch, err)
	}
	cfg.ForwardClassifier = forwardClassifierFunc(func(context.Context, model.Message) (model.Classification, error) {
		t.Fatal("replay classifier called")
		return model.ClassSkip, nil
	})
	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
}

func TestProcessForwardBatch_PendingAndDuplicateOutcomes(t *testing.T) {
	for _, result := range []model.AutoTaskResult{{Pending: []model.TaskCandidate{{ID: "pending"}}}, {Duplicates: 2}} {
		t.Run(fmt.Sprint(result.Duplicates), func(t *testing.T) {
			cfg, auto, _, msg := forwardFixture(t)
			auto.result = &result
			notifier := &mockNotifier{}
			cfg.Notifiers = []model.Notifier{notifier}
			msg.ForwardBatch.DeleteFn = func(context.Context, []string) error { t.Fatal("non-created outcome deleted forwards"); return nil }
			if err := New(cfg).Process(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if notifier.calls != 0 {
				t.Fatalf("non-created outcome sent task notifications: %d", notifier.calls)
			}
		})
	}
}

func TestProcessForwardBatch_ProjectHintAndFallback(t *testing.T) {
	for _, found := range []bool{true, false} {
		t.Run(fmt.Sprint(found), func(t *testing.T) {
			cfg, auto, _, msg := forwardFixture(t)
			projects := &mockForwardProjectService{}
			expected := cfg.TaskStore.DefaultProjectID()
			if found {
				projects.resolveRefResult = &model.Project{ID: "project-hint", Name: "Work", Slug: "work"}
				expected = "project-hint"
			} else {
				projects.resolveRefErr = errors.New("not found")
			}
			cfg.Projects = projects
			msg.ForwardBatch.TriggerText = "#work"
			if err := New(cfg).Process(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if auto.submissions[0].ProjectID != expected {
				t.Fatalf("project=%q want %q", auto.submissions[0].ProjectID, expected)
			}
		})
	}
}

func TestProcessForwardBatch_DeleteFailure(t *testing.T) {
	cfg, auto, _, msg := forwardFixture(t)
	msg.ForwardBatch.DeleteFn = func(context.Context, []string) error { return errors.New("Telegram deletion failed") }
	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(auto.processed) != 2 {
		t.Fatal("Telegram deletion failure lost committed receipts")
	}
}

func TestProcessForwardBatch_MissingDependenciesReturnsError(t *testing.T) {
	if err := New(Config{}).Process(context.Background(), model.Message{Kind: model.MessageKindForwardBatch, ForwardBatch: &model.ForwardBatchPayload{ChatID: "owner-chat", Forwards: makeTestForwards("owner-chat", 1)}}); err == nil {
		t.Fatal("missing required persistence must not be silently acknowledged")
	}
}

type observedForwardAutoTasks struct {
	model.AutoTaskService
	submissions []model.AutoTaskSubmission
	results     []model.AutoTaskResult
	committed   bool
}

func (s *observedForwardAutoTasks) Submit(ctx context.Context, submission model.AutoTaskSubmission) (model.AutoTaskResult, error) {
	s.submissions = append(s.submissions, submission)
	result, err := s.AutoTaskService.Submit(ctx, submission)
	if err == nil {
		s.results = append(s.results, result)
		s.committed = true
	}
	return result, err
}

type forwardProjectReadGuard struct {
	model.TaskStore
	auto            *observedForwardAutoTasks
	postCommitReads int
}

func (s *forwardProjectReadGuard) GetProject(ctx context.Context, id string) (*model.Project, error) {
	if s.auto.committed {
		s.postCommitReads++
	}
	return s.TaskStore.GetProject(ctx, id)
}

type forwardCheckerFunc func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error)

func (f forwardCheckerFunc) Compare(ctx context.Context, req model.CreateTaskRequest, text string, refs []model.TaskReference) (model.TaskAssessment, error) {
	return f(ctx, req, text, refs)
}

type forwardFinalizeProbe struct {
	model.ForwardBuffer
	check func(context.Context, *sql.Tx) error
}

func (b forwardFinalizeProbe) DeleteEntriesTx(ctx context.Context, tx *sql.Tx, chatID string, ids []string) error {
	if err := b.ForwardBuffer.DeleteEntriesTx(ctx, tx, chatID, ids); err != nil {
		return err
	}
	return b.check(ctx, tx)
}

type realForwardHarness struct {
	db       *sql.DB
	cfg      Config
	msg      model.Message
	buffer   *storage.SQLiteForwardBuffer
	tasks    *storage.SQLiteTaskStore
	auto     *observedForwardAutoTasks
	projects model.ProjectService
}

func newRealForwardHarness(t *testing.T, checker model.TaskDuplicateChecker) *realForwardHarness {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "real-forward.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	eventStore := events.NewSQLiteEventStore(db)
	deviceStore := devices.NewSQLiteDeviceStore(db)
	queue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})
	autoStore := storage.NewSQLiteAutoTaskStore(db)
	service := usecase.NewAutoTaskService(usecase.AutoTaskServiceDeps{
		DB: db, Tasks: tasks, Events: eventStore, Devices: deviceStore,
		Queue: queue, Broker: broker, Store: autoStore, Checker: checker, Logger: discardLogger,
	})
	auto := &observedForwardAutoTasks{AutoTaskService: service}
	projects := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: db, Tasks: tasks, Meta: storage.NewSQLiteMetaStore(db), AutoTasks: autoStore,
		Events: eventStore, Devices: deviceStore, Queue: queue, Broker: broker,
	})
	buffer := storage.NewSQLiteForwardBuffer(db)
	entries := makeTestForwards("owner-chat", 2)
	for _, entry := range entries {
		if err := buffer.Append(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := i18n.NewBundle("en")
	if err != nil {
		t.Fatal(err)
	}
	return &realForwardHarness{
		db: db, tasks: tasks, auto: auto, projects: projects, buffer: buffer,
		cfg: Config{
			Logger: discardLogger, AutoTasks: auto, Projects: projects, TaskStore: tasks,
			ForwardBuffer: buffer, ForwardClassifier: &mockClassifier{class: model.ClassPromise},
			ForwardExtractor: &mockExtractor{tasks: []model.Task{{Summary: "send slides"}}},
			Localizer:        i18n.NewLocalizer(bundle, "en"),
		},
		msg: model.Message{
			Kind:         model.MessageKindForwardBatch,
			Source:       model.Source{Kind: "telegram", ID: "owner-chat", AccountID: "personal"},
			ForwardBatch: &model.ForwardBatchPayload{ChatID: "owner-chat", Forwards: entries, Counterpart: "Alice", TriggerID: "trigger"},
		},
	}
}

func TestProcessForwardBatch_DeletedDestinationConfirmsActualInbox(t *testing.T) {
	h := newRealForwardHarness(t, nil)
	ctx := context.Background()
	project, err := h.projects.CreateProject(ctx, model.CreateProjectRequest{Name: "Work", Slug: "work"})
	if err != nil {
		t.Fatal(err)
	}
	h.msg.ForwardBatch.TriggerText = "#work"
	h.cfg.ForwardExtractor = forwardExtractorFunc(func(ctx context.Context, _ model.Message, _ []model.HistoryEntry) ([]model.Task, error) {
		if _, err := h.projects.DeleteProject(ctx, project.ID); err != nil {
			return nil, err
		}
		return []model.Task{{Summary: "send slides"}}, nil
	})
	guard := &forwardProjectReadGuard{TaskStore: h.tasks, auto: h.auto}
	h.cfg.TaskStore = guard
	var replies []string
	h.msg.ForwardBatch.ReplyFn = func(_ context.Context, text string) error {
		replies = append(replies, text)
		return nil
	}
	if err := New(h.cfg).Process(ctx, h.msg); err != nil {
		t.Fatal(err)
	}
	if len(h.auto.submissions) != 1 || h.auto.submissions[0].ProjectID != project.ID {
		t.Fatalf("forward submission changed or repeated: %+v", h.auto.submissions)
	}
	if len(h.auto.results) != 1 || len(h.auto.results[0].Created) != 1 || h.auto.results[0].Created[0].ProjectID != h.tasks.DefaultProjectID() {
		t.Fatalf("actual destination is not Inbox: %+v", h.auto.results)
	}
	if guard.postCommitReads != 0 {
		t.Fatalf("confirmation read project after commit %d times", guard.postCommitReads)
	}
	if len(replies) != 2 || replies[0] != "Extracted 1 task to project «Inbox»:\n— send slides" {
		t.Fatalf("confirmation used stale destination: %q", replies)
	}
}

func TestProcessForwardBatch_RealAutoTasksPreserveOriginalKeysAndSnapshotOnReplay(t *testing.T) {
	h := newRealForwardHarness(t, forwardCheckerFunc(func(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
		t.Fatal("single fresh task or replay must not invoke duplicate checker")
		return model.TaskAssessment{}, nil
	}))
	ctx := context.Background()
	project, err := h.projects.CreateProject(ctx, model.CreateProjectRequest{Name: "Work", Slug: "work"})
	if err != nil {
		t.Fatal(err)
	}
	h.msg.ForwardBatch.TriggerText = "#work"
	first := h.msg.ForwardBatch.Forwards[0]
	firstKey := autoInputKey(h.msg.Source, first.MessageID, first.Text, 0)
	if _, err := h.auto.AutoTaskService.Submit(ctx, model.AutoTaskSubmission{InputKeys: []string{firstKey}, ProjectID: project.ID}); err != nil {
		t.Fatal(err)
	}
	newEntry := makeTestForwards("owner-chat", 1)[0]
	newEntry.MessageID = "arrived-during-inference"
	h.cfg.ForwardExtractor = forwardExtractorFunc(func(_ context.Context, msg model.Message, _ []model.HistoryEntry) ([]model.Task, error) {
		if strings.Contains(msg.Text, first.Text) {
			t.Fatal("processed snapshot entry reached extraction")
		}
		if err := h.buffer.Append(ctx, newEntry); err != nil {
			return nil, err
		}
		if _, err := h.projects.DeleteProject(ctx, project.ID); err != nil {
			return nil, err
		}
		return []model.Task{{Summary: "send slides"}}, nil
	})
	finalizations := 0
	h.cfg.ForwardBuffer = forwardFinalizeProbe{ForwardBuffer: h.buffer, check: func(ctx context.Context, tx *sql.Tx) error {
		finalizations++
		for table, want := range map[string]int{"forward_buffer": 1, "auto_task_inputs": 2, "auto_task_candidates": 1, "tasks": 1, "events": 3} {
			var got int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
				return err
			}
			if got != want {
				t.Fatalf("Finalize transaction %s count=%d want %d", table, got, want)
			}
		}
		return nil
	}}
	if err := New(h.cfg).Process(ctx, h.msg); err != nil {
		t.Fatal(err)
	}
	if len(h.auto.results) != 1 || len(h.auto.results[0].Created) != 1 || h.auto.results[0].Created[0].ProjectID != h.tasks.DefaultProjectID() {
		t.Fatalf("snapshot was not committed to Inbox: %+v", h.auto.results)
	}
	wantKeys := make([]string, len(h.msg.ForwardBatch.Forwards))
	for i, entry := range h.msg.ForwardBatch.Forwards {
		wantKeys[i] = autoInputKey(h.msg.Source, entry.MessageID, entry.Text, 0)
		processed, err := h.auto.Processed(ctx, wantKeys[i])
		if err != nil || !processed {
			t.Fatalf("original key %d not committed: processed=%v err=%v", i, processed, err)
		}
	}
	if len(h.auto.submissions) != 1 || strings.Join(h.auto.submissions[0].InputKeys, "\n") != strings.Join(wantKeys, "\n") {
		t.Fatalf("original snapshot identities lost: %+v", h.auto.submissions)
	}
	assertNewArrival := func() {
		t.Helper()
		batch, err := h.buffer.LoadBatch(ctx, "owner-chat")
		if err != nil || len(batch) != 1 || batch[0].MessageID != newEntry.MessageID {
			t.Fatalf("new forward lost: batch=%+v err=%v", batch, err)
		}
		processed, err := h.auto.Processed(ctx, autoInputKey(h.msg.Source, newEntry.MessageID, newEntry.Text, 0))
		if err != nil || processed {
			t.Fatalf("new forward incorrectly receipted: processed=%v err=%v", processed, err)
		}
	}
	assertNewArrival()
	for _, entry := range h.msg.ForwardBatch.Forwards {
		if err := h.buffer.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	h.cfg.ForwardClassifier = forwardClassifierFunc(func(context.Context, model.Message) (model.Classification, error) {
		t.Fatal("committed replay reached classifier")
		return model.ClassSkip, nil
	})
	h.cfg.ForwardExtractor = forwardExtractorFunc(func(context.Context, model.Message, []model.HistoryEntry) ([]model.Task, error) {
		t.Fatal("committed replay reached extractor")
		return nil, nil
	})
	h.msg.ForwardBatch.DeleteFn = func(context.Context, []string) error {
		t.Fatal("replay deleted Telegram messages without new tasks")
		return nil
	}
	if err := New(h.cfg).Process(ctx, h.msg); err != nil {
		t.Fatal(err)
	}
	if finalizations != 2 || len(h.auto.results) != 2 || !h.auto.results[1].Replay {
		t.Fatalf("replay did not atomically finalize: finalizations=%d results=%+v", finalizations, h.auto.results)
	}
	assertNewArrival()
}
