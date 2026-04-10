// Package channel contains channel implementations.
package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	imapClient "github.com/emersion/go-imap/client"

	"github.com/anadale/huskwoot/internal/model"
)

// imapConn abstracts an IMAP server connection for testing.
type imapConn interface {
	Login(username, password string) error
	Select(name string, readOnly bool) (*imap.MailboxStatus, error)
	UidSearch(criteria *imap.SearchCriteria) ([]uint32, error)
	UidFetch(seqset *imap.SeqSet, items []imap.FetchItem, ch chan *imap.Message) error
	Logout() error
}

// imapDialFn is the function type for connecting to an IMAP server.
type imapDialFn func(addr string) (imapConn, error)

// realImapDial connects to an IMAP server using TLS.
func realImapDial(addr string) (imapConn, error) {
	return imapClient.DialTLS(addr, nil)
}

// IMAPChannelConfig describes settings for a single IMAP connection.
type IMAPChannelConfig struct {
	// Host is the IMAP server address.
	Host string
	// Port is the IMAP server port (typically 993 for TLS).
	Port int
	// Username is the account username (email address).
	Username string
	// Password is the account password.
	Password string
	// Folders is the list of folders to monitor (e.g. ["INBOX", "[Gmail]/Sent Mail"]).
	Folders []string
	// Senders is the list of sender addresses to filter by (empty = all).
	Senders []string
	// OnFirstConnect controls behavior on first connection: "backfill" or "monitor".
	OnFirstConnect string
	// Label is the human-readable account name (e.g. "Work email").
	// Used as Source.Name; if empty, the folder name is used instead.
	Label string
	// PollInterval is the mailbox polling interval.
	PollInterval time.Duration
}

// IMAPChannel monitors one or more IMAP mailboxes via periodic polling.
// Implements the model.Channel interface.
type IMAPChannel struct {
	configs []IMAPChannelConfig
	state   model.StateStore
	dial    imapDialFn
}

// NewIMAPChannel creates a new IMAPChannel for the given IMAP connections.
func NewIMAPChannel(configs []IMAPChannelConfig, state model.StateStore) *IMAPChannel {
	return &IMAPChannel{
		configs: configs,
		state:   state,
		dial:    realImapDial,
	}
}

// ID returns an empty string: the IMAP channel has no single identifier.
func (w *IMAPChannel) ID() string {
	return ""
}

// Watch starts monitoring all IMAP mailboxes in parallel.
// Blocks until the context is cancelled.
func (w *IMAPChannel) Watch(ctx context.Context, handler func(context.Context, model.Message) error) error {
	if len(w.configs) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, cfg := range w.configs {
		wg.Add(1)
		go func(cfg IMAPChannelConfig) {
			defer wg.Done()
			if err := w.watchAccount(ctx, cfg, handler); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Error("IMAP channel failed", "username", cfg.Username, "error", err)
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(cfg)
	}

	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(errs...)
}

// FetchHistory is not implemented for IMAP: all information is contained in the message body.
func (w *IMAPChannel) FetchHistory(_ context.Context, _ model.Source, _ int) ([]model.Message, error) {
	return nil, nil
}

// watchAccount starts one goroutine per folder from cfg.Folders.
// Waits for all goroutines to finish and returns the context error on cancellation.
func (w *IMAPChannel) watchAccount(ctx context.Context, cfg IMAPChannelConfig, handler func(context.Context, model.Message) error) error {
	if len(cfg.Folders) == 0 {
		slog.Warn("IMAP account not monitored: no folders configured", "username", cfg.Username)
		<-ctx.Done()
		return ctx.Err()
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, folder := range cfg.Folders {
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			if err := w.watchFolder(ctx, cfg, folder, handler); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Error("IMAP folder failed", "username", cfg.Username, "folder", folder, "error", err)
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(folder)
	}

	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(errs...)
}

// watchFolder contains the ticker loop for a single folder of a single IMAP mailbox.
func (w *IMAPChannel) watchFolder(ctx context.Context, cfg IMAPChannelConfig, folder string, handler func(context.Context, model.Message) error) error {
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = time.Minute
	}

	if err := w.pollOnce(ctx, cfg, folder, handler); err != nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		slog.Error("IMAP initial poll failed", "username", cfg.Username, "folder", folder, "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.pollOnce(ctx, cfg, folder, handler); err != nil &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Error("IMAP poll failed", "username", cfg.Username, "folder", folder, "error", err)
			}
		}
	}
}

