package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
)

// defaultRequestTimeout is the default read/write timeout for HTTP requests.
const defaultRequestTimeout = 30 * time.Second

// Version is the API version returned by GET /v1/me. Bumped manually on
// breaking changes to the OpenAPI contract.
const Version = "0.2.0"

// OwnerInfo is a snapshot of the instance owner's data, published at /v1/me.
type OwnerInfo struct {
	UserName       string
	TelegramUserID int64
}

// Config holds HTTP server parameters. DB is required for /readyz; Logger for
// middleware. ListenAddr is required for Run but not for Handler().
type Config struct {
	ListenAddr     string
	RequestTimeout time.Duration
	Logger         *slog.Logger
	DB             *sql.DB
	// Devices is the device store for AuthMiddleware. Required to mount protected
	// /v1/* routes; if nil only /healthz, /readyz, and 404 are served.
	Devices model.DeviceStore
	// Projects is the use-case for /v1/projects. Optional; nil — routes not mounted.
	Projects model.ProjectService
	// Tasks is the use-case for /v1/tasks. Optional; nil — routes not mounted.
	Tasks model.TaskService
	// Chat is the use-case for /v1/chat. Optional; nil — routes not mounted.
	Chat model.ChatService
	// History is the history store for /v1/chat/history. Optional; nil — returns empty history.
	History model.History
	// ChatTimeout is the agent processing timeout for /v1/chat (0 means no timeout).
	ChatTimeout time.Duration
	// Owner holds owner data published at /v1/me.
	Owner OwnerInfo
	// IdempotencyTTL/IdempotencyMaxEntries are the idempotency cache parameters;
	// zero uses IdempotencyMiddleware defaults.
	IdempotencyTTL        time.Duration
	IdempotencyMaxEntries int
	// Events is the domain event store for SSE replay and /v1/sync/snapshot.
	// If nil, /v1/events and /v1/sync/snapshot routes are not registered.
	Events model.EventStore
	// Broker is the in-memory SSE broker. If nil, /v1/events is not registered.
	Broker model.Broker
	// SSEHeartbeatInterval is the SSE keepalive comment interval. 0 → 15 seconds.
	SSEHeartbeatInterval time.Duration
	// PairingService is the use-case for /v1/pair/* and /pair/confirm/*. If nil — routes not mounted.
	PairingService model.PairingService
	// PairingRateLimit is the POST /v1/pair/request rate limit per IP per hour. 0 → default 5.
	PairingRateLimit int
	// PairingSecure: if true, the CSRF cookie is set as __Host-csrf with Secure:true (HTTPS);
	// if false — as csrf without Secure (HTTP, dev environment).
	PairingSecure bool
	// Relay is the push relay client for syncing registrations on patchMe and revoke.
	// Defaults to NilRelayClient (push disabled) when nil.
	Relay push.RelayClient
}

// Server wraps http.Server and a chi router.
type Server struct {
	cfg        Config
	logger     *slog.Logger
	db         *sql.DB
	handler    http.Handler
	pairingHdl *pairingHandler
}

// New builds the server with applied middleware and base endpoints.
func New(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{cfg: cfg, logger: logger, db: cfg.DB}
	if cfg.PairingService != nil {
		s.pairingHdl = &pairingHandler{
			service: cfg.PairingService,
			limiter: newPairingRateLimiter(cfg.PairingRateLimit),
			logger:  logger,
			secure:  cfg.PairingSecure,
		}
	}
	s.handler = s.routes()
	return s
}

