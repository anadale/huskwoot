package push

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pushproto"
)

// Templates builds push notifications for events following the decision table from the spec.
type Templates struct {
	now func() time.Time
}

// NewTemplates creates a Templates instance.
// timezone is an IANA timezone name (e.g. "Europe/Moscow") used for deadline formatting.
// Empty or unknown value → time.UTC.
func NewTemplates(timezone string) *Templates {
	loc := time.UTC
	if timezone != "" {
		if l, err := time.LoadLocation(timezone); err == nil {
			loc = l
		}
	}
	return &Templates{now: func() time.Time { return time.Now().In(loc) }}
}

// taskSnapshot is the local schema for the task payload inside an event.
type taskSnapshot struct {
	ID          string     `json:"id"`
	Number      int        `json:"number"`
	ProjectID   string     `json:"project_id"`
	ProjectSlug string     `json:"project_slug,omitempty"`
	Summary     string     `json:"summary"`
	Deadline    *time.Time `json:"deadline,omitempty"`
}

// taskEventPayload wraps the payload for task_* events.
type taskEventPayload struct {
	Task          taskSnapshot `json:"task"`
	ChangedFields []string     `json:"changedFields,omitempty"`
}

// reminderSummaryPayload is the payload for reminder_summary events.
type reminderSummaryPayload struct {
	Slot       string `json:"slot"`
	TodayCount int    `json:"todayCount"`
}

// Resolve parses the event and returns a PushRequest if the event is push-worthy.
// ok=false means the event should not be sent as a push notification.
func (t *Templates) Resolve(_ context.Context, ev *model.Event) (*pushproto.PushRequest, bool, error) {
	switch ev.Kind {
	case model.EventTaskCreated:
		return t.resolveTaskCreated(ev)
	case model.EventTaskUpdated:
		return t.resolveTaskUpdated(ev)
	case model.EventReminderSummary:
		return t.resolveReminderSummary(ev)
	default:
		return nil, false, nil
	}
}

func (t *Templates) resolveTaskCreated(ev *model.Event) (*pushproto.PushRequest, bool, error) {
	var p taskEventPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return nil, false, fmt.Errorf("parsing task_created payload: %w", err)
	}
	task := p.Task
	displayID := buildDisplayID(task.ProjectSlug, task.Number)

	body := displayID + ": " + task.Summary
	if task.Deadline != nil {
		loc := t.now().Location()
		body += " (до " + task.Deadline.In(loc).Format("02.01 15:04") + ")"
	}

	req := &pushproto.PushRequest{
		Priority:    "high",
		CollapseKey: "tasks",
		Notification: pushproto.Notification{
			Title: "Новая задача",
			Body:  body,
		},
		Data: pushproto.Data{
			Kind:      string(ev.Kind),
			EventSeq:  ev.Seq,
			TaskID:    task.ID,
			DisplayID: displayID,
		},
	}
	return req, true, nil
}

func (t *Templates) resolveTaskUpdated(ev *model.Event) (*pushproto.PushRequest, bool, error) {
	var p taskEventPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return nil, false, fmt.Errorf("parsing task_updated payload: %w", err)
	}

	if !containsField(p.ChangedFields, "summary") && !containsField(p.ChangedFields, "deadline") {
		return nil, false, nil
	}

	task := p.Task
	displayID := buildDisplayID(task.ProjectSlug, task.Number)
	body := displayID + ": " + task.Summary

	req := &pushproto.PushRequest{
		Priority:    "normal",
		CollapseKey: "tasks",
		Notification: pushproto.Notification{
			Title: "Задача обновлена",
			Body:  body,
		},
		Data: pushproto.Data{
			Kind:      string(ev.Kind),
			EventSeq:  ev.Seq,
			TaskID:    task.ID,
			DisplayID: displayID,
		},
	}
	return req, true, nil
}

func (t *Templates) resolveReminderSummary(ev *model.Event) (*pushproto.PushRequest, bool, error) {
	var p reminderSummaryPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return nil, false, fmt.Errorf("parsing reminder_summary payload: %w", err)
	}

	body := strconv.Itoa(p.TodayCount) + " задач сегодня"

	req := &pushproto.PushRequest{
		Priority:    "normal",
		CollapseKey: "reminders",
		Notification: pushproto.Notification{
			Title: "Утренняя сводка",
			Body:  body,
		},
		Data: pushproto.Data{
			Kind:     string(ev.Kind),
			EventSeq: ev.Seq,
		},
	}
	return req, true, nil
}

func buildDisplayID(slug string, number int) string {
	return slug + "#" + strconv.Itoa(number)
}

func containsField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}
