package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

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

func TestProcess_Batch_SubmitsToAutoTasks(t *testing.T) {
	msg := testBatchMsg()
	msg.ID = "42"
	msg.IMAPUIDValidity = 100
	msg.Subject = "Restore the website"
	msg.AuthorName = "Project owner"
	msg.Timestamp = time.Date(2026, time.October, 20, 9, 0, 0, 0, time.UTC)
	deadline := msg.Timestamp.Add(24 * time.Hour)
	extracted := []model.Task{{
		Summary: "Restore the website", Details: "Move the backup to another VM",
		Topic: msg.Subject, Deadline: &deadline, Priority: model.TaskPriority(2),
		Source: msg.Source,
	}}
	created := model.Task{ID: "created-task", Summary: "Accepted obligation", ProjectID: "website"}
	auto := &pipelineAutoTasks{result: &model.AutoTaskResult{
		Created:    []model.Task{created},
		Pending:    []model.TaskCandidate{{ID: "pending-candidate"}},
		Duplicates: 1,
	}}
	direct := &mockTaskService{}
	notifier := &mockNotifier{}
	cfg := testConfig()
	cfg.AutoTasks = auto
	cfg.Tasks = direct
	cfg.Projects = &mockProjectService{resolveID: "website"}
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(&mockExtractor{tasks: extracted})
	cfg.Notifiers = []model.Notifier{notifier}

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(auto.submissions) != 1 {
		t.Fatalf("automatic email submissions = %d, want 1", len(auto.submissions))
	}
	submission := auto.submissions[0]
	wantKey := autoInputKey(msg.Source, msg.ID, msg.Text, msg.IMAPUIDValidity)
	if !reflect.DeepEqual(submission.InputKeys, []string{wantKey}) {
		t.Errorf("input keys = %v, want [%s]", submission.InputKeys, wantKey)
	}
	if submission.ProjectID != "website" || submission.SourceText != msg.Text {
		t.Errorf("submission lost project/source text: %+v", submission)
	}
	wantRequests := []model.CreateTaskRequest{{
		ProjectID: "website", Summary: extracted[0].Summary, Details: extracted[0].Details,
		Topic: msg.Subject, Deadline: &deadline, Priority: extracted[0].Priority,
		Source: msg.Source, SourceDetails: model.SourceDetailsFromMessage(msg, model.SourceViaMessage),
	}}
	if !reflect.DeepEqual(submission.Tasks, wantRequests) {
		t.Errorf("task requests = %+v, want %+v", submission.Tasks, wantRequests)
	}
	if len(direct.createdTasks) != 0 {
		t.Errorf("direct TaskService created %d tasks", len(direct.createdTasks))
	}
	created.SourceMessage = msg
	if notifier.calls != 1 || !reflect.DeepEqual(notifier.tasks, []model.Task{created}) {
		t.Errorf("notified tasks = %+v in %d calls, want only the created result", notifier.tasks, notifier.calls)
	}
}

