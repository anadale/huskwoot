package model_test

import (
	"testing"

	"github.com/anadale/huskwoot/internal/model"
)

func TestTaskDisplayID(t *testing.T) {
	task := model.Task{Number: 42, ProjectSlug: "inbox"}
	if got := task.DisplayID(); got != "inbox#42" {
		t.Fatalf("DisplayID = %q, want %q", got, "inbox#42")
	}
}
