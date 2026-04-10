package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

const bearerPrefix = "Bearer "

// ctxKeyDeviceID is the context key for device_id. The value is chosen
// arbitrarily as long as it does not collide with ctxKeyRequestID.
const ctxKeyDeviceID ctxKey = 100

// DeviceIDFromContext returns the device ID stored by AuthMiddleware.
// Returns an empty string if the middleware is not applied or the request is not authorised.
func DeviceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyDeviceID).(string); ok {
		return v
	}
	return ""
}

// ContextWithDeviceID returns a context with device_id set. Useful in tests
// that do not want to set up the full AuthMiddleware.
func ContextWithDeviceID(ctx context.Context, deviceID string) context.Context {
	return context.WithValue(ctx, ctxKeyDeviceID, deviceID)
}

// AuthMiddleware validates the Authorization: Bearer <token> header, compares
// SHA256(token) against the DeviceStore, and stores device_id in the request
// context. Returns 401 with a uniform JSON body for unauthenticated requests.
// Updating last_seen_at is best-effort: errors are logged but the request proceeds.
func AuthMiddleware(store model.DeviceStore, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := extractBearerToken(r.Header.Get("Authorization"))
			if !ok {
				WriteError(w, http.StatusUnauthorized, ErrorCodeUnauthorized, "bearer token required")
				return
			}
			hash := hashBearerToken(token)
			device, err := store.FindByTokenHash(r.Context(), hash)
			if err != nil {
				logger.LogAttrs(r.Context(), slog.LevelError, "device lookup by token_hash",
					slog.String("request_id", RequestIDFromContext(r.Context())),
					slog.String("error", err.Error()),
				)
				WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "internal server error")
				return
			}
			if device == nil {
				WriteError(w, http.StatusUnauthorized, ErrorCodeUnauthorized, "unknown or revoked token")
				return
			}

			if err := store.UpdateLastSeen(r.Context(), device.ID, time.Now().UTC()); err != nil {
				logger.LogAttrs(r.Context(), slog.LevelWarn, "updating last_seen_at",
					slog.String("request_id", RequestIDFromContext(r.Context())),
					slog.String("device_id", device.ID),
					slog.String("error", err.Error()),
				)
			}

			ctx := context.WithValue(r.Context(), ctxKeyDeviceID, device.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearerToken parses the Authorization header and returns the token.
// ok=false if the prefix is not "Bearer " or the token is empty.
func extractBearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	if token == "" {
		return "", false
	}
	return token, true
}

// hashBearerToken returns the hex-encoded SHA256 of a bearer token. Matches
// the token_hash format stored in the devices table.
func hashBearerToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
