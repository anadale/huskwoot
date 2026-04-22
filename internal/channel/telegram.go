// Package channel contains channel implementations.
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/anadale/huskwoot/internal/model"
)

// botAPI abstracts the Telegram Bot API for testing without a real bot.
type botAPI interface {
	GetUpdates(config tgbotapi.UpdateConfig) ([]tgbotapi.Update, error)
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	MakeRequest(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error)
}

// reactionTypeEmoji describes an emoji reaction in Telegram Bot API format.
type reactionTypeEmoji struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

// rawUpdate is used to parse updates from getUpdates, including message_reaction
// which is not natively supported in tgbotapi v5.
type rawUpdate struct {
	UpdateID        int                         `json:"update_id"`
	Message         *tgbotapi.Message           `json:"message"`
	EditedMessage   *tgbotapi.Message           `json:"edited_message"`
	MyChatMember    *tgbotapi.ChatMemberUpdated `json:"my_chat_member"`
	MessageReaction *rawMessageReaction         `json:"message_reaction"`
}

// rawMessageReaction contains the subset of MessageReactionUpdated fields needed for guard.
type rawMessageReaction struct {
	Chat      tgbotapi.Chat  `json:"chat"`
	MessageID int            `json:"message_id"`
	User      *tgbotapi.User `json:"user"`
	Date      int            `json:"date"`
}

// pendingApproval holds the confirmation-pending state for a chat.
type pendingApproval struct {
	welcomeMsgID int
	deadline     time.Time
}

// TelegramChannelConfig holds configuration for TelegramChannel.
type TelegramChannelConfig struct {
	// ID is the unique account identifier for the StateStore key.
	// If empty, "telegram" is used as the key (backward compatibility for single accounts).
	ID string
	// OwnerIDs are the numeric Telegram user IDs of the owners. Used to identify
	// DM messages: a private chat from a user in this list becomes a DM source.
	OwnerIDs []string
	// OnJoin controls behavior on startup: "backfill" (fetch history) or "monitor" (new only).
	OnJoin string
	// ReactionEnabled — if true, ReactFn is set for each message, allowing the pipeline
	// to set emoji reactions. If false, ReactFn is nil.
	ReactionEnabled bool
	// BotID is the numeric bot user ID (as a string). Used to detect replies to bot messages:
	// if ReplyToMessage.From.ID matches, the message gets Kind=GroupDirect.
	// If empty, reply detection is skipped.
	BotID string
	// BotUsername is the bot username without the @ sign (e.g. "myhuskwootbot").
	// Used to detect @mention of the bot in message text.
	// If empty, mention detection is skipped.
	BotUsername string
	// WelcomeMessage is sent when the bot is added to a new group.
	// If empty, a language-aware default is used (see guardMessages).
	WelcomeMessage string
	// ConfirmTimeout is how long to wait for owner confirmation after the bot is added.
	// 0 disables the guard. Default: 1 minute (set in the constructor).
	ConfirmTimeout time.Duration
	// Language is the UI language ("ru" or "en"). Used for default guard messages.
	// Falls back to "ru" if empty or unrecognised.
	Language string
}

// guardMessages holds language-specific guard UI strings.
type guardMessages struct {
	welcome      string
	memberSuffix string
}

var guardMessagesByLang = map[string]guardMessages{
	"ru": {
		welcome:      "👋 Привет! Подтвердите добавление — ответьте на это сообщение или поставьте реакцию.",
		memberSuffix: "\n\n⚠️ Реакции недоступны без прав администратора — пожалуйста, ответьте на это сообщение.",
	},
	"en": {
		welcome:      "👋 Hello! Confirm the addition — reply to this message or react to it.",
		memberSuffix: "\n\n⚠️ Reactions are unavailable without admin rights — please reply to this message.",
	},
}

func guardMsgs(lang string) guardMessages {
	if m, ok := guardMessagesByLang[lang]; ok {
		return m
	}
	return guardMessagesByLang["ru"]
}

