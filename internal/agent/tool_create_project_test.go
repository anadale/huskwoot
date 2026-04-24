package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

func TestCreateProjectToolWithAliases(t *testing.T) {
	wantAliases := []string{"букинист", "books"}
	projects := &mockProjectService{
		createResult: &model.Project{
			ID:      "proj-1",
			Slug:    "bukinist",
			Name:    "Букинист",
			Aliases: wantAliases,
		},
	}
	tool := agent.NewCreateProjectTool(projects, newTestLocalizer("ru"))

	result, err := tool.Execute(context.Background(), `{"name":"Букинист","aliases":["букинист","books"]}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verify the service received the aliases.
	if len(projects.lastCreateReq.Aliases) != 2 {
		t.Fatalf("CreateProject called with %d aliases, want 2", len(projects.lastCreateReq.Aliases))
	}
	if projects.lastCreateReq.Aliases[0] != "букинист" || projects.lastCreateReq.Aliases[1] != "books" {
		t.Errorf("aliases sent to service = %v, want [букинист books]", projects.lastCreateReq.Aliases)
	}

	// Verify the response JSON contains aliases.
	var got map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v; raw: %s", err, result)
	}
	rawAliases, ok := got["aliases"]
	if !ok {
		t.Fatal("aliases field must be present in response")
	}
	aliases, ok := rawAliases.([]any)
	if !ok {
		t.Fatalf("aliases must be an array, got %T", rawAliases)
	}
	if len(aliases) != len(wantAliases) {
		t.Fatalf("aliases length = %d, want %d", len(aliases), len(wantAliases))
	}
	for i, a := range wantAliases {
		if aliases[i] != a {
			t.Errorf("aliases[%d] = %v, want %q", i, aliases[i], a)
		}
	}
}

func TestCreateProjectToolValidatesAliases(t *testing.T) {
	projects := &mockProjectService{
		createErr: usecase.ErrAliasInvalid,
	}
	tool := agent.NewCreateProjectTool(projects, newTestLocalizer("ru"))

	_, err := tool.Execute(context.Background(), `{"name":"Тест","aliases":["invalid alias with spaces"]}`)
	if err == nil {
		t.Fatal("Execute() must return error when alias is invalid")
	}

	if projects.lastCreateReq.Name != "Тест" {
		t.Errorf("CreateProject was not called with correct name, got %q", projects.lastCreateReq.Name)
	}
}
