package model

import (
	"strings"
	"time"
)

// Values of TaskSourceDetails.Via: how a task was created.
const (
	// SourceViaMessage marks tasks extracted from a channel message (Telegram group, IMAP).
	SourceViaMessage = "message"
	// SourceViaForward marks tasks extracted from a batch of forwarded Telegram messages.
	SourceViaForward = "forward"
	// SourceViaChat marks tasks created by the agent from a text chat message.
	SourceViaChat = "chat"
	// SourceViaVoice marks tasks created by the agent from a transcribed client voice message.
	SourceViaVoice = "voice"
	// SourceViaAPI marks tasks created through the REST API.
	SourceViaAPI = "api"
)

// telegramSupergroupPrefix is the chat id prefix of Telegram supergroups and channels.
const telegramSupergroupPrefix = "-100"

// SourceDetailsFromMessage builds a capped source snapshot from msg.
//
// Name comes from Source.Name, Subject from the message subject (IMAP), Author
// from AuthorName (falling back to Author), Text and At from the message itself,
// and ReplyTo from the quoted message. Name and Author stay empty for client
// sources: the client knows its own device names. Link is set only for Telegram
// supergroups and channels, see TelegramMessageLink.
func SourceDetailsFromMessage(msg Message, via string) *TaskSourceDetails {
	d := &TaskSourceDetails{
		Subject: msg.Subject,
		Text:    msg.Text,
		At:      SourceTime(msg.Timestamp),
		Link:    TelegramMessageLink(msg.Source, msg.ID),
		Via:     via,
	}
	if msg.Source.Kind != "client" {
		d.Name = msg.Source.Name
		d.Author = messageAuthor(msg)
	}
	if msg.ReplyTo != nil {
		quote := TaskSourceQuote{Author: messageAuthor(*msg.ReplyTo), Text: msg.ReplyTo.Text}
		if quote.Author != "" || quote.Text != "" {
			d.ReplyTo = &quote
		}
	}
	return d.Capped()
}

// TelegramMessageLink returns a deep link to a Telegram message, or "" when the
// chat has no stable public link. Links are built only for supergroups and
// channels (chat id "-100<id>") as https://t.me/c/<id>/<message id>; private
// chats and basic groups have none.
func TelegramMessageLink(source Source, messageID string) string {
	if source.Kind != "telegram" || !isDigits(messageID) {
		return ""
	}
	chatID, ok := strings.CutPrefix(source.ID, telegramSupergroupPrefix)
	if !ok || !isDigits(chatID) {
		return ""
	}
	return "https://t.me/c/" + chatID + "/" + messageID
}

// SourceTime returns a pointer to a copy of t, or nil for the zero time.
func SourceTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// messageAuthor returns the display name of the message author, falling back
// to the author identifier.
func messageAuthor(msg Message) string {
	if msg.AuthorName != "" {
		return msg.AuthorName
	}
	return msg.Author
}

// isDigits reports whether s is a non-empty string of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
