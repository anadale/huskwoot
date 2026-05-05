package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

const (
	// defaultChatHistoryLimit is the default number of entries returned by GET /v1/chat/history.
	defaultChatHistoryLimit = 50
	// maxChatHistoryLimit is the hard upper bound on the limit for GET /v1/chat/history.
	maxChatHistoryLimit = 500
	// chatSourceKind is the Source.Kind set for HTTP client chat.
	chatSourceKind = "client"
)

// chatSourceID builds the history source identifier for device deviceID.
// The "client:<device_id>" format isolates client chat from Telegram DMs (source=Telegram-chat-id).
func chatSourceID(deviceID string) string {
	return "client:" + deviceID
}

// chatRequest is the request body for POST /v1/chat.
type chatRequest struct {
	Message string `json:"message"`
}

// chatResponse is the response body for POST /v1/chat.
type chatResponse struct {
	Reply           string   `json:"reply"`
	TasksTouched    []string `json:"tasksTouched,omitempty"`
	ProjectsTouched []string `json:"projectsTouched,omitempty"`
}

// chatHistoryEntry is a single entry in the GET /v1/chat/history response.
type chatHistoryEntry struct {
	AuthorName string    `json:"authorName"`
	Text       string    `json:"text"`
	Timestamp  time.Time `json:"timestamp"`
}

// chatHistoryResponse is the response body for GET /v1/chat/history.
type chatHistoryResponse struct {
	Entries []chatHistoryEntry `json:"entries"`
}

// chatMessageCreatedPayload is the JSON payload for chat_message_created events.
type chatMessageCreatedPayload struct {
	MessageID  string `json:"messageId"`
	Source     struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"source"`
	Role       string     `json:"role"`
	Text       string     `json:"text"`
	AudioJobID string     `json:"audioJobId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// chatHandler handles POST /v1/chat and GET /v1/chat/history. Client messages
// are written to the History store under a separate source "client:<device_id>"
// so they do not mix with the owner's Telegram DMs.
type chatHandler struct {
	service     model.ChatService
	history     model.History
	db          *sql.DB
	events      model.EventStore
	broker      model.Broker
	chatTimeout time.Duration
	logger      *slog.Logger
	// now provides the current time for history entries; a field for
	// predictability in tests; time.Now in production.
	now func() time.Time
}

// newChatHandler is the constructor with safe defaults.
func newChatHandler(service model.ChatService, history model.History, db *sql.DB, events model.EventStore, broker model.Broker, chatTimeout time.Duration, logger *slog.Logger) *chatHandler {
	return &chatHandler{
		service:     service,
		history:     history,
		db:          db,
		events:      events,
		broker:      broker,
		chatTimeout: chatTimeout,
		logger:      logger,
		now:         time.Now,
	}
}

// post handles POST /v1/chat.
//
// Two-transaction pattern:
//
//	tx1 → History.AddTx(user) + EventStore.Insert(chat_message_created) → commit → Broker.Notify
//	ChatService.HandleMessage
//	tx2 → History.AddTx(agent) + EventStore.Insert(chat_message_created) → commit → Broker.Notify
//
// Between tx1 and tx2 there is a window where SSE subscribers see the user message
// but not yet the agent reply. Contract: clients wait ~2 minutes for the agent event
// and treat a missing agent event as a request failure (no auto-retry). On a server
// crash between the two commits the user message remains in history as an orphan —
// this is acceptable semantics ("message sent, reply lost").
func (h *chatHandler) post(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(req.Message)
	if text == "" {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "message field is required")
		return
	}

	deviceID := DeviceIDFromContext(r.Context())
	sourceID := chatSourceID(deviceID)

	// /v1/chat may take longer than the global http.Server WriteTimeout
	// (RequestTimeout=30 s default, ChatTimeout=60 s). Clear the write deadline
	// for this handler so the agent reply is not cut off at the TCP level.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	ctx := r.Context()
	if h.chatTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.chatTimeout)
		defer cancel()
	}

	now := h.now().UTC()

	userEntry := model.HistoryEntry{
		AuthorName: deviceID,
		Text:       text,
		Timestamp:  now,
	}

	// tx1: persist user message + emit chat_message_created(user)
	if h.history != nil {
		_, ev, err := h.insertChatEvent(ctx, sourceID, deviceID, "user", text, now, "")
		if err != nil {
			h.logError(r.Context(), "history add user", err)
			// fall back to non-transactional write
			if addErr := h.history.Add(ctx, sourceID, userEntry); addErr != nil {
				h.logError(r.Context(), "history add user fallback", addErr)
			}
		} else if h.broker != nil && ev.Kind != "" {
			h.broker.Notify(ev)
		}
	}

	msg := model.Message{
		ID:         deviceID + ":" + strconv.FormatInt(now.UnixNano(), 10),
		Kind:       model.MessageKindDM,
		Source:     model.Source{Kind: chatSourceKind, ID: deviceID, AccountID: sourceID},
		Author:     deviceID,
		AuthorName: deviceID,
		Text:       text,
		Timestamp:  now,
	}
	if h.history != nil {
		msg.HistoryFn = func(c context.Context) ([]model.HistoryEntry, error) {
			return h.history.Recent(c, sourceID, maxChatHistoryLimit)
		}
	}

	reply, err := h.service.HandleMessage(ctx, msg)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			WriteError(w, http.StatusGatewayTimeout, ErrorCodeTimeout, "agent did not respond within the allotted time")
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			WriteError(w, http.StatusRequestTimeout, ErrorCodeTimeout, "client disconnected before agent response")
			return
		}
		h.logError(r.Context(), "chat handle", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to process message")
		return
	}

	// tx2: persist agent reply + emit chat_message_created(agent)
	if h.history != nil && reply.Text != "" {
		agentNow := h.now().UTC()
		_, ev, err := h.insertChatEvent(r.Context(), sourceID, deviceID, "agent", reply.Text, agentNow, "")
		if err != nil {
			h.logError(r.Context(), "history add agent", err)
			// fall back to non-transactional write
			agentEntry := model.HistoryEntry{
				AuthorName: "agent",
				Text:       reply.Text,
				Timestamp:  agentNow,
			}
			if addErr := h.history.Add(r.Context(), sourceID, agentEntry); addErr != nil {
				h.logError(r.Context(), "history add agent fallback", addErr)
			}
		} else if h.broker != nil && ev.Kind != "" {
			h.broker.Notify(ev)
		}
	}

	writeJSON(w, http.StatusOK, chatResponse{
		Reply:           reply.Text,
		TasksTouched:    reply.TasksTouched,
		ProjectsTouched: reply.ProjectsTouched,
	})
}

