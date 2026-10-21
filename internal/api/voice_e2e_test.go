package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/transcribe"
	"github.com/anadale/huskwoot/internal/usecase"
)

// e2eMockTranscriber is a model.Transcriber that always returns a fixed text.
type e2eMockTranscriber struct{ text string }

func (m *e2eMockTranscriber) Transcribe(_ context.Context, _ model.AudioInput) (string, error) {
	return m.text, nil
}
func (m *e2eMockTranscriber) Name() string { return "e2e-mock" }

// e2eMockChatService is a model.ChatService that always returns a fixed reply.
type e2eMockChatService struct{ reply string }

func (s *e2eMockChatService) HandleMessage(_ context.Context, _ model.Message) (model.ChatReply, error) {
	return model.ChatReply{Text: s.reply}, nil
}

// TestVoiceE2ESmoke exercises the full client voice chat pipeline end-to-end:
// POST /v1/chat/voice → 202 → Worker transcribes → two chat_message_created
// events arrive on /v1/events SSE, and History.Recent returns both messages.
func TestVoiceE2ESmoke(t *testing.T) {
	const (
		fixedTranscript = "создай задачу позвонить маме"
		fixedReply      = "Задача создана"
	)

	db := openTestDB(t)
	uploadsDir := t.TempDir()

	voiceQueue := storage.NewSQLiteVoiceQueue(db)
	hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{MaxMessages: 500})
	eventStore := events.NewSQLiteEventStore(db)
	broker := events.NewBroker(events.BrokerConfig{})
	deviceStore := devices.NewSQLiteDeviceStore(db)

	tr := &e2eMockTranscriber{text: fixedTranscript}
	chatSvc := &e2eMockChatService{reply: fixedReply}

	consumer := usecase.NewClientVoiceConsumer(usecase.ClientVoiceConsumerDeps{
		DB:      db,
		History: hist,
		Events:  eventStore,
		Broker:  broker,
		Chat:    chatSvc,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	worker := transcribe.NewWorker(transcribe.WorkerConfig{
		Queue:       voiceQueue,
		Transcriber: tr,
		Downloaders: map[string]model.VoiceDownloader{
			"client": transcribe.NewLocalFileDownloader(uploadsDir),
		},
		Consumers: map[string]model.VoiceConsumer{
			"client": consumer,
		},
		Interval: 10 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	token := "e2e-voice-token"
	device := createTestDevice(t, db, "e2e-device", token)

	srv := api.New(api.Config{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            db,
		Devices:       deviceStore,
		History:       hist,
		Events:        eventStore,
		Broker:        broker,
		VoiceQueue:    voiceQueue,
		UploadsDir:    uploadsDir,
		VoiceMaxBytes: 10 * 1024 * 1024,
		VoiceDuration: 5 * time.Minute,
		Transcriber:   tr,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Open SSE connection first so we receive all events.
	sseCtx, sseCancel := context.WithCancel(context.Background())
	t.Cleanup(sseCancel)

	sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/v1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest SSE: %v", err)
	}
	sseReq.Header.Set("Authorization", "Bearer "+token)
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("SSE connect: %v", err)
	}
	t.Cleanup(func() { sseResp.Body.Close() })
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status=%d, want 200", sseResp.StatusCode)
	}
	sseCh := readSSEEvents(sseResp.Body)
	// The successful response already has a broker subscription and initial SQL mark.

	// POST /v1/chat/voice with a tiny fake opus payload.
	body, ct := buildVoiceMultipart(t, []byte("fake-opus"), "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
	})
	voiceReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/voice", body)
	if err != nil {
		t.Fatalf("NewRequest voice: %v", err)
	}
	voiceReq.Header.Set("Authorization", "Bearer "+token)
	voiceReq.Header.Set("Content-Type", ct)
	voiceResp, err := http.DefaultClient.Do(voiceReq)
	if err != nil {
		t.Fatalf("POST /v1/chat/voice: %v", err)
	}
	defer voiceResp.Body.Close()
	if voiceResp.StatusCode != http.StatusAccepted {
		rb, _ := io.ReadAll(voiceResp.Body)
		t.Fatalf("POST /v1/chat/voice: status=%d, body=%s", voiceResp.StatusCode, rb)
	}
	var jobResp struct {
		JobID     string    `json:"jobId"`
		CreatedAt time.Time `json:"createdAt"`
	}
	if err := json.NewDecoder(voiceResp.Body).Decode(&jobResp); err != nil {
		t.Fatalf("decode 202 body: %v", err)
	}
	if jobResp.JobID == "" {
		t.Fatal("202 response has empty jobId")
	}

	// Run the worker: it will poll the queue and process the job asynchronously.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = worker.Run(workerCtx)
	}()

	// Collect exactly two chat_message_created events from SSE.
	type msgPayload struct {
		MessageID  string `json:"messageId"`
		Role       string `json:"role"`
		Text       string `json:"text"`
		AudioJobID string `json:"audioJobId"`
		Source     struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"source"`
	}

	var chatEvs []sseEvent
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for len(chatEvs) < 2 {
		select {
		case ev, ok := <-sseCh:
			if !ok {
				t.Fatal("SSE stream closed before receiving two chat_message_created events")
			}
			if ev.kind == string(model.EventChatMessageCreated) {
				chatEvs = append(chatEvs, ev)
			}
		case <-timeout.C:
			t.Fatalf("timeout waiting for chat_message_created events; got %d of 2", len(chatEvs))
		}
	}

	workerCancel()
	<-workerDone

	var userPL, agentPL msgPayload
	if err := json.Unmarshal([]byte(chatEvs[0].data), &userPL); err != nil {
		t.Fatalf("unmarshal user event: %v", err)
	}
	if err := json.Unmarshal([]byte(chatEvs[1].data), &agentPL); err != nil {
		t.Fatalf("unmarshal agent event: %v", err)
	}

	// Verify user event.
	if userPL.Role != "user" {
		t.Errorf("event[0] role=%q, want user", userPL.Role)
	}
	if userPL.Text != fixedTranscript {
		t.Errorf("event[0] text=%q, want %q", userPL.Text, fixedTranscript)
	}
	if userPL.AudioJobID != jobResp.JobID {
		t.Errorf("event[0] audioJobId=%q, want %q", userPL.AudioJobID, jobResp.JobID)
	}
	if userPL.Source.Kind != "client" {
		t.Errorf("event[0] source.kind=%q, want client", userPL.Source.Kind)
	}
	if userPL.Source.ID != device.ID {
		t.Errorf("event[0] source.id=%q, want %q", userPL.Source.ID, device.ID)
	}
	if userPL.MessageID == "" {
		t.Error("event[0] messageId is empty")
	}

	// Verify agent event.
	if agentPL.Role != "agent" {
		t.Errorf("event[1] role=%q, want agent", agentPL.Role)
	}
	if agentPL.Text != fixedReply {
		t.Errorf("event[1] text=%q, want %q", agentPL.Text, fixedReply)
	}
	if agentPL.AudioJobID != jobResp.JobID {
		t.Errorf("event[1] audioJobId=%q, want %q", agentPL.AudioJobID, jobResp.JobID)
	}
	if agentPL.MessageID == "" {
		t.Error("event[1] messageId is empty")
	}

	// Verify both messages appear in History.Recent.
	// We check for presence without strict ordering because both entries may share
	// the same Unix-second timestamp and SQLite's tie-breaking is unspecified.
	sourceID := "client:" + device.ID
	histEntries, err := hist.Recent(context.Background(), sourceID, 10)
	if err != nil {
		t.Fatalf("History.Recent: %v", err)
	}
	if len(histEntries) != 2 {
		t.Fatalf("History.Recent: got %d entries, want 2", len(histEntries))
	}
	texts := map[string]bool{histEntries[0].Text: true, histEntries[1].Text: true}
	if !texts[fixedTranscript] {
		t.Errorf("history does not contain user transcript %q; got %v", fixedTranscript, texts)
	}
	if !texts[fixedReply] {
		t.Errorf("history does not contain agent reply %q; got %v", fixedReply, texts)
	}
}