func TestProcessBatch_ReplayAndMailboxGenerationTransition(t *testing.T) {
	msg := testBatchMsg()
	msg.ID = "42"
	msg.IMAPUIDValidity = 100
	msg.Subject = "Website recovery"
	msg.Timestamp = time.Date(2026, time.October, 20, 10, 0, 0, 0, time.UTC)
	auto := &pipelineAutoTasks{}
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: []model.Task{{
		Summary: "Restore the website", Topic: msg.Subject, Source: msg.Source,
	}}}
	projects := &mockProjectService{resolveID: "website"}
	notifier := &mockNotifier{}
	direct := &mockTaskService{}
	cfg := testConfig()
	cfg.AutoTasks = auto
	cfg.Tasks = direct
	cfg.Projects = projects
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	cfg.Notifiers = []model.Notifier{notifier}
	p := New(cfg)

	for _, generation := range []uint32{100, 100, 200, 200} {
		msg.IMAPUIDValidity = generation
		if err := p.Process(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		want := 1
		if generation == 200 {
			want = 2
		}
		if classifier.calls != want || extractor.calls != want ||
			len(auto.submissions) != want || notifier.calls != want || len(auto.processed) != want {
			t.Fatalf("generation %d repeated or skipped committed effects: classifier=%d extractor=%d submissions=%d notifications=%d receipts=%d",
				generation, classifier.calls, extractor.calls, len(auto.submissions), notifier.calls, len(auto.processed))
		}
	}
	if auto.submissions[0].InputKeys[0] == auto.submissions[1].InputKeys[0] {
		t.Fatal("mailbox generations reused the same receipt")
	}
	if !reflect.DeepEqual(projects.resolvedChannels, []string{msg.Source.ID, msg.Source.ID}) {
		t.Errorf("mailbox project binding changed: %v", projects.resolvedChannels)
	}
	for i, generation := range []uint32{100, 200} {
		submission := auto.submissions[i]
		if submission.ProjectID != "website" || submission.SourceText != msg.Text || len(submission.Tasks) != 1 {
			t.Fatalf("generation %d lost email submission context: %+v", generation, submission)
		}
		request := submission.Tasks[0]
		if request.ProjectID != "website" || request.Source != msg.Source ||
			request.Topic != msg.Subject ||
			!reflect.DeepEqual(request.SourceDetails, model.SourceDetailsFromMessage(msg, model.SourceViaMessage)) {
			t.Errorf("generation %d changed project/source/provenance: %+v", generation, request)
		}
		notified := notifier.tasks[i]
		wantMessage := msg
		wantMessage.IMAPUIDValidity = generation
		if notified.ProjectID != "website" || notified.Source != msg.Source ||
			!reflect.DeepEqual(notified.SourceDetails, request.SourceDetails) ||
			!reflect.DeepEqual(notified.SourceMessage, wantMessage) {
			t.Errorf("generation %d lost notified email provenance: %+v", generation, notified)
		}
	}
	if len(direct.createdTasks) != 0 {
		t.Fatalf("automatic email used the direct TaskService path: %+v", direct.createdTasks)
	}
}

func TestProcess_AutoTasks_SkipAndEmptyExtractionCommitReceipts(t *testing.T) {
	for _, kind := range []model.MessageKind{model.MessageKindBatch, model.MessageKindGroup} {
		for _, class := range []model.Classification{model.ClassSkip, model.ClassPromise} {
			t.Run(string(kind)+"/"+class.String(), func(t *testing.T) {
				msg := testBatchMsg()
				msg.IMAPUIDValidity = 100
				if kind == model.MessageKindGroup {
					msg = testGroupMsg("owner-123")
				}
				auto := &pipelineAutoTasks{}
				classifier := &mockClassifier{class: class}
				extractor := &mockExtractor{}
				notifier := &mockNotifier{}
				cfg := testConfig()
				cfg.AutoTasks = auto
				cfg.Classifiers = groupClassifiers(classifier)
				cfg.Extractors = groupExtractors(extractor)
				cfg.Notifiers = []model.Notifier{notifier}
				p := New(cfg)
				for range 2 {
					if err := p.Process(context.Background(), msg); err != nil {
						t.Fatal(err)
					}
				}
				wantExtractions := 0
				if class == model.ClassPromise {
					wantExtractions = 1
				}
				if classifier.calls != 1 || extractor.calls != wantExtractions || len(auto.submissions) != 1 {
					t.Fatalf("receipt replay repeated inference: classifier=%d extractor=%d submissions=%d",
						classifier.calls, extractor.calls, len(auto.submissions))
				}
				submission := auto.submissions[0]
				key := autoInputKey(msg.Source, msg.ID, msg.Text, msg.IMAPUIDValidity)
				if !reflect.DeepEqual(submission.InputKeys, []string{key}) ||
					submission.SourceText != msg.Text || len(submission.Tasks) != 0 || !auto.processed[key] {
					t.Errorf("empty input receipt was not committed: %+v", submission)
				}
				if notifier.calls != 0 {
					t.Fatalf("empty automatic submission notified %d times", notifier.calls)
				}
			})
		}
	}
}

