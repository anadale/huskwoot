package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/anadale/huskwoot/internal/relay"
	"github.com/anadale/huskwoot/internal/relay/migrations"
)

var version = "dev"

func main() {
	configFile := flag.String("config-file", "/etc/huskwoot-relay/relay.toml", "path to configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := relay.LoadRelayConfig(*configFile)
	if err != nil {
		slog.Error("loading configuration", "error", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.Server.LogLevel, cfg.Server.LogFormat)
	slog.SetDefault(logger)

	db, err := openDB(cfg.DB.Path)
	if err != nil {
		logger.Error("opening database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		logger.Error("applying migrations", "error", err)
		os.Exit(1)
	}

	store := relay.NewStore(db)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := store.SyncInstances(ctx, cfg.Instances); err != nil {
		logger.Error("syncing instances", "error", err)
		os.Exit(1)
	}

	loader := relay.NewInstanceLoader(cfg.Instances)

	var apnsSender relay.PushSender
	if cfg.APNsConfigured() {
		s, err := relay.NewAPNsSender(cfg.APNsAdapterConfig())
		if err != nil {
			logger.Error("initialising APNs adapter", "error", err)
			os.Exit(1)
		}
		apnsSender = s
		logger.Info("APNs adapter initialised")
	}

	var fcmSender relay.PushSender
	if cfg.FCMConfigured() {
		s, err := relay.NewFCMSender(cfg.FCMAdapterConfig())
		if err != nil {
			logger.Error("initialising FCM adapter", "error", err)
			os.Exit(1)
		}
		fcmSender = s
		logger.Info("FCM adapter initialised")
	}

	server := relay.NewServer(relay.ServerDeps{
		Store:      store,
		Loader:     loader,
		APNs:       apnsSender,
		FCM:        fcmSender,
		Logger:     logger,
		Skew:       cfg.Server.HMACSkew,
		ListenAddr: cfg.Server.ListenAddr,
	})

	// Hot-reload goroutine on SIGHUP.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigs:
				newCfg, err := relay.LoadRelayConfig(*configFile)
				if err != nil {
					logger.Error("reloading configuration", "error", err)
					continue
				}
				if err := store.SyncInstances(ctx, newCfg.Instances); err != nil {
					logger.Error("syncing instances on reload", "error", err)
					continue
				}
				newSecrets := make(map[string][]byte, len(newCfg.Instances))
				for _, spec := range newCfg.Instances {
					if spec.Secret != "" {
						newSecrets[spec.ID] = []byte(spec.Secret)
					}
				}
				loader.Swap(newSecrets)
				logger.Info("configuration reloaded", "instances", len(newCfg.Instances))
			}
		}
	}()

	logger.Info("huskwoot-push-relay starting", "addr", cfg.Server.ListenAddr)

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown", "error", err)
		}
	case err := <-errCh:
		logger.Error("fatal server error", "error", err)
		os.Exit(1)
	}
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("foreign keys: %w", err)
	}
	return db, nil
}

func setupLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
