package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// FormatForwardBatch formats a slice of forwarded messages as a conversation
// block suitable for LLM input. Messages are sorted by OriginalDate ascending,
// with ReceivedAt as a stable tie-breaker.
//
// Format:
//
//	Переписка между мной и {counterpart}:
//
//	[я, YYYY-MM-DD HH:MM] text
//	[Name, YYYY-MM-DD HH:MM] text
func FormatForwardBatch(forwards []model.ForwardEntry, counterpart string, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}

	sorted := make([]model.ForwardEntry, len(forwards))
	copy(sorted, forwards)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].OriginalDate.Equal(sorted[j].OriginalDate) {
			return sorted[i].ReceivedAt.Before(sorted[j].ReceivedAt)
		}
		return sorted[i].OriginalDate.Before(sorted[j].OriginalDate)
	})

	var header string
	if counterpart == "" {
		header = "Переписка с неизвестным контактом:"
	} else {
		header = fmt.Sprintf("Переписка между мной и %s:", counterpart)
	}

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("\n")
	for _, e := range sorted {
		sb.WriteString("\n")

		var author string
		if e.IsSelfAuthored {
			author = "я"
		} else if e.ForwardFromName != "" {
			author = e.ForwardFromName
		} else {
			author = "unknown"
		}

		ts := e.OriginalDate.In(loc).Format("2006-01-02 15:04")
		sb.WriteString(fmt.Sprintf("[%s, %s] ", author, ts))

		if e.Text == "" {
			sb.WriteString("[голосовое/вложение]")
		} else {
			sb.WriteString(e.Text)
		}
	}

	return sb.String()
}
