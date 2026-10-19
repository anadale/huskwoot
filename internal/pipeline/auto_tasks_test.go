package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/anadale/huskwoot/internal/model"
)

// pipelineAutoTasks models the service boundary, including durable input receipts.
type pipelineAutoTasks struct {
	model.AutoTaskService
	tasks        model.TaskService
	processed    map[string]bool
	submissions  []model.AutoTaskSubmission
	result       *model.AutoTaskResult
	err          error
	processedErr error
	db           *sql.DB
}

func (s *pipelineAutoTasks) Processed(_ context.Context, key string) (bool, error) {
	return s.processed[key], s.processedErr
}

func (s *pipelineAutoTasks) Submit(ctx context.Context, in model.AutoTaskSubmission) (model.AutoTaskResult, error) {
	s.submissions = append(s.submissions, in)
	if s.err != nil {
		return model.AutoTaskResult{}, s.err
	}
	result := model.AutoTaskResult{}
	if s.result != nil {
		result = *s.result
	} else if s.tasks != nil {
		var err error
		result.Created, err = s.tasks.CreateTasks(ctx, model.CreateTasksRequest{ProjectID: in.ProjectID, Tasks: in.Tasks})
		if err != nil {
			return result, err
		}
	} else {
		for _, r := range in.Tasks {
			result.Created = append(result.Created, model.Task{Summary: r.Summary, Details: r.Details, Topic: r.Topic, Deadline: r.Deadline, Priority: r.Priority, Source: r.Source, SourceDetails: r.SourceDetails, ProjectID: in.ProjectID})
		}
	}
	if in.Finalize != nil {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return model.AutoTaskResult{}, err
		}
		defer tx.Rollback()
		if err := in.Finalize(ctx, tx); err != nil {
			return model.AutoTaskResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.AutoTaskResult{}, err
		}
	}
	if s.processed == nil {
		s.processed = make(map[string]bool)
	}
	for _, key := range in.InputKeys {
		s.processed[key] = true
	}
	return result, nil
}

func TestProcessGroup_ReplaySkipsInferenceAndEditedTextIsNewInput(t *testing.T) {
	auto := &pipelineAutoTasks{}
	extractor := &mockExtractor{tasks: testTasks()}
	notifier := &mockNotifier{}
	cfg := testConfig()
	cfg.AutoTasks = auto
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(extractor)
	cfg.Notifiers = []model.Notifier{notifier}
	p := New(cfg)
	msg := testGroupMsg("owner-123")
	for range 2 {
		if err := p.Process(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if extractor.calls != 1 || notifier.calls != 1 {
		t.Fatalf("replay repeated inference/notification: %d/%d", extractor.calls, notifier.calls)
	}
	original := auto.submissions[0].InputKeys[0]
	msg.Text += " вечером"
	if err := p.Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if extractor.calls != 2 || auto.submissions[1].InputKeys[0] == original {
		t.Fatal("edited input did not get a new receipt identity")
	}
}

func TestProcessGroup_OnlyCreatedTasksAreNotified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result model.AutoTaskResult
		want   int
	}{
		{name: "pending", result: model.AutoTaskResult{Pending: []model.TaskCandidate{{ID: "pending"}}}},
		{name: "duplicate", result: model.AutoTaskResult{Duplicates: 2}},
		{name: "same batch", result: model.AutoTaskResult{Created: []model.Task{{Summary: "one obligation"}}, Duplicates: 1}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auto := &pipelineAutoTasks{result: &tc.result}
			notifier := &mockNotifier{}
			cfg := testConfig()
			cfg.AutoTasks = auto
			cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
			cfg.Extractors = groupExtractors(&mockExtractor{tasks: []model.Task{{Summary: "first"}, {Summary: "same obligation"}}})
			cfg.Notifiers = []model.Notifier{notifier}
			p := New(cfg)
			tracker := &reactTracker{}
			msg := testGroupMsg("owner-123")
			msg.ReactFn = tracker.fn
			msg.ReplyFn = func(context.Context, string) error {
				t.Fatal("automatic extraction must not send a review notice into the group")
				return nil
			}
			if err := p.Process(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if len(notifier.tasks) != tc.want || notifier.calls != tc.want {
				t.Fatalf("notified %d tasks in %d calls", len(notifier.tasks), notifier.calls)
			}
			want := ""
			if tc.want > 0 {
				want = "👍"
			}
			if tracker.emojis[len(tracker.emojis)-1] != want {
				t.Fatalf("incorrect success reaction: %v", tracker.emojis)
			}
		})
	}
}

func TestProcessGroup_ReceiptAndSubmitFailuresPropagate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lookup bool
	}{{"lookup", true}, {"submit", false}} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New("receipt storage failed")
			auto := &pipelineAutoTasks{}
			if tc.lookup {
				auto.processedErr = sentinel
			} else {
				auto.err = sentinel
			}
			cfg := testConfig()
			cfg.AutoTasks = auto
			cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
			cfg.Extractors = groupExtractors(&mockExtractor{tasks: testTasks()})
			if err := New(cfg).Process(context.Background(), testGroupMsg("owner-123")); !errors.Is(err, sentinel) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestProcessGroup_ReceiptIdentitySeparatesAccountsChatsAndMessages(t *testing.T) {
	auto := &pipelineAutoTasks{}
	cfg := testConfig()
	cfg.AutoTasks = auto
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(&mockExtractor{tasks: testTasks()})
	p := New(cfg)
	original := testGroupMsg("owner-123")
	variants := []model.Message{original, original, original, original}
	variants[1].Source.AccountID = "another-account"
	variants[2].Source.ID = "another-chat"
	variants[3].ID = "another-message"
	for _, msg := range variants {
		if err := p.Process(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if len(auto.processed) != len(variants) {
		t.Fatalf("source identities collided: %v", auto.processed)
	}
}