// HistoryConfig holds message history parameters for TelegramChannel.
// Applied to Group, GroupDirect, and DM messages.
type HistoryConfig struct {
	// SilenceGap is the minimum pause between messages that marks the start of a new activity wave.
	// Default: 5 minutes.
	SilenceGap time.Duration
	// FallbackLimit is the number of messages returned when no activity wave is found.
	// Default: 20.
	FallbackLimit int
}

// TelegramChannel monitors Telegram groups via long polling.
// Implements the model.Channel interface.
type TelegramChannel struct {
	bot        botAPI
	cfg        TelegramChannelConfig
	state      model.StateStore
	guardStore model.GuardStore
	ownerIDs   map[string]struct{}
	history    model.History
	historyCfg HistoryConfig
	// botID is the numeric bot user ID parsed from cfg.BotID. 0 if unset or invalid.
	botID     int64
	pending   map[int64]*pendingApproval
	pendingMu sync.Mutex
}

// telegramStateKey is the default cursor key (single account with no ID).
const telegramStateKey = "telegram"

// ID returns the channel identifier.
func (w *TelegramChannel) ID() string {
	return w.cfg.ID
}

// stateKey returns the cursor key for the StateStore.
// With ID: "telegram/<id>", otherwise: "telegram".
func (w *TelegramChannel) stateKey() string {
	if w.cfg.ID != "" {
		return telegramStateKey + "/" + w.cfg.ID
	}
	return telegramStateKey
}

// NewTelegramChannel creates a new TelegramChannel with a real Bot API.
// guardStore may be nil — without it, pending approvals are not persisted across restarts.
func NewTelegramChannel(bot *tgbotapi.BotAPI, cfg TelegramChannelConfig, state model.StateStore, guardStore model.GuardStore, history model.History, historyCfg HistoryConfig) *TelegramChannel {
	return newTelegramChannel(bot, cfg, state, guardStore, history, historyCfg)
}

// newTelegramChannel creates a TelegramChannel with an arbitrary botAPI implementation.
// Used in tests to substitute the real bot.
func newTelegramChannel(bot botAPI, cfg TelegramChannelConfig, state model.StateStore, guardStore model.GuardStore, history model.History, historyCfg HistoryConfig) *TelegramChannel {
	if historyCfg.SilenceGap == 0 {
		historyCfg.SilenceGap = 5 * time.Minute
	}
	if historyCfg.FallbackLimit == 0 {
		historyCfg.FallbackLimit = 20
	}
	ownerIDs := make(map[string]struct{}, len(cfg.OwnerIDs))
	for _, id := range cfg.OwnerIDs {
		ownerIDs[id] = struct{}{}
	}
	var botID int64
	if cfg.BotID != "" {
		if parsed, err := strconv.ParseInt(cfg.BotID, 10, 64); err == nil {
			botID = parsed
		}
	}
	if cfg.WelcomeMessage == "" {
		cfg.WelcomeMessage = guardMsgs(cfg.Language).welcome
	}
	return &TelegramChannel{
		bot:        bot,
		cfg:        cfg,
		state:      state,
		guardStore: guardStore,
		ownerIDs:   ownerIDs,
		history:    history,
		historyCfg: historyCfg,
		botID:      botID,
		pending:    make(map[int64]*pendingApproval),
	}
}

