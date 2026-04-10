package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// validBase returns a fully valid TOML config for use in tests.
func validBase() string {
	return `
[user]
user_name = "Имя Фамилия"
aliases = ["Гриша", "Greg"]
telegram_user_id = 987654321

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "sk-fast-key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "sk-smart-key"
model = "gpt-4o"

[channels.telegram]
token = "tg-bot-token"
on_join = "monitor"

[history]
max_messages = 200
ttl = "24h"
`
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad_FromDirectory(t *testing.T) {
	dir := writeConfig(t, validBase())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("expected success from Load(dir), got error: %v", err)
	}
	if cfg.User.UserName != "Имя Фамилия" {
		t.Errorf("User.UserName = %q, want %q", cfg.User.UserName, "Имя Фамилия")
	}
}

func TestLoad_MissingDirectory(t *testing.T) {
	_, err := Load("/nonexistent/directory/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent directory, got nil")
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	content := `
[user]
user_name = "Имя Фамилия"
aliases = ["Гриша", "Greg"]
telegram_user_id = 987654321

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "sk-fast-key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "sk-smart-key"
model = "gpt-4o"

[channels.telegram]
name = "myhuskwootbot"
token = "tg-bot-token"
on_join = "monitor"

[[channels.imap]]
host = "imap.example.com"
port = 993
username = "user@example.com"
password = "secret"
folders = ["INBOX"]
senders = ["boss@example.com"]
on_first_connect = "backfill"

[history]
max_messages = 500
ttl = "24h"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if cfg.User.UserName != "Имя Фамилия" {
		t.Errorf("User.UserName = %q, want %q", cfg.User.UserName, "Имя Фамилия")
	}
	if len(cfg.User.Aliases) != 2 {
		t.Errorf("User.Aliases len = %d, want 2", len(cfg.User.Aliases))
	}

	if cfg.AI.Fast.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("AI.Fast.BaseURL = %q", cfg.AI.Fast.BaseURL)
	}
	if cfg.AI.Fast.Model != "gpt-4o-mini" {
		t.Errorf("AI.Fast.Model = %q", cfg.AI.Fast.Model)
	}
	if cfg.AI.Smart.Model != "gpt-4o" {
		t.Errorf("AI.Smart.Model = %q", cfg.AI.Smart.Model)
	}

	if cfg.Channels.Telegram == nil {
		t.Fatal("Channels.Telegram must not be nil")
	}
	tg := cfg.Channels.Telegram
	if tg.Name != "myhuskwootbot" {
		t.Errorf("Telegram.Name = %q, want %q", tg.Name, "myhuskwootbot")
	}
	if tg.Token != "tg-bot-token" {
		t.Errorf("Telegram.Token = %q", tg.Token)
	}
	if tg.OnJoin != "monitor" {
		t.Errorf("Telegram.OnJoin = %q", tg.OnJoin)
	}

	if len(cfg.Channels.IMAP) != 1 {
		t.Fatalf("Watchers.IMAP len = %d, want 1", len(cfg.Channels.IMAP))
	}
	imap := cfg.Channels.IMAP[0]
	if imap.Host != "imap.example.com" {
		t.Errorf("IMAP.Host = %q", imap.Host)
	}

	if cfg.History.MaxMessages != 500 {
		t.Errorf("History.MaxMessages = %d", cfg.History.MaxMessages)
	}
	if cfg.History.TTL != 24*time.Hour {
		t.Errorf("History.TTL = %v, want 24h", cfg.History.TTL)
	}
}

func TestLoad_DateTimeDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.DateTime.TimeOfDay.Morning != 11 {
		t.Errorf("DateTime.TimeOfDay.Morning default = %d, want 11", cfg.DateTime.TimeOfDay.Morning)
	}
	if cfg.DateTime.TimeOfDay.Lunch != 12 {
		t.Errorf("DateTime.TimeOfDay.Lunch default = %d, want 12", cfg.DateTime.TimeOfDay.Lunch)
	}
	if cfg.DateTime.TimeOfDay.Afternoon != 14 {
		t.Errorf("DateTime.TimeOfDay.Afternoon default = %d, want 14", cfg.DateTime.TimeOfDay.Afternoon)
	}
	if cfg.DateTime.TimeOfDay.Evening != 20 {
		t.Errorf("DateTime.TimeOfDay.Evening default = %d, want 20", cfg.DateTime.TimeOfDay.Evening)
	}
}

func TestLoad_DateTimeCustom(t *testing.T) {
	content := validBase() + `
[datetime.time_of_day]
morning = 9
lunch = 11
afternoon = 13
evening = 21
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.DateTime.TimeOfDay.Morning != 9 {
		t.Errorf("DateTime.TimeOfDay.Morning = %d, want 9", cfg.DateTime.TimeOfDay.Morning)
	}
	if cfg.DateTime.TimeOfDay.Lunch != 11 {
		t.Errorf("DateTime.TimeOfDay.Lunch = %d, want 11", cfg.DateTime.TimeOfDay.Lunch)
	}
	if cfg.DateTime.TimeOfDay.Afternoon != 13 {
		t.Errorf("DateTime.TimeOfDay.Afternoon = %d, want 13", cfg.DateTime.TimeOfDay.Afternoon)
	}
	if cfg.DateTime.TimeOfDay.Evening != 21 {
		t.Errorf("DateTime.TimeOfDay.Evening = %d, want 21", cfg.DateTime.TimeOfDay.Evening)
	}
}

