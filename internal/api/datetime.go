package api

import (
	"fmt"
	"net/http"
	"strings"
)

type dateTimeConfigTimeOfDay struct {
	Morning   string `json:"morning"`
	Lunch     string `json:"lunch"`
	Afternoon string `json:"afternoon"`
	Evening   string `json:"evening"`
}

type dateTimeConfigResponse struct {
	Timezone           string                  `json:"timezone"`
	Weekdays           []string                `json:"weekdays"`
	TimeOfDay          dateTimeConfigTimeOfDay `json:"timeOfDay"`
	DefaultTimeOfDay   string                  `json:"defaultTimeOfDay"`
	NightCutoffMinutes int                     `json:"nightCutoffMinutes"`
}

func (s *Server) getDateTimeConfig(w http.ResponseWriter, r *http.Request) {
	dt := s.cfg.DateTime
	resp := dateTimeConfigResponse{
		Timezone: normalizeTimezone(dt.Timezone),
		Weekdays: normalizeWeekdays(dt.Weekdays),
		TimeOfDay: dateTimeConfigTimeOfDay{
			Morning:   formatHour(dt.TimeOfDay.Morning),
			Lunch:     formatHour(dt.TimeOfDay.Lunch),
			Afternoon: formatHour(dt.TimeOfDay.Afternoon),
			Evening:   formatHour(dt.TimeOfDay.Evening),
		},
		DefaultTimeOfDay:   dt.TimeOfDay.Default,
		NightCutoffMinutes: dt.NightCutoffMinutes,
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, resp)
}

func formatHour(h int) string {
	return fmt.Sprintf("%02d:00", h)
}

func normalizeWeekdays(days []string) []string {
	if len(days) == 0 {
		return []string{"mon", "tue", "wed", "thu", "fri"}
	}
	result := make([]string, len(days))
	for i, d := range days {
		s := strings.ToLower(d)
		if len(s) > 3 {
			s = s[:3]
		}
		result[i] = s
	}
	return result
}

func normalizeTimezone(tz string) string {
	if tz == "" {
		return "Europe/Moscow"
	}
	return tz
}
