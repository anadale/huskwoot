package pipeline

import (
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

func TestFormatForwardBatch(t *testing.T) {
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 25, 14, 30, 0, 0, time.UTC)

	entry := func(selfAuthored bool, name string, offsetMin int, text string) model.ForwardEntry {
		return model.ForwardEntry{
			IsSelfAuthored:  selfAuthored,
			ForwardFromName: name,
			OriginalDate:    base.Add(time.Duration(offsetMin) * time.Minute),
			ReceivedAt:      base,
			Text:            text,
		}
	}

	tests := []struct {
		name        string
		forwards    []model.ForwardEntry
		counterpart string
		loc         *time.Location
		want        string
	}{
		{
			name:        "single self-forward",
			forwards:    []model.ForwardEntry{entry(true, "", 0, "Завтра пришлю слайды.")},
			counterpart: "Вася",
			loc:         time.UTC,
			want: "Переписка между мной и Вася:\n\n[я, 2026-04-25 14:30] Завтра пришлю слайды.",
		},
		{
			name:        "single counterpart forward",
			forwards:    []model.ForwardEntry{entry(false, "Вася", 2, "Спасибо.")},
			counterpart: "Вася",
			loc:         time.UTC,
			want: "Переписка между мной и Вася:\n\n[Вася, 2026-04-25 14:32] Спасибо.",
		},
		{
			name: "mixed conversation sorted by date",
			forwards: []model.ForwardEntry{
				entry(false, "Вася", 2, "Спасибо."),
				entry(true, "", 0, "Завтра пришлю слайды."),
			},
			counterpart: "Вася",
			loc:         time.UTC,
			want: "Переписка между мной и Вася:\n\n[я, 2026-04-25 14:30] Завтра пришлю слайды.\n[Вася, 2026-04-25 14:32] Спасибо.",
		},
		{
			name:        "hidden privacy - counterpart empty",
			forwards:    []model.ForwardEntry{entry(false, "Неизвестный", 0, "Хорошо.")},
			counterpart: "",
			loc:         time.UTC,
			want: "Переписка с неизвестным контактом:\n\n[Неизвестный, 2026-04-25 14:30] Хорошо.",
		},
		{
			name:        "empty text becomes marker",
			forwards:    []model.ForwardEntry{entry(true, "", 0, "")},
			counterpart: "Маша",
			loc:         time.UTC,
			want: "Переписка между мной и Маша:\n\n[я, 2026-04-25 14:30] [голосовое/вложение]",
		},
		{
			name: "timezone applied to timestamps",
			forwards: []model.ForwardEntry{
				entry(true, "", 0, "Текст."),
			},
			counterpart: "Петя",
			loc:         moscow,
			// UTC 14:30 → MSK is UTC+3 → 17:30
			want: "Переписка между мной и Петя:\n\n[я, 2026-04-25 17:30] Текст.",
		},
		{
			name: "unknown forward from name falls back to unknown",
			forwards: []model.ForwardEntry{
				{
					IsSelfAuthored:  false,
					ForwardFromName: "",
					OriginalDate:    base,
					ReceivedAt:      base,
					Text:            "Привет.",
				},
			},
			counterpart: "Кто-то",
			loc:         time.UTC,
			want: "Переписка между мной и Кто-то:\n\n[unknown, 2026-04-25 14:30] Привет.",
		},
		{
			name: "stable sort: same original date ordered by received at",
			forwards: []model.ForwardEntry{
				{
					IsSelfAuthored: true,
					OriginalDate:   base,
					ReceivedAt:     base.Add(2 * time.Second),
					Text:           "Второй.",
				},
				{
					IsSelfAuthored: true,
					OriginalDate:   base,
					ReceivedAt:     base.Add(1 * time.Second),
					Text:           "Первый.",
				},
			},
			counterpart: "X",
			loc:         time.UTC,
			want: "Переписка между мной и X:\n\n[я, 2026-04-25 14:30] Первый.\n[я, 2026-04-25 14:30] Второй.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatForwardBatch(tc.forwards, tc.counterpart, tc.loc)
			if got != tc.want {
				t.Errorf("got:\n%s\n\nwant:\n%s", got, tc.want)
			}
		})
	}
}
