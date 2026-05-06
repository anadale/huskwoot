package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/model"
)

// fakeVoiceQueue is a mock model.VoiceQueue for testing.
type fakeVoiceQueue struct {
	enqueueCalled bool
	enqueueCount  int
	lastJob       model.VoiceJob
	enqueueErr    error
}

func (q *fakeVoiceQueue) Enqueue(_ context.Context, job model.VoiceJob) error {
	q.enqueueCalled = true
	q.enqueueCount++
	q.lastJob = job
	return q.enqueueErr
}

// fakeTranscriber is a minimal non-nil model.Transcriber used in tests to satisfy
// the 503-guard in the voice handler. It is not invoked during handler tests.
type fakeTranscriber struct{}

func (f *fakeTranscriber) Transcribe(_ context.Context, _ model.AudioInput) (string, error) {
	return "transcribed", nil
}
func (f *fakeTranscriber) Name() string { return "fake" }
func (q *fakeVoiceQueue) Claim(_ context.Context, _ time.Time) (*model.VoiceJob, error) {
	return nil, nil
}
func (q *fakeVoiceQueue) Complete(_ context.Context, _ int64) error { return nil }
func (q *fakeVoiceQueue) Reschedule(_ context.Context, _ int64, _ time.Time, _ string) error {
	return nil
}

// voiceTestTaskStore is a minimal model.TaskStore mock for testing projectId validation.
// Only GetProject is meaningfully implemented; all other methods panic or return zero values.
type voiceTestTaskStore struct {
	projects map[string]*model.Project
}

