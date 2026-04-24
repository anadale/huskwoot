package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/sashabaranov/go-openai"
)

// newTestAgent is a test helper that creates an agent.
func newTestAgent(t *testing.T, client agent.AIClient, tools []agent.Tool, cfg agent.Config) *agent.Agent {
	a, err := agent.New(client, tools, cfg, nil)
	if err != nil {
		t.Fatalf("agent.New() returned error: %v", err)
	}
	return a
}

// mockAIClient is a mock AI client for agent tests.
type mockAIClient struct {
	responses []openai.ChatCompletionResponse
	err       error
	callCount int
}

func (m *mockAIClient) CreateChatCompletion(_ context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if m.err != nil {
		return openai.ChatCompletionResponse{}, m.err
	}
	if m.callCount >= len(m.responses) {
		return openai.ChatCompletionResponse{}, errors.New("mock: exceeded expected call count")
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

// mockTool is a mock tool for tests.
type mockTool struct {
	name          string
	dmOnly        bool
	executeFunc   func(ctx context.Context, args string) (string, error)
	executeCalled bool
	executeArgs   string
}

func (t *mockTool) Name() string               { return t.name }
func (t *mockTool) Description() string        { return "mock tool " + t.name }
func (t *mockTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *mockTool) DMOnly() bool               { return t.dmOnly }
func (t *mockTool) Execute(ctx context.Context, args string) (string, error) {
	t.executeCalled = true
	t.executeArgs = args
	if t.executeFunc != nil {
		return t.executeFunc(ctx, args)
	}
	return `{"ok":true}`, nil
}

// textResponse builds a model response with text content (no tool calls).
func textResponse(content string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				},
				FinishReason: openai.FinishReasonStop,
			},
		},
	}
}

// toolCallResponse builds a model response containing a tool call.
func toolCallResponse(id, name, args string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role: openai.ChatMessageRoleAssistant,
					ToolCalls: []openai.ToolCall{
						{
							ID:   id,
							Type: openai.ToolTypeFunction,
							Function: openai.FunctionCall{
								Name:      name,
								Arguments: args,
							},
						},
					},
				},
				FinishReason: openai.FinishReasonToolCalls,
			},
		},
	}
}

func TestAgent_Handle_TextResponseNoToolCalls(t *testing.T) {
	want := "Готово! Создал задачу для тебя."

	client := &mockAIClient{
		responses: []openai.ChatCompletionResponse{
			textResponse(want),
		},
	}

	tools := []agent.Tool{
		&mockTool{name: "create_task"},
	}

	a := newTestAgent(t, client, tools, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "создай задачу написать тесты",
	}

	got, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}
	if got != want {
		t.Errorf("Handle() = %q, want %q", got, want)
	}
	if client.callCount != 1 {
		t.Errorf("AI client called %d times, want 1", client.callCount)
	}
}

func TestAgent_Handle_ToolCallThenTextResponse(t *testing.T) {
	toolResult := `{"id":"task-1","summary":"написать тесты"}`
	wantResponse := "Задача создана успешно."

	createTaskTool := &mockTool{
		name: "create_task",
		executeFunc: func(_ context.Context, _ string) (string, error) {
			return toolResult, nil
		},
	}

	client := &mockAIClient{
		responses: []openai.ChatCompletionResponse{
			toolCallResponse("call_1", "create_task", `{"summary":"написать тесты"}`),
			textResponse(wantResponse),
		},
	}

	a := newTestAgent(t, client, []agent.Tool{createTaskTool}, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "нужно написать тесты",
	}

	got, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}
	if got != wantResponse {
		t.Errorf("Handle() = %q, want %q", got, wantResponse)
	}
	if !createTaskTool.executeCalled {
		t.Error("create_task tool was not called")
	}
	if createTaskTool.executeArgs != `{"summary":"написать тесты"}` {
		t.Errorf("tool args = %q, want %q", createTaskTool.executeArgs, `{"summary":"написать тесты"}`)
	}
	if client.callCount != 2 {
		t.Errorf("AI client called %d times, want 2", client.callCount)
	}
}