// Watch starts monitoring Telegram groups via raw long polling (getUpdates).
// In "monitor" mode, starts from the last saved update_id + 1.
// In "backfill" mode, starts from offset 0 (all unprocessed updates).
// Blocks until the context is cancelled.
func (w *TelegramChannel) Watch(ctx context.Context, handler func(context.Context, model.Message) error) error {
	w.recoverPendingFromStore(ctx)

	offset := 0

	cursor, err := w.state.GetCursor(ctx, w.stateKey())
	if err != nil {
		return fmt.Errorf("getting cursor: %w", err)
	}
	if cursor != nil && w.cfg.OnJoin == "monitor" {
		if id, parseErr := strconv.Atoi(cursor.MessageID); parseErr == nil {
			offset = id + 1
		}
	}

	allowedUpdates := "[]"
	if len(w.ownerIDs) > 0 {
		b, _ := json.Marshal([]string{"message", "edited_message", "my_chat_member", "message_reaction"})
		allowedUpdates = string(b)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		w.checkAndExpirePending(ctx)

		params := tgbotapi.Params{
			"offset":          strconv.Itoa(offset),
			"timeout":         "5",
			"allowed_updates": allowedUpdates,
		}

		resp, err := w.bot.MakeRequest("getUpdates", params)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.ErrorContext(ctx, "fetching updates", "error", err)
			continue
		}

		var updates []rawUpdate
		if err := json.Unmarshal(resp.Result, &updates); err != nil {
			slog.ErrorContext(ctx, "parsing updates", "error", err)
			continue
		}

		for _, upd := range updates {
			offset = upd.UpdateID + 1
			w.processRawUpdate(ctx, upd, handler)
		}
	}
}

// processRawUpdate handles one rawUpdate: guard events, confirmations, and regular messages.
func (w *TelegramChannel) processRawUpdate(ctx context.Context, upd rawUpdate, handler func(context.Context, model.Message) error) {
	saveCursor := func() {
		if err := w.saveUpdateCursor(ctx, upd.UpdateID); err != nil {
			slog.Error("saving cursor", "error", err, "update_id", upd.UpdateID)
		}
	}

	if upd.MyChatMember != nil {
		w.handleJoin(ctx, upd.MyChatMember)
		saveCursor()
		return
	}

	if upd.MessageReaction != nil {
		r := upd.MessageReaction
		var userID int64
		if r.User != nil {
			userID = r.User.ID
		}
		slog.DebugContext(ctx, "guard: message_reaction received",
			"chat_id", r.Chat.ID,
			"message_id", r.MessageID,
			"user_id", userID,
			"owner_ids", w.cfg.OwnerIDs,
		)
		if w.isReactionConfirmation(r) {
			slog.InfoContext(ctx, "guard: reaction confirmation accepted", "chat_id", r.Chat.ID)
			w.confirmChat(ctx, r.Chat.ID)
		} else {
			slog.DebugContext(ctx, "guard: reaction confirmation rejected", "chat_id", r.Chat.ID)
		}
		saveCursor()
		return
	}

	var tgMsg *tgbotapi.Message
	switch {
	case upd.Message != nil:
		tgMsg = upd.Message
	case upd.EditedMessage != nil:
		tgMsg = upd.EditedMessage
	}
	if tgMsg == nil {
		saveCursor()
		return
	}

	chatID := tgMsg.Chat.ID
	w.pendingMu.Lock()
	_, isPending := w.pending[chatID]
	w.pendingMu.Unlock()

	if isPending {
		if w.isReplyConfirmation(tgMsg) {
			w.confirmChat(ctx, chatID)
		}
		saveCursor()
		return
	}

	msg, converted := w.convertMessage(tgMsg)
	if !converted {
		saveCursor()
		return
	}

	if w.history != nil && msg.Text != "" {
		entry := model.HistoryEntry{
			AuthorName: msg.AuthorName,
			Text:       msg.Text,
			Timestamp:  msg.Timestamp,
		}
		if err := w.history.Add(ctx, msg.Source.ID, entry); err != nil {
			slog.WarnContext(ctx, "adding to history", "error", err, "update_id", upd.UpdateID)
		}
		source := msg.Source.ID
		msgKind := msg.Kind
		msgTimestamp := msg.Timestamp
		msgText := msg.Text
		msg.HistoryFn = func(ctx context.Context) ([]model.HistoryEntry, error) {
			entries, err := w.history.RecentActivity(ctx, source, w.historyCfg.SilenceGap, w.historyCfg.FallbackLimit)
			if err != nil {
				return nil, err
			}
			// DM and GroupDirect: the current message is passed to the agent as msg.Text;
			// exclude it from history to avoid the model seeing a duplicate.
			if msgKind != model.MessageKindGroupDirect && msgKind != model.MessageKindDM {
				return entries, nil
			}
			result := entries[:0:len(entries)]
			for _, e := range entries {
				if !e.Timestamp.Equal(msgTimestamp) || e.Text != msgText {
					result = append(result, e)
				}
			}
			return result, nil
		}
	}

	if err := handler(ctx, msg); err != nil {
		slog.Error("message processing failed", "error", err, "update_id", upd.UpdateID)
	} else if err := w.saveUpdateCursor(ctx, upd.UpdateID); err != nil {
		slog.Error("saving cursor", "error", err, "update_id", upd.UpdateID)
	}
}

