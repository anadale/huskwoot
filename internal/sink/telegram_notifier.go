package sink

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	goI18n "github.com/nicksnyder/go-i18n/v2/i18n"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// TelegramNotifier sends task notifications via the Telegram Bot API.
type TelegramNotifier struct {
	bot    *tgbotapi.BotAPI
	chatID int64
	loc    *goI18n.Localizer
}

// NewTelegramNotifier creates a new TelegramNotifier with the given bot, chat ID, and localizer.
func NewTelegramNotifier(bot *tgbotapi.BotAPI, chatID int64, loc *goI18n.Localizer) *TelegramNotifier {
	return &TelegramNotifier{bot: bot, chatID: chatID, loc: loc}
}

// Name returns the notifier name for logging.
func (n *TelegramNotifier) Name() string { return "telegram-dm" }

// Notify sends a single DM with the task list. Does nothing when the list is empty.
func (n *TelegramNotifier) Notify(_ context.Context, tasks []model.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	msg := tgbotapi.NewMessage(n.chatID, formatTaskMessage(n.loc, tasks))
	if _, err := n.bot.Send(msg); err != nil {
		return fmt.Errorf("sending notification: %w", err)
	}
	return nil
}

// formatTaskMessage builds the notification text for a batch of tasks.
func formatTaskMessage(loc *goI18n.Localizer, tasks []model.Task) string {
	var sb strings.Builder
	sb.WriteString(huskwootI18n.Translate(loc, "tasks_created_header", nil))
	sb.WriteString("\n\n")

	first := tasks[0]
	if first.Source.Kind == "imap" {
		if first.SourceMessage.Subject != "" {
			if first.ProjectSlug != "" {
				fmt.Fprintf(&sb, "%s\n", huskwootI18n.Translate(loc, "source_with_project", map[string]any{
					"Subject": first.SourceMessage.Subject,
					"Slug":    first.ProjectSlug,
				}))
			} else {
				fmt.Fprintf(&sb, "%s\n", huskwootI18n.Translate(loc, "source_no_project", map[string]any{
					"Name": first.SourceMessage.Subject,
				}))
			}
		} else if first.ProjectSlug != "" {
			fmt.Fprintf(&sb, "%s\n", huskwootI18n.Translate(loc, "source_no_project", map[string]any{
				"Name": first.ProjectSlug,
			}))
		} else {
			fmt.Fprintf(&sb, "%s\n", huskwootI18n.Translate(loc, "source_no_project", map[string]any{
				"Name": first.Source.Name,
			}))
		}
	} else {
		fmt.Fprintf(&sb, "%s\n", huskwootI18n.Translate(loc, "source_with_project", map[string]any{
			"Subject": first.Source.Name,
			"Slug":    first.Source.Kind,
		}))
	}

	for _, task := range tasks {
		sb.WriteString("\n")
		summary := strings.ReplaceAll(task.Summary, "\n", " ")
		if task.Deadline != nil {
			fmt.Fprintf(&sb, "- %s 📅 %s\n", summary, task.Deadline.Format("02.01.2006"))
		} else {
			fmt.Fprintf(&sb, "- %s\n", summary)
		}
		if task.Details != "" {
			ctx := strings.ReplaceAll(task.Details, "\n", " ")
			fmt.Fprintf(&sb, "  %s\n", huskwootI18n.Translate(loc, "context_label", map[string]any{"Context": ctx}))
		}
	}

	return sb.String()
}
