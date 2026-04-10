package relay

import (
	"context"
	"errors"
	"testing"

	"firebase.google.com/go/v4/messaging"

	"github.com/anadale/huskwoot/internal/pushproto"
)

type mockFCMClient struct {
	id      string
	err     error
	lastMsg *messaging.Message
}

func (m *mockFCMClient) Send(_ context.Context, msg *messaging.Message) (string, error) {
	m.lastMsg = msg
	return m.id, m.err
}

func TestFCMSender_BuildsMessage(t *testing.T) {
	cases := []struct {
		name        string
		priority    string
		wantAndroid string
	}{
		{"high", "high", "high"},
		{"normal", "normal", "normal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockFCMClient{id: "projects/x/messages/1"}
			sender := &FCMSender{client: mock}

			req := pushproto.PushRequest{
				Priority:    tc.priority,
				CollapseKey: "tasks",
				Notification: pushproto.Notification{
					Title: "Новая задача",
					Body:  "inbox#1: купить молоко",
				},
				Data: pushproto.Data{
					Kind:      "task_created",
					EventSeq:  7,
					TaskID:    "task-uuid",
					DisplayID: "inbox#1",
				},
			}

			if err := sender.Send(context.Background(), "device-token", req); err != nil {
				t.Fatalf("Send: %v", err)
			}

			msg := mock.lastMsg
			if msg == nil {
				t.Fatal("message was not sent")
			}
			if msg.Token != "device-token" {
				t.Errorf("Token = %q", msg.Token)
			}
			if msg.Notification == nil {
				t.Fatal("Notification == nil")
			}
			if msg.Notification.Title != "Новая задача" {
				t.Errorf("notification.title = %q", msg.Notification.Title)
			}
			if msg.Notification.Body != "inbox#1: купить молоко" {
				t.Errorf("notification.body = %q", msg.Notification.Body)
			}
			if msg.Android == nil {
				t.Fatal("Android config == nil")
			}
			if msg.Android.Priority != tc.wantAndroid {
				t.Errorf("android.priority = %q, want %q", msg.Android.Priority, tc.wantAndroid)
			}
			if msg.Android.CollapseKey != "tasks" {
				t.Errorf("android.collapseKey = %q", msg.Android.CollapseKey)
			}
			if msg.Data["kind"] != "task_created" {
				t.Errorf("data.kind = %q", msg.Data["kind"])
			}
			if msg.Data["eventSeq"] != "7" {
				t.Errorf("data.eventSeq = %q", msg.Data["eventSeq"])
			}
			if msg.Data["taskId"] != "task-uuid" {
				t.Errorf("data.taskId = %q", msg.Data["taskId"])
			}
			if msg.Data["displayId"] != "inbox#1" {
				t.Errorf("data.displayId = %q", msg.Data["displayId"])
			}
		})
	}
}

func TestFCMSender_InvalidToken_ReturnsErrInvalidToken(t *testing.T) {
	mock := &mockFCMClient{err: ErrInvalidToken}
	sender := &FCMSender{client: mock}

	err := sender.Send(context.Background(), "bad-token", pushproto.PushRequest{
		Notification: pushproto.Notification{Title: "t", Body: "b"},
	})
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("want ErrInvalidToken, got %T: %v", err, err)
	}
}

func TestFCMSender_ServerError_ReturnsErrTemporary(t *testing.T) {
	mock := &mockFCMClient{err: &TemporaryError{RetryAfter: 30, Cause: errors.New("UNAVAILABLE")}}
	sender := &FCMSender{client: mock}

	err := sender.Send(context.Background(), "device-token", pushproto.PushRequest{
		Notification: pushproto.Notification{Title: "t", Body: "b"},
	})

	var tempErr *TemporaryError
	if !errors.As(err, &tempErr) {
		t.Errorf("want *TemporaryError, got %T: %v", err, err)
	}
	if tempErr.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30", tempErr.RetryAfter)
	}
}
