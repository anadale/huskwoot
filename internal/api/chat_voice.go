package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anadale/huskwoot/internal/model"
)

// allowedMimeTypes maps normalized MIME type to the file extension used for storage.
var allowedMimeTypes = map[string]string{
	"audio/ogg;codecs=opus":  ".ogg",
	"audio/mp4":              ".m4a",
	"audio/webm;codecs=opus": ".webm",
}

// voiceMetadata is the JSON payload from the "metadata" part of the multipart form.
type voiceMetadata struct {
	DurationMs int64  `json:"durationMs"`
	MimeType   string `json:"mimeType"`
	ProjectID  string `json:"projectId,omitempty"`
}

// voiceJobCreatedResponse is the 202 response body for POST /v1/chat/voice.
type voiceJobCreatedResponse struct {
	JobID     string    `json:"jobId"`
	CreatedAt time.Time `json:"createdAt"`
}

// chatVoiceHandler handles POST /v1/chat/voice.
type chatVoiceHandler struct {
	voiceQueue    model.VoiceQueue
	uploadsDir    string
	voiceMaxBytes int64
	maxDuration   time.Duration
	taskStore     model.TaskStore
	transcriber   model.Transcriber
	rateLimiter   *voiceRateLimiter
	logger        *slog.Logger
	now           func() time.Time
}

func newChatVoiceHandler(
	voiceQueue model.VoiceQueue,
	uploadsDir string,
	voiceMaxBytes int64,
	maxDuration time.Duration,
	taskStore model.TaskStore,
	transcriber model.Transcriber,
	rateLimiter *voiceRateLimiter,
	logger *slog.Logger,
) *chatVoiceHandler {
	return &chatVoiceHandler{
		voiceQueue:    voiceQueue,
		uploadsDir:    uploadsDir,
		voiceMaxBytes: voiceMaxBytes,
		maxDuration:   maxDuration,
		taskStore:     taskStore,
		transcriber:   transcriber,
		rateLimiter:   rateLimiter,
		logger:        logger,
		now:           time.Now,
	}
}