func TestConfig_ParsesDateTimeSection(t *testing.T) {
	content := validBase() + `
[datetime]
timezone = "Europe/Moscow"
weekdays = ["sat", "sun"]

[datetime.time_of_day]
morning = 9
lunch = 11
afternoon = 13
evening = 21
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.DateTime.Timezone != "Europe/Moscow" {
		t.Errorf("DateTime.Timezone = %q, want %q", cfg.DateTime.Timezone, "Europe/Moscow")
	}
	if len(cfg.DateTime.Weekdays) != 2 {
		t.Errorf("DateTime.Weekdays len = %d, want 2", len(cfg.DateTime.Weekdays))
	}
	if cfg.DateTime.Weekdays[0] != "sat" || cfg.DateTime.Weekdays[1] != "sun" {
		t.Errorf("DateTime.Weekdays = %v, want [sat sun]", cfg.DateTime.Weekdays)
	}
}

func TestConfig_DateTimeEmptyTimezoneAndWeekdays(t *testing.T) {
	content := validBase() + `
[datetime.time_of_day]
morning = 10
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.DateTime.Timezone != "" {
		t.Errorf("DateTime.Timezone = %q, want empty string", cfg.DateTime.Timezone)
	}
	if len(cfg.DateTime.Weekdays) != 0 {
		t.Errorf("DateTime.Weekdays len = %d, want 0", len(cfg.DateTime.Weekdays))
	}
}

func TestLoad_EnvVarSubstitution(t *testing.T) {
	t.Setenv("TG_TOKEN", "env-bot-token")
	t.Setenv("OPENAI_KEY", "env-api-key")

	content := `
[user]
user_name = "Имя"
telegram_user_id = 1

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_KEY}"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_KEY}"
model = "gpt-4o"

[channels.telegram]
token = "${TG_TOKEN}"
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if cfg.Channels.Telegram.Token != "env-bot-token" {
		t.Errorf("Token after substitution = %q, want %q", cfg.Channels.Telegram.Token, "env-bot-token")
	}
	if cfg.AI.Fast.APIKey != "env-api-key" {
		t.Errorf("APIKey after substitution = %q, want %q", cfg.AI.Fast.APIKey, "env-api-key")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir() // directory exists but config.toml is absent
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error when config.toml is absent from directory, got nil")
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	_, err := Load(writeConfig(t, ""))
	if err == nil {
		t.Fatal("expected validation error for empty file, got nil")
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	_, err := Load(writeConfig(t, "not valid toml [[["))
	if err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
}

func TestLoad_MissingRequiredField(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "telegram без user.telegram_user_id",
			content: `
[user]
user_name = "Имя"

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"
`,
		},
		{
			name: "telegram без token",
			content: `
[user]
user_name = "Имя"
telegram_user_id = 1

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"
`,
		},
		{
			name: "отсутствует fast model",
			content: `
[user]
user_name = "Имя"
telegram_user_id = 1

[ai]
[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			if err == nil {
				t.Fatalf("expected validation error (%s), got nil", tt.name)
			}
		})
	}
}

