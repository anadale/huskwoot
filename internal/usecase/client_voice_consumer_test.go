package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/events"
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
	setTaskIDsCalls []setTaskIDsCall
}

type setTaskIDsCall struct {
	messageID string
	ids       []string
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

func (h *mockHistory) SetTaskIDs(_ context.Context, messageID string, ids []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setTaskIDsCalls = append(h.setTaskIDsCalls, setTaskIDsCall{messageID: messageID, ids: append([]string(nil), ids...)})
	return nil
}

// entriesFor returns a copy of the entries stored under source, system rows included.
func (h *mockHistory) entriesFor(source string) []model.HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]model.HistoryEntry, len(h.entries[source]))
	copy(out, h.entries[source])
	return out
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

// ListClient returns every entry stored under source as a single page.
func (h *mockHistory) ListClient(_ context.Context, source, _ string, _ int) ([]model.HistoryEntry, string, error) {
	return h.entriesFor(source), "", nil
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

// TestClientVoiceConsumer_Consume_SetsVoiceJobID verifies that the message handed
// to the agent carries the client job id, so tasks it creates get via "voice".
func TestClientVoiceConsumer_Consume_SetsVoiceJobID(t *testing.T) {
	db := openTestDB(t)
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "ок"}}
	consumer := makeClientVoiceConsumer(t, db, newMockHistory(), newMockEventStore(), newMockBroker(), chat, nil)
	job := makeClientVoiceJob()
	job.ClientJobID = "voice-job-uuid"

	if err := consumer.Consume(context.Background(), job, "купить билеты"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	msg := chat.last()
	if msg.VoiceJobID != "voice-job-uuid" {
		t.Errorf("VoiceJobID=%q, want %q", msg.VoiceJobID, "voice-job-uuid")
	}
	if msg.Source.Kind != "client" || msg.Source.ID != job.DeviceID {
		t.Errorf("Source=%+v, want client source for %q", msg.Source, job.DeviceID)
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

// TestClientVoiceConsumer_Consume_StoresVoiceMetadata verifies the history rows of a
// voice job: the transcript is a voice entry with the audio job id and duration, and
// the agent reply is a text entry with the touched tasks.
func TestClientVoiceConsumer_Consume_StoresVoiceMetadata(t *testing.T) {
	hist := newMockHistory()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "created", TasksTouched: []string{"task-1"}}}
	consumer := makeClientVoiceConsumer(t, nil, hist, newMockEventStore(), newMockBroker(), chat, nil)
	job := makeClientVoiceJob()
	job.Duration = 9 * time.Second

	if err := consumer.Consume(context.Background(), job, "buy tickets"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	entries := hist.entriesFor("client:" + job.DeviceID)
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2; got %+v", len(entries), entries)
	}
	wantUser := model.HistoryEntry{
		AuthorName:      job.DeviceID,
		Text:            "buy tickets",
		Timestamp:       entries[0].Timestamp,
		Role:            model.HistoryRoleUser,
		Kind:            model.HistoryKindVoice,
		AudioJobID:      job.ClientJobID,
		AudioDurationMs: 9000,
	}
	if !reflect.DeepEqual(entries[0], wantUser) {
		t.Errorf("user entry:\n got %+v\nwant %+v", entries[0], wantUser)
	}
	wantAgent := model.HistoryEntry{
		AuthorName: "agent",
		Text:       "created",
		Timestamp:  entries[1].Timestamp,
		Role:       model.HistoryRoleAgent,
		Kind:       model.HistoryKindText,
		TaskIDs:    []string{"task-1"},
	}
	if !reflect.DeepEqual(entries[1], wantAgent) {
		t.Errorf("agent entry:\n got %+v\nwant %+v", entries[1], wantAgent)
	}
}

