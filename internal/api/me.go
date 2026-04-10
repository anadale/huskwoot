package api

import (
	"encoding/json"
	"net/http"
)

// meResponse is the response body for GET /v1/me. Fields reflect the OpenAPI contract.
type meResponse struct {
	DeviceID       string `json:"deviceId"`
	OwnerName      string `json:"ownerName"`
	TelegramUserID int64  `json:"telegramUserId,omitempty"`
	Version        string `json:"version"`
}

// getMe returns the current device identity and instance metadata.
// Authentication is provided by AuthMiddleware: device_id is always in context.
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	resp := meResponse{
		DeviceID:       DeviceIDFromContext(r.Context()),
		OwnerName:      s.cfg.Owner.UserName,
		TelegramUserID: s.cfg.Owner.TelegramUserID,
		Version:        Version,
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeJSON serialises a value to JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
