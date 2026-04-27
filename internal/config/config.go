package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// UserConfig holds instance owner settings.
type UserConfig struct {
	// UserName is the display name used in AI prompts and API responses.
	UserName string `toml:"user_name"`
	// Aliases is the list of alternative names (as mentioned in conversations).
	Aliases []string `toml:"aliases"`
	// TelegramUserID is the numeric Telegram user ID of the owner; required when channels.telegram is set.
	TelegramUserID int64 `toml:"telegram_user_id"`
	// Language is the UI language: "ru" or "en". Defaults to "ru" when empty.
	Language string `toml:"language"`
}

// AIModelConfig describes the connection to a single AI model.
type AIModelConfig struct {
	// BaseURL is the base URL of the OpenAI-compatible API.
	BaseURL string `toml:"base_url"`
	// APIKey is the API access key.
	APIKey string `toml:"api_key"`
	// Model is the model name (e.g. "gpt-4o-mini").
	Model string `toml:"model"`
	// MaxCompletionTokens is the maximum number of tokens in the model response.
	// If unset, defaults apply: 1024 for fast, 4096 for smart.
	MaxCompletionTokens int `toml:"max_completion_tokens"`
}

// AIConfig holds settings for two models: fast and smart.
type AIConfig struct {
	Fast  AIModelConfig `toml:"fast"`
	Smart AIModelConfig `toml:"smart"`
}

// TelegramWatcherConfig describes the Telegram bot used for group monitoring.
type TelegramWatcherConfig struct {
	// Name is the bot display name (e.g. "@myhuskwootbot"). Used in pairing messages.
	Name string `toml:"name"`
	// Token is the bot token from @BotFather.
	Token string `toml:"token"`
	// OnJoin controls behavior on startup: "backfill" or "monitor".
	OnJoin string `toml:"on_join"`
	// ReactionEnabled controls whether the ✍️ reaction is set on promises in groups.
	ReactionEnabled bool `toml:"reaction_enabled"`
	// WelcomeMessage is the message sent when the bot is added to a group.
	// If empty, a default message is used.
	WelcomeMessage string `toml:"welcome_message"`
	// ConfirmTimeoutRaw is the owner confirmation timeout as a Go duration string (e.g. "1m").
	// If empty, defaults to 1 minute. "0" disables the guard.
	ConfirmTimeoutRaw string `toml:"confirm_timeout"`
	// ConfirmTimeout is the parsed confirmation timeout value.
	ConfirmTimeout time.Duration `toml:"-"`
}

// IMAPConfig describes a single IMAP connection.
type IMAPConfig struct {
	// Host is the IMAP server address.
	Host string `toml:"host"`
	// Port is the IMAP server port (typically 993 for TLS).
	Port int `toml:"port"`
	// Username is the account username (email).
	Username string `toml:"username"`
	// Password is the account password.
	Password string `toml:"password"`
	// Folders is the list of folders to monitor (e.g. ["INBOX", "[Gmail]/Sent Mail"]).
	Folders []string `toml:"folders"`
	// Senders is the list of sender addresses to filter by.
	Senders []string `toml:"senders"`
	// Label is the human-readable account name (e.g. "Work email").
	// If empty, the folder name is used.
	Label string `toml:"label"`
	// OnFirstConnect controls behavior on first connection: "backfill" or "monitor".
	OnFirstConnect string `toml:"on_first_connect"`
	// PollIntervalRaw is the mailbox poll interval as a Go duration string (e.g. "2m30s").
	PollIntervalRaw string `toml:"poll_interval"`
	// PollInterval is the parsed poll interval value.
	PollInterval time.Duration `toml:"-"`
}

// ChannelsConfig aggregates all message sources.
type ChannelsConfig struct {
	// Telegram is the optional single Telegram bot. Absence of this section disables Telegram.
	Telegram *TelegramWatcherConfig `toml:"telegram"`
	IMAP     []IMAPConfig           `toml:"imap"`
}

