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

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
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

type failingForwardBuffer struct {
	model.ForwardBuffer
	err error
}

func (b failingForwardBuffer) DeleteEntriesTx(ctx context.Context, tx *sql.Tx, chatID string, ids []string) error {
	if err := b.ForwardBuffer.DeleteEntriesTx(ctx, tx, chatID, ids); err != nil {
		return err
	}
	return b.err
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

func TestProcessForwardBatch_FinalizeFailureRollsBackSnapshot(t *testing.T) {
	cfg, auto, buf, msg := forwardFixture(t)
	sentinel := errors.New("finalize failed")
	cfg.ForwardBuffer = failingForwardBuffer{ForwardBuffer: buf, err: sentinel}
	if err := New(cfg).Process(context.Background(), msg); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	batch, err := buf.LoadBatch(context.Background(), "owner-chat")
	if err != nil || len(batch) != 2 || len(auto.processed) != 0 {
		t.Fatalf("non-atomic finalize: %v %v receipts=%v", batch, err, auto.processed)
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
	auto.processed = map[string]bool{autoInputKey(msg.Source, first.MessageID, first.Text): true}
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
