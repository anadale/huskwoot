package relay

import (
	"context"
	"encoding/json"
	"net/http"
)

type healthPinger interface {
	Ping(ctx context.Context) error
}

type healthHandler struct {
	pinger healthPinger
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := h.pinger.Ping(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
