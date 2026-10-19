package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/dateparse"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pipeline"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/transcribe"
	"github.com/anadale/huskwoot/internal/usecase"
)

// The tests in this file are end-to-end acceptance checks for task source,
// task priority and the client chat history. They run the real pipeline,
// extractors, agent, services and SQLite stores behind the HTTP API; only the
// language model calls are scripted.

// acceptanceOwnerID is the Telegram user id of the owner in acceptance tests.
const acceptanceOwnerID = "owner-123"

// acceptanceCompleter is an ai.Completer that returns a fixed model response.
type acceptanceCompleter struct {
	response string
}

func (c *acceptanceCompleter) Complete(context.Context, string, string) (string, error) {
	return c.response, nil
}

// promiseClassifier classifies every message as a promise.
type promiseClassifier struct{}

func (promiseClassifier) Classify(context.Context, model.Message) (model.Classification, error) {
	return model.ClassPromise, nil
}

// scriptedStep is one scripted agent model response. When gate is set, the
// response is returned only after the gate is closed.
type scriptedStep struct {
	resp openai.ChatCompletionResponse
	gate <-chan struct{}
}

// scriptedAIClient is an agent.AIClient that plays back scripted responses in
// order and records every request.
type scriptedAIClient struct {
	mu       sync.Mutex
	steps    []scriptedStep
	requests []openai.ChatCompletionRequest
}

func (c *scriptedAIClient) push(steps ...scriptedStep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = append(c.steps, steps...)
}

func (c *scriptedAIClient) CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	if len(c.steps) == 0 {
		c.mu.Unlock()
		return openai.ChatCompletionResponse{}, errors.New("scripted AI client: no response left")
	}
	step := c.steps[0]
	c.steps = c.steps[1:]
	c.mu.Unlock()

	if step.gate != nil {
		select {
		case <-step.gate:
		case <-ctx.Done():
			return openai.ChatCompletionResponse{}, ctx.Err()
		}
	}
	return step.resp, nil
}

// lastRequest returns the most recent request sent to the model.
func (c *scriptedAIClient) lastRequest() openai.ChatCompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return openai.ChatCompletionRequest{}
	}
	return c.requests[len(c.requests)-1]
}

// aiText scripts a final text reply of the agent model.
func aiText(content string) scriptedStep {
	return scriptedStep{resp: openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{
		Message:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: content},
		FinishReason: openai.FinishReasonStop,
	}}}}
}

// aiToolCall scripts a single tool call of the agent model.
func aiToolCall(id, name, args string) scriptedStep {
	return scriptedStep{resp: openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{
		Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{{
				ID:       id,
				Type:     openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: name, Arguments: args},
			}},
		},
		FinishReason: openai.FinishReasonToolCalls,
	}}}}
}

// acceptanceQuote is the quoted message of a task source.
type acceptanceQuote struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

// acceptanceSource is the task source. The REST DTO (camelCase) and the task
// event snapshot use the same keys for it.
type acceptanceSource struct {
	Kind    string           `json:"kind"`
	Name    string           `json:"name"`
	Subject string           `json:"subject"`
	Author  string           `json:"author"`
	Text    string           `json:"text"`
	At      *time.Time       `json:"at"`
	ReplyTo *acceptanceQuote `json:"replyTo"`
	Link    string           `json:"link"`
	Via     string           `json:"via"`
}

// acceptanceTask holds the task keys shared by the REST DTO and the task event
// snapshot that the acceptance tests check.
type acceptanceTask struct {
	ID       string            `json:"id"`
	Summary  string            `json:"summary"`
	Priority int               `json:"priority"`
	Source   *acceptanceSource `json:"source"`
}

// acceptanceTaskEvent is the payload of a task_* event.
type acceptanceTaskEvent struct {
	Task          acceptanceTask `json:"task"`
	ChangedFields []string       `json:"changedFields"`
}