// Handler returns the root http.Handler — convenient for httptest.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// routes builds the chi.Router, attaches middleware, and registers utility
// routes. Middleware order: request-id → logger → recover; recover must come
// last so the logger sees the status code written by recoverMiddleware.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(requestIDMiddleware)
	r.Use(loggingMiddleware(s.logger))
	r.Use(recoverMiddleware(s.logger))

	r.NotFound(notFoundHandler)
	r.MethodNotAllowed(methodNotAllowedHandler)

	r.Get("/healthz", s.healthz)
	r.Get("/readyz", s.readyz)
	r.Get("/v1/openapi.yaml", s.openapiHandler)

	if s.pairingHdl != nil {
		ph := s.pairingHdl
		r.Route("/v1/pair", func(pair chi.Router) {
			pair.With(ph.rateLimitMiddleware).Post("/request", ph.request)
			pair.Get("/status/{id}", ph.status)
		})
		r.Route("/pair/confirm", func(confirm chi.Router) {
			confirm.Get("/{id}", ph.confirmPage)
			confirm.Post("/{id}", ph.confirmSubmit)
		})
	}

	if s.cfg.Devices != nil {
		r.Route("/v1", func(v1 chi.Router) {
			v1.Use(AuthMiddleware(s.cfg.Devices, s.logger))
			v1.Use(IdempotencyMiddleware(IdempotencyConfig{
				TTL:        s.cfg.IdempotencyTTL,
				MaxEntries: s.cfg.IdempotencyMaxEntries,
			}))

			v1.Get("/me", s.getMe)

			relay := s.cfg.Relay
			if relay == nil {
				relay = push.NilRelayClient{}
			}
			dh := &devicesHandler{store: s.cfg.Devices, relay: relay, logger: s.logger}
			v1.Get("/devices", dh.list)
			v1.Patch("/devices/me", dh.patchMe)
			v1.Delete("/devices/{id}", dh.delete)

			if s.cfg.Projects != nil {
				ph := &projectsHandler{service: s.cfg.Projects, logger: s.logger}
				v1.Get("/projects", ph.list)
				v1.Post("/projects", ph.create)
				v1.Get("/projects/{id}", ph.get)
				v1.Patch("/projects/{id}", ph.update)
			}

			if s.cfg.Tasks != nil {
				th := &tasksHandler{service: s.cfg.Tasks, logger: s.logger}
				v1.Get("/tasks", th.list)
				v1.Post("/tasks", th.create)
				v1.Get("/tasks/by-ref/{ref}", th.getByRef)
				v1.Get("/tasks/{id}", th.get)
				v1.Patch("/tasks/{id}", th.update)
				v1.Delete("/tasks/{id}", th.delete)
				v1.Post("/tasks/{id}/complete", th.complete)
				v1.Post("/tasks/{id}/reopen", th.reopen)
				v1.Post("/tasks/{id}/move", th.move)
			}

			if s.cfg.Chat != nil {
				ch := newChatHandler(s.cfg.Chat, s.cfg.History, s.cfg.ChatTimeout, s.logger)
				v1.Post("/chat", ch.post)
				v1.Get("/chat/history", ch.getHistory)
			}

			if s.cfg.Events != nil && s.cfg.Broker != nil {
				eh := newEventsHandler(s.cfg.Events, s.cfg.Broker, s.cfg.SSEHeartbeatInterval, s.logger)
				v1.Get("/events", eh.stream)
			}

			if s.cfg.Events != nil && s.cfg.Projects != nil && s.cfg.Tasks != nil {
				sh := newSyncHandler(s.cfg.Projects, s.cfg.Tasks, s.cfg.Events, s.logger)
				v1.Get("/sync/snapshot", sh.snapshot)
			}
		})
	}

	return r
}

// healthz is the liveness probe: responds that the process is running.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// readyz is the readiness probe: pings the database with a short SELECT 1.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrorCodeUnavailable, "database not initialized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var v int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&v); err != nil {
		s.logger.WarnContext(r.Context(), "readyz: database unavailable", slog.String("error", err.Error()))
		WriteError(w, http.StatusServiceUnavailable, ErrorCodeUnavailable, "database unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// Run starts http.Server and blocks until ctx is cancelled, then performs a
// graceful shutdown with a timeout.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.ListenAddr == "" {
		return errors.New("api: listen_addr not set")
	}
	timeout := s.cfg.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       timeout,
		WriteTimeout:      timeout,
		IdleTimeout:       120 * time.Second,
	}

	if s.pairingHdl != nil {
		go s.pairingHdl.limiter.Sweep(ctx, time.Hour)
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("api: starting HTTP server", slog.String("addr", s.cfg.ListenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}