func TestLoad_AIModel_DefaultMaxCompletionTokens(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.AI.Fast.MaxCompletionTokens != 1024 {
		t.Errorf("AI.Fast.MaxCompletionTokens default = %d, want 1024", cfg.AI.Fast.MaxCompletionTokens)
	}
	if cfg.AI.Smart.MaxCompletionTokens != 4096 {
		t.Errorf("AI.Smart.MaxCompletionTokens default = %d, want 4096", cfg.AI.Smart.MaxCompletionTokens)
	}
}

func TestLoad_AIModel_ExplicitMaxCompletionTokens(t *testing.T) {
	content := `
[user]
user_name = "Имя Фамилия"
telegram_user_id = 987654321

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "sk-fast-key"
model = "gpt-4o-mini"
max_completion_tokens = 512

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "sk-smart-key"
model = "gpt-4o"
max_completion_tokens = 8192

[channels.telegram]
token = "tg-bot-token"
on_join = "monitor"

[history]
max_messages = 200
ttl = "24h"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.AI.Fast.MaxCompletionTokens != 512 {
		t.Errorf("AI.Fast.MaxCompletionTokens = %d, want 512", cfg.AI.Fast.MaxCompletionTokens)
	}
	if cfg.AI.Smart.MaxCompletionTokens != 8192 {
		t.Errorf("AI.Smart.MaxCompletionTokens = %d, want 8192", cfg.AI.Smart.MaxCompletionTokens)
	}
}

func TestLoad_ReactionEnabled(t *testing.T) {
	content := `
[user]
user_name = "Имя"
telegram_user_id = 1

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"
on_join = "monitor"
reaction_enabled = true

[history]
max_messages = 100
ttl = "1h"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("reaction_enabled = true should be accepted without error, got: %v", err)
	}
	if !cfg.Channels.Telegram.ReactionEnabled {
		t.Errorf("Channels.Telegram.ReactionEnabled = false, want true")
	}
}

func TestLoad_IMAP_Folders(t *testing.T) {
	makeFolderConfig := func(foldersLine string) string {
		return `
[user]
user_name = "Имя"
telegram_user_id = 1

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[[channels.imap]]
host = "imap.example.com"
port = 993
username = "user@example.com"
password = "secret"
` + foldersLine + `

[channels.telegram]
token = "token"
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"
`
	}

	tests := []struct {
		name        string
		foldersLine string
		wantFolders []string
	}{
		{
			name:        "одна папка INBOX",
			foldersLine: `folders = ["INBOX"]`,
			wantFolders: []string{"INBOX"},
		},
		{
			name:        "две папки INBOX и Sent",
			foldersLine: `folders = ["INBOX", "Sent"]`,
			wantFolders: []string{"INBOX", "Sent"},
		},
		{
			name:        "без поля folders — не ломает валидацию",
			foldersLine: "",
			wantFolders: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, makeFolderConfig(tt.foldersLine)))
			if err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if len(cfg.Channels.IMAP) != 1 {
				t.Fatalf("IMAP len = %d, want 1", len(cfg.Channels.IMAP))
			}
			got := cfg.Channels.IMAP[0].Folders
			if len(got) != len(tt.wantFolders) {
				t.Fatalf("Folders = %v, want %v", got, tt.wantFolders)
			}
			for i, f := range tt.wantFolders {
				if got[i] != f {
					t.Errorf("Folders[%d] = %q, want %q", i, got[i], f)
				}
			}
		})
	}
}