// rateLimitMiddleware enforces the per-device rate limit for POST /v1/chat/voice.
func (h *chatVoiceHandler) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceID := DeviceIDFromContext(r.Context())
		if !h.rateLimiter.Allow(deviceID) {
			w.Header().Set("Retry-After", strconv.Itoa(h.rateLimiter.RetryAfterSeconds()))
			WriteError(w, http.StatusTooManyRequests, ErrorCodeRateLimited, "voice upload rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// post handles POST /v1/chat/voice: validates multipart audio upload, saves the file,
// and enqueues a transcription job. Returns 202 with {jobId, createdAt}.
func (h *chatVoiceHandler) post(w http.ResponseWriter, r *http.Request) {
	// 503 when transcription is disabled (provider="" in config). Must be the very
	// first check — before MaxBytesReader — so the client knows not to send the body.
	if h.transcriber == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrorCodeTranscriberDisabled, "voice transcription is not configured")
		return
	}

	maxBytes := h.voiceMaxBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if err := r.ParseMultipartForm(32 * 1024); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, ErrorCodeAudioTooLarge, "audio file too large")
			return
		}
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidAudio, "failed to parse multipart form: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck

	// Parse metadata part.
	metadataStr := r.FormValue("metadata")
	if metadataStr == "" {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidMetadata, "metadata part is required")
		return
	}
	var meta voiceMetadata
	if err := json.Unmarshal([]byte(metadataStr), &meta); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidMetadata, "invalid metadata JSON: "+err.Error())
		return
	}
	if meta.MimeType == "" {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidMetadata, "mimeType is required")
		return
	}

	// Validate MIME type against the allowlist.
	normMeta := normalizeMimeType(meta.MimeType)
	ext, ok := allowedMimeTypes[normMeta]
	if !ok {
		WriteError(w, http.StatusUnsupportedMediaType, ErrorCodeUnsupportedMedia,
			"unsupported audio format; accepted: audio/ogg;codecs=opus, audio/mp4, audio/webm;codecs=opus")
		return
	}

	// Get audio file part.
	audioPart, audioHeader, err := r.FormFile("audio")
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidAudio, "audio part is required")
		return
	}
	defer audioPart.Close()

	// Validate that the audio part's Content-Type matches metadata.mimeType.
	// Strip the codecs parameter before comparing: clients may include extra codec
	// params in the part header (e.g. "audio/mp4; codecs=mp4a.40.2") even when the
	// metadata mimeType omits them.
	audioCT := audioHeader.Header.Get("Content-Type")
	baseAudioCT := strings.SplitN(normalizeMimeType(audioCT), ";", 2)[0]
	baseMeta := strings.SplitN(normMeta, ";", 2)[0]
	if baseAudioCT != baseMeta {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidMetadata,
			"audio Content-Type does not match metadata.mimeType")
		return
	}

	// Validate duration.
	if meta.DurationMs < 0 {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidMetadata, "durationMs must be non-negative")
		return
	}
	if h.maxDuration > 0 && meta.DurationMs > h.maxDuration.Milliseconds() {
		WriteError(w, http.StatusBadRequest, ErrorCodeDurationTooLong,
			fmt.Sprintf("audio duration exceeds maximum of %s", h.maxDuration))
		return
	}

	// Validate projectId if provided.
	var projectID string
	if meta.ProjectID != "" {
		if _, err := uuid.Parse(meta.ProjectID); err != nil {
			WriteError(w, http.StatusBadRequest, ErrorCodeInvalidMetadata, "projectId must be a valid UUID")
			return
		}
		if h.taskStore != nil {
			proj, err := h.taskStore.GetProject(r.Context(), meta.ProjectID)
			if err != nil {
				h.logError(r.Context(), "get project for voice upload", err)
				WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to validate projectId")
				return
			}
			if proj == nil {
				WriteError(w, http.StatusBadRequest, ErrorCodeProjectNotFound, "project not found")
				return
			}
		}
		projectID = meta.ProjectID
	}

	clientJobID := uuid.NewString()
	now := h.now().UTC()

	tmpDir := filepath.Join(h.uploadsDir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		h.logError(r.Context(), "create tmp dir", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to store audio")
		return
	}

	// Create temp file; track both paths for deferred cleanup.
	tmpFile, err := os.CreateTemp(tmpDir, clientJobID+"-*")
	if err != nil {
		h.logError(r.Context(), "create tmp file", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to store audio")
		return
	}
	tmpPath := tmpFile.Name()
	finalPath := ""

	// Cleanup: remove tmp file if still present, or final file if enqueue failed.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
		if finalPath != "" {
			_ = os.Remove(finalPath)
		}
	}()

	if _, err := io.Copy(tmpFile, audioPart); err != nil {
		tmpFile.Close()
		h.logError(r.Context(), "write audio file", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to store audio")
		return
	}
	if err := tmpFile.Close(); err != nil {
		h.logError(r.Context(), "close tmp file", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to store audio")
		return
	}

	destFileName := clientJobID + ext
	destPath := filepath.Join(h.uploadsDir, destFileName)
	if err := os.Rename(tmpPath, destPath); err != nil {
		h.logError(r.Context(), "rename audio file", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to store audio")
		return
	}
	tmpPath = ""       // tmp was renamed; defer must not try to remove it
	finalPath = destPath // defer removes this if enqueue fails

	relPath := destFileName

	deviceID := DeviceIDFromContext(r.Context())
	if err := h.voiceQueue.Enqueue(r.Context(), model.VoiceJob{
		SourceKind:    "client",
		Payload:       relPath,
		MimeType:      meta.MimeType,
		Duration:      time.Duration(meta.DurationMs) * time.Millisecond,
		ReceivedAt:    now,
		NextAttemptAt: now,
		DeviceID:      deviceID,
		ClientJobID:   clientJobID,
		ProjectID:     projectID,
	}); err != nil {
		h.logError(r.Context(), "enqueue voice job", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to enqueue voice job")
		return
	}
	finalPath = "" // success — do not clean up

	writeJSON(w, http.StatusAccepted, voiceJobCreatedResponse{
		JobID:     clientJobID,
		CreatedAt: now,
	})
}

func (h *chatVoiceHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/chat/voice: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}

// normalizeMimeType parses and reconstructs a MIME type string so that parameter
// spacing is consistent (e.g. "audio/ogg; codecs=opus" → "audio/ogg;codecs=opus").
func normalizeMimeType(mt string) string {
	mt = strings.TrimSpace(mt)
	mediaType, params, err := mime.ParseMediaType(mt)
	if err != nil {
		return strings.ToLower(mt)
	}
	result := strings.ToLower(mediaType)
	if codecs, ok := params["codecs"]; ok {
		result += ";codecs=" + strings.ToLower(codecs)
	}
	return result
}
