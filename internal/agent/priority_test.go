package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anadale/huskwoot/internal/agent"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// priorityProperty returns the "priority" property of a tool's JSON schema.
func priorityProperty(t *testing.T, tool agent.Tool) map[string]any {
	t.Helper()
	props, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s: parameters have no properties", tool.Name())
	}
	prop, ok := props["priority"].(map[string]any)
	if !ok {
		t.Fatalf("%s: parameters have no priority property", tool.Name())
	}
	return prop
}

func TestPriorityParameter_Schema(t *testing.T) {
	for _, lang := range []string{"ru", "en"} {
		loc := newTestLocalizer(lang)
		tools := []agent.Tool{
			agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser(lang), loc),
			agent.NewUpdateTaskTool(&mockTaskService{}, newTestDateparser(lang), loc),
		}
		for _, tool := range tools {
			prop := priorityProperty(t, tool)
			if prop["type"] != "integer" {
				t.Errorf("%s/%s: priority type = %v, want integer", lang, tool.Name(), prop["type"])
			}
			if prop["minimum"] != 0 || prop["maximum"] != 3 {
				t.Errorf("%s/%s: priority range = %v..%v, want 0..3", lang, tool.Name(), prop["minimum"], prop["maximum"])
			}
			desc, _ := prop["description"].(string)
			if desc == "" || strings.HasPrefix(desc, "tool_") {
				t.Errorf("%s/%s: priority description is missing or untranslated: %q", lang, tool.Name(), desc)
			}
		}
		props := agent.NewCreateTaskTool(&mockTaskService{}, &mockProjectService{}, newTestDateparser(lang), loc).Parameters()
		for _, r := range props["required"].([]string) {
			if r == "priority" {
				t.Errorf("%s: priority must be optional in create_task", lang)
			}
		}
	}
}

func TestCreateTaskTool_Execute_Priority(t *testing.T) {
	tests := []struct {
		name         string
		args         string
		wantPriority model.TaskPriority
		wantMarker   string
	}{
		{name: "high priority", args: `{"summary":"починить прод","priority":3}`, wantPriority: 3, wantMarker: "!!!"},
		{name: "low priority", args: `{"summary":"починить прод","priority":1}`, wantPriority: 1, wantMarker: "!"},
		{name: "numeric string", args: `{"summary":"починить прод","priority":"2"}`, wantPriority: 2, wantMarker: "!!"},
		{name: "explicit zero", args: `{"summary":"починить прод","priority":0}`, wantPriority: 0},
		{name: "null means not set", args: `{"summary":"починить прод","priority":null}`, wantPriority: 0},
		{name: "missing means not set", args: `{"summary":"починить прод"}`, wantPriority: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &mockTaskService{}
			tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

			result, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}
			if tasks.lastCreateReq.Priority != tc.wantPriority {
				t.Errorf("CreateTaskRequest.Priority = %d, want %d", tasks.lastCreateReq.Priority, tc.wantPriority)
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(result), &got); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			if tc.wantMarker == "" {
				if _, ok := got["priority"]; ok {
					t.Errorf("priority must be omitted for priority 0, got %v", got["priority"])
				}
				if _, ok := got["priority_marker"]; ok {
					t.Errorf("priority_marker must be omitted for priority 0, got %v", got["priority_marker"])
				}
				return
			}
			if got["priority"] != float64(tc.wantPriority) {
				t.Errorf("priority = %v, want %d", got["priority"], tc.wantPriority)
			}
			if got["priority_marker"] != tc.wantMarker {
				t.Errorf("priority_marker = %v, want %q", got["priority_marker"], tc.wantMarker)
			}
			if got["summary"] != "починить прод" {
				t.Errorf("summary = %v, the marker must not be added to it", got["summary"])
			}
		})
	}
}