// FetchHistory fetches available message history via Bot API getUpdates.
// This method is limited: the Bot API only returns unprocessed updates (up to 24 hours).
// In "monitor" mode returns nil — history is not needed.
// In "backfill" mode returns the last limit messages from the given source.
func (w *TelegramChannel) FetchHistory(ctx context.Context, source model.Source, limit int) ([]model.Message, error) {
	if w.cfg.OnJoin != "backfill" {
		return nil, nil
	}

	updateCfg := tgbotapi.NewUpdate(0)
	updateCfg.Limit = limit * 2 // fetch with margin since not all updates are messages

	updates, err := w.bot.GetUpdates(updateCfg)
	if err != nil {
		return nil, fmt.Errorf("fetching history via getUpdates: %w", err)
	}

	var messages []model.Message
	for _, u := range updates {
		msg, ok := w.convertUpdate(u)
		if !ok {
			continue
		}
		if msg.Source.ID != source.ID {
			continue
		}
		messages = append(messages, msg)
		if len(messages) >= limit {
			break
		}
	}
	return messages, nil
}

// convertUpdate converts a tgbotapi.Update to a model.Message.
// Returns false if the update type is unsupported or the message is filtered out.
//
// Note: MessageReactionUpdated updates are supported starting from Telegram Bot API 7.0
// and require upgrading to tgbotapi v6+. In the current version (v5.5.1) reactions
// are not processed.
func (w *TelegramChannel) convertUpdate(update tgbotapi.Update) (model.Message, bool) {
	switch {
	case update.Message != nil:
		return w.convertMessage(update.Message)
	case update.EditedMessage != nil:
		return w.convertMessage(update.EditedMessage)
	default:
		// Reactions (MessageReactionUpdated), polls, callbacks, etc. fall here.
		return model.Message{}, false
	}
}

// convertMessage converts a tgbotapi.Message to a model.Message.
// For private chats from owners (OwnerIDs), creates a DM source (Source.ID = "dm").
// Private chats from unknown users are filtered out.
func (w *TelegramChannel) convertMessage(m *tgbotapi.Message) (model.Message, bool) {
	if m.Chat.Type == "private" {
		return w.convertDMMessage(m)
	}

	chatID := m.Chat.ID
	msgID := m.MessageID

	kind := model.MessageKindGroup
	if w.isBotDirected(m) {
		kind = model.MessageKindGroupDirect
	}

	msg := model.Message{
		ID: strconv.Itoa(msgID),
		Source: model.Source{
			Kind:      "telegram",
			ID:        strconv.FormatInt(chatID, 10),
			Name:      m.Chat.Title,
			AccountID: w.cfg.ID,
		},
		Timestamp: time.Unix(int64(m.Date), 0),
		Text:      m.Text,
		Kind:      kind,
	}

	if w.cfg.ReactionEnabled {
		msg.ReactFn = func(ctx context.Context, emoji string) error {
			return w.sendReaction(strconv.FormatInt(chatID, 10), strconv.Itoa(msgID), emoji)
		}
	}
	sourceID := strconv.FormatInt(chatID, 10)
	msg.ReplyFn = func(ctx context.Context, text string) error {
		reply := tgbotapi.NewMessage(chatID, mdToTelegramV2(text))
		reply.ParseMode = tgbotapi.ModeMarkdownV2
		sent, err := w.bot.Send(reply)
		if err != nil {
			return err
		}
		w.recordBotReply(ctx, sourceID, text, sent.Date)
		return nil
	}

	if m.From != nil {
		msg.Author = strconv.FormatInt(m.From.ID, 10)
		msg.AuthorName = buildDisplayName(m.From)
	}

	if m.ReplyToMessage != nil {
		reply := w.convertReplyMessage(m.ReplyToMessage)
		msg.ReplyTo = &reply
	}

	return msg, true
}

