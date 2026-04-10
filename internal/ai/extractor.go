package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
	"time"

	"github.com/anadale/huskwoot/internal/dateparse"
	"github.com/anadale/huskwoot/internal/model"
)

// ExtractorConfig holds parameters for TaskExtractor.
type ExtractorConfig struct {
	// UserName is the display name of the owner.
	UserName string
	// Aliases are alternative names for the user (e.g. abbreviations, Latin spelling).
	Aliases []string
	// ConfidenceThreshold is the minimum model confidence for saving a task (0.0–1.0).
	// Tasks with confidence below the threshold are filtered out.
	ConfidenceThreshold float64
	// Now is a function returning the current time. If nil, time.Now is used.
	Now func() time.Time
	// DateParse is the date parsing configuration.
	DateParse dateparse.Config
	// Language is the prompt language ("ru" or "en"). Empty string defaults to "ru".
	Language string
	// SystemTemplate is a custom system template. Empty string uses the default.
	SystemTemplate string
	// UserTemplate is a custom user template. Empty string uses the default.
	UserTemplate string
	// ProjectsFn is a function returning the current list of known projects.
	// Called on every Extract to build the model hint.
	// If nil, the project list is not included in the prompt.
	ProjectsFn func(ctx context.Context) ([]string, error)
}

// TaskExtractor extracts a structured task from a promise message.
// Implements the model.Extractor interface.
type TaskExtractor struct {
	client     Completer
	cfg        ExtractorConfig
	systemTmpl *template.Template
	userTmpl   *template.Template
	dateparser *dateparse.Dateparser
}

// NewTaskExtractor creates a new TaskExtractor with the given parameters.
func NewTaskExtractor(client Completer, cfg ExtractorConfig) (*TaskExtractor, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ConfidenceThreshold == 0 {
		cfg.ConfidenceThreshold = 0.5
	}

	lang := cfg.Language
	if lang == "" {
		lang = "ru"
	}
	systemSrc := cfg.SystemTemplate
	if systemSrc == "" {
		systemSrc = loadPrompt(promptsFS, lang, "extractor_system")
	}
	userSrc := cfg.UserTemplate
	if userSrc == "" {
		userSrc = loadPrompt(promptsFS, lang, "extractor_user")
	}

	sysTmpl, err := template.New("extractor-system").Parse(systemSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing system template: %w", err)
	}
	userTmpl, err := template.New("extractor-user").Parse(userSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing user template: %w", err)
	}

	return &TaskExtractor{
		client:     client,
		cfg:        cfg,
		systemTmpl: sysTmpl,
		userTmpl:   userTmpl,
		dateparser: dateparse.New(cfg.DateParse, dateparse.NewDateLanguage(lang)),
	}, nil
}

// extractorModelResponse is the JSON structure of the model response.
type extractorModelResponse struct {
	Summary    string  `json:"summary"`
	Context    string  `json:"context"`
	Topic      string  `json:"topic"`
	Project    string  `json:"project"`
	Deadline   *string `json:"deadline"`
	Confidence float64 `json:"confidence"`
}

// extractorData holds data for rendering TaskExtractor prompts.
type extractorData struct {
	UserName string
	Aliases  string
	Projects []string
	History  []model.HistoryEntry
	Subject  string
	Text     string
	ReplyTo  *model.Message
	Reaction *model.Reaction
	Now      string
}

// Extract extracts tasks from a promise message using dialog history.
// Returns an empty slice if no promises are found or all are filtered by the confidence threshold.
func (e *TaskExtractor) Extract(ctx context.Context, msg model.Message, history []model.HistoryEntry) ([]model.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	now := e.cfg.Now()

	// Use the message timestamp for deadline calculation and prompt timestamp,
	// not the current time — otherwise "tomorrow" would be relative to processing time,
	// not the time the message was sent.
	msgTime := msg.Timestamp
	if msgTime.IsZero() {
		msgTime = now
	}

	var projects []string
	if e.cfg.ProjectsFn != nil {
		var projErr error
		projects, projErr = e.cfg.ProjectsFn(ctx)
		if projErr != nil {
			slog.WarnContext(ctx, "failed to get project list for extractor", "error", projErr)
		}
	}

	data := extractorData{
		UserName: e.cfg.UserName,
		Aliases:  strings.Join(e.cfg.Aliases, ", "),
		Projects: projects,
		History:  history,
		Subject:  msg.Subject,
		Text:     msg.Text,
		ReplyTo:  msg.ReplyTo,
		Reaction: msg.Reaction,
		Now:      msgTime.Format("2006-01-02 15:04:05 -07:00"),
	}

	systemPrompt, err := renderTemplate(e.systemTmpl, data)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}
	userPrompt, err := renderTemplate(e.userTmpl, data)
	if err != nil {
		return nil, fmt.Errorf("rendering user prompt: %w", err)
	}

	raw, err := e.client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("calling AI client: %w", err)
	}

	// Some LLMs wrap JSON in a markdown fence (```json ... ```).
	// Extract the content between fences if present.
	if idx := strings.Index(raw, "```"); idx != -1 {
		raw = raw[idx+3:]
		if nl := strings.IndexByte(raw, '\n'); nl != -1 {
			raw = raw[nl+1:]
		}
		if end := strings.Index(raw, "```"); end != -1 {
			raw = raw[:end]
		}
		raw = strings.TrimSpace(raw)
	}

	var responses []extractorModelResponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return nil, fmt.Errorf("parsing model JSON response: %w", err)
	}

	var tasks []model.Task
	for _, resp := range responses {
		if resp.Confidence < e.cfg.ConfidenceThreshold {
			continue
		}

		task := model.Task{
			Summary:       resp.Summary,
			Details:       resp.Context,
			Topic:         resp.Topic,
			Confidence:    resp.Confidence,
			Source:        msg.Source,
			SourceMessage: msg,
			CreatedAt:     msgTime,
		}

		if resp.Deadline != nil && !strings.EqualFold(*resp.Deadline, "null") && *resp.Deadline != "" {
			deadline, err := e.dateparser.Parse(*resp.Deadline, msgTime)
			if err != nil {
				slog.WarnContext(ctx, "failed to parse deadline, saving task without it",
					"deadline", *resp.Deadline, "error", err)
			} else {
				task.Deadline = deadline
			}
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}
