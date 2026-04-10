package relay

import (
	"context"
	"fmt"
	"strconv"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"

	"github.com/anadale/huskwoot/internal/pushproto"
)

// FCMConfig holds the configuration for the FCM adapter.
type FCMConfig struct {
	ServiceAccountFile string
}

// fcmSendClient is the interface used to mock the FCM client in tests.
// Implementations must map Firebase errors to ErrInvalidToken / *TemporaryError.
type fcmSendClient interface {
	Send(ctx context.Context, msg *messaging.Message) (string, error)
}

// FCMSender sends notifications via Firebase Cloud Messaging.
type FCMSender struct {
	client fcmSendClient
}

// NewFCMSender creates an FCMSender from a Firebase service account JSON file.
func NewFCMSender(cfg FCMConfig) (*FCMSender, error) {
	opt := option.WithCredentialsFile(cfg.ServiceAccountFile)
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		return nil, fmt.Errorf("fcm: initializing Firebase: %w", err)
	}
	client, err := app.Messaging(context.Background())
	if err != nil {
		return nil, fmt.Errorf("fcm: getting messaging client: %w", err)
	}
	return &FCMSender{client: &realFCMClient{client: client}}, nil
}

// Send delivers a push notification to an Android device via FCM.
// Returns ErrInvalidToken for an invalid token, *TemporaryError for transient failures.
func (s *FCMSender) Send(ctx context.Context, deviceToken string, req pushproto.PushRequest) error {
	msg := buildFCMMessage(deviceToken, req)
	_, err := s.client.Send(ctx, msg)
	return err
}

func buildFCMMessage(deviceToken string, req pushproto.PushRequest) *messaging.Message {
	data := map[string]string{
		"kind":     req.Data.Kind,
		"eventSeq": strconv.FormatInt(req.Data.EventSeq, 10),
	}
	if req.Data.TaskID != "" {
		data["taskId"] = req.Data.TaskID
	}
	if req.Data.DisplayID != "" {
		data["displayId"] = req.Data.DisplayID
	}

	return &messaging.Message{
		Token: deviceToken,
		Notification: &messaging.Notification{
			Title: req.Notification.Title,
			Body:  req.Notification.Body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority:    fcmAndroidPriority(req.Priority),
			CollapseKey: req.CollapseKey,
		},
	}
}

func fcmAndroidPriority(p string) string {
	if p == "high" {
		return "high"
	}
	return "normal"
}

// realFCMClient wraps *messaging.Client and maps Firebase errors.
type realFCMClient struct {
	client *messaging.Client
}

func (r *realFCMClient) Send(ctx context.Context, msg *messaging.Message) (string, error) {
	id, err := r.client.Send(ctx, msg)
	if err == nil {
		return id, nil
	}
	if messaging.IsRegistrationTokenNotRegistered(err) {
		return "", ErrInvalidToken
	}
	if messaging.IsUnavailable(err) || messaging.IsInternal(err) {
		return "", &TemporaryError{RetryAfter: 30, Cause: err}
	}
	return "", fmt.Errorf("fcm: %w", err)
}