// insertChatEvent writes a history entry + chat_message_created event in a single transaction.
// Returns the generated messageID and the saved event (with Seq filled in).
// audioJobID is set only for voice-originated messages.
func (h *chatHandler) insertChatEvent(ctx context.Context, sourceID, deviceID, role, text string, ts time.Time, audioJobID string) (messageID string, ev model.Event, err error) {
	if h.db == nil || h.events == nil {
		// No DB/events — fall back to plain history write only.
		err = h.history.Add(ctx, sourceID, model.HistoryEntry{
			AuthorName: authorNameForRole(role, deviceID),
			Text:       text,
			Timestamp:  ts,
		})
		return "", model.Event{}, err
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", model.Event{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	msgID, err := h.history.AddTx(ctx, tx, sourceID, model.HistoryEntry{
		AuthorName: authorNameForRole(role, deviceID),
		Text:       text,
		Timestamp:  ts,
	})
	if err != nil {
		return "", model.Event{}, err
	}

	payload := chatMessageCreatedPayload{
		MessageID: msgID,
		Role:      role,
		Text:      text,
		CreatedAt: ts,
	}
	payload.Source.Kind = chatSourceKind
	payload.Source.ID = deviceID
	if audioJobID != "" {
		payload.AudioJobID = audioJobID
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", model.Event{}, err
	}

	seq, err := h.events.Insert(ctx, tx, model.Event{
		Kind:      model.EventChatMessageCreated,
		EntityID:  deviceID,
		Payload:   payloadBytes,
		CreatedAt: ts,
	})
	if err != nil {
		return "", model.Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return "", model.Event{}, err
	}

	ev = model.Event{
		Seq:       seq,
		Kind:      model.EventChatMessageCreated,
		EntityID:  deviceID,
		Payload:   payloadBytes,
		CreatedAt: ts,
	}
	return msgID, ev, nil
}

// authorNameForRole returns the AuthorName for a history entry based on role.
func authorNameForRole(role, deviceID string) string {
	if role == "agent" {
		return "agent"
	}
	return deviceID
}

// getHistory handles GET /v1/chat/history: returns the most recent client chat
// entries for the current device, isolated from Telegram DMs via the "client:"
// source prefix.
func (h *chatHandler) getHistory(w http.ResponseWriter, r *http.Request) {
	if h.history == nil {
		writeJSON(w, http.StatusOK, chatHistoryResponse{Entries: []chatHistoryEntry{}})
		return
	}

	limit := defaultChatHistoryLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v <= 0 {
			WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "limit must be a positive number")
			return
		}
		if v > maxChatHistoryLimit {
			v = maxChatHistoryLimit
		}
		limit = v
	}

	deviceID := DeviceIDFromContext(r.Context())
	sourceID := chatSourceID(deviceID)

	entries, err := h.history.Recent(r.Context(), sourceID, limit)
	if err != nil {
		h.logError(r.Context(), "history recent", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve history")
		return
	}

	out := make([]chatHistoryEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, chatHistoryEntry{
			AuthorName: e.AuthorName,
			Text:       e.Text,
			Timestamp:  e.Timestamp,
		})
	}
	writeJSON(w, http.StatusOK, chatHistoryResponse{Entries: out})
}

func (h *chatHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/chat: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}
