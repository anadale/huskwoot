package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

type taskDuplicateChecker struct {
	client       *Client
	systemPrompt string
}

type duplicateTask struct {
	ProjectID string     `json:"projectId"`
	Summary   string     `json:"summary"`
	Details   string     `json:"details"`
	Topic     string     `json:"topic"`
	Deadline  *time.Time `json:"deadline"`
}

func taskForComparison(task model.CreateTaskRequest) duplicateTask {
	return duplicateTask{
		ProjectID: task.ProjectID,
		Summary:   task.Summary,
		Details:   task.Details,
		Topic:     task.Topic,
		Deadline:  task.Deadline,
	}
}

type duplicateReference struct {
	ID         string        `json:"id"`
	Task       duplicateTask `json:"task"`
	State      string        `json:"state"`
	DisplayID  string        `json:"displayId"`
	CreatedAt  time.Time     `json:"createdAt"`
	ClosedAt   *time.Time    `json:"closedAt"`
	SourceText string        `json:"sourceText"`
}

// NewTaskDuplicateChecker creates a semantic checker for automatically extracted tasks.
func NewTaskDuplicateChecker(client *Client, lang string) (model.TaskDuplicateChecker, error) {
	if client == nil {
		return nil, fmt.Errorf("creating task duplicate checker: client is nil")
	}
	systemPrompt := loadPrompt(promptsFS, lang, "task_duplicate_checker_system")
	if systemPrompt == "" {
		return nil, fmt.Errorf("loading task duplicate checker system prompt: prompt is empty")
	}
	return &taskDuplicateChecker{client: client, systemPrompt: systemPrompt}, nil
}

func (c *taskDuplicateChecker) Compare(ctx context.Context, task model.CreateTaskRequest, sourceText string, references []model.TaskReference) (model.TaskAssessment, error) {
	input := struct {
		Candidate  duplicateTask        `json:"candidate"`
		SourceText string               `json:"sourceText"`
		References []duplicateReference `json:"references"`
	}{Candidate: taskForComparison(task), SourceText: sourceText, References: make([]duplicateReference, len(references))}
	for i, reference := range references {
		input.References[i] = duplicateReference{
			ID:         reference.ID,
			Task:       taskForComparison(reference.Task),
			State:      reference.State,
			DisplayID:  reference.DisplayID,
			CreatedAt:  reference.CreatedAt,
			ClosedAt:   reference.ClosedAt,
			SourceText: reference.SourceText,
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return model.TaskAssessment{}, fmt.Errorf("encoding task comparison input: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	response, err := c.client.Complete(ctx, c.systemPrompt, string(data))
	if err != nil {
		return model.TaskAssessment{}, fmt.Errorf("comparing task obligations: %w", err)
	}
	assessment, err := parseTaskAssessment(response)
	if err != nil {
		return model.TaskAssessment{}, fmt.Errorf("decoding task comparison response: %w", err)
	}
	if assessment.MatchID != "" {
		found := false
		for _, reference := range references {
			if reference.ID == assessment.MatchID {
				found = true
				break
			}
		}
		if !found {
			return model.TaskAssessment{}, fmt.Errorf("validating task comparison response: matchId is not a supplied reference")
		}
	}
	return assessment, nil
}

// parseTaskAssessment rejects missing, duplicate, unknown, and mistyped fields.
func parseTaskAssessment(response string) (model.TaskAssessment, error) {
	var fields struct {
		verdict  *string
		matchID  *string
		score    *float64
		conflict *bool
		reason   *string
	}
	decoder := json.NewDecoder(strings.NewReader(response))
	token, err := decoder.Token()
	if err != nil {
		return model.TaskAssessment{}, err
	}
	if token != json.Delim('{') {
		return model.TaskAssessment{}, fmt.Errorf("expected a JSON object")
	}
	var seen [5]bool
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return model.TaskAssessment{}, err
		}
		var field any
		var index int
		switch token {
		case "verdict":
			field, index = &fields.verdict, 0
		case "matchId":
			field, index = &fields.matchID, 1
		case "score":
			field, index = &fields.score, 2
		case "conflict":
			field, index = &fields.conflict, 3
		case "reason":
			field, index = &fields.reason, 4
		default:
			return model.TaskAssessment{}, fmt.Errorf("unexpected response field %v", token)
		}
		if seen[index] {
			return model.TaskAssessment{}, fmt.Errorf("duplicate response field %v", token)
		}
		seen[index] = true
		if err := decoder.Decode(field); err != nil {
			return model.TaskAssessment{}, fmt.Errorf("decoding response field %v: %w", token, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return model.TaskAssessment{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return model.TaskAssessment{}, fmt.Errorf("expected end of response after JSON object")
	}
	if fields.verdict == nil || fields.matchID == nil || fields.score == nil || fields.conflict == nil || fields.reason == nil {
		return model.TaskAssessment{}, fmt.Errorf("all response fields must be present and non-null")
	}
	switch *fields.verdict {
	case "same", "different", "uncertain":
	default:
		return model.TaskAssessment{}, fmt.Errorf("unexpected verdict")
	}
	if math.IsNaN(*fields.score) || math.IsInf(*fields.score, 0) || *fields.score < 0 || *fields.score > 1 {
		return model.TaskAssessment{}, fmt.Errorf("score must be finite and between 0 and 1")
	}
	if *fields.verdict == "same" && *fields.matchID == "" {
		return model.TaskAssessment{}, fmt.Errorf("same verdict requires matchId")
	}
	if strings.TrimSpace(*fields.reason) == "" {
		return model.TaskAssessment{}, fmt.Errorf("reason must not be empty")
	}
	return model.TaskAssessment{
		Verdict:  *fields.verdict,
		MatchID:  *fields.matchID,
		Score:    *fields.score,
		Conflict: *fields.conflict,
		Reason:   *fields.reason,
	}, nil
}