func (s *voiceTestTaskStore) GetProject(_ context.Context, id string) (*model.Project, error) {
	p, ok := s.projects[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (s *voiceTestTaskStore) CreateProjectTx(_ context.Context, _ *sql.Tx, _ *model.Project) error {
	return nil
}
func (s *voiceTestTaskStore) UpdateProjectTx(_ context.Context, _ *sql.Tx, _ string, _ model.ProjectUpdate) error {
	return nil
}
func (s *voiceTestTaskStore) GetProjectTx(_ context.Context, _ *sql.Tx, id string) (*model.Project, error) {
	return s.GetProject(context.Background(), id)
}
func (s *voiceTestTaskStore) ListProjects(_ context.Context) ([]model.Project, error) { return nil, nil }
func (s *voiceTestTaskStore) FindProjectByName(_ context.Context, _ string) (*model.Project, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) CreateTaskTx(_ context.Context, _ *sql.Tx, _ *model.Task) error {
	return nil
}
func (s *voiceTestTaskStore) GetTask(_ context.Context, _ string) (*model.Task, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) GetTaskTx(_ context.Context, _ *sql.Tx, _ string) (*model.Task, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) GetTaskByRef(_ context.Context, _ string, _ int) (*model.Task, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) ListTasks(_ context.Context, _ string, _ model.TaskFilter) ([]model.Task, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) UpdateTaskTx(_ context.Context, _ *sql.Tx, _ string, _ model.TaskUpdate) error {
	return nil
}
func (s *voiceTestTaskStore) MoveTaskTx(_ context.Context, _ *sql.Tx, _, _ string) error { return nil }
func (s *voiceTestTaskStore) DefaultProjectID() string                                     { return "" }
func (s *voiceTestTaskStore) AddProjectAliasTx(_ context.Context, _ *sql.Tx, _, _ string) error {
	return nil
}
func (s *voiceTestTaskStore) RemoveProjectAliasTx(_ context.Context, _ *sql.Tx, _, _ string) error {
	return nil
}
func (s *voiceTestTaskStore) ListAliasesForProject(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) ListClosedOlderThan(_ context.Context, _ time.Time) ([]model.Task, error) {
	return nil, nil
}
func (s *voiceTestTaskStore) DeleteTaskTx(_ context.Context, _ *sql.Tx, _ string) error { return nil }

// chatVoiceHarness sets up an api.Server wired for POST /v1/chat/voice.
type chatVoiceHarness struct {
	t          *testing.T
	server     *api.Server
	token      string
	device     *model.Device
	uploadsDir string
	vq         *fakeVoiceQueue
	tasks      *voiceTestTaskStore
	db         *sql.DB
}

func newChatVoiceHarness(t *testing.T) *chatVoiceHarness {
	t.Helper()
	return newChatVoiceHarnessWith(t, &fakeVoiceQueue{}, 10*1024*1024, 5*time.Minute)
}

func newChatVoiceHarnessWith(t *testing.T, vq *fakeVoiceQueue, maxBytes int64, maxDuration time.Duration) *chatVoiceHarness {
	t.Helper()
	db := openTestDB(t)
	uploadsDir := t.TempDir()

	token := "voice-test-token"
	device := createTestDevice(t, db, "voice-device", token)

	// Provide a known project UUID in the fake task store.
	knownProjID := "00000000-0000-0000-0000-000000000001"
	tasks := &voiceTestTaskStore{
		projects: map[string]*model.Project{
			knownProjID: {
				ID:   knownProjID,
				Name: "Inbox",
				Slug: "inbox",
			},
		},
	}

	cfg := api.Config{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            db,
		Devices:       devices.NewSQLiteDeviceStore(db),
		VoiceQueue:    vq,
		UploadsDir:    uploadsDir,
		VoiceMaxBytes: maxBytes,
		VoiceDuration: maxDuration,
		TaskStore:     tasks,
		// Non-nil Transcriber is required; without it the handler returns 503.
		Transcriber: &fakeTranscriber{},
	}
	return &chatVoiceHarness{
		t:          t,
		server:     api.New(cfg),
		token:      token,
		device:     device,
		uploadsDir: uploadsDir,
		vq:         vq,
		tasks:      tasks,
		db:         db,
	}
}

// buildVoiceMultipart creates a multipart/form-data request body with audio + metadata parts.
func buildVoiceMultipart(t *testing.T, audioData []byte, audioCT string, meta map[string]any) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	mw := multipart.NewWriter(body)

	// metadata part (plain form value)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := mw.WriteField("metadata", string(metaJSON)); err != nil {
		t.Fatalf("write metadata field: %v", err)
	}

	// audio part with explicit Content-Type header
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="audio"; filename="audio.ogg"`)
	if audioCT != "" {
		h.Set("Content-Type", audioCT)
	}
	pw, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("create audio part: %v", err)
	}
	if _, err := pw.Write(audioData); err != nil {
		t.Fatalf("write audio data: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, mw.FormDataContentType()
}

// doVoice sends POST /v1/chat/voice with the given body/content-type + bearer token.
func (h *chatVoiceHarness) doVoice(body io.Reader, ct, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.doVoiceWithKey(body, ct, token, "")
}

// doVoiceWithKey is like doVoice but also sets Idempotency-Key when key != "".
func (h *chatVoiceHarness) doVoiceWithKey(body io.Reader, ct, token, idempotencyKey string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/voice", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+token)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	return rec
}

func TestChatVoiceHappyPath(t *testing.T) {
	h := newChatVoiceHarness(t)
	audioData := []byte("fake opus audio content")
	body, ct := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 3500,
		"mimeType":   "audio/ogg;codecs=opus",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		JobID     string    `json:"jobId"`
		CreatedAt time.Time `json:"createdAt"`
	}
	decodeJSONResp(t, rec.Body, &resp)
	if resp.JobID == "" {
		t.Fatal("jobId is empty")
	}
	if resp.CreatedAt.IsZero() {
		t.Fatal("createdAt is zero")
	}

	// voice_queue must contain the entry
	if !h.vq.enqueueCalled {
		t.Fatal("Enqueue was not called")
	}
	job := h.vq.lastJob
	if job.SourceKind != "client" {
		t.Fatalf("source_kind=%q, want client", job.SourceKind)
	}
	if job.DeviceID != h.device.ID {
		t.Fatalf("device_id=%q, want %q", job.DeviceID, h.device.ID)
	}
	if job.ClientJobID != resp.JobID {
		t.Fatalf("client_job_id=%q, want %q", job.ClientJobID, resp.JobID)
	}
	if job.ProjectID != "" {
		t.Fatalf("project_id=%q, want empty", job.ProjectID)
	}
	if job.MimeType != "audio/ogg;codecs=opus" {
		t.Fatalf("mime_type=%q", job.MimeType)
	}
	if job.Duration != 3500*time.Millisecond {
		t.Fatalf("duration=%v, want 3500ms", job.Duration)
	}

	// File must exist under uploadsDir/<jobID>.ogg
	expectedFile := filepath.Join(h.uploadsDir, resp.JobID+".ogg")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Fatalf("expected file %q: %v", expectedFile, err)
	}

	expected := resp.JobID + ".ogg"
	if job.Payload != expected {
		t.Fatalf("payload=%q, want %q", job.Payload, expected)
	}
}

func TestChatVoiceHappyPathWithProjectID(t *testing.T) {
	h := newChatVoiceHarness(t)

	// The harness already has knownProjID in the fake store.
	projID := "00000000-0000-0000-0000-000000000001"

	audioData := []byte("audio")
	body, ct := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
		"projectId":  projID,
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if h.vq.lastJob.ProjectID != projID {
		t.Fatalf("project_id=%q, want %q", h.vq.lastJob.ProjectID, projID)
	}
}

func TestChatVoiceProjectNotFound(t *testing.T) {
	h := newChatVoiceHarness(t)
	audioData := []byte("audio")
	// This UUID is NOT in the fake task store.
	body, ct := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
		"projectId":  "22222222-2222-2222-2222-222222222222",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeProjectNotFound {
		t.Fatalf("code=%q, want project_not_found", errBody.Error.Code)
	}
}

func TestChatVoiceInvalidProjectIDFormat(t *testing.T) {
	h := newChatVoiceHarness(t)
	audioData := []byte("audio")
	body, ct := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
		"projectId":  "not-a-uuid",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeInvalidMetadata {
		t.Fatalf("code=%q, want invalid_metadata", errBody.Error.Code)
	}
}

func TestChatVoiceNoBearer(t *testing.T) {
	h := newChatVoiceHarness(t)
	body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/voice", body)
	req.Header.Set("Content-Type", ct)
	// No Authorization header.
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatVoiceBodyTooLarge(t *testing.T) {
	// Set max bytes to 100 so a small audio payload exceeds the limit.
	vq := &fakeVoiceQueue{}
	h := newChatVoiceHarnessWith(t, vq, 100, 5*time.Minute)

	// Build a form with audio data larger than 100 bytes.
	audioData := bytes.Repeat([]byte("x"), 200)
	body, ct := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeAudioTooLarge {
		t.Fatalf("code=%q, want audio_too_large", errBody.Error.Code)
	}
}

func TestChatVoiceUnsupportedMimeType(t *testing.T) {
	h := newChatVoiceHarness(t)
	body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/wav", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/wav",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d, want 415; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeUnsupportedMedia {
		t.Fatalf("code=%q, want unsupported_media_type", errBody.Error.Code)
	}
}

func TestChatVoiceDurationTooLong(t *testing.T) {
	vq := &fakeVoiceQueue{}
	h := newChatVoiceHarnessWith(t, vq, 10*1024*1024, 30*time.Second)

	body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 60_000, // 60 seconds > 30 second limit
		"mimeType":   "audio/ogg;codecs=opus",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeDurationTooLong {
		t.Fatalf("code=%q, want duration_too_long", errBody.Error.Code)
	}
}

func TestChatVoiceMissingAudioPart(t *testing.T) {
	h := newChatVoiceHarness(t)

	// Build form with only metadata, no audio part.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaJSON, _ := json.Marshal(map[string]any{"durationMs": 1000, "mimeType": "audio/ogg;codecs=opus"})
	_ = mw.WriteField("metadata", string(metaJSON))
	mw.Close()

	rec := h.doVoice(&buf, mw.FormDataContentType(), h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeInvalidAudio {
		t.Fatalf("code=%q, want invalid_audio", errBody.Error.Code)
	}
}

func TestChatVoiceMissingMetadataPart(t *testing.T) {
	h := newChatVoiceHarness(t)

	// Build form with only audio, no metadata.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="audio"; filename="audio.ogg"`)
	hdr.Set("Content-Type", "audio/ogg;codecs=opus")
	pw, _ := mw.CreatePart(hdr)
	_, _ = pw.Write([]byte("audio data"))
	mw.Close()

	rec := h.doVoice(&buf, mw.FormDataContentType(), h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeInvalidMetadata {
		t.Fatalf("code=%q, want invalid_metadata", errBody.Error.Code)
	}
}

