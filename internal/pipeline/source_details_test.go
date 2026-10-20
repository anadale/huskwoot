package pipeline

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// newStoringTaskService returns a real TaskService persisting into db.
func newStoringTaskService(db *sql.DB, store model.TaskStore) model.TaskService {
	return usecase.NewTaskService(usecase.TaskServiceDeps{
		DB:      db,
		Tasks:   store,
		Events:  events.NewSQLiteEventStore(db),
		Devices: devices.NewSQLiteDeviceStore(db),
		Queue:   push.NewSQLitePushQueue(db),
		Broker:  events.NewBroker(events.BrokerConfig{}),
	})
}

// supergroupMsg is a group promise from a Telegram supergroup replying to another message.
func supergroupMsg() model.Message {
	return model.Message{
		ID:         "77",
		Author:     "owner-123",
		AuthorName: "Gregory",
		Text:       "сделаю завтра",
		Timestamp:  time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC),
		Kind:       model.MessageKindGroup,
		Source:     model.Source{Kind: "telegram", ID: "-1005550001", Name: "Команда"},
		ReplyTo:    &model.Message{AuthorName: "Bob", Text: "кто поправит деплой?"},
	}
}

func checkMessageDetails(t *testing.T, got *model.TaskSourceDetails, msg model.Message) {
	t.Helper()
	if got == nil {
		t.Fatal("SourceDetails = nil")
	}
	if got.Via != model.SourceViaMessage {
		t.Errorf("Via = %q, want %q", got.Via, model.SourceViaMessage)
	}
	if got.Name != msg.Source.Name || got.Author != msg.AuthorName || got.Text != msg.Text {
		t.Errorf("name/author/text = %q/%q/%q, want %q/%q/%q",
			got.Name, got.Author, got.Text, msg.Source.Name, msg.AuthorName, msg.Text)
	}
	if got.At == nil || !got.At.Equal(msg.Timestamp) {
		t.Errorf("At = %v, want %v", got.At, msg.Timestamp)
	}
	if got.ReplyTo == nil || got.ReplyTo.Author != "Bob" || got.ReplyTo.Text != "кто поправит деплой?" {
		t.Errorf("ReplyTo = %+v", got.ReplyTo)
	}
	if got.Link != "https://t.me/c/5550001/77" {
		t.Errorf("Link = %q", got.Link)
	}
}

func TestProcess_Group_SubmitsSourceDetails(t *testing.T) {
	auto := &pipelineAutoTasks{}
	cfg := testConfig()
	cfg.AutoTasks = auto
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(&mockExtractor{tasks: []model.Task{{Summary: "поправить деплой"}, {Summary: "обновить доку"}}})
	msg := supergroupMsg()

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if len(auto.submissions) != 1 || len(auto.submissions[0].Tasks) != 2 {
		t.Fatalf("submissions = %+v", auto.submissions)
	}
	for _, req := range auto.submissions[0].Tasks {
		checkMessageDetails(t, req.SourceDetails, msg)
	}
	if auto.submissions[0].Tasks[0].SourceDetails == auto.submissions[0].Tasks[1].SourceDetails {
		t.Error("requests share one SourceDetails pointer")
	}
}

func TestProcess_Group_StoresSourceDetails(t *testing.T) {
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "group.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	auto := &pipelineAutoTasks{tasks: newStoringTaskService(db, store)}
	cfg := testConfig()
	cfg.AutoTasks = auto
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(&mockExtractor{tasks: []model.Task{{Summary: "поправить деплой", Source: model.Source{Kind: "telegram", ID: "-1005550001"}}}})
	notifier := &mockNotifier{}
	cfg.Notifiers = []model.Notifier{notifier}
	msg := supergroupMsg()

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if len(notifier.tasks) != 1 {
		t.Fatalf("created tasks = %d, want 1", len(notifier.tasks))
	}
	stored, err := store.GetTask(context.Background(), notifier.tasks[0].ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: %v %v", stored, err)
	}
	checkMessageDetails(t, stored.SourceDetails, msg)
}