// imapStateKey returns the cursor key for the given mailbox and folder.
func imapStateKey(username, folder string) string {
	return "imap:" + username + ":" + folder
}

// pollOnce performs one IMAP poll cycle: connect, check for new messages, process.
func (w *IMAPChannel) pollOnce(ctx context.Context, cfg IMAPChannelConfig, folder string, handler func(context.Context, model.Message) error) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	conn, err := w.dial(addr)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	defer conn.Logout() //nolint:errcheck

	if err := conn.Login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("authenticating %s: %w", cfg.Username, err)
	}

	status, err := conn.Select(folder, true)
	if err != nil {
		return fmt.Errorf("selecting folder %s: %w", folder, err)
	}

	key := imapStateKey(cfg.Username, folder)
	cursor, err := w.state.GetCursor(ctx, key)
	if err != nil {
		return fmt.Errorf("getting cursor: %w", err)
	}

	currentValidity := strconv.FormatUint(uint64(status.UidValidity), 10)

	// If UIDVALIDITY changed, reset the cursor and start over.
	if cursor != nil && cursor.FolderID != currentValidity {
		slog.Warn("UIDVALIDITY changed, resetting cursor", "username", cfg.Username, "folder", folder)
		cursor = nil
	}

	var startUID uint32

	if cursor == nil {
		if cfg.OnFirstConnect == "monitor" {
			// In monitor mode on first connect, skip existing messages.
			var lastExisting uint32
			if status.UidNext > 1 {
				lastExisting = status.UidNext - 1
			}
			return w.saveImapCursor(ctx, key, lastExisting, currentValidity)
		}
		// In backfill mode, start from the first message.
		startUID = 1
	} else {
		uid, parseErr := strconv.ParseUint(cursor.MessageID, 10, 32)
		if parseErr != nil {
			slog.Warn("invalid cursor MessageID, starting from first message",
				"messageID", cursor.MessageID, "error", parseErr)
			startUID = 1
		} else if uid == math.MaxUint32 {
			// Cursor is at max UID — no new messages possible.
			return nil
		} else {
			startUID = uint32(uid) + 1
		}
	}

	// No new messages.
	if startUID >= status.UidNext {
		return nil
	}

	criteria := &imap.SearchCriteria{
		Uid: new(imap.SeqSet),
	}
	criteria.Uid.AddRange(startUID, 0) // 0 means * (no upper limit)

	uids, err := conn.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("searching messages: %w", err)
	}

	if len(uids) == 0 {
		return nil
	}

	fetchSet := new(imap.SeqSet)
	for _, uid := range uids {
		fetchSet.AddNum(uid)
	}

	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, imap.FetchInternalDate, section.FetchItem()}

	msgCh := make(chan *imap.Message, 10)
	fetchErrCh := make(chan error, 1)
	go func() {
		fetchErrCh <- conn.UidFetch(fetchSet, items, msgCh)
	}()

	var lastUID uint32
	for msg := range msgCh {
		if msg == nil {
			continue
		}
		converted := w.convertIMAPMessage(msg, cfg, folder)
		if converted == nil {
			// Message has no body or is outside the filter — still advance the cursor.
			if msg.Uid > lastUID {
				lastUID = msg.Uid
			}
			continue
		}
		if err := handler(ctx, *converted); err != nil {
			slog.Error("message processing failed", "uid", msg.Uid, "error", err)
			// Stop processing: the message will be retried on next poll.
			// Using continue instead of break would advance the cursor past failed messages,
			// causing them to be lost if later messages succeed.
			break
		}
		if msg.Uid > lastUID {
			lastUID = msg.Uid
		}
	}
	// Drain remaining messages from the channel so the UidFetch goroutine can finish.
	// Without this, the goroutine may block writing to a full buffer and
	// <-fetchErrCh would never receive a value, causing a deadlock.
	for range msgCh {
	}

	if err := <-fetchErrCh; err != nil {
		return fmt.Errorf("fetching messages: %w", err)
	}

	if lastUID > 0 {
		return w.saveImapCursor(ctx, key, lastUID, currentValidity)
	}
	return nil
}