func remindersBase(extra string) string {
	return validBase() + `
[reminders]
[reminders.schedule]
morning = "09:00"
` + extra
}

func TestReminders_NoSection(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success without [reminders] section: %v", err)
	}
	if cfg.Reminders != nil {
		t.Errorf("Reminders must be nil when section is absent")
	}
}

func TestReminders_EmptySectionDefaultSchedule(t *testing.T) {
	content := validBase() + `
[reminders]
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("[reminders] without [reminders.schedule] expected success: %v", err)
	}
	if cfg.Reminders == nil {
		t.Fatal("Reminders must not be nil when section is present")
	}
	if cfg.Reminders.Schedule == nil {
		t.Fatal("Reminders.Schedule must not be nil — default should be applied")
	}
	if cfg.Reminders.Schedule.Morning != "09:00" {
		t.Errorf("Reminders.Schedule.Morning = %q, want %q", cfg.Reminders.Schedule.Morning, "09:00")
	}
}

func TestReminders_ValidMinimal(t *testing.T) {
	cfg, err := Load(writeConfig(t, remindersBase("")))
	if err != nil {
		t.Fatalf("minimal [reminders] expected success: %v", err)
	}
	if cfg.Reminders.Schedule == nil {
		t.Fatal("Reminders.Schedule must not be nil")
	}
	if cfg.Reminders.Schedule.Morning != "09:00" {
		t.Errorf("Morning = %q, want %q", cfg.Reminders.Schedule.Morning, "09:00")
	}
	if cfg.Reminders.PlansHorizon != 7*24*time.Hour {
		t.Errorf("PlansHorizon default = %v, want %v", cfg.Reminders.PlansHorizon, 7*24*time.Hour)
	}
	if cfg.Reminders.SendWhenEmpty != "morning" {
		t.Errorf("SendWhenEmpty default = %q, want %q", cfg.Reminders.SendWhenEmpty, "morning")
	}
}

func TestReminders_MorningRequired(t *testing.T) {
	content := validBase() + `
[reminders]
[reminders.schedule]
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("empty morning should produce an error")
	}
}

func TestReminders_InvalidHHMM(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value string
	}{
		{"morning 25:00", "morning", "25:00"},
		{"morning 9:0", "morning", "9:0"},
		{"morning abc", "morning", "abc"},
		{"afternoon 99:00", "afternoon", "99:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := tc.field + ` = "` + tc.value + `"`
			if tc.field != "morning" {
				extra = `morning = "09:00"
` + extra
			}
			content := validBase() + `
[reminders]
[reminders.schedule]
` + extra
			_, err := Load(writeConfig(t, content))
			if err == nil {
				t.Fatalf("invalid HH:MM %q should produce an error", tc.value)
			}
		})
	}
}

func TestReminders_PlansHorizon(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"empty → 7d", "", 7 * 24 * time.Hour, false},
		{"168h → 7d", `plans_horizon = "168h"`, 168 * time.Hour, false},
		{"48h → 2d", `plans_horizon = "48h"`, 48 * time.Hour, false},
		{"negative → error", `plans_horizon = "-1h"`, 0, true},
		{"zero → error", `plans_horizon = "0"`, 0, true},
		{"invalid → error", `plans_horizon = "7d"`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := validBase() + `
[reminders]
` + tc.raw + `
[reminders.schedule]
morning = "09:00"
`
			cfg, err := Load(writeConfig(t, content))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success: %v", err)
			}
			if cfg.Reminders.PlansHorizon != tc.want {
				t.Errorf("PlansHorizon = %v, want %v", cfg.Reminders.PlansHorizon, tc.want)
			}
		})
	}
}

