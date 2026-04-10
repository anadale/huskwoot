package sink

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

const telegramSummaryMaxLen = 4096

// TelegramSummaryDeliverer delivers the periodic task summary as a Telegram DM.
type TelegramSummaryDeliverer struct {
	bot    *tgbotapi.BotAPI
	chatID int64
	loc    *goI18n.Localizer
}

// NewTelegramSummaryDeliverer creates a TelegramSummaryDeliverer with the given bot, chat ID, and localizer.
func NewTelegramSummaryDeliverer(bot *tgbotapi.BotAPI, chatID int64, loc *goI18n.Localizer) *TelegramSummaryDeliverer {
	return &TelegramSummaryDeliverer{bot: bot, chatID: chatID, loc: loc}
}

// Name returns the deliverer name for logging.
func (d *TelegramSummaryDeliverer) Name() string { return "telegram-summary" }

// Deliver formats and sends the summary to Telegram.
func (d *TelegramSummaryDeliverer) Deliver(_ context.Context, summary model.Summary) error {
	var text string
	if summary.IsEmpty {
		text = formatEmptySummary(d.loc, summary)
	} else {
		text = truncate(d.loc, formatSummary(d.loc, summary), telegramSummaryMaxLen)
	}
	msg := tgbotapi.NewMessage(d.chatID, text)
	if _, err := d.bot.Send(msg); err != nil {
		return fmt.Errorf("sending summary: %w", err)
	}
	return nil
}

func summarySlotEmoji(slot string) string {
	switch slot {
	case "morning":
		return "🌅"
	case "afternoon":
		return "☀️"
	case "evening":
		return "🌙"
	default:
		return "📋"
	}
}

func summarySlotTitle(loc *goI18n.Localizer, slot string) string {
	switch slot {
	case "morning":
		return huskwootI18n.Translate(loc, "summary_morning_title", nil)
	case "afternoon":
		return huskwootI18n.Translate(loc, "summary_afternoon_title", nil)
	case "evening":
		return huskwootI18n.Translate(loc, "summary_evening_title", nil)
	default:
		return huskwootI18n.Translate(loc, "summary_generic_title", nil)
	}
}

func summaryHeader(loc *goI18n.Localizer, s model.Summary) string {
	return fmt.Sprintf("%s %s — %s",
		summarySlotEmoji(s.Slot),
		summarySlotTitle(loc, s.Slot),
		s.GeneratedAt.Format("02.01.2006"),
	)
}

func formatEmptySummary(loc *goI18n.Localizer, s model.Summary) string {
	return summaryHeader(loc, s) + "\n\n" + huskwootI18n.Translate(loc, "summary_empty", nil)
}

func formatSectionHeader(loc *goI18n.Localizer, bucket string, undatedShown, undatedTotal int) string {
	switch bucket {
	case "overdue":
		return huskwootI18n.Translate(loc, "section_overdue", nil)
	case "today":
		return huskwootI18n.Translate(loc, "section_today", nil)
	case "upcoming":
		return huskwootI18n.Translate(loc, "section_upcoming", nil)
	case "undated":
		if undatedTotal > undatedShown {
			return huskwootI18n.Translate(loc, "section_undated_limited", map[string]any{
				"Shown": undatedShown,
				"Total": undatedTotal,
			})
		}
		return huskwootI18n.Translate(loc, "section_undated", nil)
	default:
		return bucket
	}
}

func formatTaskLine(loc *goI18n.Localizer, bucket string, t model.Task) string {
	var sb strings.Builder
	sb.WriteString("    — ")
	sb.WriteString(strings.ReplaceAll(t.Summary, "\n", " "))

	if t.Deadline != nil {
		switch bucket {
		case "overdue":
			fmt.Fprintf(&sb, " %s", huskwootI18n.Translate(loc, "task_overdue_since", map[string]any{
				"Date": t.Deadline.Format("02.01"),
			}))
		case "today":
			fmt.Fprintf(&sb, " 📅 %s", t.Deadline.Format("15:04"))
		case "upcoming":
			fmt.Fprintf(&sb, " 📅 %s", t.Deadline.Format("02.01"))
		}
	}

	if t.Topic != "" {
		fmt.Fprintf(&sb, " · #%s", t.Topic)
	}

	return sb.String()
}

func countGroupTasks(groups []model.ProjectGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.Tasks)
	}
	return n
}

func formatSection(loc *goI18n.Localizer, bucket string, groups []model.ProjectGroup, undatedTotal int) string {
	if len(groups) == 0 {
		return ""
	}

	shown := countGroupTasks(groups)
	var sb strings.Builder
	sb.WriteString(formatSectionHeader(loc, bucket, shown, undatedTotal))
	sb.WriteByte('\n')

	for _, group := range groups {
		fmt.Fprintf(&sb, "  [%s]\n", group.ProjectName)
		for _, task := range group.Tasks {
			sb.WriteString(formatTaskLine(loc, bucket, task))
			sb.WriteByte('\n')
		}
	}

	return sb.String()
}

func formatSummary(loc *goI18n.Localizer, s model.Summary) string {
	type section struct {
		bucket string
		groups []model.ProjectGroup
	}
	sections := []section{
		{"overdue", s.Overdue},
		{"today", s.Today},
		{"upcoming", s.Upcoming},
		{"undated", s.Undated},
	}

	var sb strings.Builder
	sb.WriteString(summaryHeader(loc, s))

	for _, sec := range sections {
		text := formatSection(loc, sec.bucket, sec.groups, s.UndatedTotal)
		if text != "" {
			sb.WriteString("\n\n")
			sb.WriteString(strings.TrimRight(text, "\n"))
		}
	}

	return sb.String()
}

// truncate trims text to limit Unicode code points by removing task lines from the end
// and appending a tail. Guarantees len(result) <= limit.
func truncate(loc *goI18n.Localizer, text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}

	lines := strings.Split(text, "\n")

	// Collect indices of task lines (those starting with "    — ")
	var taskIndices []int
	for i, line := range lines {
		if strings.HasPrefix(line, "    — ") {
			taskIndices = append(taskIndices, i)
		}
	}

	active := make([]bool, len(lines))
	for i := range active {
		active[i] = true
	}

	rebuild := func(removed int) string {
		var sb strings.Builder
		for i, line := range lines {
			if active[i] {
				sb.WriteString(line)
				sb.WriteByte('\n')
			}
		}
		result := strings.TrimRight(sb.String(), "\n")
		if removed > 0 {
			result += "\n" + huskwootI18n.Translate(loc, "tasks_truncated", nil, removed)
		}
		return result
	}

	removed := 0
	for len(taskIndices) > 0 {
		lastIdx := taskIndices[len(taskIndices)-1]
		taskIndices = taskIndices[:len(taskIndices)-1]
		active[lastIdx] = false
		removed++

		result := rebuild(removed)
		if utf8.RuneCountInString(result) <= limit {
			return result
		}
	}

	// All task lines removed but text still exceeds limit (should not happen in practice)
	return rebuild(removed)
}