// HistoryConfig describes message history storage settings.
type HistoryConfig struct {
	// MaxMessages is the maximum number of messages stored per channel.
	MaxMessages int `toml:"max_messages"`
	// TTLRaw is the TTL as a Go duration string (e.g. "24h").
	TTLRaw string `toml:"ttl"`
	// TTL is the parsed TTL value.
	TTL time.Duration `toml:"-"`
}

// TimeOfDayConfig holds hours for various periods of the day.
type TimeOfDayConfig struct {
	// Morning is the hour for "in the morning" expressions (default 11).
	Morning int `toml:"morning"`
	// Lunch is the hour for "at lunch" expressions (default 12).
	Lunch int `toml:"lunch"`
	// Afternoon is the hour for "in the afternoon" expressions (default 14).
	Afternoon int `toml:"afternoon"`
	// Evening is the hour for "in the evening" expressions (default 20).
	Evening int `toml:"evening"`
	// Default is the time-of-day marker used when a date is given without an explicit time.
	// Accepted values: "morning", "lunch", "afternoon", "evening". Default: "evening".
	// Does not affect expressions with "до ..." semantics (those always use 23:59:59).
	Default string `toml:"default"`
}

// DateTimeConfig holds date parser and timezone settings.
type DateTimeConfig struct {
	// Timezone is the IANA timezone identifier (e.g. "Europe/Moscow"); optional.
	Timezone string `toml:"timezone"`
	// Weekdays is the list of working days (e.g. ["mon", "tue", "wed", "thu", "fri"]); optional.
	// Remaining days are treated as weekends. Empty or absent → defaults to Mon..Fri.
	Weekdays []string `toml:"weekdays"`
	// TimeOfDay is the nested table with hours for various periods of the day.
	TimeOfDay TimeOfDayConfig `toml:"time_of_day"`
	// NightCutoff is the opt-in shift boundary in "HH:MM" format (e.g. "05:00").
	// When current time is before this boundary, relative day words like "tomorrow"
	// are shifted back by one calendar day (asymmetric: "today" is never shifted).
	// Empty string or "00:00" means the feature is disabled (default).
	NightCutoff string `toml:"night_cutoff"`
	// NightCutoffMinutes is the parsed boundary in minutes since midnight; 0 = off.
	NightCutoffMinutes int `toml:"-"`
}

// ReminderSchedule describes named digest schedule slots.
type ReminderSchedule struct {
	// Morning is the morning slot time in "HH:MM" format; required when the schedule section is present.
	Morning string `toml:"morning"`
	// Afternoon is the afternoon slot time in "HH:MM" format; empty string disables the slot.
	Afternoon string `toml:"afternoon"`
	// Evening is the evening slot time in "HH:MM" format; empty string disables the slot.
	Evening string `toml:"evening"`
}

// RemindersConfig describes parameters for periodic task digests.
type RemindersConfig struct {
	// PlansHorizonRaw is the planning horizon as a Go duration string (e.g. "168h"); empty → 7 days.
	PlansHorizonRaw string `toml:"plans_horizon"`
	// PlansHorizon is the parsed horizon value.
	PlansHorizon time.Duration `toml:"-"`
	// UndatedLimit is the maximum number of undated tasks shown in a digest (0 = don't show).
	UndatedLimit int `toml:"undated_limit"`
	// SendWhenEmpty controls behavior when the digest is empty: "always", "never", or "morning".
	SendWhenEmpty string `toml:"send_when_empty"`
	// Schedule is the slot schedule; nil means the feature is disabled.
	Schedule *ReminderSchedule `toml:"schedule"`
}