func TestReminders_UndatedLimit(t *testing.T) {
	t.Run("отрицательный undated_limit → ошибка", func(t *testing.T) {
		content := validBase() + `
[reminders]
undated_limit = -1
[reminders.schedule]
morning = "09:00"
`
		_, err := Load(writeConfig(t, content))
		if err == nil {
			t.Fatal("expected error for undated_limit=-1")
		}
	})
	t.Run("нулевой undated_limit — ок", func(t *testing.T) {
		content := validBase() + `
[reminders]
undated_limit = 0
[reminders.schedule]
morning = "09:00"
`
		cfg, err := Load(writeConfig(t, content))
		if err != nil {
			t.Fatalf("expected success: %v", err)
		}
		if cfg.Reminders.UndatedLimit != 0 {
			t.Errorf("UndatedLimit = %d, want 0", cfg.Reminders.UndatedLimit)
		}
	})
}

func TestReminders_SendWhenEmpty(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"пустое → morning", "", "morning", false},
		{"always", "always", "always", false},
		{"never", "never", "never", false},
		{"morning", "morning", "morning", false},
		{"bogus → ошибка", "bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valueLine := ""
			if tc.value != "" {
				valueLine = `send_when_empty = "` + tc.value + `"`
			}
			content := validBase() + `
[reminders]
` + valueLine + `
[reminders.schedule]
morning = "09:00"
`
			cfg, err := Load(writeConfig(t, content))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success: %v", err)
			}
			if cfg.Reminders.SendWhenEmpty != tc.want {
				t.Errorf("SendWhenEmpty = %q, want %q", cfg.Reminders.SendWhenEmpty, tc.want)
			}
		})
	}
}

func TestReminders_RequiresTelegramUserID(t *testing.T) {
	// Reminders without user.telegram_user_id must fail (IMAP-only source).
	content := `
[user]
user_name = "Имя"

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o"

[[channels.imap]]
host = "imap.example.com"
port = 993
username = "user@example.com"
password = "secret"
folders = ["INBOX"]

[history]
max_messages = 100
ttl = "1h"

[reminders]
[reminders.schedule]
morning = "09:00"
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("reminders without user.telegram_user_id should produce an error")
	}
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		input   string
		wantH   int
		wantM   int
		wantErr bool
	}{
		{"09:00", 9, 0, false},
		{"23:59", 23, 59, false},
		{"00:00", 0, 0, false},
		{"14:30", 14, 30, false},
		{"25:00", 0, 0, true},
		{"9:00", 0, 0, true}, // single-digit hour
		{"09:0", 0, 0, true}, // single-digit minute
		{"abc", 0, 0, true},
		{"", 0, 0, true},
		{"24:00", 0, 0, true},
		{"00:60", 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			h, m, err := parseHHMM(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseHHMM(%q): expected error, got nil (h=%d, m=%d)", tc.input, h, m)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHHMM(%q): expected success, got %v", tc.input, err)
			}
			if h != tc.wantH || m != tc.wantM {
				t.Errorf("parseHHMM(%q) = (%d, %d), want (%d, %d)", tc.input, h, m, tc.wantH, tc.wantM)
			}
		})
	}
}

func TestLoad_APIDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.API.Enabled {
		t.Errorf("API.Enabled default = true, want false")
	}
	if cfg.API.EventsRetention != 168*time.Hour {
		t.Errorf("API.EventsRetention default = %v, want 168h", cfg.API.EventsRetention)
	}
	if cfg.API.RequestTimeout != 0 {
		t.Errorf("API.RequestTimeout default = %v, want 0 (not set)", cfg.API.RequestTimeout)
	}
	if cfg.API.ChatTimeout != 0 {
		t.Errorf("API.ChatTimeout default = %v, want 0 (not set)", cfg.API.ChatTimeout)
	}
}

func TestLoad_APIParsesSection(t *testing.T) {
	content := `
[user]
user_name = "Имя"
telegram_user_id = 42

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"

[api]
enabled = true
listen_addr = "127.0.0.1:9000"
external_base_url = "https://ex.com"
request_timeout = "30s"
chat_timeout = "60s"
events_retention = "72h"
cors_allowed_origins = ["https://client.example.com"]
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if !cfg.API.Enabled {
		t.Errorf("API.Enabled = false, want true")
	}
	if cfg.API.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("API.ListenAddr = %q, want %q", cfg.API.ListenAddr, "127.0.0.1:9000")
	}
	if cfg.API.ExternalBaseURL != "https://ex.com" {
		t.Errorf("API.ExternalBaseURL = %q, expected %q", cfg.API.ExternalBaseURL, "https://ex.com")
	}
	if cfg.API.RequestTimeout != 30*time.Second {
		t.Errorf("API.RequestTimeout = %v, want 30s", cfg.API.RequestTimeout)
	}
	if cfg.API.ChatTimeout != 60*time.Second {
		t.Errorf("API.ChatTimeout = %v, want 60s", cfg.API.ChatTimeout)
	}
	if cfg.API.EventsRetention != 72*time.Hour {
		t.Errorf("API.EventsRetention = %v, want 72h", cfg.API.EventsRetention)
	}
	if len(cfg.API.CORSAllowedOrigins) != 1 || cfg.API.CORSAllowedOrigins[0] != "https://client.example.com" {
		t.Errorf("API.CORSAllowedOrigins = %v, expected [https://client.example.com]", cfg.API.CORSAllowedOrigins)
	}
	if cfg.User.TelegramUserID != 42 {
		t.Errorf("User.TelegramUserID = %d, want 42", cfg.User.TelegramUserID)
	}
}

