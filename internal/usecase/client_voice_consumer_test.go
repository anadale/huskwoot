package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/transcribe"
	"github.com/anadale/huskwoot/internal/usecase"
)

// ---- mock types specific to clientVoiceConsumer tests ----

type mockHistory struct {
	mu              sync.Mutex
	entries         map[string][]model.HistoryEntry // source → entries
	addTxErr        error
	addTxFailAfterN int // if addTxErr set: succeed for first N calls, then fail; 0 = fail immediately
	addTxCallCount  int
	addErr          error
	calls           []string // call log for ordering verification
}

func newMockHistory() *mockHistory {
	return &mockHistory{entries: make(map[string][]model.HistoryEntry)}
}

func (h *mockHistory) Add(_ context.Context, source string, entry model.HistoryEntry) error {
	if h.addErr != nil {
		return h.addErr
	}
	h.mu.Lock()
	h.entries[source] = append(h.entries[source], entry)
	h.calls = append(h.calls, "Add")
	h.mu.Unlock()
	return nil
}

func (h *mockHistory) AddTx(_ context.Context, _ *sql.Tx, source string, entry model.HistoryEntry) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.addTxErr != nil {
		if h.addTxCallCount >= h.addTxFailAfterN {
			return "", h.addTxErr
		}
	}
	h.addTxCallCount++
	h.entries[source] = append(h.entries[source], entry)
	h.calls = append(h.calls, "AddTx:"+entry.AuthorName)
	return "msg-uuid-" + entry.AuthorName, nil
}

func (h *mockHistory) Recent(_ context.Context, source string, _ int) ([]model.HistoryEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]model.HistoryEntry, len(h.entries[source]))
	copy(out, h.entries[source])
	return out, nil
}

func (h *mockHistory) RecentActivity(_ context.Context, _ string, _ time.Duration, _ int) ([]model.HistoryEntry, error) {
	return nil, nil
}

type mockChatServiceForVoice struct {
	mu       sync.Mutex
	reply    model.ChatReply
	err      error
	received []model.Message
}

func (s *mockChatServiceForVoice) HandleMessage(_ context.Context, msg model.Message) (model.ChatReply, error) {
	s.mu.Lock()
	s.received = append(s.received, msg)
	s.mu.Unlock()
	return s.reply, s.err
}

func (s *mockChatServiceForVoice) last() model.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.received) == 0 {
		return model.Message{}
	}
	return s.received[len(s.received)-1]
}

// ---- helpers ----

func makeClientVoiceJob() model.VoiceJob {
	return model.VoiceJob{
		ID:          1,
		SourceKind:  "client",
		Payload:     "2026/05/abc.ogg",
		MimeType:    "audio/ogg",
		Duration:    5 * time.Second,
		ReceivedAt:  time.Unix(1700000000, 0),
		DeviceID:    "device-uuid-1",
		ClientJobID: "client-job-uuid-1",
		ProjectID:   "",
	}
}

func makeClientVoiceConsumer(
	t *testing.T,
	db *sql.DB,
	hist model.History,
	evs model.EventStore,
	broker model.Broker,
	chat model.ChatService,
	tasks model.TaskStore,
) model.VoiceConsumer {
	t.Helper()
	if db == nil {
		db = openTestDB(t)
	}
	return usecase.NewClientVoiceConsumer(usecase.ClientVoiceConsumerDeps{
		DB:      db,
		History: hist,
		Events:  evs,
		Broker:  broker,
		Chat:    chat,
		Tasks:   tasks,
	})
}

// ---- tests ----

// TestClientVoiceConsumer_Consume_HappyPath verifies the full two-transaction flow:
// user event → ChatService → agent event, in correct order, with Broker notified twice.
func TestClientVoiceConsumer_Consume_HappyPath(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "понял"}}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()

	if err := consumer.Consume(context.Background(), job, "создай задачу"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	// History: user + agent in correct order
	calls := hist.calls
	if len(calls) != 2 {
		t.Fatalf("AddTx call count=%d, want 2; calls=%v", len(calls), calls)
	}
	if calls[0] != "AddTx:"+job.DeviceID {
		t.Errorf("first AddTx author=%q, want device-id %q", calls[0], job.DeviceID)
	}
	if calls[1] != "AddTx:agent" {
		t.Errorf("second AddTx author=%q, want agent", calls[1])
	}

	// Events: 2 chat_message_created
	recorded := evs.recorded()
	chatEvs := filterKind(recorded, model.EventChatMessageCreated)
	if len(chatEvs) != 2 {
		t.Fatalf("chat_message_created events=%d, want 2", len(chatEvs))
	}

	// Broker notified twice
	notified := broker.notifiedEvents()
	chatNotified := filterKind(notified, model.EventChatMessageCreated)
	if len(chatNotified) != 2 {
		t.Fatalf("broker notified %d chat_message_created, want 2", len(chatNotified))
	}

	// ChatService called once
	if len(chat.received) != 1 {
		t.Fatalf("chat.HandleMessage calls=%d, want 1", len(chat.received))
	}
}