func TestProcessBatch_ReceiptAndSubmitFailuresPropagate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		class  model.Classification
		empty  bool
		lookup bool
	}{
		{name: "lookup", class: model.ClassPromise, lookup: true},
		{name: "promise submission", class: model.ClassPromise},
		{name: "skip submission", class: model.ClassSkip},
		{name: "empty extraction submission", class: model.ClassPromise, empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New("receipt storage failed")
			auto := &pipelineAutoTasks{}
			if tc.lookup {
				auto.processedErr = sentinel
			} else {
				auto.err = sentinel
			}
			classifier := &mockClassifier{class: tc.class}
			extractor := &mockExtractor{tasks: testTasks()}
			if tc.empty {
				extractor.tasks = nil
			}
			notifier := &mockNotifier{}
			direct := &mockTaskService{}
			cfg := testConfig()
			cfg.AutoTasks = auto
			cfg.Tasks = direct
			cfg.Classifiers = groupClassifiers(classifier)
			cfg.Extractors = groupExtractors(extractor)
			cfg.Notifiers = []model.Notifier{notifier}
			msg := testBatchMsg()
			msg.IMAPUIDValidity = 100
			p := New(cfg)
			if err := p.Process(context.Background(), msg); !errors.Is(err, sentinel) {
				t.Fatalf("Process error = %v, want storage failure", err)
			}
			if tc.lookup && (classifier.calls != 0 || extractor.calls != 0 || len(auto.submissions) != 0) {
				t.Fatal("failed receipt lookup reached inference or submission")
			}
			if len(auto.processed) != 0 || notifier.calls != 0 || len(direct.createdTasks) != 0 {
				t.Fatal("failed automatic input committed a receipt, notified, or used direct creation")
			}
			auto.processedErr = nil
			auto.err = nil
			for range 2 {
				if err := p.Process(context.Background(), msg); err != nil {
					t.Fatal(err)
				}
			}
			wantAttempts := 2
			if tc.lookup {
				wantAttempts = 1
			}
			wantExtractions := wantAttempts
			wantNotifications := 1
			if tc.class == model.ClassSkip {
				wantExtractions = 0
			}
			if tc.class == model.ClassSkip || tc.empty {
				wantNotifications = 0
			}
			if classifier.calls != wantAttempts || extractor.calls != wantExtractions ||
				len(auto.submissions) != wantAttempts || len(auto.processed) != 1 ||
				notifier.calls != wantNotifications {
				t.Fatalf("retry/replay effects: classifier=%d extractor=%d submissions=%d receipts=%d notifications=%d",
					classifier.calls, extractor.calls, len(auto.submissions), len(auto.processed), notifier.calls)
			}
		})
	}
}

func TestProcessBatch_OnlyCreatedTasksAreNotified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result model.AutoTaskResult
	}{
		{name: "pending", result: model.AutoTaskResult{Pending: []model.TaskCandidate{{ID: "pending"}}}},
		{name: "duplicate", result: model.AutoTaskResult{Duplicates: 2}},
		{name: "created", result: model.AutoTaskResult{Created: []model.Task{{ID: "created", Summary: "New obligation"}}}},
		{name: "mixed", result: model.AutoTaskResult{
			Created: []model.Task{{ID: "created", Summary: "New obligation"}},
			Pending: []model.TaskCandidate{{ID: "pending"}}, Duplicates: 1,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auto := &pipelineAutoTasks{result: &tc.result}
			direct := &mockTaskService{}
			notifier := &mockNotifier{}
			cfg := testConfig()
			cfg.AutoTasks = auto
			cfg.Tasks = direct
			cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
			cfg.Extractors = groupExtractors(&mockExtractor{tasks: []model.Task{
				{Summary: "First extracted obligation"}, {Summary: "Second extracted obligation"},
			}})
			cfg.Notifiers = []model.Notifier{notifier}
			p := New(cfg)
			msg := testBatchMsg()
			for range 2 {
				if err := p.Process(context.Background(), msg); err != nil {
					t.Fatal(err)
				}
			}
			want := append([]model.Task(nil), tc.result.Created...)
			for i := range want {
				want[i].SourceMessage = msg
			}
			wantCalls := 0
			if len(want) > 0 {
				wantCalls = 1
			}
			if notifier.calls != wantCalls || !reflect.DeepEqual(notifier.tasks, want) {
				t.Errorf("notifications = %+v in %d calls, want %+v in %d calls",
					notifier.tasks, notifier.calls, want, wantCalls)
			}
			if len(auto.submissions) != 1 || len(direct.createdTasks) != 0 {
				t.Fatal("automatic decisions were resubmitted or bypassed")
			}
		})
	}
}

