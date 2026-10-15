package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"

	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
)

// SetTaskCandidates configures durable automatic-task questions before Watch starts.
func (w *TelegramChannel) SetTaskCandidates(service model.AutoTaskService) {
	w.taskCandidates = service
	w.taskLocalizer = w.cfg.Localizer
	if w.taskLocalizer == nil {
		bundle, err := huskwootI18n.NewBundle(w.cfg.Language)
		if err != nil {
			slog.Error("loading task question translations", "error", err)
			return
		}
		w.taskLocalizer = huskwootI18n.NewLocalizer(bundle, w.cfg.Language)
	}
}

func (w *TelegramChannel) taskQuestionText(key string, data any) string {
	if w.taskLocalizer == nil {
		return ""
	}
	return huskwootI18n.Translate(w.taskLocalizer, key, data)
}

func parseTaskCallback(data string) (string, bool, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "task" || (parts[1] != "yes" && parts[1] != "no") {
		return "", false, false
	}
	id, err := uuid.Parse(parts[2])
	if err != nil || id.String() != parts[2] {
		return "", false, false
	}
	return parts[2], parts[1] == "yes", true
}

func (w *TelegramChannel) handleTaskCallback(ctx context.Context, callback *tgbotapi.CallbackQuery) bool {
	answer := w.taskQuestionText("auto_tasks.unavailable", nil)
	defer func() {
		params := tgbotapi.Params{"callback_query_id": callback.ID}
		if answer != "" {
			params["text"] = answer
		}
		if _, err := w.bot.MakeRequest("answerCallbackQuery", params); err != nil {
			slog.WarnContext(ctx, "answering task callback", "error", err)
		}
	}()
	candidateID, approve, valid := parseTaskCallback(callback.Data)
	if !valid || w.taskCandidates == nil || callback.From == nil || callback.Message == nil || callback.Message.Chat == nil {
		return true
	}
	message := callback.Message
	ownerID := strconv.FormatInt(callback.From.ID, 10)
	if _, owner := w.ownerIDs[ownerID]; !owner || message.Chat.Type != "private" || message.Chat.ID != callback.From.ID {
		return true
	}
	if message.From == nil || !message.From.IsBot || (w.botID != 0 && message.From.ID != w.botID) {
		return true
	}
	candidate, err := w.taskCandidates.GetCandidate(ctx, candidateID)
	if err != nil {
		slog.ErrorContext(ctx, "loading task callback candidate", "error", err, "candidate_id", candidateID)
		answer = w.taskQuestionText("auto_tasks.retry", nil)
		return false
	}
	if candidate == nil || candidate.ID != candidateID || !taskQuestionMatches(*candidate, message, callback.Data) {
		return true
	}
	decision, err := w.taskCandidates.Decide(ctx, candidateID, approve)
	if err != nil {
		slog.ErrorContext(ctx, "deciding task candidate", "error", err, "candidate_id", candidateID)
		answer = w.taskQuestionText("auto_tasks.retry", nil)
		return false
	}
	key := "auto_tasks.rejected"
	if decision.Candidate.Status == "created" {
		key = "auto_tasks.created"
	}
	answer = w.taskQuestionText(key, nil)
	summary := taskQuestionExcerpt(decision.Candidate.Task.Summary, 1000)
	if decision.Candidate.Status == "created" {
		ref := decision.Candidate.CreatedTaskID
		if decision.Task != nil {
			ref = decision.Task.DisplayID()
		}
		if ref != "" {
			summary = ref + ": " + summary
		}
	}
	text := answer + "\n" + summary
	if _, err := w.bot.MakeRequest("editMessageText", tgbotapi.Params{
		"chat_id": strconv.FormatInt(message.Chat.ID, 10), "message_id": strconv.Itoa(message.MessageID),
		"text": text, "reply_markup": `{"inline_keyboard":[]}`,
	}); err != nil {
		slog.WarnContext(ctx, "updating decided task question", "error", err, "candidate_id", candidateID)
	}
	return true
}