func TestLoad_APIEnabledRequiresListenAddr(t *testing.T) {
	content := validBase() + `
[api]
enabled = true
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected validation error when enabled=true without listen_addr, got nil")
	}
}

func TestLoad_APIDisabledAllowsEmptyListenAddr(t *testing.T) {
	content := validBase() + `
[api]
enabled = false
`
	if _, err := Load(writeConfig(t, content)); err != nil {
		t.Fatalf("when enabled=false, listen_addr is not required: %v", err)
	}
}

func TestLoad_APIInvalidDurations(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"events_retention невалидное", `events_retention = "7d"`},
		{"events_retention отрицательное", `events_retention = "-1h"`},
		{"request_timeout невалидное", `request_timeout = "forever"`},
		{"request_timeout ноль", `request_timeout = "0"`},
		{"chat_timeout невалидное", `chat_timeout = "abc"`},
		{"chat_timeout отрицательное", `chat_timeout = "-5s"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := validBase() + `
[api]
enabled = false
` + tc.field + `
`
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.field)
			}
		})
	}
}

func TestLoad_InvalidTTL(t *testing.T) {
	content := `
[user]
user_name = "Имя"
telegram_user_id = 1

[ai]
[ai.fast]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.example.com"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"
on_join = "monitor"

[history]
max_messages = 100
ttl = "невалидное-значение"
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected error for invalid TTL, got nil")
	}
}

func TestLoadConfig_PairingDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.API.PairingLinkTTL != 5*time.Minute {
		t.Errorf("API.PairingLinkTTL default = %v, want 5m", cfg.API.PairingLinkTTL)
	}
	if cfg.API.PairingStatusLongPoll != 60*time.Second {
		t.Errorf("API.PairingStatusLongPoll default = %v, want 60s", cfg.API.PairingStatusLongPoll)
	}
	if cfg.API.RateLimitPairPerHour != 5 {
		t.Errorf("API.RateLimitPairPerHour default = %d, want 5", cfg.API.RateLimitPairPerHour)
	}
}

func TestLoadConfig_PairingOverrides(t *testing.T) {
	content := `
[user]
user_name = "Имя"
telegram_user_id = 99

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"
on_join = "monitor"

[history]
max_messages = 100
ttl = "1h"

[api]
pairing_link_ttl = "10m"
pairing_status_long_poll = "30s"
rate_limit_pair_per_hour = 10
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.API.PairingLinkTTL != 10*time.Minute {
		t.Errorf("API.PairingLinkTTL = %v, want 10m", cfg.API.PairingLinkTTL)
	}
	if cfg.API.PairingStatusLongPoll != 30*time.Second {
		t.Errorf("API.PairingStatusLongPoll = %v, want 30s", cfg.API.PairingStatusLongPoll)
	}
	if cfg.API.RateLimitPairPerHour != 10 {
		t.Errorf("API.RateLimitPairPerHour = %d, want 10", cfg.API.RateLimitPairPerHour)
	}
	if cfg.User.TelegramUserID != 99 {
		t.Errorf("User.TelegramUserID = %d, want 99", cfg.User.TelegramUserID)
	}
}

