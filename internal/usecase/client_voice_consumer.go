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
	MessageID string `json:"messageId"`
	Source    struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"source"`
	Role            string    `json:"role"`
	Kind            string    `json:"kind"`
	Text            string    `json:"text"`
	AudioJobID      string    `json:"audioJobId,omitempty"`
	AudioDurationMs int64     `json:"audioDurationMs,omitempty"`
	TaskIDs         []string  `json:"taskIds,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
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

// Default timings of the voice ack race in Consume.
const (
	// defaultClientVoiceAckGrace is how long Consume waits for the real agent
	// reply after the first task mutation event before sending a synthetic one.
	defaultClientVoiceAckGrace = 1500 * time.Millisecond
	// defaultClientVoiceHardDeadline bounds the wait for the agent reply, both
	// in the race and in the background drain after a synthetic reply.
	defaultClientVoiceHardDeadline = 60 * time.Second
)

// clientVoiceSyntheticReply is the agent reply text sent when the agent has
// already mutated a task but its real reply is late or empty.
const clientVoiceSyntheticReply = "Готово"

type clientVoiceConsumer struct {
	db      *sql.DB
	history model.History
	events  model.EventStore
	broker  model.Broker
	chat    model.ChatService
	tasks   model.TaskStore
	logger  *slog.Logger
	now     func() time.Time

	ackGrace     time.Duration
	hardDeadline time.Duration
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

		ackGrace:     defaultClientVoiceAckGrace,
		hardDeadline: defaultClientVoiceHardDeadline,
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
	userEntry := model.HistoryEntry{
		AuthorName:      job.DeviceID,
		Text:            transcript,
		Timestamp:       now,
		Role:            model.HistoryRoleUser,
		Kind:            model.HistoryKindVoice,
		AudioJobID:      job.ClientJobID,
		AudioDurationMs: job.Duration.Milliseconds(),
	}
	_, userEv, err := c.insertChatEvent(ctx, sourceID, job.DeviceID, userEntry, job.ClientJobID)
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
		VoiceJobID:  job.ClientJobID,
	}
	if c.history != nil {
		msg.HistoryFn = func(hCtx context.Context) ([]model.HistoryEntry, error) {
			return c.history.Recent(hCtx, sourceID, clientHistoryLimit)
		}
	}

	// Race: ChatService.HandleMessage blocks until the LLM produces a text reply
	// (possibly after a second turn following a tool call). Once the agent has
	// mutated a task through a tool call, the broker has already published the
	// task event; everything that matters to the user is ready, so there is no
	// point in keeping the client overlay open for a few more seconds.
	//
	// HandleMessage runs in a separate goroutine while the broker is watched.
	// The broker fans every event out to every subscriber, so a task event on
	// this channel may come from another source (the Telegram pipeline, another
	// device's REST call). Task events therefore only arm the ack timer; the
	// task ids come from touched, the set scoped to this agent call that the
	// task service fills through the context passed to HandleMessage (the chat
	// service reuses it instead of creating its own). If the real reply has not
	// arrived within ackGrace after a task event (or by the hard deadline) and
	// the agent has touched a task, a synthetic agent reply ("Готово") with the
	// same audioJobId and those task ids closes the client session; a late real
	// reply is not sent, but its TasksTouched are merged into the synthetic
	// history entry. If the agent has touched nothing, the event came from
	// elsewhere and the race goes on.
	agentCtx, touched, _ := withTouched(ctx)
	ackEvents, ackUnsubscribe := c.broker.Subscribe(job.DeviceID)
	defer ackUnsubscribe()

	replyCh := make(chan model.ChatReply, 1)
	errCh := make(chan error, 1)
	go func() {
		reply, herr := c.chat.HandleMessage(agentCtx, msg)
		if herr != nil {
			errCh <- herr
			return
		}
		replyCh <- reply
	}()

	deadline := time.After(c.hardDeadline)
	var ackTimer <-chan time.Time