func TestProcessGroup_WithoutAutoTasksFailsBeforeInference(t *testing.T) {
	classifier := &mockClassifier{class: model.ClassPromise}
	extractor := &mockExtractor{tasks: testTasks()}
	cfg := testConfig()
	cfg.AutoTasks = nil
	cfg.Classifiers = groupClassifiers(classifier)
	cfg.Extractors = groupExtractors(extractor)
	if err := New(cfg).Process(context.Background(), testGroupMsg("owner-123")); err == nil {
		t.Fatal("group message without AutoTaskService was accepted")
	}
	if classifier.calls != 0 || extractor.calls != 0 {
		t.Fatal("group message without AutoTaskService reached inference")
	}
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

func TestAutoInputKey_IMAPMailboxGenerations(t *testing.T) {
	source := model.Source{Kind: "imap", ID: "owner@example.com:INBOX", Name: "Inbox"}
	receipts := make(map[string]bool)
	for _, generation := range []uint32{0, 100, 200, ^uint32(0)} {
		key := autoInputKey(source, "42", "Restore the website", generation)
		if key != autoInputKey(source, "42", "Restore the website", generation) {
			t.Fatalf("generation %d did not retain its receipt identity", generation)
		}
		if receipts[key] {
			t.Fatalf("generation %d reused another mailbox generation's receipt", generation)
		}
		receipts[key] = true

		for _, tc := range []struct {
			name      string
			source    model.Source
			messageID string
			text      string
		}{
			{name: "another UID", source: source, messageID: "43", text: "Restore the website"},
			{name: "edited body", source: source, messageID: "42", text: "Restore the website today"},
			{name: "another mailbox", source: model.Source{Kind: "imap", ID: "owner@example.com:Archive"}, messageID: "42", text: "Restore the website"},
		} {
			if key == autoInputKey(tc.source, tc.messageID, tc.text, generation) {
				t.Errorf("generation %d: %s collided with the original receipt", generation, tc.name)
			}
		}
		renamed := source
		renamed.Name = "Renamed inbox"
		if key != autoInputKey(renamed, "42", "Restore the website", generation) {
			t.Errorf("generation %d: changing the mailbox display name changed its identity", generation)
		}
	}
}

func TestAutoInputKey_NonIMAPReceiptBytesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		source    model.Source
		messageID string
		want      string
	}{
		{
			name:      "Telegram group",
			source:    model.Source{Kind: "telegram", AccountID: "bot", ID: "-10042"},
			messageID: "42",
			want:      `"telegram"/"bot"/"-10042"/"42"/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
		},
		{
			name:      "Telegram forward",
			source:    model.Source{Kind: "telegram", ID: "owner-chat"},
			messageID: "17",
			want:      `"telegram"/""/"owner-chat"/"17"/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
		},
		{
			name:      "client",
			source:    model.Source{Kind: "client", ID: "device"},
			messageID: "42",
			want:      `"client"/""/"device"/"42"/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, generation := range []uint32{0, 100, 200} {
				if got := autoInputKey(tc.source, tc.messageID, "", generation); got != tc.want {
					t.Errorf("generation %d changed the existing receipt bytes:\ngot  %s\nwant %s", generation, got, tc.want)
				}
			}
		})
	}
}

func TestWatchHandler_QueuedMessagesAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		kind             model.MessageKind
		withoutAutoTasks bool
	}{
		{name: "email without service", kind: model.MessageKindBatch, withoutAutoTasks: true},
		{name: "DM", kind: model.MessageKindDM},
		{name: "group direct", kind: model.MessageKindGroupDirect},
		{name: "forward batch", kind: model.MessageKindForwardBatch},
		{name: "DM without service", kind: model.MessageKindDM, withoutAutoTasks: true},
		{name: "group direct without service", kind: model.MessageKindGroupDirect, withoutAutoTasks: true},
		{name: "forward batch without service", kind: model.MessageKindForwardBatch, withoutAutoTasks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auto := &pipelineAutoTasks{processedErr: errors.New("must not process synchronously")}
			cfg := testConfig()
			cfg.AutoTasks = auto
			if tc.withoutAutoTasks {
				cfg.AutoTasks = nil
			}
			classifier := &mockClassifier{class: model.ClassPromise}
			cfg.Classifiers = groupClassifiers(classifier)
			p := New(cfg)
			msg := testBatchMsg()
			msg.Kind = tc.kind
			queue := make(chan model.Message, 1)
			if err := p.WatchHandler(queue)(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if len(queue) != 1 || classifier.calls != 0 || len(auto.submissions) != 0 {
				t.Fatalf("queued message processed inline: queued=%d classifications=%d submissions=%d",
					len(queue), classifier.calls, len(auto.submissions))
			}
			if got := <-queue; !reflect.DeepEqual(got, msg) {
				t.Errorf("queued message changed: got %+v want %+v", got, msg)
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			blockedQueue := make(chan model.Message)
			if err := p.WatchHandler(blockedQueue)(ctx, msg); !errors.Is(err, context.Canceled) {
				t.Errorf("blocked queue cancellation = %v, want context.Canceled", err)
			}
		})
	}
}

func TestWatchHandler_GroupWithoutAutoTasksReturnsError(t *testing.T) {
	cfg := testConfig()
	cfg.AutoTasks = nil
	queue := make(chan model.Message, 1)
	err := New(cfg).WatchHandler(queue)(context.Background(), testGroupMsg("owner-123"))
	if err == nil || err.Error() != "processing group message: AutoTaskService is not configured" {
		t.Errorf("missing-service group error = %v", err)
	}
	if len(queue) != 0 {
		t.Error("missing-service group was acknowledged by enqueueing")
	}
}

func TestWatchHandler_AutomaticInputFailuresReturnBeforeAcknowledgement(t *testing.T) {
	for _, kind := range []model.MessageKind{model.MessageKindGroup, model.MessageKindBatch} {
		for _, lookup := range []bool{false, true} {
			name := string(kind) + "/submit"
			if lookup {
				name = string(kind) + "/receipt"
			}
			t.Run(name, func(t *testing.T) {
				sentinel := errors.New("durable intake failed")
				auto := &pipelineAutoTasks{}
				if lookup {
					auto.processedErr = sentinel
				} else {
					auto.err = sentinel
				}
				cfg := testConfig()
				cfg.AutoTasks = auto
				cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
				cfg.Extractors = groupExtractors(&mockExtractor{tasks: testTasks()})
				queue := make(chan model.Message, 1)
				msg := testGroupMsg("owner-123")
				if kind == model.MessageKindBatch {
					msg = testBatchMsg()
				}
				if err := New(cfg).WatchHandler(queue)(context.Background(), msg); !errors.Is(err, sentinel) {
					t.Errorf("callback error = %v, want durable failure", err)
				}
				if len(queue) != 0 {
					t.Error("automatic input was queued before durable processing")
				}
			})
		}
	}
}