func TestCreateTaskTool_Execute_InvalidPriority(t *testing.T) {
	tests := []struct {
		name string
		lang string
		args string
	}{
		{name: "above range", lang: "ru", args: `{"summary":"задача","priority":4}`},
		{name: "negative", lang: "ru", args: `{"summary":"задача","priority":-1}`},
		{name: "fraction", lang: "ru", args: `{"summary":"задача","priority":1.5}`},
		{name: "word", lang: "ru", args: `{"summary":"задача","priority":"high"}`},
		{name: "boolean", lang: "en", args: `{"summary":"task","priority":true}`},
		{name: "english above range", lang: "en", args: `{"summary":"task","priority":10}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &mockTaskService{}
			loc := newTestLocalizer(tc.lang)
			tool := agent.NewCreateTaskTool(tasks, &mockProjectService{}, newTestDateparser(tc.lang), loc)

			_, err := tool.Execute(context.Background(), tc.args)
			if err == nil {
				t.Fatal("Execute() must return an error for an invalid priority")
			}
			want := huskwootI18n.Translate(loc, "agent_priority_invalid", nil)
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
			if tasks.lastCreateReq.Summary != "" {
				t.Error("CreateTask must not be called for an invalid priority")
			}
		})
	}
}

func TestUpdateTaskTool_Execute_Priority(t *testing.T) {
	tests := []struct {
		name         string
		args         string
		resultPrio   model.TaskPriority
		wantPriority model.TaskPriority
		wantMarker   string
	}{
		{name: "raise to high", args: `{"task_id":"task-uuid-30","priority":3}`, resultPrio: 3, wantPriority: 3, wantMarker: "!!!"},
		{name: "set medium", args: `{"task_id":"task-uuid-30","priority":2}`, resultPrio: 2, wantPriority: 2, wantMarker: "!!"},
		{name: "clear with zero", args: `{"task_id":"task-uuid-30","priority":0}`, resultPrio: 0, wantPriority: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &mockTaskService{
				getTaskResult: &model.Task{ID: "task-uuid-30", Number: 30, ProjectSlug: "inbox", Summary: "отчёт", Status: "open", Priority: 1},
				updateTaskResult: &model.Task{
					ID: "task-uuid-30", Number: 30, ProjectSlug: "inbox", Summary: "отчёт", Status: "open", Priority: tc.resultPrio,
				},
			}
			tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

			result, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}
			if tasks.lastUpdateReq.Priority == nil {
				t.Fatal("TaskUpdate.Priority must be set")
			}
			if *tasks.lastUpdateReq.Priority != tc.wantPriority {
				t.Errorf("TaskUpdate.Priority = %d, want %d", *tasks.lastUpdateReq.Priority, tc.wantPriority)
			}
			if tasks.lastUpdateReq.Summary != nil || tasks.lastUpdateReq.Details != nil || tasks.lastUpdateReq.Deadline != nil {
				t.Errorf("only priority must be updated, got %+v", tasks.lastUpdateReq)
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(result), &got); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			if tc.wantMarker == "" {
				if _, ok := got["priority_marker"]; ok {
					t.Errorf("priority_marker must be omitted for priority 0, got %v", got["priority_marker"])
				}
				return
			}
			if got["priority"] != float64(tc.wantPriority) || got["priority_marker"] != tc.wantMarker {
				t.Errorf("priority = %v / %v, want %d / %q", got["priority"], got["priority_marker"], tc.wantPriority, tc.wantMarker)
			}
		})
	}
}

func TestUpdateTaskTool_Execute_PriorityWithOtherFields(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-31", Number: 31, ProjectSlug: "inbox", Status: "open"},
		updateTaskResult: &model.Task{ID: "task-uuid-31", Number: 31, ProjectSlug: "inbox", Summary: "новое", Status: "open", Priority: 3},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	if _, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-31","summary":"новое","priority":3}`); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if tasks.lastUpdateReq.Summary == nil || *tasks.lastUpdateReq.Summary != "новое" {
		t.Errorf("TaskUpdate.Summary = %v, want &\"новое\"", tasks.lastUpdateReq.Summary)
	}
	if tasks.lastUpdateReq.Priority == nil || *tasks.lastUpdateReq.Priority != 3 {
		t.Errorf("TaskUpdate.Priority = %v, want &3", tasks.lastUpdateReq.Priority)
	}
}

func TestUpdateTaskTool_Execute_PriorityNotProvided_Untouched(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult:    &model.Task{ID: "task-uuid-32", Number: 32, ProjectSlug: "inbox", Status: "open", Priority: 2},
		updateTaskResult: &model.Task{ID: "task-uuid-32", Number: 32, ProjectSlug: "inbox", Summary: "новое", Status: "open", Priority: 2},
	}
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), newTestLocalizer("ru"))

	if _, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-32","summary":"новое"}`); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if tasks.lastUpdateReq.Priority != nil {
		t.Errorf("TaskUpdate.Priority = %d, want nil when not provided", *tasks.lastUpdateReq.Priority)
	}
}

func TestUpdateTaskTool_Execute_NullPriorityOnly_NoFieldsError(t *testing.T) {
	tasks := &mockTaskService{
		getTaskResult: &model.Task{ID: "task-uuid-33", Number: 33, ProjectSlug: "inbox", Status: "open"},
	}
	loc := newTestLocalizer("ru")
	tool := agent.NewUpdateTaskTool(tasks, newTestDateparser("ru"), loc)

	_, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-33","priority":null}`)
	if err == nil {
		t.Fatal("Execute() must return an error when priority is null and no other field is provided")
	}
	if want := huskwootI18n.Translate(loc, "agent_no_fields_to_update", nil); err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if tasks.lastUpdateID != "" {
		t.Error("UpdateTask must not be called")
	}
}

