package relay

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
)

// RelayServerConfig holds the configuration for the relay HTTP server.
type RelayServerConfig struct {
	ListenAddr  string        `toml:"listen_addr"`
	HMACSkewRaw string        `toml:"hmac_skew"`
	HMACSkew    time.Duration `toml:"-"`
	LogLevel    string        `toml:"log_level"`
	LogFormat   string        `toml:"log_format"`
}

// RelayDBConfig holds the database configuration for the relay.
type RelayDBConfig struct {
	Path string `toml:"path"`
}

// RelayAPNsConfig holds the APNs provider configuration.
type RelayAPNsConfig struct {
	KeyFile    string `toml:"key_file"`
	KeyID      string `toml:"key_id"`
	TeamID     string `toml:"team_id"`
	BundleID   string `toml:"bundle_id"`
	Production bool   `toml:"production"`
}

// RelayFCMConfig holds the FCM provider configuration.
type RelayFCMConfig struct {
	ServiceAccountFile string `toml:"service_account_file"`
}

// RelayConfig is the root configuration structure for the relay.
type RelayConfig struct {
	Server    RelayServerConfig `toml:"server"`
	DB        RelayDBConfig     `toml:"db"`
	APNs      RelayAPNsConfig   `toml:"apns"`
	FCM       RelayFCMConfig    `toml:"fcm"`
	Instances []InstanceSpec    `toml:"instances"`
}

// APNsConfigured returns true if the APNs section contains all required fields.
func (c *RelayConfig) APNsConfigured() bool {
	a := c.APNs
	return a.KeyFile != "" && a.KeyID != "" && a.TeamID != "" && a.BundleID != ""
}

// FCMConfigured returns true if the FCM section contains a service account path.
func (c *RelayConfig) FCMConfigured() bool {
	return c.FCM.ServiceAccountFile != ""
}

// APNsAdapterConfig returns an APNsConfig suitable for passing to NewAPNsSender.
func (c *RelayConfig) APNsAdapterConfig() APNsConfig {
	return APNsConfig{
		KeyFile:    c.APNs.KeyFile,
		KeyID:      c.APNs.KeyID,
		TeamID:     c.APNs.TeamID,
		BundleID:   c.APNs.BundleID,
		Production: c.APNs.Production,
	}
}

// FCMAdapterConfig returns an FCMConfig suitable for passing to NewFCMSender.
func (c *RelayConfig) FCMAdapterConfig() FCMConfig {
	return FCMConfig{ServiceAccountFile: c.FCM.ServiceAccountFile}
}

var relayEnvVarRE = regexp.MustCompile(`\$\{([^}]+)\}`)

// LoadRelayConfig loads the relay configuration from a TOML file.
// Values of the form ${ENV_VAR} are expanded from environment variables on load.
func LoadRelayConfig(path string) (*RelayConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading configuration file: %w", err)
	}

	expanded := relayEnvVarRE.ReplaceAllStringFunc(string(raw), func(match string) string {
		name := relayEnvVarRE.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match
	})

	var cfg RelayConfig
	if _, err := toml.Decode(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parsing TOML: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating configuration: %w", err)
	}

	return &cfg, nil
}

func (c *RelayConfig) validate() error {
	if c.Server.ListenAddr == "" {
		return fmt.Errorf("server.listen_addr is required")
	}
	if c.DB.Path == "" {
		return fmt.Errorf("db.path is required")
	}

	if c.Server.HMACSkewRaw == "" {
		c.Server.HMACSkew = 5 * time.Minute
	} else {
		d, err := time.ParseDuration(c.Server.HMACSkewRaw)
		if err != nil {
			return fmt.Errorf("server.hmac_skew %q: %w", c.Server.HMACSkewRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("server.hmac_skew must be positive, got %v", d)
		}
		c.Server.HMACSkew = d
	}

	if !c.APNsConfigured() && !c.FCMConfigured() {
		return fmt.Errorf("at least one provider must be configured: [apns] or [fcm]")
	}

	return nil
}
