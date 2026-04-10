package relay

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// ServerDeps holds the dependencies for creating the relay HTTP server.
type ServerDeps struct {
	Store      *Store
	Loader     InstanceLoader
	APNs       PushSender // nil if APNs is not configured
	FCM        PushSender // nil if FCM is not configured
	Logger     *slog.Logger
	Skew       time.Duration
	Clock      func() time.Time
	ListenAddr string
}

// NewServer creates the relay HTTP server with a chi router.
// GET /healthz requires no authentication; /v1/* routes are protected by HMAC middleware.
func NewServer(deps ServerDeps) *http.Server {
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.Skew == 0 {
		deps.Skew = 5 * time.Minute
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	r := chi.NewRouter()

	r.Get("/healthz", (&healthHandler{pinger: deps.Store}).ServeHTTP)

	registrar := NewRegistrar(deps.Store)
	pusher := NewPusher(deps.Store, deps.APNs, deps.FCM, deps.Clock)

	r.Group(func(r chi.Router) {
		r.Use(HMACMiddleware(deps.Loader, deps.Clock, deps.Skew))
		r.Put("/v1/registrations/{deviceID}", registrar.Put)
		r.Delete("/v1/registrations/{deviceID}", registrar.Delete)
		r.Post("/v1/push", pusher.Push)
	})

	return &http.Server{
		Addr:              deps.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