func TestUpdateTaskTool_Execute_InvalidPriority(t *testing.T) {
	tests := []struct {
		name string
		lang string
		args string
	}{
		{name: "above range", lang: "ru", args: `{"task_id":"task-uuid-34","priority":5}`},
		{name: "negative", lang: "ru", args: `{"task_id":"task-uuid-34","priority":-2}`},
		{name: "string word", lang: "en", args: `{"task_id":"task-uuid-34","priority":"urgent"}`},
		{name: "object", lang: "en", args: `{"task_id":"task-uuid-34","priority":{"level":3}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &mockTaskService{
				getTaskResult: &model.Task{ID: "task-uuid-34", Number: 34, ProjectSlug: "inbox", Status: "open"},
			}
			loc := newTestLocalizer(tc.lang)
			tool := agent.NewUpdateTaskTool(tasks, newTestDateparser(tc.lang), loc)

			_, err := tool.Execute(context.Background(), tc.args)
			if err == nil {
				t.Fatal("Execute() must return an error for an invalid priority")
			}
			want := huskwootI18n.Translate(loc, "agent_priority_invalid", nil)
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
			if tasks.lastUpdateID != "" {
				t.Error("UpdateTask must not be called for an invalid priority")
			}
		})
	}
}

func TestGetTaskTool_Execute_Priority(t *testing.T) {
	tests := []struct {
		name       string
		priority   model.TaskPriority
		wantMarker string
	}{
		{name: "none", priority: 0},
		{name: "low", priority: 1, wantMarker: "!"},
		{name: "medium", priority: 2, wantMarker: "!!"},
		{name: "high", priority: 3, wantMarker: "!!!"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &mockTaskService{
				getTaskResult: &model.Task{
					ID: "task-uuid-40", Number: 40, ProjectSlug: "inbox", Summary: "отчёт", Status: "open", Priority: tc.priority,
				},
			}
			tool := agent.NewGetTaskTool(tasks, newTestLocalizer("ru"))

			result, err := tool.Execute(context.Background(), `{"task_id":"task-uuid-40"}`)
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(result), &got); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			assertPriorityOutput(t, got, tc.priority, tc.wantMarker)
		})
	}
}

func TestListTasksTool_Execute_Priority(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "срочное", Status: "open", Priority: 3},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "обычное", Status: "open"},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "низкое", Status: "open", Priority: 1},
		},
	}
	tool := agent.NewListTasksTool(tasks, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tasks, want 3", len(got))
	}
	assertPriorityOutput(t, got[0], 3, "!!!")
	assertPriorityOutput(t, got[1], 0, "")
	assertPriorityOutput(t, got[2], 1, "!")
}

func TestSearchTasksTool_Execute_Priority(t *testing.T) {
	tasks := &mockTaskService{
		listTasksResult: []model.Task{
			{ID: "t1", Number: 1, ProjectSlug: "inbox", Summary: "отчёт срочный", Status: "open", Priority: 3},
			{ID: "t2", Number: 2, ProjectSlug: "inbox", Summary: "отчёт обычный", Status: "open"},
			{ID: "t3", Number: 3, ProjectSlug: "inbox", Summary: "отчёт средний", Status: "open", Priority: 2},
		},
	}
	tool := agent.NewSearchTasksTool(tasks, &mockProjectService{}, newTestDateparser("ru"), newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"query":"отчёт"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tasks, want 3", len(got))
	}
	assertPriorityOutput(t, got[0], 3, "!!!")
	assertPriorityOutput(t, got[1], 0, "")
	assertPriorityOutput(t, got[2], 2, "!!")
}

// assertPriorityOutput checks the priority keys of one task in a tool output.
func assertPriorityOutput(t *testing.T, got map[string]any, priority model.TaskPriority, marker string) {
	t.Helper()
	if marker == "" {
		if _, ok := got["priority"]; ok {
			t.Errorf("%v: priority must be omitted for priority 0, got %v", got["id"], got["priority"])
		}
		if _, ok := got["priority_marker"]; ok {
			t.Errorf("%v: priority_marker must be omitted for priority 0, got %v", got["id"], got["priority_marker"])
		}
		return
	}
	if got["priority"] != float64(priority) {
		t.Errorf("%v: priority = %v, want %d", got["id"], got["priority"], priority)
	}
	if got["priority_marker"] != marker {
		t.Errorf("%v: priority_marker = %v, want %q", got["id"], got["priority_marker"], marker)
	}
	if summary, _ := got["summary"].(string); strings.HasPrefix(summary, "!") {
		t.Errorf("%v: summary %q must not contain the marker", got["id"], summary)
	}
}

func TestAgent_SystemPromptIncludesPriorityGuidance(t *testing.T) {
	tests := []struct {
		lang  string
		wants []string
	}{
		{lang: "ru", wants: []string{"Работа с приоритетом", "«срочно»", "«важно»", "«в первую очередь»", "priority: 3", "Подними приоритет", "понизь приоритет", "без явного сигнала", "priority_marker"}},
		{lang: "en", wants: []string{"Working with priority", `"urgent"`, `"high priority"`, "priority: 3", "Raise the priority", "lower the priority", "without an explicit signal", "priority_marker"}},
	}

	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			captureClient := &captureToolsClient{response: textResponse("ок")}
			a := newTestAgent(t, captureClient, nil, agent.Config{Language: tc.lang})

			if _, err := a.Handle(context.Background(), model.Message{Kind: model.MessageKindDM, Text: "тест"}); err != nil {
				t.Fatalf("Handle() returned error: %v", err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(captureClient.capturedSysContent, want) {
					t.Errorf("system prompt (%s) must contain %q", tc.lang, want)
				}
			}
		})
	}
}
