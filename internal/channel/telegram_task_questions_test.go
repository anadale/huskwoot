package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/anadale/huskwoot/internal/model"
)

const taskQuestionCandidateID = "876ab549-c295-4f7c-959d-c481f6115fbd"

type taskQuestionService struct {
	model.AutoTaskService
	candidate    model.TaskCandidate
	decisionTask *model.Task
	getErr       error
	decideErr    error
	markErr      error
	created      int
	decisions    int
	marks        int
	afterMark    func()
}

func (s *taskQuestionService) GetCandidate(context.Context, string) (*model.TaskCandidate, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	c := s.candidate
	return &c, nil
}

func (s *taskQuestionService) Decide(_ context.Context, _ string, approve bool) (model.TaskCandidateDecision, error) {
	s.decisions++
	if s.decideErr != nil {
		return model.TaskCandidateDecision{}, s.decideErr
	}
	if s.candidate.Status == "pending" {
		if approve {
			s.candidate.Status = "created"
			s.candidate.CreatedTaskID = "created-task"
			s.created++
		} else {
			s.candidate.Status = "rejected"
		}
	}
	return model.TaskCandidateDecision{Candidate: s.candidate, Task: s.decisionTask}, nil
}

func (s *taskQuestionService) PendingQuestions(context.Context, int) ([]model.TaskCandidate, error) {
	if s.candidate.Status == "pending" && s.candidate.QuestionMsgID == 0 {
		return []model.TaskCandidate{s.candidate}, nil
	}
	return nil, nil
}

func (s *taskQuestionService) MarkQuestionSent(_ context.Context, _ string, chatID int64, messageID int) error {
	s.marks++
	if s.afterMark != nil {
		defer s.afterMark()
	}
	if s.markErr != nil {
		return s.markErr
	}
	s.candidate.QuestionChatID = chatID
	s.candidate.QuestionMsgID = messageID
	return nil
}

func taskQuestionPending() model.TaskCandidate {
	deadline := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	return model.TaskCandidate{
		ID: taskQuestionCandidateID, Status: "pending",
		Task:   model.CreateTaskRequest{Summary: "Send report", Details: "Quarterly report", ProjectID: "project-id", Deadline: &deadline},
		Reason: "The deadline differs", MatchID: "task:existing-task", MatchSummary: "Send the report", MatchDisplayID: "work#7",
	}
}

func taskQuestionCallback(action string, messageID int) *tgbotapi.CallbackQuery {
	data := "task:" + action + ":" + taskQuestionCandidateID
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Choose", data)))
	return &tgbotapi.CallbackQuery{
		ID: "callback-id", From: makeUser(12345, "owner", "", ""), Data: data,
		Message: &tgbotapi.Message{MessageID: messageID, Chat: makePrivateChat(12345), From: &tgbotapi.User{ID: 99, IsBot: true}, ReplyMarkup: &markup},
	}
}

func newTaskQuestionChannel(t *testing.T, bot botAPI, service model.AutoTaskService) *TelegramChannel {
	t.Helper()
	w := newTelegramChannel(bot, TelegramChannelConfig{OwnerIDs: []string{"12345"}, BotID: "99", Localizer: makeTestLocalizer(t, "en")}, newMockStateStore(), nil, nil, HistoryConfig{})
	w.SetTaskCandidates(service)
	return w
}

func TestTaskQuestionOwnerDecisionAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name, action, wantStatus string
		wantCreated              int
	}{
		{"approve", "yes", "created", 1},
		{"reject", "no", "rejected", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot := newMockBot(0)
			service := &taskQuestionService{candidate: taskQuestionPending()}
			w := newTaskQuestionChannel(t, bot, service)
			handler := func(context.Context, model.Message) error { t.Fatal("callback reached message handler"); return nil }
			if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: taskQuestionCallback(tc.action, 41)}, handler) {
				t.Fatal("decision did not complete")
			}
			if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: taskQuestionCallback("yes", 41)}, handler) {
				t.Fatal("replay did not complete")
			}
			if service.candidate.Status != tc.wantStatus || service.created != tc.wantCreated {
				t.Fatalf("status=%s, created=%d", service.candidate.Status, service.created)
			}
			acks := 0
			for _, endpoint := range bot.makeReqCalls {
				if endpoint == "answerCallbackQuery" {
					acks++
				}
			}
			if acks != 2 {
				t.Fatalf("callback acknowledgements=%d, want 2", acks)
			}
		})
	}
}