// saveImapCursor saves the IMAP cursor to the StateStore.
func (w *IMAPChannel) saveImapCursor(ctx context.Context, key string, uid uint32, uidValidity string) error {
	return w.state.SaveCursor(ctx, key, model.Cursor{
		MessageID: strconv.FormatUint(uint64(uid), 10),
		FolderID:  uidValidity,
		UpdatedAt: time.Now(),
	})
}

// senderAllowed checks whether the sender address is in the allowed list.
// If the list is empty, all senders are allowed.
func senderAllowed(addr string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	addrLower := strings.ToLower(addr)
	for _, s := range allowed {
		if strings.ToLower(s) == addrLower {
			return true
		}
	}
	return false
}

// convertIMAPMessage converts an imap.Message to a model.Message.
// Returns nil if the message does not pass the sender filter.
// For outgoing messages (from == cfg.Username) the sender filter is skipped;
// the body is split into reply and quote via splitEmailReply.
func (w *IMAPChannel) convertIMAPMessage(msg *imap.Message, cfg IMAPChannelConfig, folder string) *model.Message {
	if msg.Envelope == nil {
		return nil
	}

	if len(msg.Envelope.From) == 0 {
		return nil
	}

	from := msg.Envelope.From[0]
	fromAddr := from.Address()
	if fromAddr == "" {
		return nil
	}
	fromName := from.PersonalName
	if fromName == "" {
		fromName = fromAddr
	}

	var bodyText string
	for _, literal := range msg.Body {
		if literal == nil {
			continue
		}
		bodyText = extractEmailText(literal)
		break
	}

	sourceName := folder
	if cfg.Label != "" {
		sourceName = cfg.Label
	}

	result := &model.Message{
		ID: strconv.FormatUint(uint64(msg.Uid), 10),
		Source: model.Source{
			Kind: "imap",
			ID:   cfg.Username + ":" + folder,
			Name: sourceName,
		},
		Author:     fromAddr,
		AuthorName: fromName,
		Subject:    msg.Envelope.Subject,
		Timestamp:  msg.InternalDate,
		Kind:       model.MessageKindBatch,
	}
	if result.Timestamp.IsZero() {
		result.Timestamp = msg.Envelope.Date
	}

	if strings.EqualFold(fromAddr, cfg.Username) {
		// Outgoing message: split into reply and quote; sender filter does not apply.
		reply, quote := splitEmailReply(bodyText)
		if reply == "" {
			// Forwarded message with no original text — nothing to process.
			return nil
		}
		result.Text = normalizeLines(reply)
		if quote != "" {
			replyTo := &model.Message{Text: normalizeLines(quote)}
			if len(msg.Envelope.To) > 0 {
				to := msg.Envelope.To[0]
				replyTo.Author = to.Address()
				if to.PersonalName != "" {
					replyTo.AuthorName = to.PersonalName
				} else {
					replyTo.AuthorName = to.Address()
				}
			}
			result.ReplyTo = replyTo
		}
	} else {
		// Incoming message: apply sender filter and normalize the body.
		if !senderAllowed(fromAddr, cfg.Senders) {
			return nil
		}
		result.Text = normalizeLines(bodyText)
	}

	return result
}