func TestLoadConfig_PairingValidation(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"pairing_link_ttl отрицательный", `pairing_link_ttl = "-1m"`},
		{"pairing_link_ttl нулевой", `pairing_link_ttl = "0"`},
		{"pairing_link_ttl невалидный", `pairing_link_ttl = "5d"`},
		{"pairing_status_long_poll отрицательный", `pairing_status_long_poll = "-10s"`},
		{"pairing_status_long_poll нулевой", `pairing_status_long_poll = "0"`},
		{"pairing_status_long_poll невалидный", `pairing_status_long_poll = "abc"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := validBase() + `
[api]
enabled = false
` + tc.field + `
`
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatalf("expected validation error for %s, got nil", tc.field)
			}
		})
	}
}

func TestLoadConfig_PushSectionDefaults(t *testing.T) {
	content := validBase() + `
[push]
relay_url = "https://push.example.com"
instance_id = "test-instance"
instance_secret = "secret123"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if !cfg.Push.Enabled() {
		t.Error("Push.Enabled() = false, want true")
	}
	if cfg.Push.Timeout != 10*time.Second {
		t.Errorf("Push.Timeout default = %v, want 10s", cfg.Push.Timeout)
	}
	if cfg.Push.DispatcherInterval != 2*time.Second {
		t.Errorf("Push.DispatcherInterval default = %v, want 2s", cfg.Push.DispatcherInterval)
	}
	if cfg.Push.BatchSize != 32 {
		t.Errorf("Push.BatchSize default = %d, want 32", cfg.Push.BatchSize)
	}
	if cfg.Push.RetryMaxAttempts != 4 {
		t.Errorf("Push.RetryMaxAttempts default = %d, want 4", cfg.Push.RetryMaxAttempts)
	}
}

func TestLoadConfig_PushSectionExplicitValues(t *testing.T) {
	content := validBase() + `
[push]
relay_url = "https://push.example.com"
instance_id = "test-instance"
instance_secret = "secret123"
timeout = "15s"
dispatcher_interval = "5s"
batch_size = 16
retry_max_attempts = 3
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.Push.Timeout != 15*time.Second {
		t.Errorf("Push.Timeout = %v, want 15s", cfg.Push.Timeout)
	}
	if cfg.Push.DispatcherInterval != 5*time.Second {
		t.Errorf("Push.DispatcherInterval = %v, want 5s", cfg.Push.DispatcherInterval)
	}
	if cfg.Push.BatchSize != 16 {
		t.Errorf("Push.BatchSize = %d, want 16", cfg.Push.BatchSize)
	}
	if cfg.Push.RetryMaxAttempts != 3 {
		t.Errorf("Push.RetryMaxAttempts = %d, want 3", cfg.Push.RetryMaxAttempts)
	}
}

func TestLoadConfig_PushSectionPartial_ReturnsError(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name: "только relay_url",
			content: validBase() + `
[push]
relay_url = "https://push.example.com"
`,
		},
		{
			name: "relay_url и instance_id без secret",
			content: validBase() + `
[push]
relay_url = "https://push.example.com"
instance_id = "test"
`,
		},
		{
			name: "только instance_secret",
			content: validBase() + `
[push]
instance_secret = "secret123"
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.content))
			if err == nil {
				t.Fatalf("expected validation error for partial push section (%s), got nil", tc.name)
			}
		})
	}
}