raceLoop:
	for {
		select {
		case ev, ok := <-ackEvents:
			if !ok {
				// Broker dropped subscription (slow consumer); stop racing and
				// fall back to waiting on HandleMessage normally.
				ackEvents = nil
				continue
			}
			// Events are not filtered by timestamp: recordEvent in the task
			// service leaves CreatedAt zero, and a time filter dropped valid
			// events. Whether the event belongs to this agent call is decided
			// by touched when the timer fires: the task service records the id
			// right after notifying the broker.
			if !isTaskMutationEvent(ev.Kind) {
				continue
			}
			if ackTimer == nil {
				ackTimer = time.After(c.ackGrace)
			}
		case <-ackTimer:
			taskIDs := touched.values()
			if len(taskIDs) == 0 {
				// The event was not caused by this agent call; wait for the
				// next task event, the real reply or the hard deadline.
				ackTimer = nil
				continue
			}
			msgID, err := c.emitAgentReply(ctx, sourceID, job.DeviceID, clientVoiceSyntheticReply, job.ClientJobID, taskIDs)
			if err != nil {
				c.Fail(ctx, job, fmt.Errorf("%w: client voice: persist agent reply: %w", errAgentFailed, err))
				return nil
			}
			c.drainHandleMessage(msgID, replyCh, errCh)
			break raceLoop
		case reply := <-replyCh:
			if reply.Text == "" && len(reply.TasksTouched) > 0 {
				reply.Text = clientVoiceSyntheticReply
			}
			if reply.Text == "" {
				c.Fail(ctx, job, fmt.Errorf("%w: client voice: agent returned empty reply", errAgentFailed))
				return nil
			}
			if _, err := c.emitAgentReply(ctx, sourceID, job.DeviceID, reply.Text, job.ClientJobID, reply.TasksTouched); err != nil {
				c.Fail(ctx, job, fmt.Errorf("%w: client voice: persist agent reply: %w", errAgentFailed, err))
				return nil
			}
			break raceLoop
		case herr := <-errCh:
			c.Fail(ctx, job, fmt.Errorf("%w: client voice: chat: %w", errAgentFailed, herr))
			return nil
		case <-deadline:
			if taskIDs := touched.values(); len(taskIDs) > 0 {
				msgID, err := c.emitAgentReply(ctx, sourceID, job.DeviceID, clientVoiceSyntheticReply, job.ClientJobID, taskIDs)
				if err != nil {
					c.Fail(ctx, job, fmt.Errorf("%w: client voice: persist agent reply: %w", errAgentFailed, err))
					return nil
				}
				c.drainHandleMessage(msgID, replyCh, errCh)
			} else {
				c.Fail(ctx, job, fmt.Errorf("%w: client voice: agent timeout", errAgentFailed))
			}
			break raceLoop
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// isTaskMutationEvent reports whether ev kind represents a task mutation that
// arms the ack timer of the voice fast path. The event itself is not
// attributed to the agent call: the touched set of the call decides that.
func isTaskMutationEvent(kind model.EventKind) bool {
	switch kind {
	case model.EventTaskCreated, model.EventTaskUpdated,
		model.EventTaskCompleted, model.EventTaskReopened,
		model.EventTaskMoved:
		return true
	default:
		return false
	}
}

// emitAgentReply persists an agent chat message with the touched task ids and
// notifies the broker. Returns the stored message id. Returns the persistence
// error so the caller can decide whether to escalate to a voice_failed event
// (the user-facing client still expects a terminal signal even when the agent
// reply itself fails to persist). audioJobID only correlates the live event
// with the voice job; the history row is a plain text reply.
func (c *clientVoiceConsumer) emitAgentReply(ctx context.Context, sourceID, deviceID, text, audioJobID string, taskIDs []string) (string, error) {
	entry := model.HistoryEntry{
		AuthorName: "agent",
		Text:       text,
		Timestamp:  c.now().UTC(),
		Role:       model.HistoryRoleAgent,
		Kind:       model.HistoryKindText,
		TaskIDs:    taskIDs,
	}
	msgID, agentEv, agentErr := c.insertChatEvent(ctx, sourceID, deviceID, entry, audioJobID)
	if agentErr != nil {
		c.logger.WarnContext(ctx, "client voice: failed to persist agent reply event", "err", agentErr)
		return "", agentErr
	}
	if c.broker != nil {
		c.broker.Notify(agentEv)
	}
	return msgID, nil
}

// drainHandleMessage waits for HandleMessage to finish in the background after
// a synthetic reply (messageID) was already delivered to the client. The reply
// text is discarded, but its TasksTouched are merged into the synthetic entry so
// the history links to every task the agent touched. The wait and the merge use
// a background context bounded by the hard deadline, so the goroutine cannot
// leak and survives the cancellation of the job context.
func (c *clientVoiceConsumer) drainHandleMessage(messageID string, replyCh <-chan model.ChatReply, errCh <-chan error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), c.hardDeadline)
		defer cancel()
		select {
		case reply := <-replyCh:
			if len(reply.TasksTouched) == 0 {
				return
			}
			if err := c.history.SetTaskIDs(ctx, messageID, reply.TasksTouched); err != nil {
				c.logger.WarnContext(ctx, "client voice: failed to merge late touched tasks into the synthetic reply",
					"message_id", messageID, "err", err)
			}
		case <-errCh:
		case <-ctx.Done():
		}
	}()
}

// Fail emits a voice_failed SSE event for a permanently-dropped client voice job
// and, in the same transaction, stores a system error row (role "system", kind
// "error") with the user-facing message in the device's chat history.
// The error code is derived from the cause type:
//   - ErrEmptyTranscript   → "invalid_audio"
//   - ErrUnsupportedFormat → "unsupported_format"
//   - errAgentFailed       → "agent_failed"
//   - all other causes     → "transcription_failed"
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

	// The failure is also kept in the chat history as a system row so that a
	// reconnecting client sees it. A history error must not suppress the
	// voice_failed event: the client still needs a terminal signal.
	failureEntry := model.HistoryEntry{
		AuthorName:      model.HistoryRoleSystem,
		Text:            userMsg,
		Timestamp:       now,
		Role:            model.HistoryRoleSystem,
		Kind:            model.HistoryKindError,
		AudioJobID:      job.ClientJobID,
		AudioDurationMs: job.Duration.Milliseconds(),
	}
	if _, histErr := c.history.AddTx(ctx, tx, clientChatSourceID(job.DeviceID), failureEntry); histErr != nil {
		c.logger.ErrorContext(ctx, "client voice: store voice failure in history", "err", histErr)
	}

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
// audioJobID is the voice job the live event refers to (set for both the
// transcript and the agent reply of a voice job).
func (c *clientVoiceConsumer) insertChatEvent(ctx context.Context, sourceID, deviceID string, entry model.HistoryEntry, audioJobID string) (messageID string, ev model.Event, err error) {
	tx, txErr := c.db.BeginTx(ctx, nil)
	if txErr != nil {
		return "", model.Event{}, txErr
	}
	defer tx.Rollback() //nolint:errcheck

	msgID, addErr := c.history.AddTx(ctx, tx, sourceID, entry)
	if addErr != nil {
		return "", model.Event{}, addErr
	}

	ts := entry.Timestamp
	p := clientChatMessagePayload{
		MessageID:       msgID,
		Role:            entry.Role,
		Kind:            entry.Kind,
		Text:            entry.Text,
		AudioJobID:      audioJobID,
		AudioDurationMs: entry.AudioDurationMs,
		TaskIDs:         entry.TaskIDs,
		CreatedAt:       ts,
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
