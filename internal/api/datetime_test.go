package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/devices"
)

type dtConfigBody struct {
	Timezone  string   `json:"timezone"`
	Weekdays  []string `json:"weekdays"`
	TimeOfDay struct {
		Morning   string `json:"morning"`
		Lunch     string `json:"lunch"`
		Afternoon string `json:"afternoon"`
		Evening   string `json:"evening"`
	} `json:"timeOfDay"`
	DefaultTimeOfDay   string `json:"defaultTimeOfDay"`
	NightCutoffMinutes int    `json:"nightCutoffMinutes"`
}

func defaultDTConfig() config.DateTimeConfig {
	return config.DateTimeConfig{
		Timezone: "Europe/Moscow",
		Weekdays: []string{"mon", "tue", "wed", "thu", "fri"},
		TimeOfDay: config.TimeOfDayConfig{
			Morning:   11,
			Lunch:     12,
			Afternoon: 14,
			Evening:   20,
			Default:   "evening",
		},
		NightCutoffMinutes: 0,
	}
}

func newDTTestServer(t *testing.T, dt config.DateTimeConfig) (*api.Server, string) {
	t.Helper()
	db := openTestDB(t)
	store := devices.NewSQLiteDeviceStore(db)
	token := "dt-config-test-token"
	createTestDevice(t, db, "test-device", token)
	srv := api.New(api.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Devices:  store,
		DateTime: dt,
	})
	return srv, token
}

func doGetDTConfig(t *testing.T, srv *api.Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/datetime/config", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeDTConfig(t *testing.T, rec *httptest.ResponseRecorder) dtConfigBody {
	t.Helper()
	var body dtConfigBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	return body
}

func TestGetDateTimeConfig_HappyPath_DefaultConfig(t *testing.T) {
	srv, token := newDTTestServer(t, defaultDTConfig())
	rec := doGetDTConfig(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeDTConfig(t, rec)
	if body.Timezone != "Europe/Moscow" {
		t.Errorf("timezone = %q, want %q", body.Timezone, "Europe/Moscow")
	}
	if body.TimeOfDay.Morning != "11:00" {
		t.Errorf("morning = %q, want %q", body.TimeOfDay.Morning, "11:00")
	}
	if body.TimeOfDay.Lunch != "12:00" {
		t.Errorf("lunch = %q, want %q", body.TimeOfDay.Lunch, "12:00")
	}
	if body.TimeOfDay.Afternoon != "14:00" {
		t.Errorf("afternoon = %q, want %q", body.TimeOfDay.Afternoon, "14:00")
	}
	if body.TimeOfDay.Evening != "20:00" {
		t.Errorf("evening = %q, want %q", body.TimeOfDay.Evening, "20:00")
	}
	if body.DefaultTimeOfDay != "evening" {
		t.Errorf("defaultTimeOfDay = %q, want %q", body.DefaultTimeOfDay, "evening")
	}
	if body.NightCutoffMinutes != 0 {
		t.Errorf("nightCutoffMinutes = %d, want 0", body.NightCutoffMinutes)
	}
	if len(body.Weekdays) != 5 {
		t.Errorf("weekdays count = %d, want 5", len(body.Weekdays))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want %q", cc, "public, max-age=3600")
	}
}

func TestGetDateTimeConfig_CustomHours(t *testing.T) {
	dt := config.DateTimeConfig{
		TimeOfDay: config.TimeOfDayConfig{
			Morning:   9,
			Lunch:     13,
			Afternoon: 16,
			Evening:   22,
			Default:   "afternoon",
		},
	}
	srv, token := newDTTestServer(t, dt)
	rec := doGetDTConfig(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeDTConfig(t, rec)
	if body.TimeOfDay.Morning != "09:00" {
		t.Errorf("morning = %q, want %q", body.TimeOfDay.Morning, "09:00")
	}
	if body.TimeOfDay.Lunch != "13:00" {
		t.Errorf("lunch = %q, want %q", body.TimeOfDay.Lunch, "13:00")
	}
	if body.TimeOfDay.Afternoon != "16:00" {
		t.Errorf("afternoon = %q, want %q", body.TimeOfDay.Afternoon, "16:00")
	}
	if body.TimeOfDay.Evening != "22:00" {
		t.Errorf("evening = %q, want %q", body.TimeOfDay.Evening, "22:00")
	}
}

func TestGetDateTimeConfig_CustomWeekdays(t *testing.T) {
	dt := config.DateTimeConfig{
		Weekdays: []string{"mon", "wed", "fri"},
		TimeOfDay: config.TimeOfDayConfig{
			Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: "evening",
		},
	}
	srv, token := newDTTestServer(t, dt)
	rec := doGetDTConfig(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeDTConfig(t, rec)
	if len(body.Weekdays) != 3 {
		t.Errorf("weekdays count = %d, want 3; got %v", len(body.Weekdays), body.Weekdays)
	}
	wdSet := make(map[string]bool)
	for _, d := range body.Weekdays {
		wdSet[d] = true
	}
	for _, expected := range []string{"mon", "wed", "fri"} {
		if !wdSet[expected] {
			t.Errorf("missing weekday %q in %v", expected, body.Weekdays)
		}
	}
}

func TestGetDateTimeConfig_CustomDefaultTimeOfDay(t *testing.T) {
	dt := config.DateTimeConfig{
		TimeOfDay: config.TimeOfDayConfig{
			Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: "morning",
		},
	}
	srv, token := newDTTestServer(t, dt)
	rec := doGetDTConfig(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeDTConfig(t, rec)
	if body.DefaultTimeOfDay != "morning" {
		t.Errorf("defaultTimeOfDay = %q, want %q", body.DefaultTimeOfDay, "morning")
	}
}

func TestGetDateTimeConfig_NightCutoffMinutes(t *testing.T) {
	dt := config.DateTimeConfig{
		TimeOfDay: config.TimeOfDayConfig{
			Morning: 11, Lunch: 12, Afternoon: 14, Evening: 20, Default: "evening",
		},
		NightCutoffMinutes: 300,
	}
	srv, token := newDTTestServer(t, dt)
	rec := doGetDTConfig(t, srv, token)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeDTConfig(t, rec)
	if body.NightCutoffMinutes != 300 {
		t.Errorf("nightCutoffMinutes = %d, want 300", body.NightCutoffMinutes)
	}
}

func TestGetDateTimeConfig_UnauthorizedReturns401(t *testing.T) {
	srv, _ := newDTTestServer(t, defaultDTConfig())
	rec := doGetDTConfig(t, srv, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Error.Code != api.ErrorCodeUnauthorized {
		t.Errorf("code = %q, want %q", body.Error.Code, api.ErrorCodeUnauthorized)
	}
}