func TestAgent_Handle_MaxIterationsExceeded(t *testing.T) {
	// The mock returns a tool call for every request — the agent must stop after 5 iterations.
	const iterLimit = 5

	fakeTool := &mockTool{name: "loop_tool"}

	responses := make([]openai.ChatCompletionResponse, iterLimit)
	for i := range responses {
		responses[i] = toolCallResponse("call_"+string(rune('0'+i)), "loop_tool", `{}`)
	}

	client := &mockAIClient{responses: responses}

	a := newTestAgent(t, client, []agent.Tool{fakeTool}, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "зациклились",
	}

	_, err := a.Handle(context.Background(), msg)
	if err == nil {
		t.Fatal("Handle() must return an error when iteration limit is exceeded")
	}
	if client.callCount != iterLimit {
		t.Errorf("AI client called %d times, want %d", client.callCount, iterLimit)
	}
}

func TestAgent_Handle_DMScope_AllToolsIncluded(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ответ"),
	}

	dmOnlyTool := &mockTool{name: "create_project", dmOnly: true}
	regularTool := &mockTool{name: "create_task", dmOnly: false}

	a := newTestAgent(t, captureClient, []agent.Tool{dmOnlyTool, regularTool}, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if len(captureClient.capturedToolNames) != 2 {
		t.Errorf("DM scope: want 2 tools, got %d: %v", len(captureClient.capturedToolNames), captureClient.capturedToolNames)
	}
}

func TestAgent_Handle_GroupDirectScope_RestrictedToolsExcluded(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ответ"),
	}

	dmOnlyTool := &mockTool{name: "create_project", dmOnly: true}
	anotherDMOnlyTool := &mockTool{name: "list_projects", dmOnly: true}
	regularTool := &mockTool{name: "create_task", dmOnly: false}

	a := newTestAgent(t, captureClient, []agent.Tool{dmOnlyTool, anotherDMOnlyTool, regularTool}, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindGroupDirect,
		Text: "покажи задачи",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if len(captureClient.capturedToolNames) != 1 {
		t.Errorf("GroupDirect scope: want 1 tool, got %d: %v",
			len(captureClient.capturedToolNames), captureClient.capturedToolNames)
	}
	if captureClient.capturedToolNames[0] != "create_task" {
		t.Errorf("GroupDirect scope: want tool %q, got %q",
			"create_task", captureClient.capturedToolNames[0])
	}
}

// captureToolsClient is a mock AI client that records the list of tool names from each request.
type captureToolsClient struct {
	response           openai.ChatCompletionResponse
	capturedToolNames  []string
	capturedSysContent string
}

func (c *captureToolsClient) CreateChatCompletion(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	c.capturedToolNames = make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Function != nil {
			c.capturedToolNames = append(c.capturedToolNames, t.Function.Name)
		}
	}
	if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
		c.capturedSysContent = req.Messages[0].Content
	}
	return c.response, nil
}

func TestAgent_Handle_GroupDirect_HistoryFnIncludedInSystemPrompt(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ок"),
	}

	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	entries := []model.HistoryEntry{
		{AuthorName: "Иван", Text: "нужно сделать деплой", Timestamp: ts},
		{AuthorName: "Мария", Text: "хорошо, займусь", Timestamp: ts.Add(time.Minute)},
	}

	a := newTestAgent(t, captureClient, nil, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindGroupDirect,
		Text: "создай задачу",
		HistoryFn: func(_ context.Context) ([]model.HistoryEntry, error) {
			return entries, nil
		},
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	sys := captureClient.capturedSysContent
	if !strings.Contains(sys, "Иван") {
		t.Error("system prompt must contain author name from history")
	}
	if !strings.Contains(sys, "деплой") {
		t.Error("system prompt must contain text from history")
	}
	if !strings.Contains(sys, "10:30") {
		t.Error("system prompt must contain timestamp from history")
	}
}

