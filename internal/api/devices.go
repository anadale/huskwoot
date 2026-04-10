package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/pushproto"
)

// deviceDTO is the public device snapshot in JSON responses. token_hash is
// intentionally not exposed: the client must not have access to the stored
// bearer-token hash.
type deviceDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Platform   string     `json:"platform"`
	APNSToken  *string    `json:"apnsToken,omitempty"`
	FCMToken   *string    `json:"fcmToken,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

func toDeviceDTO(d *model.Device) deviceDTO {
	return deviceDTO{
		ID:         d.ID,
		Name:       d.Name,
		Platform:   d.Platform,
		APNSToken:  d.APNSToken,
		FCMToken:   d.FCMToken,
		CreatedAt:  d.CreatedAt,
		LastSeenAt: d.LastSeenAt,
		RevokedAt:  d.RevokedAt,
	}
}

type devicesHandler struct {
	store  model.DeviceStore
	relay  push.RelayClient
	logger *slog.Logger
}

func (h *devicesHandler) list(w http.ResponseWriter, r *http.Request) {
	devs, err := h.store.List(r.Context())
	if err != nil {
		h.logError(r.Context(), "list devices", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve devices")
		return
	}
	out := make([]deviceDTO, 0, len(devs))
	for i := range devs {
		out = append(out, toDeviceDTO(&devs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// patchMe updates the push tokens (APNs/FCM) for the current device. Semantics:
// field absent — no change; null — clear; string — set new value.
func (h *devicesHandler) patchMe(w http.ResponseWriter, r *http.Request) {
	deviceID := DeviceIDFromContext(r.Context())
	if deviceID == "" {
		WriteError(w, http.StatusUnauthorized, ErrorCodeUnauthorized, "device_id missing from context")
		return
	}

	raw, err := decodeRawMap(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}
	if len(raw) == 0 {
		WriteError(w, http.StatusUnprocessableEntity, ErrorCodeUnprocessable, "no fields provided")
		return
	}
	apns, fcm, err := buildPushTokensUpdate(raw)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, err.Error())
		return
	}

	current, err := h.store.Get(r.Context(), deviceID)
	if err != nil {
		h.logError(r.Context(), "lookup device", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve device")
		return
	}
	if current == nil {
		WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "device not found")
		return
	}

	newAPNS := current.APNSToken
	newFCM := current.FCMToken
	if apns.set {
		newAPNS = apns.value
	}
	if fcm.set {
		newFCM = fcm.value
	}

	if err := h.store.UpdatePushTokens(r.Context(), deviceID, newAPNS, newFCM); err != nil {
		h.logError(r.Context(), "update push tokens", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to update push tokens")
		return
	}

	updated, err := h.store.Get(r.Context(), deviceID)
	if err != nil {
		h.logError(r.Context(), "lookup device after update", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve device")
		return
	}
	if updated == nil {
		WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "device not found")
		return
	}

	if updated.APNSToken != nil || updated.FCMToken != nil {
		reg := pushproto.RegistrationRequest{
			APNSToken: updated.APNSToken,
			FCMToken:  updated.FCMToken,
			Platform:  updated.Platform,
		}
		if err := h.relay.UpsertRegistration(r.Context(), deviceID, reg); err != nil {
			h.logWarn(r.Context(), "upsert relay registration", err)
		}
	} else {
		if err := h.relay.DeleteRegistration(r.Context(), deviceID); err != nil {
			h.logWarn(r.Context(), "delete relay registration after token clear", err)
		}
	}

	writeJSON(w, http.StatusOK, toDeviceDTO(updated))
}

func (h *devicesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	device, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.logError(r.Context(), "lookup device", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to retrieve device")
		return
	}
	if device == nil {
		WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "device not found")
		return
	}
	if err := h.store.Revoke(r.Context(), id); err != nil {
		h.logError(r.Context(), "revoke device", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to revoke device")
		return
	}

	if err := h.relay.DeleteRegistration(r.Context(), id); err != nil {
		h.logWarnTarget(r.Context(), "delete relay registration", id, err)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *devicesHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/devices: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}

func (h *devicesHandler) logWarn(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelWarn, "api/devices: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}

func (h *devicesHandler) logWarnTarget(ctx context.Context, op, targetID string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelWarn, "api/devices: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("caller_device_id", DeviceIDFromContext(ctx)),
		slog.String("target_device_id", targetID),
		slog.String("error", err.Error()),
	)
}

// pushTokenPatch represents a single PATCH payload field: set=false — field absent;
// set=true, value=nil — explicit clear; set=true, value!=nil — new string value.
type pushTokenPatch struct {
	set   bool
	value *string
}

var allowedDeviceUpdateFields = map[string]bool{
	"apnsToken": true,
	"fcmToken":  true,
}

func buildPushTokensUpdate(raw map[string]json.RawMessage) (apns, fcm pushTokenPatch, err error) {
	for k := range raw {
		if !allowedDeviceUpdateFields[k] {
			return pushTokenPatch{}, pushTokenPatch{}, errors.New("unknown field: " + k)
		}
	}
	if v, ok := raw["apnsToken"]; ok {
		patch, parseErr := parsePushTokenField("apnsToken", v)
		if parseErr != nil {
			return pushTokenPatch{}, pushTokenPatch{}, parseErr
		}
		apns = patch
	}
	if v, ok := raw["fcmToken"]; ok {
		patch, parseErr := parsePushTokenField("fcmToken", v)
		if parseErr != nil {
			return pushTokenPatch{}, pushTokenPatch{}, parseErr
		}
		fcm = patch
	}
	return apns, fcm, nil
}

func parsePushTokenField(name string, raw json.RawMessage) (pushTokenPatch, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return pushTokenPatch{set: true, value: nil}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return pushTokenPatch{}, errors.New(name + " must be a string or null")
	}
	if s == "" {
		return pushTokenPatch{}, errors.New(name + " cannot be an empty string")
	}
	return pushTokenPatch{set: true, value: &s}, nil
}