// assertSource compares a task source with want; At is compared by instant.
func assertSource(t *testing.T, where string, got *acceptanceSource, want acceptanceSource) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: source is missing", where)
		return
	}
	gotCopy := *got
	if (gotCopy.At == nil) != (want.At == nil) || (want.At != nil && !gotCopy.At.Equal(*want.At)) {
		t.Errorf("%s: source.at=%v, want %v", where, gotCopy.At, want.At)
	}
	gotCopy.At, want.At = nil, nil
	if !reflect.DeepEqual(gotCopy, want) {
		t.Errorf("%s: source=%+v, want %+v", where, gotCopy, want)
	}
}

// acceptanceHarness wires the whole server: pipeline, agent, chat service,
// client voice consumer and the HTTP API on one SQLite database.
type acceptanceHarness struct {
	t        *testing.T
	db       *sql.DB
	server   *api.Server
	token    string
	device   *model.Device
	tasks    model.TaskService
	events   *events.SQLiteEventStore
	agentAI  *scriptedAIClient
	voice    model.VoiceConsumer
	pipeline *pipeline.Pipeline
	// extractorAI answers the message extractor (groups and email).
	extractorAI *acceptanceCompleter
	// forwardAI answers the forward batch extractor.
	forwardAI *acceptanceCompleter
	forwards  *storage.SQLiteForwardBuffer
}

func newAcceptanceHarness(t *testing.T) *acceptanceHarness {
	t.Helper()
	db := openTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sqliteTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	taskStore := storage.NewCachedTaskStore(sqliteTasks)
	eventStore := events.NewSQLiteEventStore(db)
	pushQueue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})
	deviceStore := devices.NewSQLiteDeviceStore(db)
	history := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{})

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: db, Tasks: taskStore, Meta: storage.NewSQLiteMetaStore(db), Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: db, Tasks: taskStore, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})
	autoTaskSvc := usecase.NewAutoTaskService(usecase.AutoTaskServiceDeps{
		DB: db, Tasks: taskStore, Events: eventStore, Devices: deviceStore,
		Queue: pushQueue, Broker: broker, Store: storage.NewSQLiteAutoTaskStore(db), Logger: logger,
	})

	bundle, err := huskwootI18n.NewBundle("ru")
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	loc := huskwootI18n.NewLocalizer(bundle, "ru")
	dp := dateparse.New(dateparse.Config{
		TimeOfDay: dateparse.TimeOfDay{Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20},
		Weekdays:  []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
	}, dateparse.NewDateLanguage("ru"))

	agentAI := &scriptedAIClient{}
	ag, err := agent.New(agentAI, []agent.Tool{
		agent.NewCreateTaskTool(taskSvc, projectSvc, dp, loc),
		agent.NewUpdateTaskTool(taskSvc, dp, loc),
	}, agent.Config{}, logger)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	chatSvc := usecase.NewChatService(usecase.ChatServiceDeps{Agent: ag, DB: db, Events: eventStore, Broker: broker})
	voice := usecase.NewClientVoiceConsumer(usecase.ClientVoiceConsumerDeps{
		DB: db, History: history, Events: eventStore, Broker: broker, Chat: chatSvc, Tasks: taskStore, Logger: logger,
	})

	extractorAI := &acceptanceCompleter{}
	extractor, err := ai.NewTaskExtractor(extractorAI, ai.ExtractorConfig{UserName: "Gregory"})
	if err != nil {
		t.Fatalf("NewTaskExtractor: %v", err)
	}
	forwardAI := &acceptanceCompleter{}
	forwardExtractor, err := ai.NewForwardExtractor(forwardAI, "ru", dateparse.Config{})
	if err != nil {
		t.Fatalf("NewForwardExtractor: %v", err)
	}
	forwards := storage.NewSQLiteForwardBuffer(db)
	p := pipeline.New(pipeline.Config{
		OwnerIDs:  []string{acceptanceOwnerID},
		Tasks:     taskSvc,
		AutoTasks: autoTaskSvc,
		Projects:  projectSvc,
		Classifiers: map[model.MessageKind]model.Classifier{
			model.MessageKindGroup: promiseClassifier{},
			model.MessageKindBatch: promiseClassifier{},
		},
		Extractors: map[model.MessageKind]model.Extractor{
			model.MessageKindGroup: extractor,
			model.MessageKindBatch: extractor,
		},
		Logger:            logger,
		ForwardClassifier: promiseClassifier{},
		ForwardExtractor:  forwardExtractor,
		ForwardBuffer:     forwards,
		TaskStore:         taskStore,
	})

	token := "acceptance-token"
	device := createTestDevice(t, db, "acceptance-device", token)
	srv := api.New(api.Config{
		Logger:   logger,
		DB:       db,
		Devices:  deviceStore,
		Projects: projectSvc,
		Tasks:    taskSvc,
		Chat:     chatSvc,
		History:  history,
		Events:   eventStore,
		Broker:   broker,
		Owner:    api.OwnerInfo{UserName: "Gregory"},
	})

	return &acceptanceHarness{
		t: t, db: db, server: srv, token: token, device: device,
		tasks: taskSvc, events: eventStore, agentAI: agentAI, voice: voice, pipeline: p,
		extractorAI: extractorAI, forwardAI: forwardAI, forwards: forwards,
	}
}