func TestProcess_Batch_SourceDetailsKeepSubject(t *testing.T) {
	auto := &pipelineAutoTasks{}
	cfg := testConfig()
	cfg.Classifiers = groupClassifiers(&mockClassifier{class: model.ClassPromise})
	cfg.Extractors = groupExtractors(&mockExtractor{tasks: []model.Task{{Summary: "подготовить отчёт", Confidence: 0.9}}})
	cfg.AutoTasks = auto
	msg := testBatchMsg()
	msg.Subject = "Отчёт за Q1"
	msg.Timestamp = time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if len(auto.submissions) != 1 || len(auto.submissions[0].Tasks) != 1 {
		t.Fatalf("submissions = %+v, want one email task", auto.submissions)
	}
	got := auto.submissions[0].Tasks[0].SourceDetails
	if got == nil {
		t.Fatal("SourceDetails = nil")
	}
	if got.Subject != "Отчёт за Q1" || got.Name != "Рабочая почта" || got.Author != "boss@example.com" || got.Text != msg.Text {
		t.Errorf("details = %+v", got)
	}
	if got.Link != "" || got.Via != model.SourceViaMessage {
		t.Errorf("link/via = %q/%q", got.Link, got.Via)
	}
	if got.At == nil || !got.At.Equal(msg.Timestamp) {
		t.Errorf("At = %v, want %v", got.At, msg.Timestamp)
	}
}

func TestProcessForwardBatch_SourceDetailsFromMessageIndex(t *testing.T) {
	cfg, auto, _, msg := forwardFixture(t)
	forwards := msg.ForwardBatch.Forwards
	forwards[1].IsSelfAuthored = false
	forwards[1].ForwardFromName = "Bob"
	first, second := forwards[0], forwards[1] // original date order
	// Reverse the payload order: numbering must follow the sorted order, not the input order.
	msg.ForwardBatch.Forwards = []model.ForwardEntry{second, first}
	msg.ForwardBatch.Counterpart = "Bob"
	cfg.ForwardExtractor = &mockExtractor{tasks: []model.Task{
		{Summary: "first", ForwardIndex: 1},
		{Summary: "second", ForwardIndex: 2},
		{Summary: "missing"},
		{Summary: "out of range", ForwardIndex: 3},
	}}

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	reqs := auto.submissions[0].Tasks
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want 4", len(reqs))
	}
	for i, want := range []struct {
		author, text string
		at           time.Time
	}{
		{author: first.ForwardFromName, text: first.Text, at: first.OriginalDate},
		{author: second.ForwardFromName, text: second.Text, at: second.OriginalDate},
		{at: second.OriginalDate},
		{at: second.OriginalDate},
	} {
		got := reqs[i].SourceDetails
		if got == nil {
			t.Fatalf("request %d: SourceDetails = nil", i)
		}
		if got.Via != model.SourceViaForward || got.Name != "Bob" || got.Link != "" {
			t.Errorf("request %d: via/name/link = %q/%q/%q", i, got.Via, got.Name, got.Link)
		}
		if got.Author != want.author || got.Text != want.text {
			t.Errorf("request %d: author/text = %q/%q, want %q/%q", i, got.Author, got.Text, want.author, want.text)
		}
		if got.At == nil || !got.At.Equal(want.at) {
			t.Errorf("request %d: At = %v, want %v", i, got.At, want.at)
		}
		if reqs[i].Source.Kind != "telegram" || reqs[i].Source.ID != "owner-chat" {
			t.Errorf("request %d: Source = %+v", i, reqs[i].Source)
		}
	}
}

func TestProcessForwardBatch_StoresSourceDetails(t *testing.T) {
	cfg, auto, _, msg := forwardFixture(t)
	auto.tasks = newStoringTaskService(auto.db, cfg.TaskStore)
	cfg.ForwardExtractor = &mockExtractor{tasks: []model.Task{{Summary: "send slides", ForwardIndex: 2}}}
	notifier := &mockNotifier{}
	cfg.Notifiers = []model.Notifier{notifier}

	if err := New(cfg).Process(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	created := auto.submissions[0]
	if len(created.Tasks) != 1 {
		t.Fatalf("requests = %d, want 1", len(created.Tasks))
	}
	tasks, err := cfg.TaskStore.ListTasks(context.Background(), cfg.TaskStore.DefaultProjectID(), model.TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("stored tasks = %v, err = %v", tasks, err)
	}
	got := tasks[0].SourceDetails
	entry := msg.ForwardBatch.Forwards[1]
	if got == nil || got.Via != model.SourceViaForward || got.Author != entry.ForwardFromName || got.Text != entry.Text {
		t.Fatalf("stored details = %+v", got)
	}
	if got.At == nil || !got.At.Equal(entry.OriginalDate) {
		t.Errorf("At = %v, want %v", got.At, entry.OriginalDate)
	}
}

func TestForwardSourceDetails_EmptyBatchFallback(t *testing.T) {
	got := forwardSourceDetails(nil, 1, "")
	if got == nil || got.Via != model.SourceViaForward || got.At != nil || got.Author != "" || got.Text != "" {
		t.Fatalf("details = %+v", got)
	}
}
