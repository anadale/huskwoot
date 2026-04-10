package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apns2 "github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"

	"github.com/anadale/huskwoot/internal/pushproto"
)

// APNsConfig holds the configuration for the APNs adapter.
type APNsConfig struct {
	KeyFile    string
	KeyID      string
	TeamID     string
	BundleID   string
	Production bool
}

// apns2Pusher is the interface used to mock apns2.Client in tests.
type apns2Pusher interface {
	PushWithContext(ctx apns2.Context, n *apns2.Notification) (*apns2.Response, error)
}

// APNsSender sends notifications via Apple Push Notification service.
type APNsSender struct {
	client   apns2Pusher
	bundleID string
}

// ErrInvalidToken is returned when the device token is invalid or expired.
var ErrInvalidToken = errors.New("invalid device token")

// TemporaryError is returned for transient failures from the notification provider.
// RetryAfter is the recommended number of seconds to wait before retrying.
type TemporaryError struct {
	RetryAfter int
	Cause      error
}

func (e *TemporaryError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "transient send error"
}

func (e *TemporaryError) Unwrap() error { return e.Cause }

// NewAPNsSender creates an APNsSender from an APNs .p8 key file.
func NewAPNsSender(cfg APNsConfig) (*APNsSender, error) {
	authKey, err := token.AuthKeyFromFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("apns: loading key %s: %w", cfg.KeyFile, err)
	}
	t := &token.Token{
		AuthKey: authKey,
		KeyID:   cfg.KeyID,
		TeamID:  cfg.TeamID,
	}
	client := apns2.NewTokenClient(t)
	if cfg.Production {
		client = client.Production()
	} else {
		client = client.Development()
	}
	return &APNsSender{client: client, bundleID: cfg.BundleID}, nil
}

// Send delivers a push notification to an Apple device via APNs.
// Returns ErrInvalidToken for an invalid token, *TemporaryError for transient failures.
func (s *APNsSender) Send(ctx context.Context, deviceToken string, req pushproto.PushRequest) error {
	priority := apns2.PriorityLow
	if req.Priority == "high" {
		priority = apns2.PriorityHigh
	}

	payloadBytes, err := json.Marshal(buildAPNsPayload(req))
	if err != nil {
		return fmt.Errorf("apns: serializing payload: %w", err)
	}

	n := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       s.bundleID,
		CollapseID:  req.CollapseKey,
		Priority:    priority,
		PushType:    apns2.PushTypeAlert,
		Payload:     payloadBytes,
	}

	resp, err := s.client.PushWithContext(ctx, n)
	if err != nil {
		return &TemporaryError{RetryAfter: 60, Cause: err}
	}
	if resp == nil {
		return &TemporaryError{RetryAfter: 60, Cause: errors.New("apns: empty response")}
	}

	if resp.StatusCode == apns2.StatusSent {
		return nil
	}
	return mapAPNsError(resp)
}

type apnsAPS struct {
	Alert apnsAlert `json:"alert"`
	Badge *int      `json:"badge,omitempty"`
}

type apnsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type apnsPayloadBody struct {
	APS       apnsAPS `json:"aps"`
	Kind      string  `json:"kind,omitempty"`
	EventSeq  int64   `json:"eventSeq,omitempty"`
	TaskID    string  `json:"taskId,omitempty"`
	DisplayID string  `json:"displayId,omitempty"`
}

func buildAPNsPayload(req pushproto.PushRequest) apnsPayloadBody {
	return apnsPayloadBody{
		APS: apnsAPS{
			Alert: apnsAlert{
				Title: req.Notification.Title,
				Body:  req.Notification.Body,
			},
			Badge: req.Notification.Badge,
		},
		Kind:      req.Data.Kind,
		EventSeq:  req.Data.EventSeq,
		TaskID:    req.Data.TaskID,
		DisplayID: req.Data.DisplayID,
	}
}

func mapAPNsError(resp *apns2.Response) error {
	switch resp.Reason {
	case apns2.ReasonBadDeviceToken,
		apns2.ReasonUnregistered,
		apns2.ReasonDeviceTokenNotForTopic,
		apns2.ReasonBadTopic,
		apns2.ReasonExpiredToken,
		apns2.ReasonMissingDeviceToken:
		return ErrInvalidToken
	default:
		return &TemporaryError{
			RetryAfter: 30,
			Cause:      fmt.Errorf("APNs %d: %s", resp.StatusCode, resp.Reason),
		}
	}
}
