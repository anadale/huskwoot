package relay

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/anadale/huskwoot/internal/pushproto"
)

type registrationStore interface {
	UpsertRegistration(ctx context.Context, instanceID, deviceID string, reg RegistrationFields) error
	DeleteRegistration(ctx context.Context, instanceID, deviceID string) error
}

// Registrar handles PUT/DELETE /v1/registrations/{deviceID}.
type Registrar struct {
	store registrationStore
}

// NewRegistrar creates a new Registrar.
func NewRegistrar(store registrationStore) *Registrar {
	return &Registrar{store: store}
}

// Put handles PUT /v1/registrations/{deviceID}: creates or updates a registration.
func (rg *Registrar) Put(w http.ResponseWriter, r *http.Request) {
	instanceID := InstanceIDFromContext(r.Context())
	deviceID := chi.URLParam(r, "deviceID")

	var req pushproto.RegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "bad_json", "invalid JSON in request body")
		return
	}

	if req.APNSToken == nil && req.FCMToken == nil {
		writeAuthError(w, http.StatusUnprocessableEntity, "empty_tokens", "at least one of apnsToken or fcmToken must be provided")
		return
	}

	fields := RegistrationFields{
		APNSToken: req.APNSToken,
		FCMToken:  req.FCMToken,
		Platform:  req.Platform,
	}
	if err := rg.store.UpsertRegistration(r.Context(), instanceID, deviceID, fields); err != nil {
		writeAuthError(w, http.StatusInternalServerError, "store_error", "error saving registration")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Delete handles DELETE /v1/registrations/{deviceID}: removes a registration (idempotent).
func (rg *Registrar) Delete(w http.ResponseWriter, r *http.Request) {
	instanceID := InstanceIDFromContext(r.Context())
	deviceID := chi.URLParam(r, "deviceID")

	if err := rg.store.DeleteRegistration(r.Context(), instanceID, deviceID); err != nil {
		writeAuthError(w, http.StatusInternalServerError, "store_error", "error deleting registration")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
