package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/ai"
)

// openAIResponse builds a minimal response compatible with the OpenAI Chat Completions API.
func openAIResponse(content string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "gpt-4o-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}
}

func TestClient_Complete_Success(t *testing.T) {
	want := "Это тестовый ответ от модели."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIResponse(want))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	got, err := client.Complete(context.Background(), "системный промпт", "пользовательский промпт")
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if got != want {
		t.Errorf("Complete() = %q, want %q", got, want)
	}
}

func TestClient_Complete_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "Incorrect API key provided.",
				"type":    "invalid_request_error",
				"code":    "invalid_api_key",
			},
		})
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "wrong-key",
	})

	_, err := client.Complete(context.Background(), "системный промпт", "пользовательский промпт")
	if err == nil {
		t.Fatal("Complete() must return an error on status 401")
	}
}

func TestClient_Complete_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(openAIResponse("поздний ответ"))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Complete(ctx, "системный промпт", "пользовательский промпт")
	if err == nil {
		t.Fatal("Complete() must return an error when timeout expires")
	}
}

func TestClient_Complete_SendsMaxCompletionTokens(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIResponse("ответ"))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL:             srv.URL + "/v1",
		Model:               "test-model",
		APIKey:              "test-key",
		MaxCompletionTokens: 2048,
	})

	_, err := client.Complete(context.Background(), "системный промпт", "пользовательский промпт")
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}

	got, ok := capturedBody["max_completion_tokens"]
	if !ok {
		t.Fatal("request does not contain max_completion_tokens field")
	}
	// JSON numbers are decoded as float64
	if int(got.(float64)) != 2048 {
		t.Errorf("max_completion_tokens = %v, want 2048", got)
	}
}

type testResult struct {
	Answer string `json:"answer"`
	Score  int    `json:"score"`
}

func TestClient_CompleteJSON_Success(t *testing.T) {
	want := testResult{Answer: "Go — отличный язык", Score: 10}

	responseJSON, _ := json.Marshal(want)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIResponse(string(responseJSON)))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	got, err := ai.CompleteJSON[testResult](client, context.Background(), "системный промпт", "пользовательский промпт")
	if err != nil {
		t.Fatalf("CompleteJSON() returned error: %v", err)
	}
	if got != want {
		t.Errorf("CompleteJSON() = %+v, want %+v", got, want)
	}
}

func TestClient_CompleteJSON_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIResponse("это не JSON вообще"))
	}))
	defer srv.Close()

	client := ai.NewClient(ai.ClientConfig{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
	})

	_, err := ai.CompleteJSON[testResult](client, context.Background(), "системный промпт", "пользовательский промпт")
	if err == nil {
		t.Fatal("CompleteJSON() must return an error when response contains invalid JSON")
	}
}

func TestClient_Complete_EmptyChoices(t *testing.T) {
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

	_, err := client.Complete(context.Background(), "системный промпт", "пользовательский промпт")
	if err == nil {
		t.Fatal("Complete() must return an error when choices list is empty")
	}
}
