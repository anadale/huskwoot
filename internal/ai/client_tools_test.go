package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anadale/huskwoot/internal/ai"
	"github.com/sashabaranov/go-openai"
)

// openAIToolCallResponse builds a response with tool calls from the OpenAI API.
func openAIToolCallResponse(toolCalls []map[string]any) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-tools-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "gpt-4o-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": toolCalls,
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 20,
			"total_tokens":      30,
		},
	}
}

func TestClient_CreateChatCompletion_WithToolCalls(t *testing.T) {
	expectedToolCall := map[string]any{
		"id":   "call_abc123",
		"type": "function",
		"function": map[string]any{
			"name":      "create_project",
			"arguments": `{"name":"Тест"}`,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIToolCallResponse([]map[string]any{expectedToolCall}))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "создай проект Тест"},
		},
		Tools: []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        "create_project",
					Description: "Создать проект",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion() returned error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("want non-empty choices list")
	}
	if resp.Choices[0].FinishReason != openai.FinishReasonToolCalls {
		t.Errorf("finish_reason = %q, want %q", resp.Choices[0].FinishReason, openai.FinishReasonToolCalls)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("tool call ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Function.Name != "create_project" {
		t.Errorf("tool call function name = %q, want %q", tc.Function.Name, "create_project")
	}
}

func TestClient_CreateChatCompletion_WithoutToolCalls(t *testing.T) {
	want := "Привет! Чем могу помочь?"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIResponse(want))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "привет"},
		},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion() returned error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("want non-empty choices list")
	}
	if resp.Choices[0].Message.Content != want {
		t.Errorf("content = %q, want %q", resp.Choices[0].Message.Content, want)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 0 {
		t.Errorf("want 0 tool calls, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
}

func TestClient_CreateChatCompletion_MultipleToolCalls(t *testing.T) {
	toolCalls := []map[string]any{
		{
			"id":   "call_1",
			"type": "function",
			"function": map[string]any{
				"name":      "create_task",
				"arguments": `{"summary":"задача 1"}`,
			},
		},
		{
			"id":   "call_2",
			"type": "function",
			"function": map[string]any{
				"name":      "create_task",
				"arguments": `{"summary":"задача 2"}`,
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIToolCallResponse(toolCalls))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "создай две задачи"},
		},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion() returned error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("want non-empty choices list")
	}
	if len(resp.Choices[0].Message.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
	if resp.Choices[0].Message.ToolCalls[0].ID != "call_1" {
		t.Errorf("first tool call ID = %q, want %q", resp.Choices[0].Message.ToolCalls[0].ID, "call_1")
	}
	if resp.Choices[0].Message.ToolCalls[1].ID != "call_2" {
		t.Errorf("second tool call ID = %q, want %q", resp.Choices[0].Message.ToolCalls[1].ID, "call_2")
	}
}

func TestClient_CreateChatCompletion_SetsModelAndTokens(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIResponse("ответ"))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL:             srv.URL + "/v1",
		Model:               "gpt-4o",
		APIKey:              "test-key",
		MaxCompletionTokens: 1024,
	})

	// Send a request without Model and MaxCompletionTokens — the client must supply them
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "тест"},
		},
	}

	_, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion() returned error: %v", err)
	}

	gotModel, ok := capturedBody["model"]
	if !ok {
		t.Fatal("request does not contain model field")
	}
	if gotModel.(string) != "gpt-4o" {
		t.Errorf("model = %q, want %q", gotModel, "gpt-4o")
	}

	gotTokens, ok := capturedBody["max_completion_tokens"]
	if !ok {
		t.Fatal("request does not contain max_completion_tokens field")
	}
	if int(gotTokens.(float64)) != 1024 {
		t.Errorf("max_completion_tokens = %v, want 1024", gotTokens)
	}
}

func TestClient_CreateChatCompletion_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "test-model",
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 0, "total_tokens": 5},
		})
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "тест"},
		},
	}

	// CreateChatCompletion does not validate empty choices — that is the caller's job.
	// The method must return a response without an error (go-openai itself does not check).
	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion() returned error: %v", err)
	}
	if len(resp.Choices) != 0 {
		t.Errorf("want 0 choices, got %d", len(resp.Choices))
	}
}