func (h *acceptanceHarness) do(method, target string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	r := httptest.NewRequest(method, target, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, r)
	return rec
}

// getJSON requests target, expects 200 and decodes the body into dst.
func (h *acceptanceHarness) getJSON(target string, dst any) {
	h.t.Helper()
	rec := h.do(http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET %s: status=%d, body=%s", target, rec.Code, rec.Body.String())
	}
	decodeJSONResp(h.t, rec.Body, dst)
}

// onlyTask returns the single task listed by GET /v1/tasks.
func (h *acceptanceHarness) onlyTask() acceptanceTask {
	h.t.Helper()
	var list struct {
		Tasks []acceptanceTask `json:"tasks"`
	}
	h.getJSON("/v1/tasks", &list)
	if len(list.Tasks) != 1 {
		h.t.Fatalf("GET /v1/tasks: tasks=%+v, want exactly one", list.Tasks)
	}
	return list.Tasks[0]
}

// getTask returns GET /v1/tasks/{id}.
func (h *acceptanceHarness) getTask(id string) acceptanceTask {
	h.t.Helper()
	var task acceptanceTask
	h.getJSON("/v1/tasks/"+id, &task)
	return task
}

// snapshotTask returns the task with id from GET /v1/sync/snapshot.
func (h *acceptanceHarness) snapshotTask(id string) acceptanceTask {
	h.t.Helper()
	var snapshot struct {
		OpenTasks []acceptanceTask `json:"openTasks"`
	}
	h.getJSON("/v1/sync/snapshot", &snapshot)
	for _, task := range snapshot.OpenTasks {
		if task.ID == id {
			return task
		}
	}
	h.t.Fatalf("sync snapshot has no task %s: %+v", id, snapshot.OpenTasks)
	return acceptanceTask{}
}

// taskEvents returns the decoded payloads of the stored events of kind.
func (h *acceptanceHarness) taskEvents(kind model.EventKind) []acceptanceTaskEvent {
	h.t.Helper()
	evs, err := h.events.SinceSeq(context.Background(), 0, 1000)
	if err != nil {
		h.t.Fatalf("SinceSeq: %v", err)
	}
	var out []acceptanceTaskEvent
	for _, ev := range evs {
		if ev.Kind != kind {
			continue
		}
		var payload acceptanceTaskEvent
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			h.t.Fatalf("unmarshal %s payload: %v (raw %s)", kind, err, ev.Payload)
		}
		out = append(out, payload)
	}
	return out
}

// postChat sends a client chat message and returns the decoded reply.
func (h *acceptanceHarness) postChat(text string) (reply string, tasksTouched []string) {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/v1/chat", map[string]string{"message": text})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("POST /v1/chat: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Reply        string   `json:"reply"`
		TasksTouched []string `json:"tasksTouched"`
	}
	decodeJSONResp(h.t, rec.Body, &resp)
	return resp.Reply, resp.TasksTouched
}

