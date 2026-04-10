package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/anadale/huskwoot/internal/config"
	devicesstore "github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
)

// allowedPlatforms перечисляет поддерживаемые значения --platform.
// Флаг проверяется на этапе CLI, чтобы не принимать произвольные строки.
var allowedPlatforms = []string{"ios", "android", "macos", "windows", "linux"}

// newDevicesCommand собирает группу `huskwoot devices ...` для
// администрирования клиентских устройств (выдача bearer-токена, список, revoke).
func newDevicesCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "Manage client devices (pre-pairing dev flow)",
	}
	cmd.AddCommand(
		newDevicesCreateCommand(flags),
		newDevicesListCommand(flags),
		newDevicesRevokeCommand(flags),
	)
	return cmd
}

func newDevicesCreateCommand(flags *rootFlags) *cobra.Command {
	var name, platform string
	c := &cobra.Command{
		Use:   "create",
		Short: "Register a new device and issue a bearer token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validatePlatform(platform); err != nil {
				return err
			}
			db, err := storage.OpenDB(filepath.Join(flags.configDir, "huskwoot.db"))
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			store := devicesstore.NewSQLiteDeviceStore(db)
			return runDevicesCreate(cmd.Context(), cmd.OutOrStdout(), db, store, name, platform)
		},
	}
	c.Flags().StringVar(&name, "name", "", "human-readable name (e.g. \"iPhone 17\")")
	c.Flags().StringVar(&platform, "platform", "", "ios|android|macos|windows|linux")
	_ = c.MarkFlagRequired("name")
	_ = c.MarkFlagRequired("platform")
	return c
}

func newDevicesListCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := storage.OpenDB(filepath.Join(flags.configDir, "huskwoot.db"))
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			store := devicesstore.NewSQLiteDeviceStore(db)
			return runDevicesList(cmd.Context(), cmd.OutOrStdout(), store)
		},
	}
}

func newDevicesRevokeCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <device-id>",
		Short: "Revoke a device (mark revoked_at)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := storage.OpenDB(filepath.Join(flags.configDir, "huskwoot.db"))
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer db.Close()

			cfg, err := config.Load(flags.configDir)
			if err != nil {
				return fmt.Errorf("loading configuration: %w", err)
			}

			store := devicesstore.NewSQLiteDeviceStore(db)
			var relay push.RelayClient
			if cfg.Push.Enabled() {
				relay = push.NewHTTPRelayClient(push.HTTPRelayClientConfig{
					BaseURL:    cfg.Push.RelayURL,
					InstanceID: cfg.Push.InstanceID,
					Secret:     []byte(cfg.Push.InstanceSecret),
					Timeout:    cfg.Push.Timeout,
				})
			} else {
				relay = push.NilRelayClient{}
			}
			return runDevicesRevoke(cmd.Context(), cmd.OutOrStdout(), store, relay, args[0])
		},
	}
}

// runDevicesCreate — ядро подкоманды create; вынесено отдельно для тестируемости
// (можно подменить DeviceStore без реального CLI-парсинга флагов).
func runDevicesCreate(ctx context.Context, out io.Writer, db *sql.DB, store model.DeviceStore, name, platform string) error {
	token, err := generateBearerToken()
	if err != nil {
		return fmt.Errorf("generating token: %w", err)
	}
	d := &model.Device{
		ID:        uuid.NewString(),
		Name:      name,
		Platform:  platform,
		TokenHash: sha256Hex(token),
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("opening transaction: %w", err)
	}
	defer tx.Rollback()
	if err := store.Create(ctx, tx, d); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	fmt.Fprintf(out, "device_id: %s\nbearer:    %s\n", d.ID, token)
	fmt.Fprintln(out, "(save the bearer token: it will not be shown again)")
	return nil
}

func runDevicesList(ctx context.Context, out io.Writer, store model.DeviceStore) error {
	devs, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("listing devices: %w", err)
	}
	if len(devs) == 0 {
		fmt.Fprintln(out, "no devices registered")
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPLATFORM\tCREATED\tLAST_SEEN\tSTATUS")
	for _, d := range devs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			d.ID, d.Name, d.Platform,
			formatTime(&d.CreatedAt),
			formatTimePtr(d.LastSeenAt),
			statusLabel(d),
		)
	}
	return tw.Flush()
}

func runDevicesRevoke(ctx context.Context, out io.Writer, store model.DeviceStore, relay push.RelayClient, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("device-id is required")
	}
	if err := store.Revoke(ctx, id); err != nil {
		return err
	}
	if err := relay.DeleteRegistration(ctx, id); err != nil {
		slog.WarnContext(ctx, "devices revoke: relay delete failed",
			"device_id", id,
			"error", err,
		)
	}
	fmt.Fprintf(out, "device %s revoked\n", id)
	return nil
}

// generateBearerToken возвращает 32-байтный bearer (base64url без паддинга).
func generateBearerToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func validatePlatform(p string) error {
	for _, v := range allowedPlatforms {
		if v == p {
			return nil
		}
	}
	return fmt.Errorf("invalid --platform value %q (expected %s)", p, strings.Join(allowedPlatforms, "|"))
}

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

func statusLabel(d model.Device) string {
	if d.RevokedAt != nil {
		return "revoked " + d.RevokedAt.UTC().Format(time.RFC3339)
	}
	return "active"
}