// chatPayloads decodes the chat_message_created payloads among evs as raw key maps.
func chatPayloads(t *testing.T, evs []model.Event) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, ev := range filterKind(evs, model.EventChatMessageCreated) {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal chat_message_created payload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// TestClientVoiceConsumer_Consume_PayloadCarriesKindDurationAndTaskIDs verifies the
// live payload keys of a voice job: the transcript has kind "voice" and the audio
// length, the agent reply has kind "text" and the touched tasks.
func TestClientVoiceConsumer_Consume_PayloadCarriesKindDurationAndTaskIDs(t *testing.T) {
	evs := newMockEventStore()
	chat := &mockChatServiceForVoice{reply: model.ChatReply{Text: "created", TasksTouched: []string{"task-1", "task-2"}}}
	consumer := makeClientVoiceConsumer(t, nil, newMockHistory(), evs, newMockBroker(), chat, nil)
	job := makeClientVoiceJob()
	job.Duration = 9 * time.Second

	if err := consumer.Consume(context.Background(), job, "buy tickets"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	payloads := chatPayloads(t, evs.recorded())
	if len(payloads) != 2 {
		t.Fatalf("chat_message_created events=%d, want 2", len(payloads))
	}

	tests := []struct {
		name         string
		payload      map[string]json.RawMessage
		wantKind     string
		wantDuration string
		wantTaskIDs  string
	}{
		{name: "transcript", payload: payloads[0], wantKind: `"voice"`, wantDuration: `9000`},
		{name: "agent reply", payload: payloads[1], wantKind: `"text"`, wantTaskIDs: `["task-1","task-2"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.payload["kind"]); got != tt.wantKind {
				t.Errorf("kind=%s, want %s", got, tt.wantKind)
			}
			if got := string(tt.payload["audioDurationMs"]); got != tt.wantDuration {
				t.Errorf("audioDurationMs=%s, want %q", got, tt.wantDuration)
			}
			if got := string(tt.payload["taskIds"]); got != tt.wantTaskIDs {
				t.Errorf("taskIds=%s, want %q", got, tt.wantTaskIDs)
			}
			if got := string(tt.payload["audioJobId"]); got != `"`+job.ClientJobID+`"` {
				t.Errorf("audioJobId=%s, want %q", got, job.ClientJobID)
			}
		})
	}
}

// TestClientVoiceConsumer_Fail_StoresSystemErrorEntry verifies that every voice_failed
// event is accompanied by a system error row with the user-facing message.
func TestClientVoiceConsumer_Fail_StoresSystemErrorEntry(t *testing.T) {
	tests := []struct {
		name     string
		cause    error
		wantCode string
	}{
		{name: "transcription failure", cause: errors.New("stt crash"), wantCode: "transcription_failed"},
		{name: "empty transcript", cause: transcribe.ErrEmptyTranscript, wantCode: "invalid_audio"},
		{name: "unsupported format", cause: transcribe.ErrUnsupportedFormat, wantCode: "unsupported_format"},
		{name: "agent failure", cause: usecase.ErrAgentFailedForTest, wantCode: "agent_failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hist := newMockHistory()
			evs := newMockEventStore()
			consumer := makeClientVoiceConsumer(t, nil, hist, evs, newMockBroker(), &mockChatServiceForVoice{}, nil)
			job := makeClientVoiceJob()
			job.Duration = 4 * time.Second

			consumer.Fail(context.Background(), job, tt.cause)

			failEvs := filterKind(evs.recorded(), model.EventVoiceFailed)
			if len(failEvs) != 1 {
				t.Fatalf("voice_failed events=%d, want 1", len(failEvs))
			}
			var payload struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(failEvs[0].Payload, &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if payload.Code != tt.wantCode {
				t.Errorf("code=%q, want %q", payload.Code, tt.wantCode)
			}

			entries := hist.entriesFor("client:" + job.DeviceID)
			if len(entries) != 1 {
				t.Fatalf("history entries=%d, want 1", len(entries))
			}
			want := model.HistoryEntry{
				AuthorName:      "system",
				Text:            payload.Message,
				Timestamp:       entries[0].Timestamp,
				Role:            model.HistoryRoleSystem,
				Kind:            model.HistoryKindError,
				AudioJobID:      job.ClientJobID,
				AudioDurationMs: 4000,
			}
			if !reflect.DeepEqual(entries[0], want) {
				t.Errorf("failure entry:\n got %+v\nwant %+v", entries[0], want)
			}
		})
	}
}

// TestClientVoiceConsumer_Fail_HistoryErrorStillEmitsEvent verifies that a history
// write failure does not suppress the voice_failed event the client waits for.
func TestClientVoiceConsumer_Fail_HistoryErrorStillEmitsEvent(t *testing.T) {
	hist := newMockHistory()
	hist.addTxErr = errors.New("history unavailable")
	evs := newMockEventStore()
	broker := newMockBroker()
	consumer := makeClientVoiceConsumer(t, nil, hist, evs, broker, &mockChatServiceForVoice{}, nil)

	consumer.Fail(context.Background(), makeClientVoiceJob(), errors.New("stt crash"))

	if n := len(filterKind(evs.recorded(), model.EventVoiceFailed)); n != 1 {
		t.Fatalf("voice_failed events=%d, want 1", n)
	}
	if n := len(filterKind(broker.notifiedEvents(), model.EventVoiceFailed)); n != 1 {
		t.Fatalf("broker voice_failed notifications=%d, want 1", n)
	}
}

// TestClientVoiceConsumer_Fail_ErrorEntryInSameTransaction verifies with a real
// SQLiteHistory that the failure row is rolled back together with the event, and
// that system rows never reach the agent context (Recent).
func TestClientVoiceConsumer_Fail_ErrorEntryInSameTransaction(t *testing.T) {
	tests := []struct {
		name      string
		insertErr error
		wantRows  int
	}{
		{name: "event stored", insertErr: nil, wantRows: 1},
		{name: "event insert fails", insertErr: errors.New("db overloaded"), wantRows: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatalf("OpenDB: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
			evs := newMockEventStore()
			evs.insertErr = tt.insertErr

			consumer := makeClientVoiceConsumer(t, db, hist, evs, newMockBroker(), &mockChatServiceForVoice{}, nil)
			job := makeClientVoiceJob()
			consumer.Fail(context.Background(), job, errors.New("stt crash"))

			var rows int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM messages WHERE source_id = ? AND role = 'system' AND kind = 'error'`,
				"client:"+job.DeviceID,
			).Scan(&rows); err != nil {
				t.Fatalf("count: %v", err)
			}
			if rows != tt.wantRows {
				t.Errorf("system error rows=%d, want %d", rows, tt.wantRows)
			}

			recent, err := hist.Recent(context.Background(), "client:"+job.DeviceID, 10)
			if err != nil {
				t.Fatalf("Recent: %v", err)
			}
			if len(recent) != 0 {
				t.Errorf("Recent must skip system rows, got %+v", recent)
			}
		})
	}
}