// APIConfig describes the HTTP API server (v1) parameters.
type APIConfig struct {
	// Enabled controls whether the HTTP server is started; safe default is false.
	Enabled bool `toml:"enabled"`
	// ListenAddr is the listen address (e.g. "127.0.0.1:8080"); required when Enabled=true.
	ListenAddr string `toml:"listen_addr"`
	// ExternalBaseURL is the external base URL (for generating absolute links); optional.
	ExternalBaseURL string `toml:"external_base_url"`
	// RequestTimeoutRaw is the timeout for regular REST requests as a Go duration string (e.g. "30s").
	RequestTimeoutRaw string `toml:"request_timeout"`
	// RequestTimeout is the parsed request timeout value.
	RequestTimeout time.Duration `toml:"-"`
	// ChatTimeoutRaw is the timeout for /v1/chat (long generations) as a Go duration string (e.g. "60s").
	ChatTimeoutRaw string `toml:"chat_timeout"`
	// ChatTimeout is the parsed chat timeout value.
	ChatTimeout time.Duration `toml:"-"`
	// EventsRetentionRaw is the event retention window as a Go duration string (e.g. "168h"); default 168h.
	EventsRetentionRaw string `toml:"events_retention"`
	// EventsRetention is the parsed retention window value.
	EventsRetention time.Duration `toml:"-"`
	// CORSAllowedOrigins is the list of origins for which CORS is enabled; empty list disables CORS.
	CORSAllowedOrigins []string `toml:"cors_allowed_origins"`
	// PairingLinkTTLRaw is the magic-link and pairing_requests record TTL as a Go duration string (e.g. "5m"); default 5m.
	PairingLinkTTLRaw string `toml:"pairing_link_ttl"`
	// PairingLinkTTL is the parsed pairing link TTL value.
	PairingLinkTTL time.Duration `toml:"-"`
	// PairingStatusLongPollRaw is the long-poll timeout for /v1/pair/status as a Go duration string (e.g. "60s"); default 60s.
	PairingStatusLongPollRaw string `toml:"pairing_status_long_poll"`
	// PairingStatusLongPoll is the parsed long-poll timeout value.
	PairingStatusLongPoll time.Duration `toml:"-"`
	// RateLimitPairPerHour is the POST /v1/pair/request budget per IP per hour; default 5.
	RateLimitPairPerHour int `toml:"rate_limit_pair_per_hour"`
}

// PushConfig describes push dispatcher and relay connection parameters.
type PushConfig struct {
	// RelayURL is the push relay base URL (e.g. "https://push.huskwoot.app").
	RelayURL string `toml:"relay_url"`
	// InstanceID is the instance identifier in the relay.
	InstanceID string `toml:"instance_id"`
	// InstanceSecret is the secret used for HMAC-signing relay requests.
	InstanceSecret string `toml:"instance_secret"`
	// TimeoutRaw is the HTTP request timeout for relay calls as a Go duration string (e.g. "10s"); default 10s.
	TimeoutRaw string `toml:"timeout"`
	// Timeout is the parsed timeout value.
	Timeout time.Duration `toml:"-"`
	// DispatcherIntervalRaw is the dispatcher tick interval as a Go duration string (e.g. "2s"); default 2s.
	DispatcherIntervalRaw string `toml:"dispatcher_interval"`
	// DispatcherInterval is the parsed interval value.
	DispatcherInterval time.Duration `toml:"-"`
	// BatchSize is the number of jobs processed per dispatcher iteration; default 32.
	BatchSize int `toml:"batch_size"`
	// RetryMaxAttempts is the maximum delivery attempts before dropping a job; default 4.
	RetryMaxAttempts int `toml:"retry_max_attempts"`
}

// Enabled returns true if all three required fields (relay_url, instance_id, instance_secret) are set.
func (p PushConfig) Enabled() bool {
	return p.RelayURL != "" && p.InstanceID != "" && p.InstanceSecret != ""
}

