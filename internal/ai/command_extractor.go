package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// CommandExtractorConfig holds parameters for AICommandExtractor.
type CommandExtractorConfig struct {
	// Language is the prompt language ("ru" or "en"). Empty string defaults to "ru".
	Language string
	// SystemTemplate is a custom system template. Empty string = default.
	SystemTemplate string
	// UserTemplate is a custom user template. Empty string = default.
	UserTemplate string
}

// commandExtractorData holds data for rendering AICommandExtractor prompts.
type commandExtractorData struct {
	Text string
}

// commandExtractorResponse is the JSON structure of the model response.
type commandExtractorResponse struct {
	Type    string            `json:"type"`
	Payload map[string]string `json:"payload"`
}

// AICommandExtractor extracts a structured command from a message via AI.
// Implements the model.CommandExtractor interface.
type AICommandExtractor struct {
	client     Completer
	cfg        CommandExtractorConfig
	systemTmpl *template.Template
	userTmpl   *template.Template
}

// NewAICommandExtractor creates a new AICommandExtractor with the given parameters.
func NewAICommandExtractor(client Completer, cfg CommandExtractorConfig) (*AICommandExtractor, error) {
	lang := cfg.Language
	if lang == "" {
		lang = "ru"
	}
	systemSrc := cfg.SystemTemplate
	if systemSrc == "" {
		systemSrc = loadPrompt(promptsFS, lang, "command_extractor_system")
	}
	userSrc := cfg.UserTemplate
	if userSrc == "" {
		userSrc = loadPrompt(promptsFS, lang, "command_extractor_user")
	}

	sysTmpl, err := template.New("command-extractor-system").Parse(systemSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing system template: %w", err)
	}
	userTmpl, err := template.New("command-extractor-user").Parse(userSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing user template: %w", err)
	}

	return &AICommandExtractor{
		client:     client,
		cfg:        cfg,
		systemTmpl: sysTmpl,
		userTmpl:   userTmpl,
	}, nil
}

// Extract extracts a command from a message using the AI client.
func (e *AICommandExtractor) Extract(ctx context.Context, msg model.Message) (model.Command, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	data := commandExtractorData{
		Text: msg.Text,
	}

	systemPrompt, err := renderTemplate(e.systemTmpl, data)
	if err != nil {
		return model.Command{}, fmt.Errorf("rendering system prompt: %w", err)
	}
	userPrompt, err := renderTemplate(e.userTmpl, data)
	if err != nil {
		return model.Command{}, fmt.Errorf("rendering user prompt: %w", err)
	}

	raw, err := e.client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return model.Command{}, fmt.Errorf("calling AI client: %w", err)
	}

	// Some LLMs wrap JSON in a markdown code fence (```json ... ```).
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

	var resp commandExtractorResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return model.Command{}, fmt.Errorf("parsing model JSON response: %w", err)
	}

	return model.Command{
		Type:          resp.Type,
		Payload:       resp.Payload,
		Source:        msg.Source,
		SourceMessage: msg,
	}, nil
}