func TestLoadConfig_PushSectionAbsent_Disabled(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success when push section is absent: %v", err)
	}
	if cfg.Push.Enabled() {
		t.Error("Push.Enabled() = true, want false when section is absent")
	}
}

func TestLoadConfig_PushSectionEnvExpansion(t *testing.T) {
	t.Setenv("HUSKWOOT_PUSH_SECRET", "my-secret-value")
	content := validBase() + `
[push]
relay_url = "https://push.example.com"
instance_id = "test-instance"
instance_secret = "${HUSKWOOT_PUSH_SECRET}"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if cfg.Push.InstanceSecret != "my-secret-value" {
		t.Errorf("Push.InstanceSecret = %q, want %q", cfg.Push.InstanceSecret, "my-secret-value")
	}
}

func TestLoad_ConfirmTimeout_Default(t *testing.T) {
	cfg, err := Load(writeConfig(t, validBase()))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	got := cfg.Channels.Telegram.ConfirmTimeout
	if got != 1*time.Minute {
		t.Errorf("ConfirmTimeout = %v, want 1m", got)
	}
}

func TestLoad_ConfirmTimeout_Custom(t *testing.T) {
	content := `
[user]
telegram_user_id = 987654321

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "sk-fast-key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "sk-smart-key"
model = "gpt-4o"

[channels.telegram]
token = "tg-bot-token"
confirm_timeout = "2m30s"

[history]
max_messages = 200
ttl = "24h"
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	got := cfg.Channels.Telegram.ConfirmTimeout
	want := 2*time.Minute + 30*time.Second
	if got != want {
		t.Errorf("ConfirmTimeout = %v, want %v", got, want)
	}
}

func TestLoad_ConfirmTimeout_Negative(t *testing.T) {
	content := `
[user]
telegram_user_id = 987654321

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "sk-fast-key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "sk-smart-key"
model = "gpt-4o"

[channels.telegram]
token = "tg-bot-token"
confirm_timeout = "-1m"

[history]
max_messages = 200
ttl = "24h"
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("expected error for negative confirm_timeout, got nil")
	}
}

func TestLoad_UserLanguage(t *testing.T) {
	cases := []struct {
		name     string
		lang     string
		wantLang string
		wantErr  bool
	}{
		{"empty defaults to ru", "", "ru", false},
		{"explicit ru", "ru", "ru", false},
		{"explicit en", "en", "en", false},
		{"unknown fr returns error", "fr", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			langLine := ""
			if tc.lang != "" {
				langLine = `language = "` + tc.lang + `"`
			}
			content := `
[user]
telegram_user_id = 1
` + langLine + `

[ai]
[ai.fast]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o-mini"

[ai.smart]
base_url = "https://api.openai.com/v1"
api_key = "key"
model = "gpt-4o"

[channels.telegram]
token = "token"

[history]
max_messages = 100
ttl = "1h"
`
			cfg, err := Load(writeConfig(t, content))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for language=%q, got nil", tc.lang)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success for language=%q, got: %v", tc.lang, err)
			}
			if cfg.User.Language != tc.wantLang {
				t.Errorf("User.Language = %q, want %q", cfg.User.Language, tc.wantLang)
			}
		})
	}
}