func TestAgent_Handle_HistoryFnNil_SystemPromptUnchanged(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ок"),
	}

	a := newTestAgent(t, captureClient, nil, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if strings.Contains(captureClient.capturedSysContent, "Контекст") {
		t.Error("system prompt must not contain context block when HistoryFn is nil")
	}
}

func TestAgent_Handle_HistoryFnError_ContinuesWithoutHistory(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ответ"),
	}

	a := newTestAgent(t, captureClient, nil, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindGroupDirect,
		Text: "тест",
		HistoryFn: func(_ context.Context) ([]model.HistoryEntry, error) {
			return nil, errors.New("history store недоступен")
		},
	}

	got, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() must not return an error on HistoryFn failure: %v", err)
	}
	if got == "" {
		t.Error("Handle() must return a response even when HistoryFn fails")
	}
	if strings.Contains(captureClient.capturedSysContent, "Контекст") {
		t.Error("when HistoryFn errors, context must not be added to the prompt")
	}
}

func TestAgent_Handle_AIClientError(t *testing.T) {
	client := &mockAIClient{err: errors.New("сеть недоступна")}

	a := newTestAgent(t, client, nil, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	_, err := a.Handle(context.Background(), msg)
	if err == nil {
		t.Fatal("Handle() must return an error when AI client fails")
	}
}

func TestAgent_Handle_UnknownToolCall_ContinuesWithErrorResult(t *testing.T) {
	client := &mockAIClient{
		responses: []openai.ChatCompletionResponse{
			toolCallResponse("call_1", "nonexistent_tool", `{}`),
			textResponse("Не удалось выполнить операцию."),
		},
	}

	a := newTestAgent(t, client, nil, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	got, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() must not return an error for unknown tool: %v", err)
	}
	if got == "" {
		t.Error("Handle() must return a response from the model after an unknown tool call")
	}
	if client.callCount != 2 {
		t.Errorf("AI client called %d times, want 2", client.callCount)
	}
}

func TestAgent_Handle_ToolExecuteError_ContinuesWithErrorResult(t *testing.T) {
	failingTool := &mockTool{
		name: "bad_tool",
		executeFunc: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("инструмент сломан")
		},
	}

	client := &mockAIClient{
		responses: []openai.ChatCompletionResponse{
			toolCallResponse("call_1", "bad_tool", `{}`),
			textResponse("Извини, произошла ошибка."),
		},
	}

	a := newTestAgent(t, client, []agent.Tool{failingTool}, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "вызови плохой инструмент",
	}

	got, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() must not return an error when a tool fails: %v", err)
	}
	if got == "" {
		t.Error("Handle() must return a text response from the model")
	}
}

func TestAgent_Handle_CustomSystemPrompt(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ок"),
	}

	customPrompt := "Ты специализированный ассистент для команды разработчиков."
	a := newTestAgent(t, captureClient, nil, agent.Config{SystemPrompt: customPrompt})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if captureClient.capturedSysContent != customPrompt {
		t.Errorf("system prompt = %q, want %q", captureClient.capturedSysContent, customPrompt)
	}
}

func TestAgent_InjectsNowIntoSystemPrompt(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ок"),
	}

	fixedNow := time.Date(2026, 4, 15, 14, 30, 45, 0, time.FixedZone("MSK", 3*3600))
	cfg := agent.Config{
		Now: func() time.Time { return fixedNow },
	}

	a := newTestAgent(t, captureClient, nil, cfg)

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	sys := captureClient.capturedSysContent
	expectedNow := "2026-04-15 14:30:45 +03:00"
	if !strings.Contains(sys, expectedNow) {
		t.Errorf("system prompt does not contain expected date %q\nFull prompt: %q", expectedNow, sys)
	}
	if !strings.Contains(sys, "Текущая дата и время:") {
		t.Error("system prompt must contain 'Текущая дата и время:' block")
	}
}

