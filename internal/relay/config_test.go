package relay_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anadale/huskwoot/internal/relay"
)

func writeRelayTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeRelayTOML: %v", err)
	}
	return path
}

func TestLoadRelayConfig_ParsesInstances(t *testing.T) {
	content := `
[server]
listen_addr = "0.0.0.0:8080"

[db]
path = "/tmp/relay.db"

[apns]
key_file  = "/run/secrets/apns-key.p8"
key_id    = "KEY123"
team_id   = "TEAM456"
bundle_id = "com.example.app"

[[instances]]
id            = "alpha"
owner_contact = "@alpha"
secret        = "secret-alpha"

[[instances]]
id            = "beta"
owner_contact = "beta@example.com"
secret        = "secret-beta"
`
	path := writeRelayTOML(t, content)
	cfg, err := relay.LoadRelayConfig(path)
	if err != nil {
		t.Fatalf("LoadRelayConfig: %v", err)
	}

	if len(cfg.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(cfg.Instances))
	}

	cases := []struct {
		idx          int
		id           string
		ownerContact string
		secret       string
	}{
		{0, "alpha", "@alpha", "secret-alpha"},
		{1, "beta", "beta@example.com", "secret-beta"},
	}
	for _, c := range cases {
		got := cfg.Instances[c.idx]
		if got.ID != c.id {
			t.Errorf("Instances[%d].ID = %q, want %q", c.idx, got.ID, c.id)
		}
		if got.OwnerContact != c.ownerContact {
			t.Errorf("Instances[%d].OwnerContact = %q, want %q", c.idx, got.OwnerContact, c.ownerContact)
		}
		if got.Secret != c.secret {
			t.Errorf("Instances[%d].Secret = %q, want %q", c.idx, got.Secret, c.secret)
		}
	}
}

func TestLoadRelayConfig_ExpandsEnvVars(t *testing.T) {
	t.Setenv("RELAY_TEST_SECRET", "super-secret-42")

	content := `
[server]
listen_addr = "127.0.0.1:9090"

[db]
path = "/tmp/relay-test.db"

[fcm]
service_account_file = "/run/secrets/fcm-sa.json"

[[instances]]
id            = "test-instance"
owner_contact = "@test"
secret        = "${RELAY_TEST_SECRET}"
`
	path := writeRelayTOML(t, content)
	cfg, err := relay.LoadRelayConfig(path)
	if err != nil {
		t.Fatalf("LoadRelayConfig: %v", err)
	}

	if len(cfg.Instances) != 1 {
		t.Fatalf("want 1 instance, got %d", len(cfg.Instances))
	}
	if cfg.Instances[0].Secret != "super-secret-42" {
		t.Errorf("Secret = %q, want %q", cfg.Instances[0].Secret, "super-secret-42")
	}
}

func TestLoadRelayConfig_ValidatesRequiredSections(t *testing.T) {
	content := `
[server]
listen_addr = "0.0.0.0:8080"

[db]
path = "/tmp/relay.db"

# Секции [apns] и [fcm] отсутствуют
`
	path := writeRelayTOML(t, content)
	_, err := relay.LoadRelayConfig(path)
	if err == nil {
		t.Fatal("want an error when [apns] and [fcm] are missing, got nil")
	}
}
