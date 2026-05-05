package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/transcribe"
)

const clientHistoryLimit = 500

// clientChatSourceID returns the history source identifier for client voice jobs.
// Mirrors api.chatSourceID — keeping "client:<deviceID>" format for consistency.
func clientChatSourceID(deviceID string) string {
	return "client:" + deviceID
}

// clientChatMessagePayload is the JSON payload for chat_message_created events
// produced by the client voice pipeline.
type clientChatMessagePayload struct {
	MessageID  string `json:"messageId"`
	Source     struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"source"`
	Role       string    `json:"role"`
	Text       string    `json:"text"`
	AudioJobID string    `json:"audioJobId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// clientVoiceFailedPayload is the JSON payload for voice_failed events.
type clientVoiceFailedPayload struct {
	JobID   string `json:"jobId"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ClientVoiceConsumerDeps collects the dependencies for clientVoiceConsumer.
type ClientVoiceConsumerDeps struct {
	DB      *sql.DB
	History model.History
	Events  model.EventStore
	Broker  model.Broker
	Chat    model.ChatService
	// Tasks is used to resolve the optional project hint from job.ProjectID.
	// May be nil; hint resolution is skipped when nil.
	Tasks  model.TaskStore
	Logger *slog.Logger
}

// errAgentFailed is a sentinel that marks failures caused by ChatService.HandleMessage,
// distinct from transcription failures.
var errAgentFailed = errors.New("agent failed")

type clientVoiceConsumer struct {
	db      *sql.DB
	history model.History
	events  model.EventStore
	broker  model.Broker
	chat    model.ChatService
	tasks   model.TaskStore
	logger  *slog.Logger
	now     func() time.Time
}

// NewClientVoiceConsumer creates a model.VoiceConsumer for HTTP API client voice jobs.
// DB, History, Events, Broker, and Chat are required; Tasks may be nil.
func NewClientVoiceConsumer(deps ClientVoiceConsumerDeps) model.VoiceConsumer {
	if deps.DB == nil {
		panic("NewClientVoiceConsumer: DB is required")
	}
	if deps.History == nil {
		panic("NewClientVoiceConsumer: History is required")
	}
	if deps.Events == nil {
		panic("NewClientVoiceConsumer: Events is required")
	}
	if deps.Broker == nil {
		panic("NewClientVoiceConsumer: Broker is required")
	}
	if deps.Chat == nil {
		panic("NewClientVoiceConsumer: Chat is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &clientVoiceConsumer{
		db:      deps.DB,
		history: deps.History,
		events:  deps.Events,
		broker:  deps.Broker,
		chat:    deps.Chat,
		tasks:   deps.Tasks,
		logger:  logger,
		now:     time.Now,
	}
}

// Consume processes a successfully transcribed client voice job.
//
// Two-transaction pattern:
//
//	tx1 → History.AddTx(user) + EventStore.Insert(chat_message_created) → commit → Broker.Notify
//	ChatService.HandleMessage
//	tx2 → History.AddTx(agent) + EventStore.Insert(chat_message_created) → commit → Broker.Notify
func (c *clientVoiceConsumer) Consume(ctx context.Context, job model.VoiceJob, transcript string) error {
	sourceID := clientChatSourceID(job.DeviceID)
	now := c.now().UTC()

	// tx1: persist user message + emit chat_message_created(user) with audioJobId
	_, userEv, err := c.insertChatEvent(ctx, sourceID, job.DeviceID, "user", transcript, now, job.ClientJobID)
	if err != nil {
		return fmt.Errorf("client voice: user event: %w", err)
	}
	if c.broker != nil {
		c.broker.Notify(userEv)
	}

	// Resolve optional project context hint
	var projectHint *model.Project
	if job.ProjectID != "" && c.tasks != nil {
		p, pErr := c.tasks.GetProject(ctx, job.ProjectID)
		if pErr != nil {
			c.logger.WarnContext(ctx, "client voice: failed to resolve project hint, continuing without hint",
				"project_id", job.ProjectID, "err", pErr)
		} else if p != nil {
			projectHint = p
		}
	}

	msg := model.Message{
		ID:          job.DeviceID + ":" + strconv.FormatInt(now.UnixNano(), 10),
		Kind:        model.MessageKindDM,
		Source:      model.Source{Kind: "client", ID: job.DeviceID, AccountID: sourceID},
		Author:      job.DeviceID,
		AuthorName:  job.DeviceID,
		Text:        transcript,
		Timestamp:   now,
		ProjectHint: projectHint,
	}
	if c.history != nil {
		msg.HistoryFn = func(hCtx context.Context) ([]model.HistoryEntry, error) {
			return c.history.Recent(hCtx, sourceID, clientHistoryLimit)
		}
	}

	reply, err := c.chat.HandleMessage(ctx, msg)
	if err != nil {
		// tx1 is already committed — retrying would emit a duplicate user message.
		// Signal failure via voice_failed and tell the worker the job is done.
		c.Fail(ctx, job, fmt.Errorf("%w: client voice: chat: %w", errAgentFailed, err))
		return nil
	}

	// tx2: persist agent reply + emit chat_message_created(agent) with audioJobId
	if reply.Text == "" {
		// An empty reply has no message to persist; signal failure so the client
		// receives a voice_failed event instead of hanging indefinitely.
		c.Fail(ctx, job, fmt.Errorf("%w: client voice: agent returned empty reply", errAgentFailed))
		return nil
	}
	agentNow := c.now().UTC()
	_, agentEv, agentErr := c.insertChatEvent(ctx, sourceID, job.DeviceID, "agent", reply.Text, agentNow, job.ClientJobID)
	if agentErr != nil {
		c.logger.WarnContext(ctx, "client voice: failed to persist agent reply event", "err", agentErr)
		c.Fail(ctx, job, fmt.Errorf("%w: client voice: persist agent reply: %w", errAgentFailed, agentErr))
	} else if c.broker != nil {
		c.broker.Notify(agentEv)
	}

	return nil
}

// Fail emits a voice_failed SSE event for a permanently-dropped client voice job.
// The error code is derived from the cause type:
//   - ErrEmptyTranscript → "invalid_audio"
//   - errAgentFailed     → "agent_failed"
//   - all other causes   → "transcription_failed"
func (c *clientVoiceConsumer) Fail(ctx context.Context, job model.VoiceJob, cause error) {
	code := "transcription_failed"
	var userMsg string
	switch {
	case errors.Is(cause, transcribe.ErrEmptyTranscript):
		code = "invalid_audio"
		userMsg = "audio does not contain recognisable speech"
	case errors.Is(cause, transcribe.ErrUnsupportedFormat):
		code = "unsupported_format"
		userMsg = "audio format is not supported by the transcription service"
	case errors.Is(cause, errAgentFailed):
		code = "agent_failed"
		userMsg = "agent could not process the message"
	default:
		userMsg = "transcription failed; please try again"
	}
	c.logger.ErrorContext(ctx, "client voice: job failed", "code", code, "err", cause)

	payload := clientVoiceFailedPayload{
		JobID:   job.ClientJobID,
		Code:    code,
		Message: userMsg,
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		c.logger.ErrorContext(ctx, "client voice: marshal voice_failed payload", "err", marshalErr)
		return
	}

	if c.events == nil || c.db == nil {
		return
	}

	now := c.now().UTC()
	tx, txErr := c.db.BeginTx(ctx, nil)
	if txErr != nil {
		c.logger.ErrorContext(ctx, "client voice: begin tx for voice_failed event", "err", txErr)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	ev := model.Event{
		Kind:      model.EventVoiceFailed,
		EntityID:  job.DeviceID,
		Payload:   raw,
		CreatedAt: now,
	}
	seq, insertErr := c.events.Insert(ctx, tx, ev)
	if insertErr != nil {
		c.logger.ErrorContext(ctx, "client voice: insert voice_failed event", "err", insertErr)
		return
	}
	ev.Seq = seq

	if commitErr := tx.Commit(); commitErr != nil {
		c.logger.ErrorContext(ctx, "client voice: commit voice_failed event", "err", commitErr)
		return
	}

	if c.broker != nil {
		c.broker.Notify(ev)
	}
}

// insertChatEvent writes a history entry + chat_message_created event in a single transaction.
// Returns the generated messageID and the saved event (with Seq filled in).
// audioJobID is set for voice-originated messages.
func (c *clientVoiceConsumer) insertChatEvent(ctx context.Context, sourceID, deviceID, role, text string, ts time.Time, audioJobID string) (messageID string, ev model.Event, err error) {
	tx, txErr := c.db.BeginTx(ctx, nil)
	if txErr != nil {
		return "", model.Event{}, txErr
	}
	defer tx.Rollback() //nolint:errcheck

	authorName := deviceID
	if role == "agent" {
		authorName = "agent"
	}

	msgID, addErr := c.history.AddTx(ctx, tx, sourceID, model.HistoryEntry{
		AuthorName: authorName,
		Text:       text,
		Timestamp:  ts,
	})
	if addErr != nil {
		return "", model.Event{}, addErr
	}

	p := clientChatMessagePayload{
		MessageID:  msgID,
		Role:       role,
		Text:       text,
		AudioJobID: audioJobID,
		CreatedAt:  ts,
	}
	p.Source.Kind = "client"
	p.Source.ID = deviceID

	payloadBytes, marshErr := json.Marshal(p)
	if marshErr != nil {
		return "", model.Event{}, marshErr
	}

	seq, insertErr := c.events.Insert(ctx, tx, model.Event{
		Kind:      model.EventChatMessageCreated,
		EntityID:  deviceID,
		Payload:   payloadBytes,
		CreatedAt: ts,
	})
	if insertErr != nil {
		return "", model.Event{}, insertErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return "", model.Event{}, commitErr
	}

	savedEv := model.Event{
		Seq:       seq,
		Kind:      model.EventChatMessageCreated,
		EntityID:  deviceID,
		Payload:   payloadBytes,
		CreatedAt: ts,
	}
	return msgID, savedEv, nil
}