func TestAgent_InjectsProjectsIntoSystemPrompt(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ок"),
	}

	projects := []model.Project{
		{ID: "uuid-inbox", Slug: "inbox", Name: "Inbox"},
		{ID: "uuid-7", Slug: "na-start", Name: "На Старт"},
		{ID: "uuid-9", Slug: "huskwoot", Name: "Huskwoot"},
	}
	cfg := agent.Config{
		ListProjects: func(_ context.Context) ([]model.Project, error) {
			return projects, nil
		},
	}

	a := newTestAgent(t, captureClient, nil, cfg)

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "нужно пройти проверку разработчика Android для На Старт",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	sys := captureClient.capturedSysContent
	for _, p := range projects {
		if !strings.Contains(sys, p.Name) {
			t.Errorf("system prompt does not contain project name %q\nFull prompt: %q", p.Name, sys)
		}
	}
}

func TestAgent_ListProjectsNil_SystemPromptWithoutProjectsBlock(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ок"),
	}

	a := newTestAgent(t, captureClient, nil, agent.Config{})

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if strings.Contains(captureClient.capturedSysContent, "Известные проекты") {
		t.Error("when ListProjects is nil, system prompt must not contain projects block")
	}
}

func TestAgent_ListProjectsError_ContinuesWithoutProjectsBlock(t *testing.T) {
	captureClient := &captureToolsClient{
		response: textResponse("ответ"),
	}

	cfg := agent.Config{
		ListProjects: func(_ context.Context) ([]model.Project, error) {
			return nil, errors.New("бд недоступна")
		},
	}

	a := newTestAgent(t, captureClient, nil, cfg)

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "тест",
	}

	got, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() must not return an error when ListProjects fails: %v", err)
	}
	if got == "" {
		t.Error("Handle() must return a response even when ListProjects fails")
	}
	if strings.Contains(captureClient.capturedSysContent, "Известные проекты") {
		t.Error("when ListProjects errors, projects block must not be added to the prompt")
	}
}

func TestAgent_PutsNowInToolContext(t *testing.T) {
	fixedNow := time.Date(2026, 4, 15, 14, 30, 45, 0, time.FixedZone("MSK", 3*3600))
	var contextNow time.Time
	var contextNowWasCalled bool

	toolWithContextCapture := &mockTool{
		name: "capture_context",
		executeFunc: func(ctx context.Context, _ string) (string, error) {
			val := ctx.Value(agent.NowKey)
			if val != nil {
				contextNow = val.(time.Time)
				contextNowWasCalled = true
			}
			return `{"ok":true}`, nil
		},
	}

	client := &mockAIClient{
		responses: []openai.ChatCompletionResponse{
			toolCallResponse("call_1", "capture_context", `{}`),
			textResponse("Задача создана."),
		},
	}

	cfg := agent.Config{
		Now: func() time.Time { return fixedNow },
	}

	a := newTestAgent(t, client, []agent.Tool{toolWithContextCapture}, cfg)

	msg := model.Message{
		Kind: model.MessageKindDM,
		Text: "создай задачу",
	}

	_, err := a.Handle(context.Background(), msg)
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if !contextNowWasCalled {
		t.Fatal("tool was not called or now was not in context")
	}

	if contextNow != fixedNow {
		t.Errorf("now from context = %v, want %v", contextNow, fixedNow)
	}
}

func TestAgentToolSetIncludesProjectManagement_DM(t *testing.T) {
	captureClient := &captureToolsClient{response: textResponse("ок")}

	tools := []agent.Tool{
		&mockTool{name: "create_project", dmOnly: true},
		&mockTool{name: "list_projects", dmOnly: true},
		&mockTool{name: "get_project", dmOnly: true},
		&mockTool{name: "update_project", dmOnly: true},
		&mockTool{name: "add_project_alias", dmOnly: true},
		&mockTool{name: "remove_project_alias", dmOnly: true},
		&mockTool{name: "create_task", dmOnly: false},
	}

	a := newTestAgent(t, captureClient, tools, agent.Config{})
	_, err := a.Handle(context.Background(), model.Message{Kind: model.MessageKindDM, Text: "тест"})
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	wantTools := []string{"create_project", "list_projects", "get_project", "update_project", "add_project_alias", "remove_project_alias"}
	for _, want := range wantTools {
		found := false
		for _, got := range captureClient.capturedToolNames {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DM scope: expected project management tool %q not found in tool set: %v", want, captureClient.capturedToolNames)
		}
	}
	if len(captureClient.capturedToolNames) != len(tools) {
		t.Errorf("DM scope: want %d tools, got %d: %v", len(tools), len(captureClient.capturedToolNames), captureClient.capturedToolNames)
	}
}

