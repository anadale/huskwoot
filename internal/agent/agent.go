package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/sashabaranov/go-openai"
)

const maxIterations = 5

// contextKey is the key type for values stored in the agent context.
type contextKey int

const (
	// sourceIDKey is the key for the message source ID (chat/channel ID).
	sourceIDKey contextKey = iota
	// nowKey is the key for the current time in the tool context.
	nowKey
	// originMessageKey is the key for the incoming message the agent is handling.
	originMessageKey
)

// WithOriginMessage returns a context carrying msg as the message the agent is
// handling. Tools read it to attribute created tasks to their origin.
func WithOriginMessage(ctx context.Context, msg model.Message) context.Context {
	return context.WithValue(ctx, originMessageKey, msg)
}

// OriginMessageFromContext returns the message stored by WithOriginMessage.
// The second value is false when the context carries no origin message.
func OriginMessageFromContext(ctx context.Context) (model.Message, bool) {
	msg, ok := ctx.Value(originMessageKey).(model.Message)
	return msg, ok
}

// AIClient is the AI client interface for the tool-calling loop.
type AIClient interface {
	CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// Config holds agent parameters.
type Config struct {
	// SystemPrompt is a custom system prompt. Empty string = built-in default.
	SystemPrompt string
	// Language is the prompt language ("ru" or "en"). Empty string defaults to "ru".
	Language string
	// Now is the function for obtaining the current time. If nil — time.Now is used.
	Now func() time.Time
	// ListProjects is an optional function that fetches the project list for injection
	// into the system prompt. Lets the model recognise project names even in
	// non-standard phrasing without a mandatory list_projects call.
	// nil — the projects block is not added to the prompt. Errors are logged
	// but do not interrupt processing.
	ListProjects func(ctx context.Context) ([]model.Project, error)
	// NightCutoffMinutes is the night day boundary in minutes since midnight
	// (datetime.night_cutoff); 0 disables it. Before the boundary the prompt tells
	// the model that "tomorrow" in the user's words means the current date, the
	// same shift dateparse applies to relative day phrases.
	NightCutoffMinutes int
}

// Tool describes a single agent tool.
type Tool interface {
	// Name returns the tool name (used in function calling).
	Name() string
	// Description describes the tool's purpose for the model.
	Description() string
	// Parameters returns the JSON schema for the tool parameters.
	Parameters() map[string]any
	// DMOnly returns true if the tool is only available in DM mode (full access).
	// In GroupDirect mode such tools are excluded from the tools set.
	DMOnly() bool
	// Execute runs the tool with the given arguments (JSON string) and returns the result.
	Execute(ctx context.Context, args string) (string, error)
}

// Agent processes DM/GroupDirect messages through a tool-calling loop.
type Agent struct {
	client       AIClient
	tools        []Tool
	listProjects func(ctx context.Context) ([]model.Project, error)
	now          func() time.Time
	nightCutoff  int
	systemTmpl   *template.Template
	logger       *slog.Logger
}

// New creates a new Agent.
func New(client AIClient, tools []Tool, cfg Config, logger *slog.Logger) (*Agent, error) {
	if logger == nil {
		logger = slog.Default()
	}
	lang := cfg.Language
	if lang == "" {
		lang = "ru"
	}
	sp := cfg.SystemPrompt
	if sp == "" {
		sp = loadPrompt(agentPromptsFS, lang, "agent_system")
	}
	tmpl, err := template.New("agent-system").Parse(sp)
	if err != nil {
		return nil, fmt.Errorf("parsing agent system template: %w", err)
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Agent{
		client:       client,
		tools:        tools,
		listProjects: cfg.ListProjects,
		now:          nowFn,
		nightCutoff:  cfg.NightCutoffMinutes,
		systemTmpl:   tmpl,
		logger:       logger,
	}, nil
}

// Handle processes an incoming message through the tool-calling loop.
// Returns the text reply for the user.
func (a *Agent) Handle(ctx context.Context, msg model.Message) (string, error) {
	if msg.Source.ID != "" {
		ctx = context.WithValue(ctx, sourceIDKey, msg.Source.ID)
	}
	ctx = WithOriginMessage(ctx, msg)

	// A single agent message uses a fixed now: stored in context and rendered into the prompt.
	now := a.now()
	ctx = context.WithValue(ctx, nowKey, now)

	scopedTools := a.buildScopeTools(msg)

	type projectEntry struct {
		ID      string
		Name    string
		Slug    string
		Aliases []string
	}
	var projectEntries []projectEntry
	if a.listProjects != nil {
		projects, err := a.listProjects(ctx)
		if err != nil {
			a.logger.WarnContext(ctx, "fetching project list for prompt", "msg_id", msg.ID, "error", err)
		} else {
			projectEntries = make([]projectEntry, 0, len(projects))
			for _, p := range projects {
				projectEntries = append(projectEntries, projectEntry{ID: p.ID, Name: p.Name, Slug: p.Slug, Aliases: p.Aliases})
			}
		}
	}

	type projectHintEntry struct {
		Name string
		Slug string
	}
	var projectHint *projectHintEntry
	if msg.ProjectHint != nil {
		projectHint = &projectHintEntry{Name: msg.ProjectHint.Name, Slug: msg.ProjectHint.Slug}
	}

	type nightShiftEntry struct {
		Cutoff   string
		Today    string
		Tomorrow string
	}
	var nightShift *nightShiftEntry
	if a.nightCutoff > 0 && now.Hour()*60+now.Minute() < a.nightCutoff {
		nightShift = &nightShiftEntry{
			Cutoff:   fmt.Sprintf("%02d:%02d", a.nightCutoff/60, a.nightCutoff%60),
			Today:    now.Format("2006-01-02"),
			Tomorrow: now.AddDate(0, 0, 1).Format("2006-01-02"),
		}
	}

	data := struct {
		Now         string
		NightShift  *nightShiftEntry
		Projects    []projectEntry
		ProjectHint *projectHintEntry
	}{
		Now:         now.Format("2006-01-02 15:04:05 -07:00"),
		NightShift:  nightShift,
		Projects:    projectEntries,
		ProjectHint: projectHint,
	}
	var systemPromptBuf strings.Builder
	if err := a.systemTmpl.Execute(&systemPromptBuf, data); err != nil {
		return "", fmt.Errorf("rendering system prompt: %w", err)
	}
	sysContent := systemPromptBuf.String()

	if msg.HistoryFn != nil {
		entries, err := msg.HistoryFn(ctx)
		if err != nil {
			a.logger.WarnContext(ctx, "fetching chat history", "msg_id", msg.ID, "error", err)
		} else if len(entries) > 0 {
			var sb strings.Builder
			sb.WriteString(sysContent)
			sb.WriteString("\n\nRecent chat context:\n")
			for _, e := range entries {
				fmt.Fprintf(&sb, "[%s] %s: %s\n", e.Timestamp.Format("15:04"), e.AuthorName, e.Text)
			}
			sysContent = sb.String()
		}
	}

	a.logger.DebugContext(ctx, "agent started processing",
		"msg_id", msg.ID, "kind", msg.Kind, "tools", len(scopedTools))

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: sysContent},
		{Role: openai.ChatMessageRoleUser, Content: msg.Text},
	}

	for i := 0; i < maxIterations; i++ {
		a.logger.DebugContext(ctx, "tool calling iteration", "iteration", i+1, "msg_id", msg.ID)

		req := openai.ChatCompletionRequest{
			Messages: messages,
			Tools:    scopedTools,
		}

		resp, err := a.client.CreateChatCompletion(ctx, req)
		if err != nil {
			return "", fmt.Errorf("calling AI: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("AI returned empty choices list")
		}

		choice := resp.Choices[0]

		if len(choice.Message.ToolCalls) == 0 {
			a.logger.DebugContext(ctx, "agent finished without tool calls",
				"msg_id", msg.ID, "iterations", i+1)
			return choice.Message.Content, nil
		}

		a.logger.DebugContext(ctx, "AI requested tool calls",
			"msg_id", msg.ID, "count", len(choice.Message.ToolCalls))

		messages = append(messages, choice.Message)

		for _, tc := range choice.Message.ToolCalls {
			result := a.executeTool(ctx, tc)
			messages = append(messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return "", fmt.Errorf("maximum iterations exceeded (%d)", maxIterations)
}

// buildScopeTools builds the openai.Tool list for the request according to message scope.
// GroupDirect: DMOnly tools are excluded.
// DM: all tools are available.
func (a *Agent) buildScopeTools(msg model.Message) []openai.Tool {
	result := make([]openai.Tool, 0, len(a.tools))
	for _, t := range a.tools {
		if msg.Kind == model.MessageKindGroupDirect && t.DMOnly() {
			continue
		}
		result = append(result, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return result
}

// executeTool finds the tool by name and executes it.
// On error returns a JSON error description (does not interrupt the loop).
func (a *Agent) executeTool(ctx context.Context, tc openai.ToolCall) string {
	for _, t := range a.tools {
		if t.Name() == tc.Function.Name {
			a.logger.DebugContext(ctx, "calling tool",
				"tool", tc.Function.Name, "args", tc.Function.Arguments)
			result, err := t.Execute(ctx, tc.Function.Arguments)
			if err != nil {
				a.logger.WarnContext(ctx, "tool execution error",
					"tool", tc.Function.Name, "error", err)
				return fmt.Sprintf(`{"error":%q}`, err.Error())
			}
			a.logger.DebugContext(ctx, "tool result",
				"tool", tc.Function.Name, "result", result)
			return result
		}
	}
	a.logger.WarnContext(ctx, "tool not found", "tool", tc.Function.Name)
	return fmt.Sprintf(`{"error":"tool %q not found"}`, tc.Function.Name)
}
