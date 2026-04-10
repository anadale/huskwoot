package api

import (
	"context"
	cryptorand "crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/go-chi/chi/v5"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/usecase"
)

//go:embed templates/pair_confirm.html.tmpl
var confirmPageTmplStr string

var confirmPageTmpl = template.Must(template.New("pair_confirm").Parse(confirmPageTmplStr))

type confirmPageData struct {
	DeviceName string
	Platform   string
	CreatedAt  string
	CSRF       string
}

// pairingRequestBody is the request body for POST /v1/pair/request.
type pairingRequestBody struct {
	DeviceName  string  `json:"deviceName"`
	Platform    string  `json:"platform"`
	ClientNonce string  `json:"clientNonce"`
	APNSToken   *string `json:"apnsToken,omitempty"`
	FCMToken    *string `json:"fcmToken,omitempty"`
}

// pendingPairingResponse is the 202 Accepted response body for POST /v1/pair/request.
type pendingPairingResponse struct {
	PairID    string    `json:"pairId"`
	PollURL   string    `json:"pollUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// csrfCookieNameSecure is the CSRF cookie name over HTTPS (__Host- prefix requires Secure+Path=/).
const csrfCookieNameSecure = "__Host-csrf"

// csrfCookieNameInsecure is the CSRF cookie name over HTTP (dev environment).
const csrfCookieNameInsecure = "csrf"

type pairingHandler struct {
	service model.PairingService
	limiter *pairingRateLimiter
	logger  *slog.Logger
	secure  bool
}

// csrfCookieName returns the CSRF cookie name based on the TLS mode.
func (h *pairingHandler) csrfCookieName() string {
	if h.secure {
		return csrfCookieNameSecure
	}
	return csrfCookieNameInsecure
}

var validPlatforms = map[string]bool{
	"ios": true, "android": true, "macos": true, "windows": true, "linux": true,
}

// request handles POST /v1/pair/request.
func (h *pairingHandler) request(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body pairingRequestBody
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}

	if body.DeviceName == "" || body.Platform == "" || body.ClientNonce == "" {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "fields deviceName, platform and clientNonce are required")
		return
	}
	if !validPlatforms[body.Platform] {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "invalid platform value")
		return
	}

	pending, err := h.service.RequestPairing(r.Context(), model.PairingRequest{
		DeviceName:  body.DeviceName,
		Platform:    body.Platform,
		ClientNonce: body.ClientNonce,
		APNSToken:   body.APNSToken,
		FCMToken:    body.FCMToken,
	})
	if err != nil {
		if errors.Is(err, usecase.ErrSenderFailed) {
			h.logger.WarnContext(r.Context(), "pairing: failed to send magic-link", slog.String("error", err.Error()))
			WriteError(w, http.StatusBadGateway, ErrorCodeUnavailable, "failed to send magic-link to owner")
			return
		}
		h.logger.ErrorContext(r.Context(), "pairing: RequestPairing error", slog.String("error", err.Error()))
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "внутренняя ошибка")
		return
	}

	resp := pendingPairingResponse{
		PairID:    pending.ID,
		PollURL:   "/v1/pair/status/" + pending.ID,
		ExpiresAt: pending.ExpiresAt,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(resp)
}

// status handles GET /v1/pair/status/{id} (long-poll).
func (h *pairingHandler) status(w http.ResponseWriter, r *http.Request) {
	pairID := chi.URLParam(r, "id")
	nonce := r.URL.Query().Get("nonce")
	if nonce == "" {
		WriteError(w, http.StatusBadRequest, ErrorCodeBadRequest, "nonce parameter is required")
		return
	}

	// Clear the WriteTimeout so the long-poll is not cut off by the global server timeout.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		h.logger.WarnContext(r.Context(), "pairing status: failed to clear WriteDeadline", slog.String("error", err.Error()))
	}

	result, err := h.service.PollStatus(r.Context(), pairID, nonce)
	if err != nil {
		switch {
		case errors.Is(err, usecase.ErrPairingNotFound):
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "pairing request not found")
		case errors.Is(err, usecase.ErrNonceMismatch):
			WriteError(w, http.StatusForbidden, ErrorCodeForbidden, "nonce mismatch")
		case errors.Is(err, usecase.ErrAlreadyConfirmed):
			WriteError(w, http.StatusGone, "gone", "pairing request already confirmed, token is no longer available")
		default:
			h.logger.ErrorContext(r.Context(), "pairing: PollStatus error", slog.String("error", err.Error()))
			WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "internal error")
		}
		return
	}

	if result.Status == model.PairingStatusExpired {
		WriteError(w, http.StatusGone, "gone", "link has expired")
		return
	}

	var respBody any
	if result.Status == model.PairingStatusConfirmed {
		respBody = struct {
			Status      string `json:"status"`
			DeviceID    string `json:"deviceId"`
			BearerToken string `json:"bearerToken"`
		}{
			Status:      string(result.Status),
			DeviceID:    result.DeviceID,
			BearerToken: result.BearerToken,
		}
	} else {
		respBody = struct {
			Status string `json:"status"`
		}{Status: string(result.Status)}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(respBody)
}

// confirmPage handles GET /pair/confirm/{id} — renders the HTML device confirmation page.
func (h *pairingHandler) confirmPage(w http.ResponseWriter, r *http.Request) {
	pairID := chi.URLParam(r, "id")

	// Generate CSRF token (32 bytes → base64url).
	csrfBytes := make([]byte, 32)
	if _, err := io.ReadFull(cryptorand.Reader, csrfBytes); err != nil {
		h.logger.ErrorContext(r.Context(), "pairing confirm: CSRF generation error", slog.String("error", err.Error()))
		writeHTMLError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	csrfToken := base64.RawURLEncoding.EncodeToString(csrfBytes)

	p, err := h.service.PrepareConfirm(r.Context(), pairID, csrfToken)
	if err != nil {
		if errors.Is(err, usecase.ErrPairingNotFound) || errors.Is(err, usecase.ErrPairingExpired) {
			writeHTMLError(w, http.StatusGone, "Link has expired or not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "pairing confirm: PrepareConfirm error", slog.String("error", err.Error()))
		writeHTMLError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// __Host- prefix requires Path=/ per RFC 6265bis; dev mode uses a narrower path.
	cookiePath := "/pair/confirm/" + pairID
	if h.secure {
		cookiePath = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     h.csrfCookieName(),
		Value:    csrfToken,
		Path:     cookiePath,
		Secure:   h.secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   600,
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := confirmPageData{
		DeviceName: p.DeviceName,
		Platform:   p.Platform,
		CreatedAt:  p.CreatedAt.Format("02.01.2006 15:04"),
		CSRF:       csrfToken,
	}
	if err := confirmPageTmpl.Execute(w, data); err != nil {
		h.logger.ErrorContext(r.Context(), "pairing confirm: template render error", slog.String("error", err.Error()))
	}
}

// confirmSubmit handles POST /pair/confirm/{id} — validates CSRF and creates the device.
func (h *pairingHandler) confirmSubmit(w http.ResponseWriter, r *http.Request) {
	pairID := chi.URLParam(r, "id")

	if err := r.ParseForm(); err != nil {
		writeHTMLError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	cookie, err := r.Cookie(h.csrfCookieName())
	if err != nil {
		writeHTMLError(w, http.StatusForbidden, "Forbidden: CSRF cookie missing")
		return
	}
	formCSRF := r.FormValue("csrf")
	if cookie.Value == "" || cookie.Value != formCSRF {
		writeHTMLError(w, http.StatusForbidden, "Forbidden: CSRF token mismatch")
		return
	}

	_, err = h.service.ConfirmWithCSRF(r.Context(), pairID, formCSRF)
	if err != nil {
		switch {
		case errors.Is(err, usecase.ErrCSRFMismatch):
			writeHTMLError(w, http.StatusForbidden, "Forbidden: invalid confirmation token")
		case errors.Is(err, usecase.ErrAlreadyConfirmed):
			writeHTMLError(w, http.StatusGone, "Device was already connected")
		case errors.Is(err, usecase.ErrPairingExpired), errors.Is(err, usecase.ErrPairingNotFound):
			writeHTMLError(w, http.StatusGone, "Link has expired or not found")
		default:
			h.logger.ErrorContext(r.Context(), "pairing confirm submit: ConfirmWithCSRF error", slog.String("error", err.Error()))
			writeHTMLError(w, http.StatusInternalServerError, "Internal server error")
		}
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Device connected · Huskwoot</title>`+
		`<style>body{font-family:-apple-system,sans-serif;max-width:480px;margin:48px auto;padding:0 16px;color:#222}</style></head>`+
		`<body><h1>✓ Device connected</h1><p>The device has been successfully connected to Huskwoot. You can close this tab.</p></body></html>`)
}