func TestAgentToolSetIncludesProjectManagement_GroupDirect(t *testing.T) {
	captureClient := &captureToolsClient{response: textResponse("ок")}

	dmOnlyNames := []string{"create_project", "list_projects", "get_project", "update_project", "add_project_alias", "remove_project_alias"}
	tools := []agent.Tool{
		&mockTool{name: "create_project", dmOnly: true},
		&mockTool{name: "list_projects", dmOnly: true},
		&mockTool{name: "get_project", dmOnly: true},
		&mockTool{name: "update_project", dmOnly: true},
		&mockTool{name: "add_project_alias", dmOnly: true},
		&mockTool{name: "remove_project_alias", dmOnly: true},
		&mockTool{name: "create_task", dmOnly: false},
	}

	a := newTestAgent(t, captureClient, tools, agent.Config{})
	_, err := a.Handle(context.Background(), model.Message{Kind: model.MessageKindGroupDirect, Text: "тест"})
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	for _, name := range dmOnlyNames {
		for _, got := range captureClient.capturedToolNames {
			if got == name {
				t.Errorf("GroupDirect scope: DMOnly tool %q must not be in tool set", name)
				break
			}
		}
	}
	if len(captureClient.capturedToolNames) != 1 || captureClient.capturedToolNames[0] != "create_task" {
		t.Errorf("GroupDirect scope: want only [create_task], got: %v", captureClient.capturedToolNames)
	}
}

func TestAgent_SystemPromptIncludesProjectAliases(t *testing.T) {
	captureClient := &captureToolsClient{response: textResponse("ок")}

	projects := []model.Project{
		{ID: "uuid-1", Slug: "bukinist", Name: "Букинист", Aliases: []string{"букинист", "книги"}},
		{ID: "uuid-2", Slug: "inbox", Name: "Inbox", Aliases: []string{}},
	}
	cfg := agent.Config{
		ListProjects: func(_ context.Context) ([]model.Project, error) {
			return projects, nil
		},
	}

	a := newTestAgent(t, captureClient, nil, cfg)
	_, err := a.Handle(context.Background(), model.Message{Kind: model.MessageKindDM, Text: "тест"})
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	sys := captureClient.capturedSysContent
	if !strings.Contains(sys, "«букинист»") {
		t.Errorf("system prompt must contain alias in «...» format\nFull prompt: %q", sys)
	}
	if !strings.Contains(sys, "«книги»") {
		t.Errorf("system prompt must contain second alias in «...» format\nFull prompt: %q", sys)
	}
}

func TestAgent_SystemPromptNoAliasesTail_WhenEmpty(t *testing.T) {
	captureClient := &captureToolsClient{response: textResponse("ок")}

	projects := []model.Project{
		{ID: "uuid-2", Slug: "inbox", Name: "Inbox", Aliases: []string{}},
	}
	cfg := agent.Config{
		ListProjects: func(_ context.Context) ([]model.Project, error) {
			return projects, nil
		},
	}

	a := newTestAgent(t, captureClient, nil, cfg)
	_, err := a.Handle(context.Background(), model.Message{Kind: model.MessageKindDM, Text: "тест"})
	if err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	sys := captureClient.capturedSysContent
	if strings.Contains(sys, "aliases:") {
		t.Errorf("system prompt must not contain 'aliases:' when project has no aliases\nFull prompt: %q", sys)
	}
}