// convertDMMessage handles a private chat. Only accepts messages from owners.
func (w *TelegramChannel) convertDMMessage(m *tgbotapi.Message) (model.Message, bool) {
	if m.From == nil {
		return model.Message{}, false
	}
	authorID := strconv.FormatInt(m.From.ID, 10)
	if _, ok := w.ownerIDs[authorID]; !ok {
		return model.Message{}, false
	}

	chatID := m.Chat.ID
	msgID := m.MessageID

	msg := model.Message{
		ID: strconv.Itoa(msgID),
		Source: model.Source{
			Kind:      "telegram",
			ID:        "dm",
			Name:      "DM",
			AccountID: w.cfg.ID,
		},
		Author:     authorID,
		AuthorName: buildDisplayName(m.From),
		Timestamp:  time.Unix(int64(m.Date), 0),
		Text:       m.Text,
		Kind:       model.MessageKindDM,
	}

	if w.cfg.ReactionEnabled {
		msg.ReactFn = func(ctx context.Context, emoji string) error {
			return w.sendReaction(strconv.FormatInt(chatID, 10), strconv.Itoa(msgID), emoji)
		}
	}
	msg.ReplyFn = func(ctx context.Context, text string) error {
		reply := tgbotapi.NewMessage(chatID, mdToTelegramV2(text))
		reply.ParseMode = tgbotapi.ModeMarkdownV2
		sent, err := w.bot.Send(reply)
		if err != nil {
			return err
		}
		w.recordBotReply(ctx, "dm", text, sent.Date)
		return nil
	}

	if m.ReplyToMessage != nil {
		reply := w.convertReplyMessage(m.ReplyToMessage)
		msg.ReplyTo = &reply
	}

	return msg, true
}

// convertReplyMessage converts the message being replied to.
// Does not check group membership since ReplyTo is always in the same chat.
func (w *TelegramChannel) convertReplyMessage(m *tgbotapi.Message) model.Message {
	msg := model.Message{
		ID: strconv.Itoa(m.MessageID),
		Source: model.Source{
			Kind:      "telegram",
			ID:        strconv.FormatInt(m.Chat.ID, 10),
			Name:      m.Chat.Title,
			AccountID: w.cfg.ID,
		},
		Timestamp: time.Unix(int64(m.Date), 0),
		Text:      m.Text,
	}
	if m.From != nil {
		msg.Author = strconv.FormatInt(m.From.ID, 10)
		msg.AuthorName = buildDisplayName(m.From)
	}
	return msg
}

// isBotDirected returns true if the group message is addressed to the bot:
// - the message contains a @mention of the bot (cfg.BotUsername set, entity type "mention" matches)
// - the message is a reply to a bot message (cfg.BotID set, ReplyToMessage.From.ID matches)
func (w *TelegramChannel) isBotDirected(m *tgbotapi.Message) bool {
	// Check reply to bot message.
	if w.botID != 0 && m.ReplyToMessage != nil && m.ReplyToMessage.From != nil {
		if m.ReplyToMessage.From.ID == w.botID {
			return true
		}
	}

	// Check @mention of the bot in the text.
	// Telegram entity offsets are in UTF-16 units, so convert the string to UTF-16
	// for correct offset application when non-ASCII text precedes the mention.
	if w.cfg.BotUsername != "" {
		target := "@" + w.cfg.BotUsername
		utf16Text := utf16.Encode([]rune(m.Text))
		for _, entity := range m.Entities {
			if entity.Type == "mention" {
				end := entity.Offset + entity.Length
				if end <= len(utf16Text) {
					mentioned := string(utf16.Decode(utf16Text[entity.Offset:end]))
					if strings.EqualFold(mentioned, target) {
						return true
					}
				}
			}
		}
	}

	return false
}