// voiceJob builds a client voice job of the harness device.
func (h *acceptanceHarness) voiceJob(clientJobID string, duration time.Duration) model.VoiceJob {
	return model.VoiceJob{
		SourceKind:  "client",
		Payload:     clientJobID + ".ogg",
		MimeType:    "audio/ogg",
		Duration:    duration,
		DeviceID:    h.device.ID,
		ClientJobID: clientJobID,
	}
}

// TestAcceptanceTelegramGroupTaskSource covers the criteria "a task created from
// a Telegram group message returns source with kind, chat title, author, text,
// reply-to and a deep link (supergroup) in GET, sync snapshot and task_created"
// and "priority is inferred for urgent group promises".
func TestAcceptanceTelegramGroupTaskSource(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.extractorAI.response = `[{"summary":"поправить деплой","context":"","topic":"","project":"","deadline":null,"priority":3,"confidence":0.9}]`
	at := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID:         "77",
		Author:     acceptanceOwnerID,
		AuthorName: "Gregory",
		Text:       "срочно поправлю деплой",
		Timestamp:  at,
		Kind:       model.MessageKindGroup,
		Source:     model.Source{Kind: "telegram", ID: "-1005550001", Name: "Команда"},
		ReplyTo:    &model.Message{AuthorName: "Bob", Text: "кто поправит деплой?"},
	}

	if err := h.pipeline.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process: %v", err)
	}

	want := acceptanceSource{
		Kind:    "telegram",
		Name:    "Команда",
		Author:  "Gregory",
		Text:    "срочно поправлю деплой",
		At:      &at,
		ReplyTo: &acceptanceQuote{Author: "Bob", Text: "кто поправит деплой?"},
		Link:    "https://t.me/c/5550001/77",
		Via:     model.SourceViaMessage,
	}
	listed := h.onlyTask()
	assertSource(t, "GET /v1/tasks", listed.Source, want)

	got := h.getTask(listed.ID)
	assertSource(t, "GET /v1/tasks/{id}", got.Source, want)
	if got.Priority != int(model.TaskPriorityHigh) {
		t.Errorf("GET /v1/tasks/{id}: priority=%d, want 3 (urgent promise)", got.Priority)
	}

	snap := h.snapshotTask(listed.ID)
	assertSource(t, "sync snapshot", snap.Source, want)
	if snap.Priority != int(model.TaskPriorityHigh) {
		t.Errorf("sync snapshot: priority=%d, want 3", snap.Priority)
	}

	created := h.taskEvents(model.EventTaskCreated)
	if len(created) != 1 || created[0].Task.ID != listed.ID {
		t.Fatalf("task_created events=%+v, want one for %s", created, listed.ID)
	}
	assertSource(t, "task_created", created[0].Task.Source, want)
	if created[0].Task.Priority != int(model.TaskPriorityHigh) {
		t.Errorf("task_created: priority=%d, want 3", created[0].Task.Priority)
	}
}

