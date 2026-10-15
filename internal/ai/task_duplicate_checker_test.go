package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/model"
)

func newDuplicateCheckerEndpoint(t *testing.T, handler http.HandlerFunc, lang string) model.TaskDuplicateChecker {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	checker, err := ai.NewTaskDuplicateChecker(ai.NewClient(ai.ClientConfig{
		BaseURL: server.URL + "/v1",
		Model:   "duplicate-check-test",
		APIKey:  "test-key",
	}), lang)
	if err != nil {
		t.Fatalf("NewTaskDuplicateChecker: %v", err)
	}
	return checker
}

func TestTaskDuplicateChecker_InvalidResponseReturnsError(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "empty", response: ""},
		{name: "malformed", response: "not JSON"},
		{name: "null object", response: "null"},
		{name: "array", response: "[]"},
		{name: "empty object", response: "{}"},
		{name: "unknown verdict", response: `{"verdict":"duplicate","matchId":"task:known","score":0.99,"conflict":false,"reason":"Same"}`},
		{name: "case sensitive verdict", response: `{"verdict":"Same","matchId":"task:known","score":0.99,"conflict":false,"reason":"Same"}`},
		{name: "negative score", response: `{"verdict":"same","matchId":"task:known","score":-0.01,"conflict":false,"reason":"Same"}`},
		{name: "score above one", response: `{"verdict":"same","matchId":"task:known","score":1.01,"conflict":false,"reason":"Same"}`},
		{name: "nonfinite score", response: `{"verdict":"same","matchId":"task:known","score":NaN,"conflict":false,"reason":"Same"}`},
		{name: "overflow score", response: `{"verdict":"same","matchId":"task:known","score":1e999,"conflict":false,"reason":"Same"}`},
		{name: "score string", response: `{"verdict":"same","matchId":"task:known","score":"0.99","conflict":false,"reason":"Same"}`},
		{name: "missing score", response: `{"verdict":"different","matchId":"","conflict":false,"reason":"Different"}`},
		{name: "null score", response: `{"verdict":"different","matchId":"","score":null,"conflict":false,"reason":"Different"}`},
		{name: "missing conflict", response: `{"verdict":"same","matchId":"task:known","score":0.99,"reason":"Same"}`},
		{name: "null conflict", response: `{"verdict":"same","matchId":"task:known","score":0.99,"conflict":null,"reason":"Same"}`},
		{name: "conflict string", response: `{"verdict":"same","matchId":"task:known","score":0.99,"conflict":"false","reason":"Same"}`},
		{name: "missing match field", response: `{"verdict":"different","score":0,"conflict":false,"reason":"Different"}`},
		{name: "same without match", response: `{"verdict":"same","matchId":"","score":0.99,"conflict":false,"reason":"Same"}`},
		{name: "same unknown match", response: `{"verdict":"same","matchId":"task:invented","score":0.99,"conflict":false,"reason":"Same"}`},
		{name: "different unknown match", response: `{"verdict":"different","matchId":"task:invented","score":0,"conflict":false,"reason":"Different"}`},
		{name: "uncertain unknown match", response: `{"verdict":"uncertain","matchId":"candidate:invented","score":0.8,"conflict":true,"reason":"Unclear"}`},
		{name: "empty reason", response: `{"verdict":"different","matchId":"","score":0,"conflict":false,"reason":"  "}`},
		{name: "missing reason", response: `{"verdict":"different","matchId":"","score":0,"conflict":false}`},
		{name: "unknown field", response: `{"verdict":"different","matchId":"","score":0,"conflict":false,"reason":"Different","confidence":1}`},
		{name: "wrong field case", response: `{"Verdict":"different","matchId":"","score":0,"conflict":false,"reason":"Different"}`},
		{name: "duplicate field", response: `{"verdict":"different","verdict":"same","matchId":"task:known","score":1,"conflict":false,"reason":"Same"}`},
		{name: "trailing value", response: `{"verdict":"different","matchId":"","score":0,"conflict":false,"reason":"Different"} {}`},
		{name: "markdown", response: "```json\n{\"verdict\":\"different\",\"matchId\":\"\",\"score\":0,\"conflict\":false,\"reason\":\"Different\"}\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := newDuplicateCheckerEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(openAIResponse(tt.response))
			}, "en")
			got, err := checker.Compare(context.Background(), model.CreateTaskRequest{Summary: "Send report"}, "I will send it", []model.TaskReference{
				{ID: "task:known", State: "open", Task: model.CreateTaskRequest{Summary: "Send report"}},
			})
			if err == nil {
				t.Fatalf("Compare accepted invalid response: %+v", got)
			}
			if !reflect.DeepEqual(got, model.TaskAssessment{}) {
				t.Errorf("Compare fabricated assessment on error: %+v", got)
			}
		})
	}
}

func TestTaskDuplicateChecker_ClientFailuresReturnNoAssessment(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		response any
	}{
		{name: "service unavailable", status: http.StatusServiceUnavailable, response: map[string]any{"error": map[string]any{"message": "Unavailable", "type": "server_error"}}},
		{name: "no choices", status: http.StatusOK, response: map[string]any{"choices": []any{}}},
		{name: "empty completion", status: http.StatusOK, response: openAIResponse("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := newDuplicateCheckerEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				json.NewEncoder(w).Encode(tt.response)
			}, "")
			got, err := checker.Compare(context.Background(), model.CreateTaskRequest{Summary: "Send report"}, "I will send it", nil)
			if err == nil || !reflect.DeepEqual(got, model.TaskAssessment{}) {
				t.Errorf("Compare = %+v, %v; want zero assessment and error", got, err)
			}
		})
	}
}

func TestTaskDuplicateChecker_CancelledContextReturnsError(t *testing.T) {
	checker := newDuplicateCheckerEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("cancelled comparison must not reach the endpoint")
		w.WriteHeader(http.StatusInternalServerError)
	}, "en")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := checker.Compare(ctx, model.CreateTaskRequest{Summary: "Send report"}, "I will send it", nil)
	if err == nil || !reflect.DeepEqual(got, model.TaskAssessment{}) {
		t.Errorf("Compare = %+v, %v; want zero assessment and cancellation error", got, err)
	}
}

func TestTaskDuplicateChecker_UnencodableInputReturnsError(t *testing.T) {
	checker := newDuplicateCheckerEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unencodable comparison input must not reach the endpoint")
		w.WriteHeader(http.StatusInternalServerError)
	}, "en")
	deadline := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	got, err := checker.Compare(context.Background(), model.CreateTaskRequest{Summary: "Send report", Deadline: &deadline}, "I will send it", nil)
	if err == nil || !reflect.DeepEqual(got, model.TaskAssessment{}) {
		t.Errorf("Compare = %+v, %v; want zero assessment and encoding error", got, err)
	}
}

func TestTaskDuplicateChecker_RejectsNilClient(t *testing.T) {
	checker, err := ai.NewTaskDuplicateChecker(nil, "en")
	if err == nil || checker != nil {
		t.Errorf("NewTaskDuplicateChecker(nil) = %v, %v; want nil and error", checker, err)
	}
}
