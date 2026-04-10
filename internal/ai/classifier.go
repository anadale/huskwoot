package ai

import (
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// ClassifierConfig holds parameters for message classifiers.
type ClassifierConfig struct {
	// UserName is the display name of the user, substituted into prompts.
	UserName string
	// Aliases are additional user identifiers (e.g. shortened names).
	Aliases []string
	// Language is the prompt language ("ru" or "en"). Empty string defaults to "ru".
	Language string
	// SystemTemplate is a custom system template. Empty string = default.
	SystemTemplate string
	// UserTemplate is a custom user template. Empty string = default.
	UserTemplate string
}

// classifierData holds data for rendering classifier prompts.
type classifierData struct {
	UserName string
	Aliases  string
	Subject  string
	Text     string
	ReplyTo  *model.Message
	Reaction *model.Reaction
}

// ParseClassification parses the model response, expecting the first word to be "promise", "skip", or "command".
// Case-insensitive; suffixes like "promise." or "Skip" are accepted.
func ParseClassification(response string) (model.Classification, error) {
	trimmed := strings.TrimSpace(response)
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ' ' || r == '.' || r == ',' || r == '!' || r == '?'
	})
	if len(fields) == 0 {
		return model.ClassSkip, fmt.Errorf("empty model response")
	}
	switch strings.ToLower(fields[0]) {
	case "promise":
		return model.ClassPromise, nil
	case "skip":
		return model.ClassSkip, nil
	case "command":
		return model.ClassCommand, nil
	default:
		return model.ClassSkip, fmt.Errorf("unexpected model response format: %q", response)
	}
}

// parseSimpleClassification parses a SimpleClassifier response — only promise or skip.
func parseSimpleClassification(response string) (model.Classification, error) {
	trimmed := strings.TrimSpace(response)
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ' ' || r == '.' || r == ',' || r == '!' || r == '?'
	})
	if len(fields) == 0 {
		return model.ClassSkip, fmt.Errorf("empty model response")
	}
	switch strings.ToLower(fields[0]) {
	case "promise":
		return model.ClassPromise, nil
	case "skip":
		return model.ClassSkip, nil
	default:
		return model.ClassSkip, fmt.Errorf("unexpected model response format: %q", response)
	}
}

// SimpleClassifier classifies DM and Batch messages: Promise or Skip.
// Implements the model.Classifier interface.
type SimpleClassifier struct {
	client     Completer
	cfg        ClassifierConfig
	systemTmpl *template.Template
	userTmpl   *template.Template
}

// NewSimpleClassifier creates a new SimpleClassifier with the given parameters.
func NewSimpleClassifier(client Completer, cfg ClassifierConfig) (*SimpleClassifier, error) {
	lang := cfg.Language
	if lang == "" {
		lang = "ru"
	}
	systemSrc := cfg.SystemTemplate
	if systemSrc == "" {
		systemSrc = loadPrompt(promptsFS, lang, "classifier_simple_system")
	}
	userSrc := cfg.UserTemplate
	if userSrc == "" {
		userSrc = loadPrompt(promptsFS, lang, "classifier_user")
	}

	sysTmpl, err := template.New("simple-classifier-system").Parse(systemSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing system template: %w", err)
	}
	userTmpl, err := template.New("simple-classifier-user").Parse(userSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing user template: %w", err)
	}
	return &SimpleClassifier{
		client:     client,
		cfg:        cfg,
		systemTmpl: sysTmpl,
		userTmpl:   userTmpl,
	}, nil
}

// Classify classifies the message, returning ClassPromise or ClassSkip.
func (c *SimpleClassifier) Classify(ctx context.Context, msg model.Message) (model.Classification, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	data := classifierData{
		UserName: c.cfg.UserName,
		Aliases:  strings.Join(c.cfg.Aliases, ", "),
		Subject:  msg.Subject,
		Text:     msg.Text,
		ReplyTo:  msg.ReplyTo,
		Reaction: msg.Reaction,
	}

	systemPrompt, err := renderTemplate(c.systemTmpl, data)
	if err != nil {
		return model.ClassSkip, fmt.Errorf("rendering system prompt: %w", err)
	}
	userPrompt, err := renderTemplate(c.userTmpl, data)
	if err != nil {
		return model.ClassSkip, fmt.Errorf("rendering user prompt: %w", err)
	}

	resp, err := c.client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return model.ClassSkip, fmt.Errorf("calling AI client: %w", err)
	}

	return parseSimpleClassification(resp)
}

// GroupClassifier classifies group messages: Promise, Command, or Skip.
// Implements the model.Classifier interface.
type GroupClassifier struct {
	client     Completer
	cfg        ClassifierConfig
	systemTmpl *template.Template
	userTmpl   *template.Template
}

// NewGroupClassifier creates a new GroupClassifier with the given parameters.
func NewGroupClassifier(client Completer, cfg ClassifierConfig) (*GroupClassifier, error) {
	lang := cfg.Language
	if lang == "" {
		lang = "ru"
	}
	systemSrc := cfg.SystemTemplate
	if systemSrc == "" {
		systemSrc = loadPrompt(promptsFS, lang, "classifier_group_system")
	}
	userSrc := cfg.UserTemplate
	if userSrc == "" {
		userSrc = loadPrompt(promptsFS, lang, "classifier_user")
	}

	sysTmpl, err := template.New("group-classifier-system").Parse(systemSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing system template: %w", err)
	}
	userTmpl, err := template.New("group-classifier-user").Parse(userSrc)
	if err != nil {
		return nil, fmt.Errorf("parsing user template: %w", err)
	}
	return &GroupClassifier{
		client:     client,
		cfg:        cfg,
		systemTmpl: sysTmpl,
		userTmpl:   userTmpl,
	}, nil
}

// Classify classifies the message, returning ClassPromise, ClassCommand, or ClassSkip.
func (g *GroupClassifier) Classify(ctx context.Context, msg model.Message) (model.Classification, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	data := classifierData{
		UserName: g.cfg.UserName,
		Aliases:  strings.Join(g.cfg.Aliases, ", "),
		Subject:  msg.Subject,
		Text:     msg.Text,
		ReplyTo:  msg.ReplyTo,
		Reaction: msg.Reaction,
	}

	systemPrompt, err := renderTemplate(g.systemTmpl, data)
	if err != nil {
		return model.ClassSkip, fmt.Errorf("rendering system prompt: %w", err)
	}
	userPrompt, err := renderTemplate(g.userTmpl, data)
	if err != nil {
		return model.ClassSkip, fmt.Errorf("rendering user prompt: %w", err)
	}

	resp, err := g.client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return model.ClassSkip, fmt.Errorf("calling AI client: %w", err)
	}

	return ParseClassification(resp)
}