// writeHTMLError writes a simple HTML error page.
func writeHTMLError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Error</title></head><body><p>%s</p></body></html>", html.EscapeString(message))
}

// rateLimitMiddleware checks the request rate limit for the current IP.
func (h *pairingHandler) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r.RemoteAddr)
		if !h.limiter.Allow(ip) {
			w.Header().Set("Retry-After", "3600")
			WriteError(w, http.StatusTooManyRequests, ErrorCodeRateLimited, "device pairing request rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// remoteIP extracts the IP address from an addr in "ip:port" format.
func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// --- Rate limiter ---

type ipLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// pairingRateLimiter is an in-memory token bucket rate limiter keyed by IP address.
type pairingRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiterEntry
	limit    rate.Limit
	burst    int
}

func newPairingRateLimiter(perHour int) *pairingRateLimiter {
	if perHour <= 0 {
		perHour = 5
	}
	return &pairingRateLimiter{
		limiters: make(map[string]*ipLimiterEntry),
		limit:    rate.Every(time.Hour / time.Duration(perHour)),
		burst:    perHour,
	}
}

// Allow reports whether a request from the given IP is permitted.
func (l *pairingRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.limiters[ip]
	if !ok {
		entry = &ipLimiterEntry{
			limiter: rate.NewLimiter(l.limit, l.burst),
		}
		l.limiters[ip] = entry
	}
	entry.lastUsed = time.Now()
	return entry.limiter.Allow()
}

// Sweep removes idle limiters every every interval; stops when the context is cancelled.
func (l *pairingRateLimiter) Sweep(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			for ip, entry := range l.limiters {
				if time.Since(entry.lastUsed) > time.Hour {
					delete(l.limiters, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}
