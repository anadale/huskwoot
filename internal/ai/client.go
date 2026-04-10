package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// ClientConfig holds connection parameters for an OpenAI-compatible API.
type ClientConfig struct {
	// BaseURL is the API endpoint (e.g. "https://api.openai.com/v1" or an Ollama-compatible URL).
	BaseURL string
	// Model is the model identifier (e.g. "gpt-4o-mini").
	Model string
	// APIKey is the authentication key.
	APIKey string
	// MaxCompletionTokens is the maximum number of tokens in the model response.
	MaxCompletionTokens int
}

// Client is a wrapper around an OpenAI-compatible API with a configurable endpoint.
type Client struct {
	inner               *openai.Client
	model               string
	maxCompletionTokens int
}

// NewClient creates a new Client with the given configuration.
func NewClient(cfg ClientConfig) *Client {
	config := openai.DefaultConfig(cfg.APIKey)
	config.BaseURL = cfg.BaseURL

	return &Client{
		inner:               openai.NewClientWithConfig(config),
		model:               cfg.Model,
		maxCompletionTokens: cfg.MaxCompletionTokens,
	}
}

// Complete sends a request to the Chat Completions API and returns the model's text response.
func (c *Client) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resp, err := c.inner.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: c.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		MaxCompletionTokens: c.maxCompletionTokens,
	})
	if err != nil {
		return "", fmt.Errorf("calling ChatCompletion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("API returned empty choices list")
	}

	return resp.Choices[0].Message.Content, nil
}

// CreateChatCompletion is a thin wrapper around go-openai that fills in Model and MaxCompletionTokens
// from the client configuration. Used by the agent for the tool-calling loop.
func (c *Client) CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	req.Model = c.model
	req.MaxCompletionTokens = c.maxCompletionTokens
	resp, err := c.inner.CreateChatCompletion(ctx, req)
	if err != nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("calling ChatCompletion: %w", err)
	}
	return resp, nil
}

// CompleteJSON sends a request and deserializes the model's JSON response into type T.
// A standalone function (not a method) due to Go's generics restrictions on methods.
func CompleteJSON[T any](c *Client, ctx context.Context, systemPrompt, userPrompt string) (T, error) {
	var zero T

	text, err := c.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return zero, err
	}

	var result T
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return zero, fmt.Errorf("parsing model JSON response: %w", err)
	}

	return result, nil
}