// slowAgentChat publishes task events through the broker, then blocks until
// release is closed before returning its reply — a slow agent reply after a
// tool call. Like the task service, it records the ids listed in own in the
// touched set of the call context right after notifying the broker; the other
// events simulate mutations from other sources (the Telegram pipeline, another
// device), which the broker delivers to every subscriber.
type slowAgentChat struct {
	broker  model.Broker
	events  []model.Event
	own     map[string]bool
	release chan struct{}
	reply   model.ChatReply
}

func (s *slowAgentChat) HandleMessage(ctx context.Context, _ model.Message) (model.ChatReply, error) {
	for _, ev := range s.events {
		s.broker.Notify(ev)
		if s.own[ev.EntityID] {
			usecase.AppendTouchedTasksForTest(ctx, ev.EntityID)
		}
	}
	select {
	case <-s.release:
		return s.reply, nil
	case <-ctx.Done():
		return model.ChatReply{}, ctx.Err()
	}
}

// TestClientVoiceConsumer_SyntheticReplyKeepsTaskIDs verifies that the synthetic
// "Готово" reply (ack grace and hard deadline branches) stores the ids of the
// tasks this agent call mutated, but not tasks mutated by other sources whose
// events reach the same broker subscription, and that the late real
// TasksTouched are merged into it.
func TestClientVoiceConsumer_SyntheticReplyKeepsTaskIDs(t *testing.T) {
	tests := []struct {
		name         string
		ackGrace     time.Duration
		hardDeadline time.Duration
	}{
		{name: "ack grace", ackGrace: 30 * time.Millisecond, hardDeadline: 5 * time.Second},
		{name: "hard deadline", ackGrace: 5 * time.Second, hardDeadline: 300 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatalf("OpenDB: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
			broker := events.NewBroker(events.BrokerConfig{})

			chat := &slowAgentChat{
				broker: broker,
				events: []model.Event{
					{Kind: model.EventTaskCreated, EntityID: "task-foreign-1"},
					{Kind: model.EventTaskCreated, EntityID: "task-1"},
					{Kind: model.EventProjectUpdated, EntityID: "project-1"},
					{Kind: model.EventTaskCompleted, EntityID: "task-foreign-2"},
					{Kind: model.EventTaskUpdated, EntityID: "task-2"},
					{Kind: model.EventTaskUpdated, EntityID: "task-1"},
				},
				own:     map[string]bool{"task-1": true, "task-2": true},
				release: make(chan struct{}),
				reply: model.ChatReply{
					Text:         "Created two tasks",
					TasksTouched: []string{"task-1", "task-3"},
				},
			}
			evs := newMockEventStore()
			consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
			usecase.SetClientVoiceTimings(consumer, tt.ackGrace, tt.hardDeadline)
			job := makeClientVoiceJob()
			source := "client:" + job.DeviceID

			if err := consumer.Consume(context.Background(), job, "create tasks"); err != nil {
				t.Fatalf("Consume: %v", err)
			}

			agentEntry := func() model.HistoryEntry {
				t.Helper()
				entries, err := hist.Recent(context.Background(), source, 10)
				if err != nil {
					t.Fatalf("Recent: %v", err)
				}
				for _, e := range entries {
					if e.Role == model.HistoryRoleAgent {
						return e
					}
				}
				t.Fatalf("no agent entry in %+v", entries)
				return model.HistoryEntry{}
			}

			synthetic := agentEntry()
			if synthetic.Text != "Готово" {
				t.Errorf("synthetic reply text=%q, want Готово", synthetic.Text)
			}
			if want := []string{"task-1", "task-2"}; !reflect.DeepEqual(synthetic.TaskIDs, want) {
				t.Errorf("synthetic TaskIDs=%v, want %v", synthetic.TaskIDs, want)
			}
			// The live payload of the synthetic reply carries the same ids.
			payloads := chatPayloads(t, evs.recorded())
			if len(payloads) != 2 {
				t.Fatalf("chat_message_created events=%d, want 2", len(payloads))
			}
			if got, want := string(payloads[1]["taskIds"]), `["task-1","task-2"]`; got != want {
				t.Errorf("synthetic payload taskIds=%s, want %s", got, want)
			}

			close(chat.release)

			want := []string{"task-1", "task-2", "task-3"}
			deadline := time.Now().Add(2 * time.Second)
			for {
				got := agentEntry()
				if reflect.DeepEqual(got.TaskIDs, want) {
					if got.Text != "Готово" {
						t.Errorf("late reply must not replace the synthetic text, got %q", got.Text)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("late TasksTouched not merged: TaskIDs=%v, want %v", got.TaskIDs, want)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// TestClientVoiceConsumer_ForeignTaskEventsDoNotEndRace verifies that task
// events from other sources do not produce a synthetic reply while the agent
// has touched nothing: the real reply is still delivered after the ack grace,
// and the hard deadline fails the job as an agent timeout.
func TestClientVoiceConsumer_ForeignTaskEventsDoNotEndRace(t *testing.T) {
	tests := []struct {
		name         string
		hardDeadline time.Duration
		releaseAfter time.Duration // 0 = the agent never replies within the test
		wantReply    string
		wantFailed   bool
	}{
		{name: "real reply after ack grace", hardDeadline: 5 * time.Second, releaseAfter: 200 * time.Millisecond, wantReply: "Nothing to change"},
		{name: "hard deadline", hardDeadline: 200 * time.Millisecond, wantFailed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatalf("OpenDB: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
			broker := events.NewBroker(events.BrokerConfig{})

			chat := &slowAgentChat{
				broker: broker,
				events: []model.Event{
					{Kind: model.EventTaskCreated, EntityID: "task-foreign-1"},
					{Kind: model.EventTaskUpdated, EntityID: "task-foreign-2"},
				},
				release: make(chan struct{}),
				reply:   model.ChatReply{Text: "Nothing to change"},
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(chat.release) }) }
			t.Cleanup(release)
			if tt.releaseAfter > 0 {
				timer := time.AfterFunc(tt.releaseAfter, release)
				t.Cleanup(func() { timer.Stop() })
			}

			evs := newMockEventStore()
			consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
			usecase.SetClientVoiceTimings(consumer, 20*time.Millisecond, tt.hardDeadline)
			job := makeClientVoiceJob()

			if err := consumer.Consume(context.Background(), job, "what is new?"); err != nil {
				t.Fatalf("Consume: %v", err)
			}

			entries, err := hist.Recent(context.Background(), "client:"+job.DeviceID, 10)
			if err != nil {
				t.Fatalf("Recent: %v", err)
			}
			var agentEntries []model.HistoryEntry
			for _, e := range entries {
				if e.Role == model.HistoryRoleAgent {
					agentEntries = append(agentEntries, e)
				}
			}

			failed := filterKind(evs.recorded(), model.EventVoiceFailed)
			if tt.wantFailed {
				if len(agentEntries) != 0 {
					t.Errorf("agent entries=%+v, want none", agentEntries)
				}
				if len(failed) != 1 {
					t.Fatalf("voice_failed events=%d, want 1", len(failed))
				}
				var payload struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(failed[0].Payload, &payload); err != nil {
					t.Fatalf("unmarshal voice_failed payload: %v", err)
				}
				if payload.Code != "agent_failed" {
					t.Errorf("voice_failed code=%q, want agent_failed", payload.Code)
				}
				return
			}

			if len(failed) != 0 {
				t.Errorf("voice_failed events=%d, want 0", len(failed))
			}
			if len(agentEntries) != 1 {
				t.Fatalf("agent entries=%+v, want exactly the real reply", agentEntries)
			}
			if agentEntries[0].Text != tt.wantReply || len(agentEntries[0].TaskIDs) != 0 {
				t.Errorf("agent entry text=%q TaskIDs=%v, want %q without task ids", agentEntries[0].Text, agentEntries[0].TaskIDs, tt.wantReply)
			}
			payloads := chatPayloads(t, evs.recorded())
			if len(payloads) != 2 {
				t.Fatalf("chat_message_created events=%d, want 2", len(payloads))
			}
			if raw, ok := payloads[1]["taskIds"]; ok {
				t.Errorf("agent payload taskIds=%s, want the key omitted", raw)
			}
		})
	}
}

// blockingTaskAgent creates a task from another source (a context without the
// agent call's touched set, like the Telegram pipeline) and then its own task,
// and blocks until release is closed — a slow agent reply after a tool call.
type blockingTaskAgent struct {
	taskSvc model.TaskService
	release chan struct{}
}

func (a *blockingTaskAgent) Handle(ctx context.Context, _ model.Message) (string, error) {
	if _, err := a.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "foreign"}); err != nil {
		return "", err
	}
	if _, err := a.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "own"}); err != nil {
		return "", err
	}
	select {
	case <-a.release:
		return "Created the task", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestClientVoiceConsumer_SyntheticReplyUsesAgentCallTasks wires the consumer
// with the real chat and task services and verifies that the synthetic reply
// links only the task the agent call created, although the broker also
// delivers the event of a task created elsewhere at the same time.
func TestClientVoiceConsumer_SyntheticReplyUsesAgentCallTasks(t *testing.T) {
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})
	broker := events.NewBroker(events.BrokerConfig{})
	evs := newMockEventStore()
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB:      db,
		Tasks:   newMockTaskStore("inbox-uuid"),
		Events:  evs,
		Devices: &mockDeviceStore{},
		Queue:   &mockPushQueue{},
		Broker:  broker,
	})
	agent := &blockingTaskAgent{taskSvc: taskSvc, release: make(chan struct{})}
	chat := usecase.NewChatService(usecase.ChatServiceDeps{Agent: agent})

	consumer := makeClientVoiceConsumer(t, db, hist, evs, broker, chat, nil)
	usecase.SetClientVoiceTimings(consumer, 30*time.Millisecond, 5*time.Second)
	job := makeClientVoiceJob()
	source := "client:" + job.DeviceID

	if err := consumer.Consume(context.Background(), job, "create a task"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	agentEntry := func() model.HistoryEntry {
		t.Helper()
		entries, err := hist.Recent(context.Background(), source, 10)
		if err != nil {
			t.Fatalf("Recent: %v", err)
		}
		for _, e := range entries {
			if e.Role == model.HistoryRoleAgent {
				return e
			}
		}
		t.Fatalf("no agent entry in %+v", entries)
		return model.HistoryEntry{}
	}

	// The mock task store numbers tasks in creation order: task-uuid-1 is the
	// foreign task, task-uuid-2 the agent's own.
	want := []string{"task-uuid-2"}
	synthetic := agentEntry()
	if synthetic.Text != "Готово" || !reflect.DeepEqual(synthetic.TaskIDs, want) {
		t.Errorf("synthetic reply text=%q TaskIDs=%v, want Готово with %v", synthetic.Text, synthetic.TaskIDs, want)
	}
	payloads := chatPayloads(t, evs.recorded())
	if len(payloads) != 2 {
		t.Fatalf("chat_message_created events=%d, want 2", len(payloads))
	}
	if got := string(payloads[1]["taskIds"]); got != `["task-uuid-2"]` {
		t.Errorf("synthetic payload taskIds=%s, want [\"task-uuid-2\"]", got)
	}

	// The late real reply keeps the row linked to the agent's task only.
	close(agent.release)
	time.Sleep(100 * time.Millisecond)
	if got := agentEntry(); !reflect.DeepEqual(got.TaskIDs, want) {
		t.Errorf("after the late reply TaskIDs=%v, want %v", got.TaskIDs, want)
	}
}