func TestTaskQuestionRejectsUnauthorizedOrTamperedCallbacks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*tgbotapi.CallbackQuery)
	}{
		{"other actor", func(c *tgbotapi.CallbackQuery) { c.From.ID = 67890 }},
		{"other private chat", func(c *tgbotapi.CallbackQuery) { c.Message.Chat.ID = 67890 }},
		{"group chat", func(c *tgbotapi.CallbackQuery) { c.Message.Chat.Type = "group" }},
		{"wrong bot", func(c *tgbotapi.CallbackQuery) { c.Message.From.ID = 101 }},
		{"not a bot message", func(c *tgbotapi.CallbackQuery) { c.Message.From.IsBot = false }},
		{"tampered action", func(c *tgbotapi.CallbackQuery) { c.Data = "task:maybe:" + taskQuestionCandidateID }},
		{"tampered candidate", func(c *tgbotapi.CallbackQuery) { c.Data = "task:yes:10000000-0000-4000-8000-000000000000" }},
		{"unbound message without keyboard", func(c *tgbotapi.CallbackQuery) { c.Message.ReplyMarkup = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot := newMockBot(0)
			service := &taskQuestionService{candidate: taskQuestionPending()}
			w := newTaskQuestionChannel(t, bot, service)
			callback := taskQuestionCallback("yes", 41)
			tc.mutate(callback)
			if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: callback}, nil) {
				t.Fatal("invalid callback requested retry")
			}
			if service.decisions != 0 || service.created != 0 {
				t.Fatal("unauthorized callback decided candidate")
			}
			if len(bot.makeReqCalls) != 1 || bot.makeReqCalls[0] != "answerCallbackQuery" {
				t.Fatalf("callback not safely acknowledged: %v", bot.makeReqCalls)
			}
		})
	}
}

func TestTaskQuestionDatabaseFailurePreventsCursorAdvance(t *testing.T) {
	for _, operation := range []string{"get", "decide"} {
		t.Run(operation, func(t *testing.T) {
			bot := newMockBot(0)
			service := &taskQuestionService{candidate: taskQuestionPending()}
			if operation == "get" {
				service.getErr = errors.New("database unavailable")
			} else {
				service.decideErr = errors.New("database unavailable")
			}
			w := newTaskQuestionChannel(t, bot, service)
			if w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: taskQuestionCallback("yes", 41)}, nil) {
				t.Fatal("database failure allowed cursor advancement")
			}
			if service.created != 0 {
				t.Fatal("database failure created a task")
			}
			if len(bot.makeReqCalls) != 1 || bot.makeReqCalls[0] != "answerCallbackQuery" {
				t.Fatal("failed callback was not acknowledged")
			}
		})
	}
}

func TestTaskQuestionCallbackDoesNotFlushBufferedForwards(t *testing.T) {
	bot := newMockBot(0)
	service := &taskQuestionService{candidate: taskQuestionPending()}
	w := newTaskQuestionChannel(t, bot, service)
	buffer := newMockForwardBuffer()
	buffer.hasLast = true
	signals := make(chan model.ForwardDispatchSignal, 1)
	w.cfg.ForwardBuffer = buffer
	w.cfg.ForwardSignal = signals
	history := &mockHistory{}
	w.history = history
	callback := taskQuestionCallback("no", 41)
	callback.Message.Text = "Reject task"
	if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: callback}, func(context.Context, model.Message) error {
		t.Fatal("callback reached message handler")
		return nil
	}) {
		t.Fatal("callback failed")
	}
	if len(signals) != 0 || len(buffer.appendCalls) != 0 {
		t.Fatal("callback dispatched or appended forwards")
	}
	if len(history.addCalls) != 0 {
		t.Fatal("callback was recorded in conversation history")
	}
	if service.candidate.Status != "rejected" {
		t.Fatal("callback was not routed to task decision")
	}
}

type taskQuestionBot struct {
	*mockBot
	afterSend func()
}

func (b *taskQuestionBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	message, err := b.mockBot.Send(c)
	if b.afterSend != nil {
		b.afterSend()
	}
	return message, err
}