// TestClientVoiceConsumer_Consume_AudioJobID verifies that the chat_message_created payload
// has audioJobId equal to job.ClientJobID (not job.ID).
func TestClientVoiceConsumer_Consume_AudioJobID(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()
	job.ClientJobID = "my-client-job-uuid"
	job.ID = 42 // internal PK, must NOT appear in payload

	if err := consumer.Consume(context.Background(), job, "позвони маме"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	recorded := evs.recorded()
	chatEvs := filterKind(recorded, model.EventChatMessageCreated)
	if len(chatEvs) < 1 {
		t.Fatal("no chat_message_created events")
	}

	var payload struct {
		AudioJobID string `json:"audioJobId"`
	}
	if err := json.Unmarshal(chatEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.AudioJobID != "my-client-job-uuid" {
		t.Errorf("audioJobId=%q, want %q", payload.AudioJobID, "my-client-job-uuid")
	}
}

// TestClientVoiceConsumer_Consume_SourceID verifies the history source uses "client:<deviceID>".
func TestClientVoiceConsumer_Consume_SourceID(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()
	job.DeviceID = "my-device"

	if err := consumer.Consume(context.Background(), job, "текст"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	// Check that history entries were stored under "client:my-device"
	entries, err := hist.Recent(context.Background(), "client:my-device", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries under client source=%d, want 2 (user+agent)", len(entries))
	}

	// Source in the event payload
	recorded := evs.recorded()
	chatEvs := filterKind(recorded, model.EventChatMessageCreated)
	if len(chatEvs) < 1 {
		t.Fatal("no events recorded")
	}
	var payload struct {
		Source struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"source"`
	}
	if err := json.Unmarshal(chatEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Source.Kind != "client" {
		t.Errorf("source.kind=%q, want client", payload.Source.Kind)
	}
	if payload.Source.ID != "my-device" {
		t.Errorf("source.id=%q, want my-device", payload.Source.ID)
	}
}

// TestClientVoiceConsumer_Consume_ChatServiceError verifies that when ChatService fails,
// Consume returns nil (no retry), the user message is persisted, and a voice_failed
// event is emitted so the client knows the agent could not respond.
func TestClientVoiceConsumer_Consume_ChatServiceError(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{err: errors.New("агент недоступен")}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()

	// Consume must succeed (return nil) so the worker does not retry and create
	// a duplicate user message — tx1 is already committed at this point.
	if err := consumer.Consume(context.Background(), job, "текст"); err != nil {
		t.Fatalf("Consume must return nil when ChatService fails (no retry), got: %v", err)
	}

	recorded := evs.recorded()

	// Exactly one chat_message_created event (user only — no agent reply)
	chatEvs := filterKind(recorded, model.EventChatMessageCreated)
	if len(chatEvs) != 1 {
		t.Fatalf("chat_message_created events=%d, want 1 (user only)", len(chatEvs))
	}
	var payload struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(chatEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Role != "user" {
		t.Errorf("role=%q, want user", payload.Role)
	}

	// A voice_failed event must be emitted via Fail
	failedEvs := filterKind(recorded, model.EventVoiceFailed)
	if len(failedEvs) != 1 {
		t.Fatalf("voice_failed events=%d, want 1", len(failedEvs))
	}
	var failedPayload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(failedEvs[0].Payload, &failedPayload); err != nil {
		t.Fatalf("unmarshal voice_failed payload: %v", err)
	}
	if failedPayload.Code != "agent_failed" {
		t.Errorf("voice_failed code=%q, want agent_failed", failedPayload.Code)
	}

	// Broker notified: user event + voice_failed
	notified := broker.notifiedEvents()
	chatN := filterKind(notified, model.EventChatMessageCreated)
	if len(chatN) != 1 {
		t.Fatalf("broker chat_message_created notifications=%d, want 1", len(chatN))
	}
	failedN := filterKind(notified, model.EventVoiceFailed)
	if len(failedN) != 1 {
		t.Fatalf("broker voice_failed notifications=%d, want 1", len(failedN))
	}
}

// TestClientVoiceConsumer_Consume_AgentPersistFailEmitsVoiceFailed verifies that when
// the agent-reply tx2 insert fails, Consume calls Fail so the client receives a
// voice_failed event with code=agent_failed instead of waiting forever.
func TestClientVoiceConsumer_Consume_AgentPersistFailEmitsVoiceFailed(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	// Fail AddTx starting from the 2nd call (agent). First call (user) succeeds.
	hist.addTxErr = errors.New("db unavailable")
	hist.addTxFailAfterN = 1

	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ответ агента"}}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()

	// Consume must return nil — the error is handled internally via Fail.
	if err := consumer.Consume(context.Background(), job, "текст"); err != nil {
		t.Fatalf("Consume must return nil when tx2 fails (handled via Fail), got: %v", err)
	}

	// A voice_failed event must be emitted with code=agent_failed.
	recorded := evs.recorded()
	failedEvs := filterKind(recorded, model.EventVoiceFailed)
	if len(failedEvs) == 0 {
		t.Fatal("voice_failed event not emitted when agent reply tx2 fails")
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(failedEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal voice_failed payload: %v", err)
	}
	if payload.Code != "agent_failed" {
		t.Errorf("voice_failed code=%q, want agent_failed", payload.Code)
	}

	// Broker must be notified of the voice_failed event.
	notified := broker.notifiedEvents()
	failedN := filterKind(notified, model.EventVoiceFailed)
	if len(failedN) == 0 {
		t.Fatal("broker not notified of voice_failed event")
	}
}

// TestClientVoiceConsumer_Fail_EmitsVoiceFailedEvent verifies that Fail inserts
// a voice_failed event with jobId=ClientJobID, correct code, and notifies broker.
func TestClientVoiceConsumer_Fail_EmitsVoiceFailedEvent(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()
	job.ClientJobID = "fail-job-uuid"

	consumer.Fail(context.Background(), job, errors.New("stt crash"))

	recorded := evs.recorded()
	failEvs := filterKind(recorded, model.EventVoiceFailed)
	if len(failEvs) != 1 {
		t.Fatalf("voice_failed events=%d, want 1", len(failEvs))
	}

	var payload struct {
		JobID   string `json:"jobId"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(failEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.JobID != "fail-job-uuid" {
		t.Errorf("jobId=%q, want fail-job-uuid", payload.JobID)
	}
	if payload.Code != "transcription_failed" {
		t.Errorf("code=%q, want transcription_failed", payload.Code)
	}
	if payload.Message == "" {
		t.Error("message must not be empty")
	}

	// Broker notified
	notified := broker.notifiedEvents()
	failN := filterKind(notified, model.EventVoiceFailed)
	if len(failN) != 1 {
		t.Fatalf("broker notified %d voice_failed, want 1", len(failN))
	}
}

// TestClientVoiceConsumer_Fail_ErrEmptyTranscript verifies code="invalid_audio" for empty transcript.
func TestClientVoiceConsumer_Fail_ErrEmptyTranscript(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	job := makeClientVoiceJob()

	consumer.Fail(context.Background(), job, transcribe.ErrEmptyTranscript)

	recorded := evs.recorded()
	failEvs := filterKind(recorded, model.EventVoiceFailed)
	if len(failEvs) != 1 {
		t.Fatalf("voice_failed events=%d, want 1", len(failEvs))
	}

	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(failEvs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Code != "invalid_audio" {
		t.Errorf("code=%q, want invalid_audio for ErrEmptyTranscript", payload.Code)
	}
}

// TestClientVoiceConsumer_Consume_WithProjectHint verifies that when job.ProjectID is set,
// the consumer resolves the project via TaskStore and passes it as ProjectHint in the message.
func TestClientVoiceConsumer_Consume_WithProjectHint(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}
	tasks := newMockTaskStore("inbox-uuid")
	tasks.projects["proj-uuid-1"] = &model.Project{ID: "proj-uuid-1", Name: "Мой проект", Slug: "my-project"}

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, tasks)
	job := makeClientVoiceJob()
	job.ProjectID = "proj-uuid-1"

	if err := consumer.Consume(context.Background(), job, "текст"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	msg := chat.last()
	if msg.ProjectHint == nil {
		t.Fatal("ProjectHint must not be nil when job.ProjectID is set")
	}
	if msg.ProjectHint.ID != "proj-uuid-1" {
		t.Errorf("ProjectHint.ID=%q, want proj-uuid-1", msg.ProjectHint.ID)
	}
	if msg.ProjectHint.Slug != "my-project" {
		t.Errorf("ProjectHint.Slug=%q, want my-project", msg.ProjectHint.Slug)
	}
}

// TestClientVoiceConsumer_Consume_EmptyProjectIDNoHint verifies that when job.ProjectID
// is empty, no TaskStore call is made and ProjectHint is nil.
func TestClientVoiceConsumer_Consume_EmptyProjectIDNoHint(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}
	tasks := newMockTaskStore("inbox-uuid")

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, tasks)
	job := makeClientVoiceJob()
	job.ProjectID = "" // explicit empty

	if err := consumer.Consume(context.Background(), job, "текст"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	msg := chat.last()
	if msg.ProjectHint != nil {
		t.Errorf("ProjectHint must be nil when job.ProjectID is empty, got %+v", msg.ProjectHint)
	}
	// TaskStore.GetProject must not be called when project ID is empty
	if tasks.getProjectCalls != 0 {
		t.Errorf("GetProject must not be called when job.ProjectID is empty, got %d call(s)", tasks.getProjectCalls)
	}
}

// TestClientVoiceConsumer_Consume_ProjectNotFoundContinues verifies that if TaskStore.GetProject
// fails, consumer logs a warning and continues without a hint (does not emit voice_failed).
func TestClientVoiceConsumer_Consume_ProjectNotFoundContinues(t *testing.T) {
	db := openTestDB(t)
	hist := newMockHistory()
	evs := newMockEventStore()
	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}
	tasks := newMockTaskStore("inbox-uuid")
	tasks.getProjectErr = errors.New("проект не найден")

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, tasks)
	job := makeClientVoiceJob()
	job.ProjectID = "vanished-proj-uuid"

	// Must succeed even when project resolution fails
	if err := consumer.Consume(context.Background(), job, "текст"); err != nil {
		t.Fatalf("Consume must succeed even when project resolution fails: %v", err)
	}

	// No voice_failed event emitted
	recorded := evs.recorded()
	failEvs := filterKind(recorded, model.EventVoiceFailed)
	if len(failEvs) != 0 {
		t.Fatalf("voice_failed events=%d, want 0 (project hint failure is non-fatal)", len(failEvs))
	}

	// Chat was still called, ProjectHint is nil
	msg := chat.last()
	if msg.ProjectHint != nil {
		t.Errorf("ProjectHint must be nil when GetProject fails, got %+v", msg.ProjectHint)
	}
}

// TestClientVoiceConsumer_Consume_TransactionRollback verifies that if EventStore.Insert
// fails, History.AddTx is rolled back and Recent returns no entries.
// Uses real SQLite + storage.SQLiteHistory + mock EventStore that fails.
func TestClientVoiceConsumer_Consume_TransactionRollback(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.OpenDB(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
	evs := newMockEventStore()
	evs.insertErr = errors.New("db overloaded")
	evs.insertErrN = 0 // fail immediately on first Insert

	broker := newMockBroker()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}

	consumer := usecase.NewClientVoiceConsumer(usecase.ClientVoiceConsumerDeps{
		DB:      db,
		History: hist,
		Events:  evs,
		Broker:  broker,
		Chat:    chat,
	})

	job := makeClientVoiceJob()
	sourceID := "client:" + job.DeviceID

	err = consumer.Consume(context.Background(), job, "текст")
	if err == nil {
		t.Fatal("Consume must return error when EventStore.Insert fails")
	}

	// History must be empty — AddTx was rolled back
	entries, recErr := hist.Recent(context.Background(), sourceID, 10)
	if recErr != nil {
		t.Fatalf("Recent: %v", recErr)
	}
	if len(entries) != 0 {
		t.Fatalf("history entries=%d after rollback, want 0; entries=%+v", len(entries), entries)
	}
}

// filterKind returns events with the given kind.
func filterKind(evs []model.Event, kind model.EventKind) []model.Event {
	var out []model.Event
	for _, ev := range evs {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}