// DevicesConfig describes retention windows for client devices.
type DevicesConfig struct {
	// InactiveThresholdRaw is the idle period after which a device is auto-revoked.
	// Go duration string (e.g. "720h" = 30 days). Default: 720h.
	InactiveThresholdRaw string `toml:"inactive_threshold"`
	// InactiveThreshold is the parsed value.
	InactiveThreshold time.Duration `toml:"-"`
	// RetentionPeriodRaw is the period after revocation before the device is
	// physically deleted. Go duration string (e.g. "2160h" = 90 days). Default: 2160h.
	RetentionPeriodRaw string `toml:"retention_period"`
	// RetentionPeriod is the parsed value.
	RetentionPeriod time.Duration `toml:"-"`
}

// WhisperConfig holds settings for the whisper.cpp subprocess transcriber.
type WhisperConfig struct {
	// Binary is the path to the whisper-cli executable; required.
	Binary string `toml:"binary"`
	// Model is the path to the GGML model file; required.
	Model string `toml:"model"`
	// Language is the recognition language (e.g. "ru"); empty means auto-detect.
	Language string `toml:"language"`
	// Ffmpeg is the path or name of the ffmpeg binary; defaults to "ffmpeg".
	Ffmpeg string `toml:"ffmpeg"`
	// Threads is the number of CPU threads passed via -t; 0 means flag is omitted.
	Threads int `toml:"threads"`
	// TimeoutRaw is the subprocess execution timeout as a Go duration string (e.g. "60s"); default 60s.
	TimeoutRaw string `toml:"timeout"`
	// Timeout is the parsed timeout value.
	Timeout time.Duration `toml:"-"`
}

// YandexConfig holds settings for the Yandex SpeechKit REST transcriber.
type YandexConfig struct {
	// APIKey is the Yandex Cloud API key; required.
	APIKey string `toml:"api_key"`
	// FolderID is the Yandex Cloud folder ID; required.
	FolderID string `toml:"folder_id"`
	// Language is the recognition language (e.g. "ru-RU"); defaults to "ru-RU".
	Language string `toml:"language"`
	// Endpoint is the SpeechKit REST endpoint URL; defaults to the official endpoint.
	Endpoint string `toml:"endpoint"`
	// TimeoutRaw is the HTTP request timeout as a Go duration string (e.g. "30s"); default 30s.
	TimeoutRaw string `toml:"timeout"`
	// Timeout is the parsed timeout value.
	Timeout time.Duration `toml:"-"`
}

// TranscriberConfig holds settings for the voice transcription feature.
type TranscriberConfig struct {
	// Provider selects the transcription backend: "" (disabled), "whisper", or "yandex".
	Provider string `toml:"provider"`
	// MaxDurationRaw is the hard upper bound on voice message duration as a Go duration string (e.g. "5m"); default 5m.
	MaxDurationRaw string `toml:"max_duration"`
	// MaxDuration is the parsed maximum duration value.
	MaxDuration time.Duration `toml:"-"`
	// Whisper holds whisper.cpp-specific settings.
	Whisper WhisperConfig `toml:"whisper"`
	// Yandex holds Yandex SpeechKit-specific settings.
	Yandex YandexConfig `toml:"yandex"`
}

// Config is the root configuration structure for the application.
type Config struct {
	User        UserConfig        `toml:"user"`
	AI          AIConfig          `toml:"ai"`
	Channels    ChannelsConfig    `toml:"channels"`
	History     HistoryConfig     `toml:"history"`
	DateTime    DateTimeConfig    `toml:"datetime"`
	Reminders   *RemindersConfig  `toml:"reminders"`
	API         APIConfig         `toml:"api"`
	Push        PushConfig        `toml:"push"`
	Devices     DevicesConfig     `toml:"devices"`
	Transcriber TranscriberConfig `toml:"transcriber"`
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Load reads the configuration from config.toml in the given directory.
// Substitutes ${ENV_VAR} environment variable references before parsing.
func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading configuration file: %w", err)
	}

	expanded := expandEnvVars(string(raw))

	var cfg Config
	if _, err := toml.Decode(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parsing TOML: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating configuration: %w", err)
	}

	return &cfg, nil
}

// expandEnvVars substitutes ${ENV_VAR} references with values from the environment.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarPattern.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match
	})
}