func TestChatVoiceInvalidMetadataJSON(t *testing.T) {
	h := newChatVoiceHarness(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("metadata", "not json {")
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="audio"; filename="audio.ogg"`)
	hdr.Set("Content-Type", "audio/ogg;codecs=opus")
	pw, _ := mw.CreatePart(hdr)
	_, _ = pw.Write([]byte("audio"))
	mw.Close()

	rec := h.doVoice(&buf, mw.FormDataContentType(), h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeInvalidMetadata {
		t.Fatalf("code=%q, want invalid_metadata", errBody.Error.Code)
	}
}

func TestChatVoiceMimeTypeMismatch(t *testing.T) {
	h := newChatVoiceHarness(t)
	// metadata says ogg, audio part says mp4.
	body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/mp4", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeInvalidMetadata {
		t.Fatalf("code=%q, want invalid_metadata", errBody.Error.Code)
	}
}

func TestChatVoiceEnqueueErrorCleansUpFile(t *testing.T) {
	vq := &fakeVoiceQueue{enqueueErr: errors.New("db down")}
	h := newChatVoiceHarnessWith(t, vq, 10*1024*1024, 5*time.Minute)

	body, ct := buildVoiceMultipart(t, []byte("audio content"), "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
	})

	rec := h.doVoice(body, ct, h.token)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}

	// No audio files should remain in uploadsDir after the cleanup.
	var remaining []string
	_ = filepath.WalkDir(h.uploadsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		remaining = append(remaining, path)
		return nil
	})
	if len(remaining) > 0 {
		t.Fatalf("orphan files after enqueue failure: %v", remaining)
	}
}

func TestChatVoiceWebmAndMp4Accepted(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		wantExt  string
	}{
		{"webm", "audio/webm;codecs=opus", ".webm"},
		{"mp4", "audio/mp4", ".m4a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newChatVoiceHarness(t)
			body, ct := buildVoiceMultipart(t, []byte("audio"), tc.mimeType, map[string]any{
				"durationMs": 1000,
				"mimeType":   tc.mimeType,
			})
			rec := h.doVoice(body, ct, h.token)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("mime=%q: status=%d, want 202; body=%s", tc.mimeType, rec.Code, rec.Body.String())
			}
			var resp struct {
				JobID string `json:"jobId"`
			}
			decodeJSONResp(t, rec.Body, &resp)
			expectedFile := filepath.Join(h.uploadsDir, resp.JobID+tc.wantExt)
			if _, err := os.Stat(expectedFile); err != nil {
				t.Fatalf("expected file %q: %v", expectedFile, err)
			}
		})
	}
}

// ---- Task 9b tests: rate limit, 503, idempotency ----

func TestChatVoiceTranscriberDisabled(t *testing.T) {
	db := openTestDB(t)
	token := "voice-notranscriber-token"
	createTestDevice(t, db, "notranscriber-device", token)
	vq := &fakeVoiceQueue{}

	// Transcriber is nil — handler must return 503 before parsing the body.
	cfg := api.Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          db,
		Devices:     devices.NewSQLiteDeviceStore(db),
		VoiceQueue:  vq,
		UploadsDir:  t.TempDir(),
		Transcriber: nil,
	}
	srv := api.New(cfg)

	body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 1000,
		"mimeType":   "audio/ogg;codecs=opus",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/voice", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	errBody := readErrorBody(t, rec.Body)
	if errBody.Error.Code != api.ErrorCodeTranscriberDisabled {
		t.Fatalf("code=%q, want transcriber_disabled", errBody.Error.Code)
	}
	if vq.enqueueCalled {
		t.Fatal("Enqueue must not be called when transcriber is disabled")
	}
}

func TestChatVoiceRateLimitExceeded(t *testing.T) {
	db := openTestDB(t)
	token := "voice-ratelimit-token"
	createTestDevice(t, db, "ratelimit-device", token)
	vq := &fakeVoiceQueue{}

	// VoiceRateLimit=1: burst=1, so second request is immediately rate-limited.
	cfg := api.Config{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             db,
		Devices:        devices.NewSQLiteDeviceStore(db),
		VoiceQueue:     vq,
		UploadsDir:     t.TempDir(),
		Transcriber:    &fakeTranscriber{},
		VoiceRateLimit: 1,
	}
	srv := api.New(cfg)

	doReq := func() *httptest.ResponseRecorder {
		body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/ogg;codecs=opus", map[string]any{
			"durationMs": 1000,
			"mimeType":   "audio/ogg;codecs=opus",
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/voice", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// First request consumes the single burst token.
	rec1 := doReq()
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: status=%d, want 202; body=%s", rec1.Code, rec1.Body.String())
	}

	// Second request immediately after must be rate-limited.
	rec2 := doReq()
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status=%d, want 429; body=%s", rec2.Code, rec2.Body.String())
	}
	errBody := readErrorBody(t, rec2.Body)
	if errBody.Error.Code != api.ErrorCodeRateLimited {
		t.Fatalf("code=%q, want rate_limited", errBody.Error.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing on 429 response")
	}
}

func TestChatVoiceRateLimitPerDevice(t *testing.T) {
	db := openTestDB(t)
	tokenA := "voice-rl-device-a"
	tokenB := "voice-rl-device-b"
	createTestDevice(t, db, "rl-device-a", tokenA)
	createTestDevice(t, db, "rl-device-b", tokenB)
	vq := &fakeVoiceQueue{}

	// Limit=1: each device has its own bucket, so each can make 1 request.
	cfg := api.Config{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             db,
		Devices:        devices.NewSQLiteDeviceStore(db),
		VoiceQueue:     vq,
		UploadsDir:     t.TempDir(),
		Transcriber:    &fakeTranscriber{},
		VoiceRateLimit: 1,
	}
	srv := api.New(cfg)

	doWith := func(token string) *httptest.ResponseRecorder {
		body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/ogg;codecs=opus", map[string]any{
			"durationMs": 1000,
			"mimeType":   "audio/ogg;codecs=opus",
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/voice", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Both devices can each make one request independently.
	recA := doWith(tokenA)
	if recA.Code != http.StatusAccepted {
		t.Fatalf("device A: status=%d, want 202; body=%s", recA.Code, recA.Body.String())
	}
	recB := doWith(tokenB)
	if recB.Code != http.StatusAccepted {
		t.Fatalf("device B: status=%d, want 202; body=%s", recB.Code, recB.Body.String())
	}

	// Second request from device A must be blocked; device B's bucket is independent.
	recA2 := doWith(tokenA)
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("device A second request: status=%d, want 429; body=%s", recA2.Code, recA2.Body.String())
	}
}

func TestChatVoiceIdempotencyKey(t *testing.T) {
	h := newChatVoiceHarness(t)
	const idemKey = "test-idem-key-abc123"

	audioData := []byte("fake opus content for idempotency test")

	// First request.
	body1, ct1 := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 2000,
		"mimeType":   "audio/ogg;codecs=opus",
	})
	rec1 := h.doVoiceWithKey(body1, ct1, h.token, idemKey)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: status=%d, want 202; body=%s", rec1.Code, rec1.Body.String())
	}
	var resp1 struct {
		JobID     string    `json:"jobId"`
		CreatedAt time.Time `json:"createdAt"`
	}
	decodeJSONResp(t, rec1.Body, &resp1)
	if resp1.JobID == "" {
		t.Fatal("first response: empty jobId")
	}
	if h.vq.enqueueCount != 1 {
		t.Fatalf("after first request enqueueCount=%d, want 1", h.vq.enqueueCount)
	}

	// Second request with the same key — handler must not run again.
	body2, ct2 := buildVoiceMultipart(t, audioData, "audio/ogg;codecs=opus", map[string]any{
		"durationMs": 2000,
		"mimeType":   "audio/ogg;codecs=opus",
	})
	rec2 := h.doVoiceWithKey(body2, ct2, h.token, idemKey)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("second request: status=%d, want 202; body=%s", rec2.Code, rec2.Body.String())
	}
	var resp2 struct {
		JobID     string    `json:"jobId"`
		CreatedAt time.Time `json:"createdAt"`
	}
	decodeJSONResp(t, rec2.Body, &resp2)

	// Responses must be identical (replayed from cache).
	if resp1.JobID != resp2.JobID {
		t.Fatalf("jobId mismatch: first=%q second=%q", resp1.JobID, resp2.JobID)
	}
	if !resp1.CreatedAt.Equal(resp2.CreatedAt) {
		t.Fatalf("createdAt mismatch: first=%v second=%v", resp1.CreatedAt, resp2.CreatedAt)
	}

	// Enqueue must have been called exactly once (not twice).
	if h.vq.enqueueCount != 1 {
		t.Fatalf("enqueueCount=%d after two requests with same key, want 1", h.vq.enqueueCount)
	}
}

func TestChatVoiceIdempotencyPerDevice(t *testing.T) {
	db := openTestDB(t)
	tokenA := "voice-idem-dev-a"
	tokenB := "voice-idem-dev-b"
	createTestDevice(t, db, "idem-dev-a", tokenA)
	createTestDevice(t, db, "idem-dev-b", tokenB)
	vq := &fakeVoiceQueue{}

	cfg := api.Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          db,
		Devices:     devices.NewSQLiteDeviceStore(db),
		VoiceQueue:  vq,
		UploadsDir:  t.TempDir(),
		Transcriber: &fakeTranscriber{},
	}
	srv := api.New(cfg)

	const sharedKey = "same-idempotency-key"

	doWith := func(token string) *httptest.ResponseRecorder {
		body, ct := buildVoiceMultipart(t, []byte("audio"), "audio/ogg;codecs=opus", map[string]any{
			"durationMs": 1000,
			"mimeType":   "audio/ogg;codecs=opus",
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/voice", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", sharedKey)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	recA := doWith(tokenA)
	if recA.Code != http.StatusAccepted {
		t.Fatalf("device A: status=%d, want 202; body=%s", recA.Code, recA.Body.String())
	}
	recB := doWith(tokenB)
	if recB.Code != http.StatusAccepted {
		t.Fatalf("device B: status=%d, want 202; body=%s", recB.Code, recB.Body.String())
	}

	var rA, rB struct{ JobID string `json:"jobId"` }
	decodeJSONResp(t, recA.Body, &rA)
	decodeJSONResp(t, recB.Body, &rB)

	// Idempotency is per (deviceID, key) — different devices get independent responses.
	if rA.JobID == rB.JobID {
		t.Fatalf("device A and B got same jobId=%q — idempotency is not per-device", rA.JobID)
	}

	// Both devices triggered an actual enqueue.
	if vq.enqueueCount != 2 {
		t.Fatalf("enqueueCount=%d, want 2 (one per device)", vq.enqueueCount)
	}
}