// TestAcceptanceForwardedTaskNamesPromiseAuthor covers the criterion "a task from
// a forwarded conversation names the author and text of the message with the
// promise".
func TestAcceptanceForwardedTaskNamesPromiseAuthor(t *testing.T) {
	h := newAcceptanceHarness(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)
	received := t0.Add(time.Hour)
	entries := []model.ForwardEntry{
		{ChatID: "owner-chat", MessageID: "501", ForwardFromName: "Bob", OriginalDate: t0, ReceivedAt: received, Text: "пришлёшь слайды к пятнице?"},
		{ChatID: "owner-chat", MessageID: "502", ForwardFromName: "Gregory", IsSelfAuthored: true, OriginalDate: t0.Add(5 * time.Minute), ReceivedAt: received, Text: "да, пришлю в четверг"},
		{ChatID: "owner-chat", MessageID: "503", ForwardFromName: "Bob", OriginalDate: t0.Add(10 * time.Minute), ReceivedAt: received, Text: "спасибо!"},
	}
	for _, entry := range entries {
		if err := h.forwards.Append(ctx, entry); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Message 2 in date order is the owner's promise.
	h.forwardAI.response = `[{"summary":"прислать слайды Бобу","context":"","topic":"","project":"","deadline":null,"message":2,"confidence":0.9}]`
	msg := model.Message{
		Kind:   model.MessageKindForwardBatch,
		Source: model.Source{Kind: "telegram", ID: "owner-chat", AccountID: "personal"},
		ForwardBatch: &model.ForwardBatchPayload{
			ChatID: "owner-chat",
			// The buffer order differs from the date order the messages are numbered in.
			Forwards:    []model.ForwardEntry{entries[2], entries[0], entries[1]},
			Counterpart: "Bob",
		},
	}

	if err := h.pipeline.Process(ctx, msg); err != nil {
		t.Fatalf("Process: %v", err)
	}

	task := h.getTask(h.onlyTask().ID)
	assertSource(t, "GET /v1/tasks/{id}", task.Source, acceptanceSource{
		Kind:   "telegram",
		Name:   "Bob",
		Author: "Gregory",
		Text:   "да, пришлю в четверг",
		At:     &entries[1].OriginalDate,
		Via:    model.SourceViaForward,
	})
}

// TestAcceptanceEmailTaskReturnsSubject covers the criterion "a task created
// from email returns source.subject".
func TestAcceptanceEmailTaskReturnsSubject(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.extractorAI.response = `[{"summary":"подготовить договор","context":"","topic":"","project":"","deadline":null,"confidence":0.9}]`
	at := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID:         "imap-1",
		Author:     "boss@example.com",
		AuthorName: "Alice Boss",
		Text:       "Gregory, подготовь договор к понедельнику",
		Subject:    "Договор поставки",
		Timestamp:  at,
		Kind:       model.MessageKindBatch,
		Source:     model.Source{Kind: "imap", ID: "user@example.com:INBOX", Name: "Рабочая почта"},
	}

	if err := h.pipeline.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process: %v", err)
	}

	id := h.onlyTask().ID
	want := acceptanceSource{
		Kind:    "imap",
		Name:    "Рабочая почта",
		Subject: "Договор поставки",
		Author:  "Alice Boss",
		Text:    msg.Text,
		At:      &at,
		Via:     model.SourceViaMessage,
	}
	assertSource(t, "GET /v1/tasks/{id}", h.getTask(id).Source, want)
	assertSource(t, "sync snapshot", h.snapshotTask(id).Source, want)
}

// TestAcceptanceAgentTaskFromClientChat covers the criteria "a task created from
// client chat returns kind client, via chat and the user's text" and "priority
// can be changed by the agent, appears in DTO, events and agent output".
func TestAcceptanceAgentTaskFromClientChat(t *testing.T) {
	h := newAcceptanceHarness(t)
	ctx := context.Background()

	h.agentAI.push(aiToolCall("call-1", "create_task", `{"summary":"купить билеты"}`), aiText("Создал задачу"))
	reply, touched := h.postChat("напомни купить билеты")
	if reply != "Создал задачу" || len(touched) != 1 {
		t.Fatalf("POST /v1/chat: reply=%q tasksTouched=%v, want the reply and one task", reply, touched)
	}
	id := touched[0]

	task := h.getTask(id)
	if task.Source == nil || task.Source.At == nil {
		t.Fatalf("source=%+v, want the origin time", task.Source)
	}
	wantSource := acceptanceSource{Kind: "client", Text: "напомни купить билеты", At: task.Source.At, Via: model.SourceViaChat}
	assertSource(t, "created by the agent", task.Source, wantSource)
	if task.Priority != int(model.TaskPriorityNone) {
		t.Errorf("priority=%d, want 0 without an explicit signal", task.Priority)
	}
	stored, err := h.tasks.GetTask(ctx, id)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: %v %v", stored, err)
	}
	if stored.Source.ID != h.device.ID {
		t.Errorf("stored source id=%q, want the device id %q", stored.Source.ID, h.device.ID)
	}

	// "сделай её срочной": the agent raises the priority through update_task.
	h.agentAI.push(aiToolCall("call-2", "update_task", fmt.Sprintf(`{"task_id":%q,"priority":3}`, id)), aiText("Сделал срочной"))
	if reply, _ := h.postChat("сделай её срочной"); reply != "Сделал срочной" {
		t.Fatalf("POST /v1/chat: reply=%q", reply)
	}

	// The tool output the agent model reads carries the priority and its marker.
	msgs := h.agentAI.lastRequest().Messages
	toolOutput := msgs[len(msgs)-1]
	if toolOutput.Role != openai.ChatMessageRoleTool ||
		!strings.Contains(toolOutput.Content, `"priority":3`) || !strings.Contains(toolOutput.Content, `"priority_marker":"!!!"`) {
		t.Errorf("update_task output=%+v, want priority 3 and marker !!!", toolOutput)
	}

	updated := h.getTask(id)
	if updated.Priority != int(model.TaskPriorityHigh) {
		t.Errorf("GET /v1/tasks/{id}: priority=%d, want 3", updated.Priority)
	}
	assertSource(t, "after update", updated.Source, wantSource)
	if snap := h.snapshotTask(id); snap.Priority != int(model.TaskPriorityHigh) {
		t.Errorf("sync snapshot: priority=%d, want 3", snap.Priority)
	}
	updates := h.taskEvents(model.EventTaskUpdated)
	if len(updates) != 1 {
		t.Fatalf("task_updated events=%+v, want one", updates)
	}
	if updates[0].Task.Priority != int(model.TaskPriorityHigh) || !reflect.DeepEqual(updates[0].ChangedFields, []string{"priority"}) {
		t.Errorf("task_updated: priority=%d changedFields=%v, want 3 and [priority]", updates[0].Task.Priority, updates[0].ChangedFields)
	}
}