func TestTaskQuestionSendFailureIsDeliveredAfterRestart(t *testing.T) {
	service := &taskQuestionService{candidate: taskQuestionPending()}
	ctx, cancel := context.WithCancel(context.Background())
	bot := &taskQuestionBot{mockBot: newMockBot(0), afterSend: cancel}
	bot.sendErr = errors.New("Telegram unavailable")
	w := newTaskQuestionChannel(t, bot, service)
	if err := w.RunTaskQuestions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("sender error=%v", err)
	}
	if service.marks != 0 || service.candidate.QuestionMsgID != 0 {
		t.Fatal("failed send was marked delivered")
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	service.afterMark = cancel
	restartedBot := newMockBot(0)
	restartedBot.sendResult = tgbotapi.Message{MessageID: 42}
	restarted := newTaskQuestionChannel(t, restartedBot, service)
	if err := restarted.RunTaskQuestions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("restart sender error=%v", err)
	}
	if len(restartedBot.sendCalled) != 1 || service.candidate.QuestionMsgID != 42 || service.candidate.QuestionChatID != 12345 {
		t.Fatal("restart did not deliver pending question")
	}
	message := restartedBot.sendCalled[0].(tgbotapi.MessageConfig)
	if message.ChatID != 12345 {
		t.Fatalf("question chat=%d", message.ChatID)
	}
	for _, value := range []string{service.candidate.Task.Summary, service.candidate.Task.Details, service.candidate.Task.ProjectID, service.candidate.Task.Deadline.Format(time.RFC3339), service.candidate.MatchSummary, service.candidate.MatchDisplayID, service.candidate.Reason} {
		if !strings.Contains(message.Text, value) {
			t.Fatalf("question omits review context %q", value)
		}
	}
	markup := message.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatal("question does not offer both choices")
	}
	for _, button := range markup.InlineKeyboard[0] {
		if button.CallbackData == nil || len(*button.CallbackData) > 64 {
			t.Fatal("invalid callback data")
		}
	}
}

func TestTaskQuestionOrphanRemainsUsableAfterRedelivery(t *testing.T) {
	service := &taskQuestionService{candidate: taskQuestionPending(), markErr: errors.New("database unavailable")}
	ctx, cancel := context.WithCancel(context.Background())
	service.afterMark = cancel
	bot := newMockBot(0)
	bot.sendResult = tgbotapi.Message{MessageID: 41}
	w := newTaskQuestionChannel(t, bot, service)
	if err := w.RunTaskQuestions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("sender error=%v", err)
	}
	if service.candidate.QuestionMsgID != 0 {
		t.Fatal("failed mark persisted binding")
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	service.markErr = nil
	service.afterMark = cancel
	bot.sendResult.MessageID = 42
	restarted := newTaskQuestionChannel(t, bot, service)
	if err := restarted.RunTaskQuestions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("restart sender error=%v", err)
	}
	if service.candidate.QuestionMsgID != 42 {
		t.Fatal("retry was not bound")
	}
	for _, messageID := range []int{41, 42, 41} {
		if !restarted.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: taskQuestionCallback("yes", messageID)}, nil) {
			t.Fatal("legitimate retry/orphan question was rejected")
		}
	}
	if service.created != 1 || service.candidate.Status != "created" {
		t.Fatalf("orphan decisions created %d tasks", service.created)
	}
}

func TestTaskQuestionTerminalReplayWithoutKeyboardSurvivesUIFailure(t *testing.T) {
	bot := newMockBot(0)
	bot.makeReqErr = errors.New("Telegram unavailable")
	candidate := taskQuestionPending()
	candidate.QuestionChatID = 12345
	candidate.QuestionMsgID = 41
	service := &taskQuestionService{candidate: candidate}
	w := newTaskQuestionChannel(t, bot, service)
	if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: taskQuestionCallback("no", 41)}, nil) {
		t.Fatal("UI failure requested decision retry")
	}
	callback := taskQuestionCallback("yes", 41)
	callback.Message.ReplyMarkup = nil
	if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: callback}, nil) {
		t.Fatal("terminal replay failed")
	}
	if service.candidate.Status != "rejected" || service.created != 0 {
		t.Fatal("UI failure or terminal replay reversed decision")
	}
	acks := 0
	for _, endpoint := range bot.makeReqCalls {
		if endpoint == "answerCallbackQuery" {
			acks++
		}
	}
	if acks != 2 {
		t.Fatal("UI failure suppressed callback acknowledgement")
	}
}

func TestTaskQuestionCreatedResultShowsTaskReference(t *testing.T) {
	for _, tc := range []struct {
		name    string
		task    *model.Task
		wantRef string
	}{
		{"existing task", &model.Task{ProjectSlug: "work", Number: 17}, "work#17"},
		{"retention replay", nil, "persisted-task-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot := newMockBot(0)
			candidate := taskQuestionPending()
			candidate.Status = "created"
			candidate.CreatedTaskID = "persisted-task-id"
			service := &taskQuestionService{candidate: candidate, decisionTask: tc.task}
			w := newTaskQuestionChannel(t, bot, service)
			if !w.processRawUpdate(context.Background(), rawUpdate{CallbackQuery: taskQuestionCallback("yes", 41)}, nil) {
				t.Fatal("created decision replay failed")
			}
			found := false
			for index, endpoint := range bot.makeReqCalls {
				if endpoint == "editMessageText" {
					found = true
					if !strings.Contains(bot.makeReqParams[index]["text"], tc.wantRef) {
						t.Fatalf("created result omits task reference %q", tc.wantRef)
					}
				}
			}
			if !found {
				t.Fatal("created result was not displayed")
			}
			if service.created != 0 {
				t.Fatal("showing created result created another task")
			}
		})
	}
}