// validate checks that required fields are present and values are valid.
func (c *Config) validate() error {
	if c.AI.Fast.Model == "" {
		return fmt.Errorf("ai.fast.model is required")
	}
	if c.AI.Fast.BaseURL == "" {
		return fmt.Errorf("ai.fast.base_url is required")
	}
	if c.AI.Smart.Model == "" {
		return fmt.Errorf("ai.smart.model is required")
	}
	if c.AI.Smart.BaseURL == "" {
		return fmt.Errorf("ai.smart.base_url is required")
	}

	hasTelegram := c.Channels.Telegram != nil
	if !hasTelegram && len(c.Channels.IMAP) == 0 {
		return fmt.Errorf("at least one source must be configured: channels.telegram or channels.imap")
	}

	if hasTelegram {
		tg := c.Channels.Telegram
		if tg.Token == "" {
			return fmt.Errorf("channels.telegram.token is required")
		}
		if tg.ConfirmTimeoutRaw == "" {
			c.Channels.Telegram.ConfirmTimeout = 1 * time.Minute
		} else {
			d, err := time.ParseDuration(tg.ConfirmTimeoutRaw)
			if err != nil {
				return fmt.Errorf("channels.telegram.confirm_timeout: invalid format %q: %w", tg.ConfirmTimeoutRaw, err)
			}
			if d < 0 {
				return fmt.Errorf("channels.telegram.confirm_timeout cannot be negative")
			}
			c.Channels.Telegram.ConfirmTimeout = d
		}
	}

	if c.History.MaxMessages <= 0 {
		return fmt.Errorf("history.max_messages must be greater than zero")
	}
	if c.History.TTLRaw == "" {
		return fmt.Errorf("history.ttl is required")
	}

	ttl, err := time.ParseDuration(c.History.TTLRaw)
	if err != nil {
		return fmt.Errorf("history.ttl: invalid format %q: %w", c.History.TTLRaw, err)
	}
	c.History.TTL = ttl

	if hasTelegram {
		if c.User.TelegramUserID == 0 {
			return fmt.Errorf("user.telegram_user_id is required when channels.telegram is configured")
		}
	}
	for i := range c.Channels.IMAP {
		if c.Channels.IMAP[i].PollIntervalRaw != "" {
			d, err := time.ParseDuration(c.Channels.IMAP[i].PollIntervalRaw)
			if err != nil {
				return fmt.Errorf("channels.imap[%d].poll_interval: invalid format %q: %w", i, c.Channels.IMAP[i].PollIntervalRaw, err)
			}
			c.Channels.IMAP[i].PollInterval = d
		}
	}

	// Apply defaults for max_completion_tokens.
	if c.AI.Fast.MaxCompletionTokens == 0 {
		c.AI.Fast.MaxCompletionTokens = 1024
	}
	if c.AI.Smart.MaxCompletionTokens == 0 {
		c.AI.Smart.MaxCompletionTokens = 4096
	}

	// Apply defaults for time-of-day settings.
	if c.DateTime.TimeOfDay.Morning == 0 {
		c.DateTime.TimeOfDay.Morning = 11
	}
	if c.DateTime.TimeOfDay.Lunch == 0 {
		c.DateTime.TimeOfDay.Lunch = 12
	}
	if c.DateTime.TimeOfDay.Afternoon == 0 {
		c.DateTime.TimeOfDay.Afternoon = 14
	}
	if c.DateTime.TimeOfDay.Evening == 0 {
		c.DateTime.TimeOfDay.Evening = 20
	}
	if c.DateTime.TimeOfDay.Default == "" {
		c.DateTime.TimeOfDay.Default = "evening"
	}
	switch c.DateTime.TimeOfDay.Default {
	case "morning", "lunch", "afternoon", "evening":
		// valid
	default:
		return fmt.Errorf("invalid datetime.time_of_day.default: %q (expected one of: morning, lunch, afternoon, evening)", c.DateTime.TimeOfDay.Default)
	}

	if nc := c.DateTime.NightCutoff; nc == "" || nc == "00:00" {
		c.DateTime.NightCutoffMinutes = 0
	} else {
		h, m, err := parseHHMM(nc)
		if err != nil {
			return fmt.Errorf("invalid datetime.night_cutoff %q: %w", nc, err)
		}
		c.DateTime.NightCutoffMinutes = h*60 + m
	}

	switch c.User.Language {
	case "":
		c.User.Language = "ru"
	case "ru", "en":
		// valid
	default:
		return fmt.Errorf("user.language %q: allowed values: ru, en", c.User.Language)
	}

	if err := c.validateReminders(); err != nil {
		return err
	}

	if err := c.validateAPI(); err != nil {
		return err
	}

	if err := c.validatePush(); err != nil {
		return err
	}

	if err := c.validateDevices(); err != nil {
		return err
	}

	if err := c.validateTranscriber(); err != nil {
		return err
	}

	return nil
}

