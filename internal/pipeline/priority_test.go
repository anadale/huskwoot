package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// staticCompleter returns a fixed model response.
type staticCompleter struct {
	response string
}

func (c staticCompleter) Complete(context.Context, string, string) (string, error) {
	return c.response, nil
}

func TestProcess_Group_UrgentPromiseCreatesHighPriorityTask(t *testing.T) {
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// The reference set is empty, so the service creates the task without a checker call.
	autoTasks := usecase.NewAutoTaskService(usecase.AutoTaskServiceDeps{
		DB:      db,
		Tasks:   store,
		Events:  events.NewSQLiteEventStore(db),
		Devices: devices.NewSQLiteDeviceStore(db),
		Queue:   push.NewSQLitePushQueue(db),
		Broker:  events.NewBroker(events.BrokerConfig{}),
		Store:   storage.NewSQLiteAutoTaskStore(db),
	})
	extractor, err := ai.NewTaskExtractor(staticCompleter{
		response: `[{"summary":"поправить деплой на проде","context":"","topic":"","project":"","deadline":null,"priority":3,"confidence":0.9}]`,
	}, ai.ExtractorConfig{UserName: "Gregory", Now: func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.AutoTasks = autoTasks
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(extractor)
	notifier := &mockNotifier{}
	cfg.Notifiers = []model.Notifier{notifier}
	msg := supergroupMsg()
	msg.Text = "срочно поправлю деплой"

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if len(notifier.tasks) != 1 {
		t.Fatalf("created tasks = %d, want 1", len(notifier.tasks))
	}
	if notifier.tasks[0].Priority != model.TaskPriorityHigh {
		t.Errorf("notified Priority = %d, want %d", notifier.tasks[0].Priority, model.TaskPriorityHigh)
	}
	stored, err := store.GetTask(context.Background(), notifier.tasks[0].ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: %v %v", stored, err)
	}
	if stored.Priority != model.TaskPriorityHigh {
		t.Errorf("stored Priority = %d, want %d", stored.Priority, model.TaskPriorityHigh)
	}
}

func TestProcess_PassesExtractedPriority(t *testing.T) {
	tests := []struct {
		name string
		msg  model.Message
	}{
		{name: "group", msg: supergroupMsg()},
		{name: "batch", msg: testBatchMsg()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auto := &pipelineAutoTasks{}
			taskSvc := &mockTaskService{}
			cfg := testConfig()
			cfg.AutoTasks = auto
			cfg.Tasks = taskSvc
			cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
			cfg.Extractors = groupExtractors(&mockExtractor{tasks: []model.Task{
				{Summary: "поправить деплой", Priority: model.TaskPriorityHigh, Confidence: 0.9},
				{Summary: "обновить доку", Confidence: 0.9},
			}})

			if err := New(cfg).Process(context.Background(), tc.msg); err != nil {
				t.Fatal(err)
			}

			var got []model.TaskPriority
			if tc.msg.Kind == model.MessageKindGroup {
				if len(auto.submissions) != 1 {
					t.Fatalf("submissions = %d, want 1", len(auto.submissions))
				}
				for _, req := range auto.submissions[0].Tasks {
					got = append(got, req.Priority)
				}
			} else {
				taskSvc.mu.Lock()
				for _, task := range taskSvc.createdTasks {
					got = append(got, task.Priority)
				}
				taskSvc.mu.Unlock()
			}
			if len(got) != 2 || got[0] != model.TaskPriorityHigh || got[1] != model.TaskPriorityNone {
				t.Errorf("priorities = %v, want [3 0]", got)
			}
		})
	}
}

func TestProcessForwardBatch_PassesExtractedPriority(t *testing.T) {
	cfg, auto, _, msg := forwardFixture(t)
	cfg.ForwardExtractor = &mockExtractor{tasks: []model.Task{
		{Summary: "send slides", ForwardIndex: 1, Priority: model.TaskPriorityHigh},
		{Summary: "send report", ForwardIndex: 2},
	}}

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if len(auto.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(auto.submissions))
	}
	reqs := auto.submissions[0].Tasks
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[0].Priority != model.TaskPriorityHigh || reqs[1].Priority != model.TaskPriorityNone {
		t.Errorf("priorities = %d/%d, want 3/0", reqs[0].Priority, reqs[1].Priority)
	}
}
