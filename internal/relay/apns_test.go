package relay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	apns2 "github.com/sideshow/apns2"

	"github.com/anadale/huskwoot/internal/pushproto"
)

type mockAPNs2Client struct {
	resp             *apns2.Response
	err              error
	lastNotification *apns2.Notification
}

func (m *mockAPNs2Client) PushWithContext(ctx apns2.Context, n *apns2.Notification) (*apns2.Response, error) {
	m.lastNotification = n
	return m.resp, m.err
}

func TestAPNsSender_BuildsPayload_HighPriority(t *testing.T) {
	badge := 3
	cases := []struct {
		name     string
		priority string
		wantPrio int
	}{
		{"high", "high", apns2.PriorityHigh},
		{"normal", "normal", apns2.PriorityLow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockAPNs2Client{resp: &apns2.Response{StatusCode: apns2.StatusSent}}
			sender := &APNsSender{client: mock, bundleID: "com.example.app"}

			req := pushproto.PushRequest{
				Priority:    tc.priority,
				CollapseKey: "tasks",
				Notification: pushproto.Notification{
					Title: "Заголовок",
					Body:  "Текст",
					Badge: &badge,
				},
				Data: pushproto.Data{
					Kind:      "task_created",
					EventSeq:  42,
					TaskID:    "task-uuid",
					DisplayID: "inbox#1",
				},
			}

			if err := sender.Send(context.Background(), "device-token", req); err != nil {
				t.Fatalf("Send: %v", err)
			}

			n := mock.lastNotification
			if n == nil {
				t.Fatal("notification was not sent")
			}
			if n.Topic != "com.example.app" {
				t.Errorf("Topic = %q, want %q", n.Topic, "com.example.app")
			}
			if n.Priority != tc.wantPrio {
				t.Errorf("Priority = %d, want %d", n.Priority, tc.wantPrio)
			}
			if n.CollapseID != "tasks" {
				t.Errorf("CollapseID = %q", n.CollapseID)
			}

			raw, ok := n.Payload.([]byte)
			if !ok {
				t.Fatalf("Payload type %T, want []byte", n.Payload)
			}
			var pl apnsPayloadBody
			if err := json.Unmarshal(raw, &pl); err != nil {
				t.Fatalf("payload unmarshal: %v", err)
			}
			if pl.APS.Alert.Title != "Заголовок" {
				t.Errorf("aps.alert.title = %q", pl.APS.Alert.Title)
			}
			if pl.APS.Alert.Body != "Текст" {
				t.Errorf("aps.alert.body = %q", pl.APS.Alert.Body)
			}
			if pl.APS.Badge == nil || *pl.APS.Badge != 3 {
				t.Errorf("aps.badge = %v", pl.APS.Badge)
			}
			if pl.Kind != "task_created" {
				t.Errorf("kind = %q", pl.Kind)
			}
			if pl.EventSeq != 42 {
				t.Errorf("eventSeq = %d", pl.EventSeq)
			}
			if pl.TaskID != "task-uuid" {
				t.Errorf("taskId = %q", pl.TaskID)
			}
			if pl.DisplayID != "inbox#1" {
				t.Errorf("displayId = %q", pl.DisplayID)
			}
		})
	}
}

func TestAPNsSender_InvalidToken_ReturnsErrInvalidToken(t *testing.T) {
	reasons := []string{
		apns2.ReasonBadDeviceToken,
		apns2.ReasonUnregistered,
		apns2.ReasonDeviceTokenNotForTopic,
		apns2.ReasonBadTopic,
	}

	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			mock := &mockAPNs2Client{
				resp: &apns2.Response{StatusCode: 400, Reason: reason},
			}
			sender := &APNsSender{client: mock, bundleID: "com.example.app"}

			err := sender.Send(context.Background(), "bad-token", pushproto.PushRequest{
				Notification: pushproto.Notification{Title: "t", Body: "b"},
			})
			if !errors.Is(err, ErrInvalidToken) {
				t.Errorf("want ErrInvalidToken, got %T: %v", err, err)
			}
		})
	}
}

func TestAPNsSender_ServerError_ReturnsErrTemporary(t *testing.T) {
	cases := []struct {
		name string
		resp *apns2.Response
		err  error
	}{
		{
			"5xx InternalServerError",
			&apns2.Response{StatusCode: 500, Reason: apns2.ReasonInternalServerError},
			nil,
		},
		{
			"network error",
			nil,
			errors.New("connection refused"),
		},
		{
			"TooManyRequests",
			&apns2.Response{StatusCode: 429, Reason: apns2.ReasonTooManyRequests},
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockAPNs2Client{resp: tc.resp, err: tc.err}
			sender := &APNsSender{client: mock, bundleID: "com.example.app"}

			err := sender.Send(context.Background(), "device-token", pushproto.PushRequest{
				Notification: pushproto.Notification{Title: "t", Body: "b"},
			})

			var tempErr *TemporaryError
			if !errors.As(err, &tempErr) {
				t.Errorf("want *TemporaryError, got %T: %v", err, err)
			}
		})
	}
}