// validateDevices validates and normalizes the [devices] section.
func (c *Config) validateDevices() error {
	d := &c.Devices

	if d.InactiveThresholdRaw == "" {
		d.InactiveThreshold = 30 * 24 * time.Hour
	} else {
		v, err := time.ParseDuration(d.InactiveThresholdRaw)
		if err != nil {
			return fmt.Errorf("devices.inactive_threshold %q: %w", d.InactiveThresholdRaw, err)
		}
		if v <= 0 {
			return fmt.Errorf("devices.inactive_threshold must be positive, got %v", v)
		}
		d.InactiveThreshold = v
	}

	if d.RetentionPeriodRaw == "" {
		d.RetentionPeriod = 90 * 24 * time.Hour
	} else {
		v, err := time.ParseDuration(d.RetentionPeriodRaw)
		if err != nil {
			return fmt.Errorf("devices.retention_period %q: %w", d.RetentionPeriodRaw, err)
		}
		if v <= 0 {
			return fmt.Errorf("devices.retention_period must be positive, got %v", v)
		}
		d.RetentionPeriod = v
	}

	return nil
}

// validateAPI validates and normalizes the [api] section.
func (c *Config) validateAPI() error {
	if c.API.RequestTimeoutRaw != "" {
		d, err := time.ParseDuration(c.API.RequestTimeoutRaw)
		if err != nil {
			return fmt.Errorf("api.request_timeout %q: %w", c.API.RequestTimeoutRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("api.request_timeout must be positive, got %v", d)
		}
		c.API.RequestTimeout = d
	}

	if c.API.ChatTimeoutRaw != "" {
		d, err := time.ParseDuration(c.API.ChatTimeoutRaw)
		if err != nil {
			return fmt.Errorf("api.chat_timeout %q: %w", c.API.ChatTimeoutRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("api.chat_timeout must be positive, got %v", d)
		}
		c.API.ChatTimeout = d
	}

	if c.API.EventsRetentionRaw == "" {
		c.API.EventsRetention = 168 * time.Hour
	} else {
		d, err := time.ParseDuration(c.API.EventsRetentionRaw)
		if err != nil {
			return fmt.Errorf("api.events_retention %q: %w", c.API.EventsRetentionRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("api.events_retention must be positive, got %v", d)
		}
		c.API.EventsRetention = d
	}

	if c.API.PairingLinkTTLRaw == "" {
		c.API.PairingLinkTTL = 5 * time.Minute
	} else {
		d, err := time.ParseDuration(c.API.PairingLinkTTLRaw)
		if err != nil {
			return fmt.Errorf("api.pairing_link_ttl %q: %w", c.API.PairingLinkTTLRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("api.pairing_link_ttl must be positive, got %v", d)
		}
		c.API.PairingLinkTTL = d
	}

	if c.API.PairingStatusLongPollRaw == "" {
		c.API.PairingStatusLongPoll = 60 * time.Second
	} else {
		d, err := time.ParseDuration(c.API.PairingStatusLongPollRaw)
		if err != nil {
			return fmt.Errorf("api.pairing_status_long_poll %q: %w", c.API.PairingStatusLongPollRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("api.pairing_status_long_poll must be positive, got %v", d)
		}
		c.API.PairingStatusLongPoll = d
	}

	if c.API.RateLimitPairPerHour == 0 {
		c.API.RateLimitPairPerHour = 5
	}

	if c.API.Enabled && c.API.ListenAddr == "" {
		return fmt.Errorf("api.listen_addr is required when api.enabled = true")
	}

	return nil
}

// validateReminders validates the [reminders] section if present.
func (c *Config) validateReminders() error {
	if c.Reminders == nil {
		return nil
	}

	if c.Reminders.Schedule == nil {
		c.Reminders.Schedule = &ReminderSchedule{Morning: "09:00"}
	}

	sched := c.Reminders.Schedule
	if sched.Morning == "" {
		return fmt.Errorf("reminders.schedule.morning is required")
	}
	if _, _, err := parseHHMM(sched.Morning); err != nil {
		return fmt.Errorf("reminders.schedule.morning %q: %w", sched.Morning, err)
	}
	if sched.Afternoon != "" {
		if _, _, err := parseHHMM(sched.Afternoon); err != nil {
			return fmt.Errorf("reminders.schedule.afternoon %q: %w", sched.Afternoon, err)
		}
	}
	if sched.Evening != "" {
		if _, _, err := parseHHMM(sched.Evening); err != nil {
			return fmt.Errorf("reminders.schedule.evening %q: %w", sched.Evening, err)
		}
	}

	if c.Reminders.PlansHorizonRaw == "" {
		c.Reminders.PlansHorizon = 7 * 24 * time.Hour
	} else {
		d, err := time.ParseDuration(c.Reminders.PlansHorizonRaw)
		if err != nil {
			return fmt.Errorf("reminders.plans_horizon %q: %w", c.Reminders.PlansHorizonRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("reminders.plans_horizon must be positive, got %v", d)
		}
		c.Reminders.PlansHorizon = d
	}

	if c.Reminders.UndatedLimit < 0 {
		return fmt.Errorf("reminders.undated_limit cannot be negative")
	}

	switch c.Reminders.SendWhenEmpty {
	case "", "always", "never", "morning":
		if c.Reminders.SendWhenEmpty == "" {
			c.Reminders.SendWhenEmpty = "morning"
		}
	default:
		return fmt.Errorf("reminders.send_when_empty %q: allowed values: always, never, morning", c.Reminders.SendWhenEmpty)
	}

	if c.User.TelegramUserID == 0 {
		return fmt.Errorf("reminders: set user.telegram_user_id")
	}

	return nil
}

// validatePush validates and normalizes the [push] section.
func (c *Config) validatePush() error {
	p := &c.Push

	nSet := 0
	if p.RelayURL != "" {
		nSet++
	}
	if p.InstanceID != "" {
		nSet++
	}
	if p.InstanceSecret != "" {
		nSet++
	}

	if nSet == 0 {
		return nil
	}

	if nSet < 3 {
		return fmt.Errorf("push: all three fields must be set: relay_url, instance_id, instance_secret (%d of 3 set)", nSet)
	}

	if p.TimeoutRaw == "" {
		p.Timeout = 10 * time.Second
	} else {
		d, err := time.ParseDuration(p.TimeoutRaw)
		if err != nil {
			return fmt.Errorf("push.timeout %q: %w", p.TimeoutRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("push.timeout must be positive, got %v", d)
		}
		p.Timeout = d
	}

	if p.DispatcherIntervalRaw == "" {
		p.DispatcherInterval = 2 * time.Second
	} else {
		d, err := time.ParseDuration(p.DispatcherIntervalRaw)
		if err != nil {
			return fmt.Errorf("push.dispatcher_interval %q: %w", p.DispatcherIntervalRaw, err)
		}
		if d <= 0 {
			return fmt.Errorf("push.dispatcher_interval must be positive, got %v", d)
		}
		p.DispatcherInterval = d
	}

	if p.BatchSize == 0 {
		p.BatchSize = 32
	}

	if p.RetryMaxAttempts == 0 {
		p.RetryMaxAttempts = 4
	}

	return nil
}

const defaultTranscriberMaxDuration = 5 * time.Minute
const defaultWhisperTimeout = 60 * time.Second
const defaultYandexTimeout = 30 * time.Second
const defaultYandexLanguage = "ru-RU"
const defaultYandexEndpoint = "https://stt.api.cloud.yandex.net/speech/v1/stt:recognize"

// validateTranscriber validates and normalizes the [transcriber] section.
func (c *Config) validateTranscriber() error {
	t := &c.Transcriber

	switch t.Provider {
	case "", "whisper", "yandex":
		// valid
	default:
		return fmt.Errorf("transcriber.provider %q: allowed values: \"\", \"whisper\", \"yandex\"", t.Provider)
	}

	if t.MaxDurationRaw == "" {
		t.MaxDuration = defaultTranscriberMaxDuration
	} else {
		d, err := time.ParseDuration(t.MaxDurationRaw)
		if err != nil {
			return fmt.Errorf("transcriber.max_duration %q: %w", t.MaxDurationRaw, err)
		}
		if d < 0 {
			return fmt.Errorf("transcriber.max_duration must be non-negative")
		}
		t.MaxDuration = d
	}

	if t.Provider == "whisper" {
		if t.Whisper.Binary == "" {
			return fmt.Errorf("transcriber.whisper.binary is required")
		}
		if t.Whisper.Model == "" {
			return fmt.Errorf("transcriber.whisper.model is required")
		}
		if t.Whisper.Ffmpeg == "" {
			t.Whisper.Ffmpeg = "ffmpeg"
		}
		if t.Whisper.TimeoutRaw == "" {
			t.Whisper.Timeout = defaultWhisperTimeout
		} else {
			d, err := time.ParseDuration(t.Whisper.TimeoutRaw)
			if err != nil {
				return fmt.Errorf("transcriber.whisper.timeout %q: %w", t.Whisper.TimeoutRaw, err)
			}
			if d <= 0 {
				return fmt.Errorf("transcriber.whisper.timeout must be positive, got %v", d)
			}
			t.Whisper.Timeout = d
		}
	}

	if t.Provider == "yandex" {
		if t.Yandex.APIKey == "" {
			return fmt.Errorf("transcriber.yandex.api_key is required")
		}
		if t.Yandex.FolderID == "" {
			return fmt.Errorf("transcriber.yandex.folder_id is required")
		}
		if t.Yandex.Language == "" {
			t.Yandex.Language = defaultYandexLanguage
		}
		if t.Yandex.Endpoint == "" {
			t.Yandex.Endpoint = defaultYandexEndpoint
		}
		if t.Yandex.TimeoutRaw == "" {
			t.Yandex.Timeout = defaultYandexTimeout
		} else {
			d, err := time.ParseDuration(t.Yandex.TimeoutRaw)
			if err != nil {
				return fmt.Errorf("transcriber.yandex.timeout %q: %w", t.Yandex.TimeoutRaw, err)
			}
			if d <= 0 {
				return fmt.Errorf("transcriber.yandex.timeout must be positive, got %v", d)
			}
			t.Yandex.Timeout = d
		}
	}

	return nil
}

// parseHHMM parses a string in "HH:MM" format (hours 0–23, minutes 0–59).
func parseHHMM(s string) (hour, minute int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM format")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("expected HH:MM format")
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("expected HH:MM format")
	}
	if h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("hours must be in range 0–23, got %d", h)
	}
	if m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("minutes must be in range 0–59, got %d", m)
	}
	return h, m, nil
}
