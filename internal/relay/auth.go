package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

const maxBodySize = 1 << 20 // 1 MiB

type contextKey int

const instanceIDKey contextKey = 1

// InstanceLoader provides the secret for an instance by its ID.
// Returns nil if the instance is unknown or disabled.
type InstanceLoader interface {
	Secret(id string) []byte
}

// InstanceIDFromContext returns the instance ID from the request context.
func InstanceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(instanceIDKey).(string)
	return id
}

// HMACMiddleware validates the HMAC signature of incoming requests.
// Order: headers → timestamp skew → known instance → body read → signature.
func HMACMiddleware(loader InstanceLoader, clock func() time.Time, skew time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			instanceID := r.Header.Get("X-Huskwoot-Instance")
			ts := r.Header.Get("X-Huskwoot-Timestamp")
			sig := r.Header.Get("X-Huskwoot-Signature")

			if instanceID == "" || ts == "" || sig == "" {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized", "required X-Huskwoot-* headers are missing")
				return
			}

			if err := pushproto.VerifyTimestamp(ts, clock(), skew); err != nil {
				writeAuthError(w, http.StatusUnauthorized, "timestamp_skew", err.Error())
				return
			}

			secret := loader.Secret(instanceID)
			if secret == nil {
				writeAuthError(w, http.StatusUnauthorized, "unknown_instance", "unknown instance")
				return
			}

			limited := http.MaxBytesReader(w, r.Body, maxBodySize)
			body, err := io.ReadAll(limited)
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					writeAuthError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 1 MiB")
					return
				}
				http.Error(w, "error reading request body", http.StatusInternalServerError)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			if err := pushproto.Verify(secret, sig, r.Method, r.URL.RequestURI(), ts, body); err != nil {
				writeAuthError(w, http.StatusUnauthorized, "bad_signature", err.Error())
				return
			}

			ctx := context.WithValue(r.Context(), instanceIDKey, instanceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type authErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(authErrorResponse{Code: code, Message: message}) //nolint:errcheck
}