// buildDisplayName builds the display name for a user.
// Prefers username, then first + last name.
func buildDisplayName(u *tgbotapi.User) string {
	if u.UserName != "" {
		return u.UserName
	}
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

// sendReaction sends an emoji reaction to a message via Telegram Bot API setMessageReaction.
// Uses MakeRequest since go-telegram-bot-api v5 does not support this method natively.
// An empty emoji clears all reactions (sends an empty array).
func (w *TelegramChannel) sendReaction(chatID, messageID, emoji string) error {
	var reaction string
	if emoji == "" {
		reaction = "[]"
	} else {
		b, err := json.Marshal([]reactionTypeEmoji{{Type: "emoji", Emoji: emoji}})
		if err != nil {
			return fmt.Errorf("serializing reaction: %w", err)
		}
		reaction = string(b)
	}
	params := tgbotapi.Params{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   reaction,
	}
	if _, err := w.bot.MakeRequest("setMessageReaction", params); err != nil {
		return fmt.Errorf("setting reaction: %w", err)
	}
	return nil
}

// botDisplayName returns the bot's display name for history recording.
func (w *TelegramChannel) botDisplayName() string {
	if w.cfg.BotUsername != "" {
		return w.cfg.BotUsername
	}
	return "bot"
}

// recordBotReply records the bot's reply to history. No-op if history is nil or text is empty.
// Write errors are logged as warnings and not returned.
func (w *TelegramChannel) recordBotReply(ctx context.Context, sourceID, text string, date int) {
	if w.history == nil || text == "" {
		return
	}
	ts := time.Now()
	if date != 0 {
		ts = time.Unix(int64(date), 0)
	}
	entry := model.HistoryEntry{
		AuthorName: w.botDisplayName(),
		Text:       text,
		Timestamp:  ts,
	}
	if err := w.history.Add(ctx, sourceID, entry); err != nil {
		slog.WarnContext(ctx, "recording bot reply to history", "error", err)
	}
}

func (w *TelegramChannel) saveUpdateCursor(ctx context.Context, updateID int) error {
	return w.state.SaveCursor(ctx, w.stateKey(), model.Cursor{
		MessageID: strconv.Itoa(updateID),
		UpdatedAt: time.Now(),
	})
}

// handleJoin handles the event of the bot being added to a group.
// If guard is enabled (ConfirmTimeout > 0) and the chat is not whitelisted,
// sends the welcome message and registers it as pending.
func (w *TelegramChannel) handleJoin(ctx context.Context, upd *tgbotapi.ChatMemberUpdated) {
	if w.cfg.ConfirmTimeout == 0 {
		slog.DebugContext(ctx, "guard: disabled (ConfirmTimeout=0), skipping")
		return
	}
	if upd.NewChatMember.User == nil {
		slog.DebugContext(ctx, "guard: NewChatMember.User is nil, skipping")
		return
	}

	// Check that the bot itself was added.
	newStatus := upd.NewChatMember.Status
	oldStatus := upd.OldChatMember.Status
	isBotAdded := (newStatus == "member" || newStatus == "administrator") &&
		oldStatus != "member" && oldStatus != "administrator" && oldStatus != "creator"

	slog.DebugContext(ctx, "guard: my_chat_member update",
		"chat_id", upd.Chat.ID,
		"chat_title", upd.Chat.Title,
		"new_member_user_id", upd.NewChatMember.User.ID,
		"old_status", oldStatus,
		"new_status", newStatus,
		"is_bot_added", isBotAdded,
	)

	if !isBotAdded {
		return
	}

	// If the owner themselves added the bot, no confirmation is needed.
	if upd.From.ID != 0 {
		adderID := strconv.FormatInt(upd.From.ID, 10)
		if _, ok := w.ownerIDs[adderID]; ok {
			slog.InfoContext(ctx, "guard: bot added by owner, auto-confirmed",
				"chat_id", upd.Chat.ID,
				"adder_id", adderID,
			)
			return
		}
	}

	// message_reaction updates require admin rights (Telegram Bot API restriction).
	// If the bot is added as a regular member, reactions won't arrive — only reply confirmation works.
	isAdmin := newStatus == "administrator"
	if !isAdmin {
		slog.WarnContext(ctx, "guard: bot added as member (not admin) — message_reaction updates unavailable, only reply confirmation will work",
			"chat_id", upd.Chat.ID,
		)
	}

	chatID := upd.Chat.ID

	welcomeText := w.cfg.WelcomeMessage
	if !isAdmin {
		welcomeText += guardMsgs(w.cfg.Language).memberSuffix
	}

	msg := tgbotapi.NewMessage(chatID, welcomeText)
	sent, err := w.bot.Send(msg)
	if err != nil {
		slog.ErrorContext(ctx, "guard: failed to send welcome message", "chat_id", chatID, "error", err)
		return
	}
	msgID := sent.MessageID

	deadline := time.Now().Add(w.cfg.ConfirmTimeout)
	w.pendingMu.Lock()
	w.pending[chatID] = &pendingApproval{
		welcomeMsgID: msgID,
		deadline:     deadline,
	}
	w.pendingMu.Unlock()

	slog.InfoContext(ctx, "guard: pending approval registered",
		"chat_id", chatID,
		"welcome_msg_id", msgID,
		"is_admin", isAdmin,
		"deadline", deadline,
		"timeout", w.cfg.ConfirmTimeout,
	)

	if w.guardStore != nil {
		if err := w.guardStore.UpsertPending(ctx, chatID, msgID, deadline); err != nil {
			slog.ErrorContext(ctx, "guard: failed to persist pending approval", "chat_id", chatID, "error", err)
		}
	}
}

// confirmChat removes the chat from pending (confirmation received).
func (w *TelegramChannel) confirmChat(ctx context.Context, chatID int64) {
	w.pendingMu.Lock()
	delete(w.pending, chatID)
	w.pendingMu.Unlock()
	slog.Info("guard: chat confirmed, removed from pending", "chat_id", chatID)
	if w.guardStore != nil {
		if err := w.guardStore.DeletePending(ctx, chatID); err != nil {
			slog.ErrorContext(ctx, "guard: failed to delete pending from store", "chat_id", chatID, "error", err)
		}
	}
}

// isReplyConfirmation returns true if the message is an owner reply to the welcome message.
func (w *TelegramChannel) isReplyConfirmation(msg *tgbotapi.Message) bool {
	if msg.From == nil || msg.ReplyToMessage == nil {
		return false
	}
	authorID := strconv.FormatInt(msg.From.ID, 10)
	if _, ok := w.ownerIDs[authorID]; !ok {
		return false
	}
	w.pendingMu.Lock()
	pa, ok := w.pending[msg.Chat.ID]
	w.pendingMu.Unlock()
	if !ok {
		return false
	}
	return msg.ReplyToMessage.MessageID == pa.welcomeMsgID
}

// isReactionConfirmation returns true if the reaction is from an owner on the welcome message.
func (w *TelegramChannel) isReactionConfirmation(r *rawMessageReaction) bool {
	if r.User == nil {
		slog.Debug("guard: isReactionConfirmation: User is nil")
		return false
	}
	userID := strconv.FormatInt(r.User.ID, 10)
	if _, ok := w.ownerIDs[userID]; !ok {
		slog.Debug("guard: isReactionConfirmation: user not in ownerIDs", "user_id", userID, "owner_ids", w.cfg.OwnerIDs)
		return false
	}
	w.pendingMu.Lock()
	pa, ok := w.pending[r.Chat.ID]
	w.pendingMu.Unlock()
	if !ok {
		slog.Debug("guard: isReactionConfirmation: chat not in pending", "chat_id", r.Chat.ID)
		return false
	}
	match := r.MessageID == pa.welcomeMsgID
	slog.Debug("guard: isReactionConfirmation: message_id check",
		"reaction_msg_id", r.MessageID,
		"welcome_msg_id", pa.welcomeMsgID,
		"match", match,
	)
	return match
}

// leaveChat calls the leaveChat Bot API method for the given chat.
func (w *TelegramChannel) leaveChat(ctx context.Context, chatID int64) error {
	params := tgbotapi.Params{
		"chat_id": strconv.FormatInt(chatID, 10),
	}
	if _, err := w.bot.MakeRequest("leaveChat", params); err != nil {
		return fmt.Errorf("leaving chat %d: %w", chatID, err)
	}
	return nil
}

// checkAndExpirePending checks for expired pending records and leaves the corresponding chats.
func (w *TelegramChannel) checkAndExpirePending(ctx context.Context) {
	now := time.Now()
	w.pendingMu.Lock()
	var expired []int64
	for chatID, pa := range w.pending {
		remaining := pa.deadline.Sub(now)
		slog.DebugContext(ctx, "guard: pending chat status",
			"chat_id", chatID,
			"welcome_msg_id", pa.welcomeMsgID,
			"deadline", pa.deadline,
			"remaining", remaining,
		)
		if now.After(pa.deadline) {
			expired = append(expired, chatID)
		}
	}
	for _, chatID := range expired {
		delete(w.pending, chatID)
	}
	w.pendingMu.Unlock()

	for _, chatID := range expired {
		slog.InfoContext(ctx, "guard: timeout expired, leaving chat", "chat_id", chatID)
		if err := w.leaveChat(ctx, chatID); err != nil {
			slog.ErrorContext(ctx, "guard: failed to leave chat", "chat_id", chatID, "error", err)
		} else {
			slog.InfoContext(ctx, "guard: left chat successfully", "chat_id", chatID)
		}
		if w.guardStore != nil {
			if err := w.guardStore.DeletePending(ctx, chatID); err != nil {
				slog.ErrorContext(ctx, "guard: failed to delete expired pending from store", "chat_id", chatID, "error", err)
			}
		}
	}
}

// recoverPendingFromStore loads persisted pending approvals on startup.
// Expired entries trigger immediate leaveChat; valid entries are restored to the in-memory map.
func (w *TelegramChannel) recoverPendingFromStore(ctx context.Context) {
	if w.guardStore == nil {
		return
	}
	records, err := w.guardStore.ListPending(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "guard: failed to load pending from store", "error", err)
		return
	}
	now := time.Now()
	for _, p := range records {
		if now.After(p.Deadline) {
			slog.InfoContext(ctx, "guard: leaving chat after restart (timeout expired)",
				"chat_id", p.ChatID,
				"deadline", p.Deadline,
			)
			if err := w.leaveChat(ctx, p.ChatID); err != nil {
				slog.ErrorContext(ctx, "guard: failed to leave chat after restart", "chat_id", p.ChatID, "error", err)
			}
			if err := w.guardStore.DeletePending(ctx, p.ChatID); err != nil {
				slog.ErrorContext(ctx, "guard: failed to delete expired pending from store", "chat_id", p.ChatID, "error", err)
			}
		} else {
			slog.InfoContext(ctx, "guard: restored pending approval from store",
				"chat_id", p.ChatID,
				"welcome_msg_id", p.WelcomeMsgID,
				"deadline", p.Deadline,
			)
			w.pendingMu.Lock()
			w.pending[p.ChatID] = &pendingApproval{
				welcomeMsgID: p.WelcomeMsgID,
				deadline:     p.Deadline,
			}
			w.pendingMu.Unlock()
		}
	}
}
