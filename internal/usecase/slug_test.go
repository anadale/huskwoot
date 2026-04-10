package usecase_test

import (
	"testing"

	"github.com/anadale/huskwoot/internal/usecase"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"latin", "Hello World", "hello-world"},
		{"cyrillic_simple", "Проект", "proekt"},
		{"cyrillic_phrase", "На Старт!", "na-start"},
		{"mixed", "Проект NA-Старт", "proekt-na-start"},
		{"yo", "Ёлка", "yolka"},
		{"hard_soft_signs", "объявление", "obyavlenie"},
		{"sch_zh_ts", "Щука Жук Цветок", "schuka-zhuk-tsvetok"},
		{"digits", "Проект 2026", "proekt-2026"},
		{"trim_dashes", "  ---hello---  ", "hello"},
		{"empty_fallback", "!!!", "project"},
		{"only_spaces_fallback", "   ", "project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usecase.Slugify(tc.in)
			if got != tc.want {
				t.Fatalf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