// TestAcceptanceAgentTaskFromClientVoice covers the criteria "a task created
// from client voice returns kind client, via voice and the transcript" and
// "GET /v1/chat/history returns kinds, voice metadata and task cards".
func TestAcceptanceAgentTaskFromClientVoice(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.agentAI.push(aiToolCall("call-1", "create_task", `{"summary":"позвонить маме"}`), aiText("Записал"))
	job := h.voiceJob("voice-job-1", 4200*time.Millisecond)

	if err := h.voice.Consume(context.Background(), job, "напомни позвонить маме"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	task := h.onlyTask()
	if task.Source == nil || task.Source.At == nil {
		t.Fatalf("source=%+v, want the origin time", task.Source)
	}
	assertSource(t, "created from voice", task.Source, acceptanceSource{
		Kind: "client", Text: "напомни позвонить маме", At: task.Source.At, Via: model.SourceViaVoice,
	})

	var history chatHistoryRespDTO
	h.getJSON("/v1/chat/history", &history)
	if len(history.Entries) != 2 {
		t.Fatalf("history entries=%+v, want the transcript and the reply", history.Entries)
	}
	user, agentReply := history.Entries[0], history.Entries[1]
	if user.Role != model.HistoryRoleUser || user.Kind != model.HistoryKindVoice || user.Text != "напомни позвонить маме" ||
		user.Audio == nil || user.Audio.JobID != "voice-job-1" || user.Audio.DurationMs != 4200 {
		t.Errorf("transcript entry=%+v, want a user voice entry with the audio metadata", user)
	}
	wantCards := []chatHistoryTaskDTO{{ID: task.ID, Ref: "inbox-1", Summary: "позвонить маме", Status: "open"}}
	if agentReply.Role != model.HistoryRoleAgent || agentReply.Text != "Записал" || !reflect.DeepEqual(agentReply.Tasks, wantCards) {
		t.Errorf("reply entry=%+v, want the agent reply with card %+v", agentReply, wantCards)
	}
}

// TestAcceptanceSlowVoiceReplyKeepsTaskCards covers the criterion "task cards
// also after a slow voice reply": the synthetic reply links the task the agent
// already created, and the late real reply adds the rest. It waits for the
// production ack grace (1.5 s).
func TestAcceptanceSlowVoiceReplyKeepsTaskCards(t *testing.T) {
	h := newAcceptanceHarness(t)
	release := make(chan struct{})
	// The model stalls after the first task until release is closed.
	slowStep := aiToolCall("call-2", "create_task", `{"summary":"купить цветы"}`)
	slowStep.gate = release
	h.agentAI.push(
		aiToolCall("call-1", "create_task", `{"summary":"купить торт"}`),
		slowStep,
		aiText("Создал две задачи"),
	)
	job := h.voiceJob("voice-job-slow", 3*time.Second)

	if err := h.voice.Consume(context.Background(), job, "купи торт и цветы"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	agentEntry := func() chatHistoryEntryDTO {
		t.Helper()
		var history chatHistoryRespDTO
		h.getJSON("/v1/chat/history", &history)
		for _, e := range history.Entries {
			if e.Role == model.HistoryRoleAgent {
				return e
			}
		}
		t.Fatalf("no agent entry in %+v", history.Entries)
		return chatHistoryEntryDTO{}
	}

	synthetic := agentEntry()
	if synthetic.Text != "Готово" || len(synthetic.Tasks) != 1 || synthetic.Tasks[0].Summary != "купить торт" {
		t.Fatalf("synthetic reply=%+v, want Готово with the card of the first task", synthetic)
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := agentEntry()
		summaries := make([]string, 0, len(got.Tasks))
		for _, card := range got.Tasks {
			summaries = append(summaries, card.Summary)
		}
		if reflect.DeepEqual(summaries, []string{"купить торт", "купить цветы"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("late reply tasks not merged: cards=%v, want both tasks", summaries)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAcceptanceVoiceFailureInHistoryNotInAgentContext covers the criterion
// "GET /v1/chat/history returns failures; failures do not appear in the agent
// context".
func TestAcceptanceVoiceFailureInHistoryNotInAgentContext(t *testing.T) {
	h := newAcceptanceHarness(t)
	ctx := context.Background()
	const failureText = "audio does not contain recognisable speech"

	h.agentAI.push(aiText("Привет!"))
	h.postChat("привет")
	h.voice.Fail(ctx, h.voiceJob("voice-job-quiet", 2*time.Second), transcribe.ErrEmptyTranscript)
	h.agentAI.push(aiText("Ничего нового"))
	h.postChat("что нового?")

	prompt := h.agentAI.lastRequest().Messages[0]
	if prompt.Role != openai.ChatMessageRoleSystem || !strings.Contains(prompt.Content, "Привет!") {
		t.Fatalf("system prompt=%q, want the recent conversation in it", prompt.Content)
	}
	if strings.Contains(prompt.Content, failureText) {
		t.Errorf("system prompt contains the voice failure: %q", prompt.Content)
	}

	var history chatHistoryRespDTO
	h.getJSON("/v1/chat/history", &history)
	var got []string
	for _, e := range history.Entries {
		got = append(got, e.Role+":"+e.Kind+":"+e.Text)
	}
	want := []string{
		"user:text:привет",
		"agent:text:Привет!",
		"system:error:" + failureText,
		"user:text:что нового?",
		"agent:text:Ничего нового",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history=%v, want %v", got, want)
	}
	failure := history.Entries[2]
	if failure.AuthorName != model.HistoryRoleSystem || failure.Audio == nil || failure.Audio.JobID != "voice-job-quiet" {
		t.Errorf("failure entry=%+v, want author system and the audio job id", failure)
	}
}

// assertKeys reports the keys of want that obj lacks.
func assertKeys(t *testing.T, where string, obj map[string]json.RawMessage, want ...string) {
	t.Helper()
	for _, key := range want {
		if _, ok := obj[key]; !ok {
			t.Errorf("%s: key %q is missing: %v", where, key, obj)
		}
	}
}

// TestAcceptanceOldClientsKeepWorking covers the criterion "old clients keep
// working: requests without new fields, responses with unknown fields". Old
// requests are accepted and do not reset the new fields; every response keeps
// all keys of the previous contract, so clients that ignore unknown keys read
// them as before.
func TestAcceptanceOldClientsKeepWorking(t *testing.T) {
	h := newAcceptanceHarness(t)
	ctx := context.Background()
	oldTaskKeys := []string{"id", "number", "ref", "projectId", "projectSlug", "summary", "status", "createdAt", "updatedAt"}

	// POST /v1/tasks with the old request body.
	rec := h.do(http.MethodPost, "/v1/tasks", map[string]any{"summary": "старый клиент"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/tasks: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]json.RawMessage
	decodeJSONResp(t, rec.Body, &created)
	assertKeys(t, "POST /v1/tasks", created, oldTaskKeys...)
	if got := string(created["priority"]); got != "0" {
		t.Errorf("POST /v1/tasks: priority=%s, want 0", got)
	}
	var id string
	if err := json.Unmarshal(created["id"], &id); err != nil {
		t.Fatalf("unmarshal id: %v", err)
	}

	// A new client sets the priority; an old client then edits the task
	// without the field and must not reset it.
	if rec := h.do(http.MethodPatch, "/v1/tasks/"+id, map[string]any{"priority": 2}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH priority: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	rec = h.do(http.MethodPatch, "/v1/tasks/"+id, map[string]any{"summary": "переименовано"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH summary: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var patched map[string]json.RawMessage
	decodeJSONResp(t, rec.Body, &patched)
	assertKeys(t, "PATCH /v1/tasks/{id}", patched, oldTaskKeys...)
	if got := string(patched["priority"]); got != "2" {
		t.Errorf("PATCH without priority: priority=%s, want 2 (unchanged)", got)
	}

	// GET /v1/tasks/{id} and the sync snapshot keep the old keys.
	var got map[string]json.RawMessage
	h.getJSON("/v1/tasks/"+id, &got)
	assertKeys(t, "GET /v1/tasks/{id}", got, oldTaskKeys...)
	var snapshot struct {
		OpenTasks []map[string]json.RawMessage `json:"openTasks"`
	}
	h.getJSON("/v1/sync/snapshot", &snapshot)
	if len(snapshot.OpenTasks) != 1 {
		t.Fatalf("sync snapshot: openTasks=%d, want 1", len(snapshot.OpenTasks))
	}
	assertKeys(t, "sync snapshot", snapshot.OpenTasks[0], oldTaskKeys...)

	// Task event payloads keep the old snapshot keys.
	evs, err := h.events.SinceSeq(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("SinceSeq: %v", err)
	}
	taskEvents := 0
	for _, ev := range evs {
		if ev.Kind != model.EventTaskCreated && ev.Kind != model.EventTaskUpdated {
			continue
		}
		taskEvents++
		var payload struct {
			Task map[string]json.RawMessage `json:"task"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal %s: %v", ev.Kind, err)
		}
		assertKeys(t, string(ev.Kind), payload.Task, "id", "number", "project_id", "summary", "status", "created_at", "updated_at")
	}
	if taskEvents != 3 {
		t.Errorf("task events=%d, want 3 (one created, two updated)", taskEvents)
	}

	// POST /v1/chat with the old request body and the old response keys.
	h.agentAI.push(aiText("Привет!"))
	rec = h.do(http.MethodPost, "/v1/chat", map[string]string{"message": "привет"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/chat: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var chatResp map[string]json.RawMessage
	decodeJSONResp(t, rec.Body, &chatResp)
	assertKeys(t, "POST /v1/chat", chatResp, "reply")

	// A voice failure adds a system row; every entry keeps the old keys.
	h.voice.Fail(ctx, h.voiceJob("voice-job-old", time.Second), transcribe.ErrEmptyTranscript)

	// GET /v1/chat/history without the new query parameters.
	rec = h.do(http.MethodGet, "/v1/chat/history?limit=50", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/chat/history: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var history struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	decodeJSONResp(t, rec.Body, &history)
	if len(history.Entries) != 3 {
		t.Fatalf("history entries=%d, want 3", len(history.Entries))
	}
	for i, entry := range history.Entries {
		assertKeys(t, fmt.Sprintf("history entry %d", i), entry, "authorName", "text", "timestamp")
	}
}