func taskQuestionMatches(candidate model.TaskCandidate, message *tgbotapi.Message, data string) bool {
	if candidate.QuestionChatID != 0 && candidate.QuestionChatID != message.Chat.ID {
		return false
	}
	if candidate.QuestionChatID == message.Chat.ID && candidate.QuestionMsgID == message.MessageID {
		return true
	}
	// A crash after send and before MarkQuestionSent can leave an older usable
	// question. Its bot-authored keyboard proves which stable candidate it asks about.
	if message.ReplyMarkup != nil {
		for _, row := range message.ReplyMarkup.InlineKeyboard {
			for _, button := range row {
				if button.CallbackData != nil && *button.CallbackData == data {
					return true
				}
			}
		}
	}
	return false
}

// RunTaskQuestions delivers durable pending questions immediately and every five seconds.
// Failed sends or delivery records leave the question pending for the next attempt.
func (w *TelegramChannel) RunTaskQuestions(ctx context.Context) error {
	if w.taskCandidates == nil {
		return nil
	}
	if w.taskLocalizer == nil {
		return fmt.Errorf("task question localizer is unavailable")
	}
	var ownerID int64
	for _, id := range w.cfg.OwnerIDs {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err == nil && parsed > 0 {
			ownerID = parsed
			break
		}
	}
	if ownerID == 0 {
		return fmt.Errorf("task questions require an owner private chat")
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.sendTaskQuestions(ctx, ownerID); err != nil {
			slog.ErrorContext(ctx, "delivering task questions", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *TelegramChannel) sendTaskQuestions(ctx context.Context, ownerID int64) error {
	candidates, err := w.taskCandidates.PendingQuestions(ctx, 100)
	if err != nil {
		return fmt.Errorf("listing pending task questions: %w", err)
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		message := tgbotapi.NewMessage(ownerID, w.formatTaskQuestion(candidate))
		message.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(w.taskQuestionText("auto_tasks.approve", nil), "task:yes:"+candidate.ID),
			tgbotapi.NewInlineKeyboardButtonData(w.taskQuestionText("auto_tasks.reject", nil), "task:no:"+candidate.ID),
		))
		sent, err := w.bot.Send(message)
		if err != nil {
			return fmt.Errorf("sending task question %s: %w", candidate.ID, err)
		}
		if err := w.taskCandidates.MarkQuestionSent(ctx, candidate.ID, ownerID, sent.MessageID); err != nil {
			return fmt.Errorf("recording sent task question %s: %w", candidate.ID, err)
		}
	}
	return nil
}

func (w *TelegramChannel) formatTaskQuestion(candidate model.TaskCandidate) string {
	reason := candidate.Reason
	if reason == "duplicate_check_unavailable" || reason == "" {
		reason = w.taskQuestionText("auto_tasks.comparison_unavailable", nil)
	}
	matchRef := candidate.MatchDisplayID
	if matchRef == "" {
		matchRef = candidate.MatchID
	}
	var deadline string
	if candidate.Task.Deadline != nil {
		deadline = candidate.Task.Deadline.Format(time.RFC3339)
	}
	return w.taskQuestionText("auto_tasks.question", struct {
		Summary, Details, Deadline, Project, MatchSummary, MatchRef, Reason string
	}{
		Summary:      taskQuestionExcerpt(candidate.Task.Summary, 600),
		Details:      taskQuestionExcerpt(candidate.Task.Details, 1000),
		Deadline:     deadline,
		Project:      taskQuestionExcerpt(candidate.Task.ProjectID, 128),
		MatchSummary: taskQuestionExcerpt(candidate.MatchSummary, 600),
		MatchRef:     taskQuestionExcerpt(matchRef, 128),
		Reason:       taskQuestionExcerpt(reason, 600),
	})
}

func taskQuestionExcerpt(text string, limit int) string {
	units := 0
	for offset, char := range text {
		width := 1
		if char > 0xffff {
			width = 2
		}
		if units+width > limit {
			return text[:offset] + "…"
		}
		units += width
	}
	return text
}
