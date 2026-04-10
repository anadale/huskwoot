package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/anadale/huskwoot/internal/model"
)

// Agent is the interface for processing messages via a tool-calling loop.
type Agent interface {
	Handle(ctx context.Context, msg model.Message) (string, error)
}

// ChatServiceDeps collects the dependencies for ChatService.
type ChatServiceDeps struct {
	// Agent is the message handler (tool-calling loop).
	Agent Agent
	// DB is the database used to open a transaction for publishing chat_reply.
	// If nil, no event is published (ChatService acts as a thin wrapper).
	DB *sql.DB
	// Events is the domain event store; if nil, chat_reply is not published.
	Events model.EventStore
	// Broker is the in-memory SSE broker; Notify is called after commit (nil allowed).
	Broker model.Broker
}

type chatService struct {
	agent  Agent
	db     *sql.DB
	events model.EventStore
	broker model.Broker
}

// NewChatService creates a ChatService that delegates processing to the agent.
// It also accumulates touched task/project IDs via context: TaskService and
// ProjectService inside agent tools find the touched sets in ctx and append
// IDs of affected entities. After the agent replies, ChatService returns the
// collected IDs in ChatReply and publishes a chat_reply event via
// EventStore/Broker.
//
// Per-client history isolation is achieved via msg.Source.AccountID:
// the /v1/chat HTTP handler sets Source{AccountID: "client:<device_id>"} and
// a HistoryFn that reads the same source group. ChatService passes msg to the
// agent unmodified — the agent and its tools already operate on msg.Source.
func NewChatService(deps ChatServiceDeps) model.ChatService {
	return &chatService{
		agent:  deps.Agent,
		db:     deps.DB,
		events: deps.Events,
		broker: deps.Broker,
	}
}

// chatReplyPayload is the JSON snapshot of the agent response for the chat_reply event.
type chatReplyPayload struct {
	Reply           string   `json:"reply"`
	TasksTouched    []string `json:"tasks_touched,omitempty"`
	ProjectsTouched []string `json:"projects_touched,omitempty"`
}

func (s *chatService) HandleMessage(ctx context.Context, msg model.Message) (model.ChatReply, error) {
	ctx, tasks, projects := withTouched(ctx)

	text, err := s.agent.Handle(ctx, msg)
	if err != nil {
		return model.ChatReply{}, fmt.Errorf("agent: %w", err)
	}

	reply := model.ChatReply{
		Text:            text,
		TasksTouched:    tasks.values(),
		ProjectsTouched: projects.values(),
	}

	if err := s.emitChatReply(ctx, msg, reply); err != nil {
		return model.ChatReply{}, err
	}

	return reply, nil
}

// emitChatReply inserts a chat_reply into EventStore within a transaction and
// notifies the broker after commit. The push queue is not touched — per the
// decision table, chat_reply does not trigger push notifications.
func (s *chatService) emitChatReply(ctx context.Context, msg model.Message, reply model.ChatReply) error {
	if s.events == nil || s.db == nil {
		return nil
	}

	payload := chatReplyPayload{
		Reply:           reply.Text,
		TasksTouched:    reply.TasksTouched,
		ProjectsTouched: reply.ProjectsTouched,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("serializing chat_reply: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("opening chat_reply transaction: %w", err)
	}
	defer tx.Rollback()

	ev := model.Event{
		Kind:     model.EventChatReply,
		EntityID: msg.Source.AccountID,
		Payload:  raw,
	}
	seq, err := s.events.Insert(ctx, tx, ev)
	if err != nil {
		return fmt.Errorf("inserting chat_reply: %w", err)
	}
	ev.Seq = seq

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chat_reply: %w", err)
	}

	if s.broker != nil {
		s.broker.Notify(ev)
	}
	return nil
}
