package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

// pusherStore is the subset of Store required by the push handler.
type pusherStore interface {
	GetRegistration(ctx context.Context, instanceID, deviceID string) (*Registration, error)
	DeleteRegistration(ctx context.Context, instanceID, deviceID string) error
	MarkUsed(ctx context.Context, instanceID, deviceID string, at time.Time) error
}

// PushSender is the common interface for sending notifications via a specific provider.
// Implemented by *APNsSender and *FCMSender.
type PushSender interface {
	Send(ctx context.Context, deviceToken string, req pushproto.PushRequest) error
}

// Pusher handles POST /v1/push: routes the request to APNs or FCM.
type Pusher struct {
	store pusherStore
	apns  PushSender
	fcm   PushSender
	clock func() time.Time
}

// NewPusher creates a Pusher. Uses time.Now if clock is nil.
func NewPusher(store pusherStore, apns, fcm PushSender, clock func() time.Time) *Pusher {
	if clock == nil {
		clock = time.Now
	}
	return &Pusher{store: store, apns: apns, fcm: fcm, clock: clock}
}

// Push handles POST /v1/push.
func (p *Pusher) Push(w http.ResponseWriter, r *http.Request) {
	instanceID := InstanceIDFromContext(r.Context())

	var req pushproto.PushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writePushResponse(w, http.StatusBadRequest, pushproto.PushResponse{
			Status:  pushproto.StatusBadPayload,
			Message: "invalid JSON in request body",
		})
		return
	}

	reg, err := p.store.GetRegistration(r.Context(), instanceID, req.DeviceID)
	if err != nil {
		writePushResponse(w, http.StatusInternalServerError, pushproto.PushResponse{
			Status:  pushproto.StatusUpstreamError,
			Message: "error retrieving registration",
		})
		return
	}
	if reg == nil {
		writePushResponse(w, http.StatusNotFound, pushproto.PushResponse{
			Status: pushproto.StatusInvalidToken,
		})
		return
	}

	sender, token, err := p.selectSender(reg)
	if err != nil {
		writePushResponse(w, http.StatusBadRequest, pushproto.PushResponse{
			Status:  pushproto.StatusBadPayload,
			Message: err.Error(),
		})
		return
	}

	sendErr := sender.Send(r.Context(), token, req)
	if sendErr == nil {
		_ = p.store.MarkUsed(r.Context(), instanceID, req.DeviceID, p.clock())
		writePushResponse(w, http.StatusOK, pushproto.PushResponse{Status: pushproto.StatusSent})
		return
	}

	if errors.Is(sendErr, ErrInvalidToken) {
		_ = p.store.DeleteRegistration(r.Context(), instanceID, req.DeviceID)
		writePushResponse(w, http.StatusOK, pushproto.PushResponse{Status: pushproto.StatusInvalidToken})
		return
	}

	var tempErr *TemporaryError
	if errors.As(sendErr, &tempErr) {
		writePushResponse(w, http.StatusOK, pushproto.PushResponse{
			Status:     pushproto.StatusUpstreamError,
			RetryAfter: tempErr.RetryAfter,
			Message:    tempErr.Error(),
		})
		return
	}

	writePushResponse(w, http.StatusOK, pushproto.PushResponse{
		Status:  pushproto.StatusBadPayload,
		Message: sendErr.Error(),
	})
}

// selectSender picks the provider and device token based on the registration.
// APNs takes priority for iOS/macOS platforms when the apns adapter is present.
func (p *Pusher) selectSender(reg *Registration) (PushSender, string, error) {
	if reg.APNSToken != nil && isAPNsPlatform(reg.Platform) && p.apns != nil {
		return p.apns, *reg.APNSToken, nil
	}
	if reg.FCMToken != nil && p.fcm != nil {
		return p.fcm, *reg.FCMToken, nil
	}
	return nil, "", errors.New("no tokens available to send notification")
}

func isAPNsPlatform(platform string) bool {
	return platform == "ios" || platform == "macos"
}

func writePushResponse(w http.ResponseWriter, status int, resp pushproto.PushResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
