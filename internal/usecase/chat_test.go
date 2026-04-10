package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

type mockAgent struct {
	reply  string
	err    error
	calls  int
	gotCtx context.Context
}

func (m *mockAgent) Handle(ctx context.Context, _ model.Message) (string, error) {
	m.calls++
	m.gotCtx = ctx
	return m.reply, m.err
}

// mockAgentUsingTaskService is an agent that calls TaskService.CreateTask inside
// Handle to verify that ChatService threads touched sets through ctx
// and collects them in ChatReply.
type mockAgentUsingTaskService struct {
	taskSvc model.TaskService
	reply   string
}

func (m *mockAgentUsingTaskService) Handle(ctx context.Context, _ model.Message) (string, error) {
	if _, err := m.taskSvc.CreateTask(ctx, model.CreateTaskRequest{Summary: "через агента"}); err != nil {
		return "", err
	}
	return m.reply, nil
}

func TestChatServiceDelegatesToAgent(t *testing.T) {
	a := &mockAgent{reply: "ответ"}
	svc := usecase.NewChatService(usecase.ChatServiceDeps{Agent: a})
	rep, err := svc.HandleMessage(context.Background(), model.Message{Text: "привет"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Text != "ответ" {
		t.Fatalf("Text=%q, want %q", rep.Text, "ответ")
	}
	if a.calls != 1 {
		t.Fatalf("agent.Handle called %d times, expected 1", a.calls)
	}
}

func TestChatServicePropagatesAgentError(t *testing.T) {
	a := &mockAgent{err: errors.New("агент упал")}
	svc := usecase.NewChatService(usecase.ChatServiceDeps{Agent: a})
	_, err := svc.HandleMessage(context.Background(), model.Message{Text: "привет"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatServicePopulatesTouchedTasks(t *testing.T) {
	f := newTestFixture(t)
	agent := &mockAgentUsingTaskService{taskSvc: f.svc, reply: "создал задачу"}
	svc := usecase.NewChatService(usecase.ChatServiceDeps{
		Agent:  agent,
		DB:     f.db,
		Events: f.events,
		Broker: f.broker,
	})

	rep, err := svc.HandleMessage(context.Background(), model.Message{Text: "запиши"})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if rep.Text != "создал задачу" {
		t.Fatalf("Text=%q, want %q", rep.Text, "создал задачу")
	}
	if len(rep.TasksTouched) != 1 {
		t.Fatalf("TasksTouched=%v, want 1 item", rep.TasksTouched)
	}
	if rep.TasksTouched[0] != "task-uuid-1" {
		t.Fatalf("TasksTouched[0]=%q, want %q", rep.TasksTouched[0], "task-uuid-1")
	}
	if len(rep.ProjectsTouched) != 0 {
		t.Fatalf("ProjectsTouched=%v, want empty", rep.ProjectsTouched)
	}
}

func TestChatServiceEmitsChatReplyEvent(t *testing.T) {
	f := newTestFixture(t)
	svc := usecase.NewChatService(usecase.ChatServiceDeps{
		Agent:  &mockAgent{reply: "готово"},
		DB:     f.db,
		Events: f.events,
		Broker: f.broker,
	})

	_, err := svc.HandleMessage(context.Background(), model.Message{
		Text:   "привет",
		Source: model.Source{AccountID: "client:dev-1"},
	})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	events := f.events.recorded()
	var chatEv *model.Event
	for i := range events {
		if events[i].Kind == model.EventChatReply {
			chatEv = &events[i]
			break
		}
	}
	if chatEv == nil {
		t.Fatalf("chat_reply not recorded (events=%+v)", events)
	}
	if chatEv.EntityID != "client:dev-1" {
		t.Fatalf("EntityID=%q, want %q", chatEv.EntityID, "client:dev-1")
	}

	var payload struct {
		Reply           string   `json:"reply"`
		TasksTouched    []string `json:"tasks_touched"`
		ProjectsTouched []string `json:"projects_touched"`
	}
	if err := json.Unmarshal(chatEv.Payload, &payload); err != nil {
		t.Fatalf("payload failed to parse: %v", err)
	}
	if payload.Reply != "готово" {
		t.Fatalf("payload.reply=%q, want %q", payload.Reply, "готово")
	}

	if n := len(f.queue.snapshot()); n != 0 {
		t.Fatalf("push.Enqueue called %d times, expected 0 (chat_reply does not trigger push)", n)
	}

	notified := f.broker.notifiedEvents()
	var gotChat bool
	for _, n := range notified {
		if n.Kind == model.EventChatReply {
			gotChat = true
			break
		}
	}
	if !gotChat {
		t.Fatalf("broker.Notify not called for chat_reply: %+v", notified)
	}
}

func TestChatServiceWithoutEventStoreSkipsPublish(t *testing.T) {
	svc := usecase.NewChatService(usecase.ChatServiceDeps{Agent: &mockAgent{reply: "без realtime"}})
	rep, err := svc.HandleMessage(context.Background(), model.Message{Text: "привет"})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if rep.Text != "без realtime" {
		t.Fatalf("Text=%q, want %q", rep.Text, "без realtime")
	}
}
