package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"

	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pipeline"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// mockImapConn implements imapConn for testing without a real IMAP server.
type mockImapConn struct {
	loginErr      error
	selectStatus  *imap.MailboxStatus
	selectErr     error
	searchUIDs    []uint32
	searchErr     error
	fetchMessages []*imap.Message
	fetchErr      error
	fetchSent     int

	loginCalled  bool
	logoutCalled bool
}

func (m *mockImapConn) Login(_, _ string) error {
	m.loginCalled = true
	return m.loginErr
}

func (m *mockImapConn) Select(_ string, _ bool) (*imap.MailboxStatus, error) {
	return m.selectStatus, m.selectErr
}

func (m *mockImapConn) UidSearch(_ *imap.SearchCriteria) ([]uint32, error) {
	return m.searchUIDs, m.searchErr
}

func (m *mockImapConn) UidFetch(_ *imap.SeqSet, _ []imap.FetchItem, ch chan *imap.Message) error {
	for _, msg := range m.fetchMessages {
		ch <- msg
		m.fetchSent++
	}
	close(ch)
	return m.fetchErr
}

func (m *mockImapConn) Logout() error {
	m.logoutCalled = true
	return nil
}

// newTestIMAPChannel creates an IMAPChannel with a mock connection.
func newTestIMAPChannel(conn *mockImapConn, cfg IMAPChannelConfig, state model.StateStore) *IMAPChannel {
	return &IMAPChannel{
		configs: []IMAPChannelConfig{cfg},
		state:   state,
		dial:    func(_ string) (imapConn, error) { return conn, nil },
	}
}

// makeTestIMAPMessage creates a test IMAP message.
func makeTestIMAPMessage(uid uint32, subject, fromMailbox, fromHost, fromName string, date time.Time) *imap.Message {
	return &imap.Message{
		Uid: uid,
		Envelope: &imap.Envelope{
			Subject: subject,
			Date:    date,
			From: []*imap.Address{
				{PersonalName: fromName, MailboxName: fromMailbox, HostName: fromHost},
			},
		},
	}
}

// makeTestStatus creates a test mailbox status.
func makeTestStatus(uidValidity, uidNext uint32) *imap.MailboxStatus {
	return &imap.MailboxStatus{
		UidValidity: uidValidity,
		UidNext:     uidNext,
		Messages:    uidNext - 1,
	}
}

// TestIMAPChannel_ReceivesNewEmails verifies that the handler is called for each new email.
func TestIMAPChannel_ReceivesNewEmails(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 4), // UidNext=4, meaning UIDs 1,2,3 exist
		searchUIDs:   []uint32{2, 3},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(2, "Нужно сделать отчёт", "alice", "example.com", "Alice", now),
			makeTestIMAPMessage(3, "Сделаешь до пятницы?", "bob", "example.com", "Bob", now.Add(time.Hour)),
		},
	}

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Folders:  []string{"INBOX"},
	}
	state := newMockStateStore()
	// Set cursor to UID=1 so that polling starts from UID=2.
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "1",
		FolderID:  "100",
		UpdatedAt: time.Now(),
	})

	w := newTestIMAPChannel(conn, cfg, state)

	var received []model.Message
	err := w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(received) != 2 {
		t.Fatalf("want 2 emails, got %d", len(received))
	}
	if received[0].ID != "2" {
		t.Errorf("first email ID: want %q, got %q", "2", received[0].ID)
	}
	if received[0].Subject != "Нужно сделать отчёт" {
		t.Errorf("first email Subject: want %q, got %q", "Нужно сделать отчёт", received[0].Subject)
	}
	if received[0].Text != "" {
		t.Errorf("first email Text must be empty (no body): got %q", received[0].Text)
	}
	if received[0].Source.Kind != "imap" {
		t.Errorf("Source.Kind: want %q, got %q", "imap", received[0].Source.Kind)
	}
	if received[0].AuthorName != "Alice" {
		t.Errorf("AuthorName: want %q, got %q", "Alice", received[0].AuthorName)
	}

	// Verify that the cursor advanced to the last UID.
	cursor, _ := state.GetCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"))
	if cursor == nil {
		t.Fatal("cursor was not saved")
	}
	if cursor.MessageID != "3" {
		t.Errorf("cursor MessageID: want %q, got %q", "3", cursor.MessageID)
	}
}

// TestIMAPChannel_FiltersBySenders verifies that emails are filtered by sender address.
func TestIMAPChannel_FiltersBySenders(t *testing.T) {
	now := time.Now()
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 4),
		searchUIDs:   []uint32{2, 3},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(2, "Разрешённое", "allowed", "example.com", "Alice", now),
			makeTestIMAPMessage(3, "Запрещённое", "denied", "example.com", "Bob", now),
		},
	}

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Folders:  []string{"INBOX"},
		Senders:  []string{"allowed@example.com"},
	}
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "1",
		FolderID:  "100",
	})

	w := newTestIMAPChannel(conn, cfg, state)

	var received []model.Message
	if err := w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	}); err != nil {
		t.Fatalf("pollOnce returned error: %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("want 1 email (allowed only), got %d", len(received))
	}
	if received[0].Subject != "Разрешённое" {
		t.Errorf("Subject: want %q, got %q", "Разрешённое", received[0].Subject)
	}
}

// TestIMAPChannel_SendersCaseInsensitive verifies that sender filtering is case-insensitive.
func TestIMAPChannel_SendersCaseInsensitive(t *testing.T) {
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 3),
		searchUIDs:   []uint32{2},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(2, "Тема", "ALICE", "EXAMPLE.COM", "Alice", time.Now()),
		},
	}

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Folders:  []string{"INBOX"},
		Senders:  []string{"alice@example.com"}, // lowercase in config
	}
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "1",
		FolderID:  "100",
	})

	w := newTestIMAPChannel(conn, cfg, state)

	var count int
	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, _ model.Message) error {
		count++
		return nil
	})

	if count != 1 {
		t.Errorf("want 1 (case-insensitive filtering), got %d", count)
	}
}

// TestIMAPChannel_UIDValidityChange verifies reset policy and generation-aware delivery.
func TestIMAPChannel_UIDValidityChange(t *testing.T) {
	for _, mode := range []string{"backfill", "monitor"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			conn := &mockImapConn{
				selectStatus: makeTestStatus(200, 3),
				searchUIDs:   []uint32{1, 2},
				fetchMessages: []*imap.Message{
					makeTestIMAPMessage(1, "Old email", "alice", "example.com", "Alice", now),
					makeTestIMAPMessage(2, "New email", "bob", "example.com", "Bob", now.Add(time.Minute)),
				},
			}
			cfg := IMAPChannelConfig{
				Username:       "user@work.com",
				Folders:        []string{"INBOX"},
				OnFirstConnect: mode,
			}
			state := newMockStateStore()
			key := imapStateKey(cfg.Username, "INBOX")
			if err := state.SaveCursor(ctx, key, model.Cursor{MessageID: "50", FolderID: "100"}); err != nil {
				t.Fatal(err)
			}
			w := newTestIMAPChannel(conn, cfg, state)
			var received []model.Message
			handler := func(_ context.Context, msg model.Message) error {
				received = append(received, msg)
				return nil
			}
			if err := w.pollOnce(ctx, cfg, "INBOX", handler); err != nil {
				t.Fatal(err)
			}

			wantInitial := 2
			if mode == "monitor" {
				wantInitial = 0
			}
			if len(received) != wantInitial {
				t.Fatalf("after UIDVALIDITY reset want %d emails, got %d", wantInitial, len(received))
			}
			cursor, err := state.GetCursor(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if cursor == nil || cursor.MessageID != "2" || cursor.FolderID != "200" {
				t.Fatalf("reset cursor: want UID 2 in generation 200, got %+v", cursor)
			}

			// A later arrival must be delivered after either backfilling or skipping the epoch.
			conn.selectStatus = makeTestStatus(200, 4)
			conn.searchUIDs = []uint32{3}
			conn.fetchMessages = []*imap.Message{
				makeTestIMAPMessage(3, "Later email", "alice", "example.com", "Alice", now.Add(2*time.Minute)),
			}
			if err := w.pollOnce(ctx, cfg, "INBOX", handler); err != nil {
				t.Fatal(err)
			}
			if len(received) != wantInitial+1 {
				t.Fatalf("later arrival: want %d total emails, got %d", wantInitial+1, len(received))
			}
			wantIDs := []string{"1", "2", "3"}
			if mode == "monitor" {
				wantIDs = []string{"3"}
			}
			for i, msg := range received {
				if msg.ID != wantIDs[i] {
					t.Errorf("email %d: want decimal UID %q, got %q", i, wantIDs[i], msg.ID)
				}
				if msg.Source.Kind != "imap" || msg.Source.ID != "user@work.com:INBOX" || msg.Source.AccountID != "" {
					t.Errorf("mailbox generation changed the source identity: %+v", msg.Source)
				}
				if msg.IMAPUIDValidity != 200 {
					t.Errorf("UID %s: want mailbox generation 200, got %d", msg.ID, msg.IMAPUIDValidity)
				}
			}
			cursor, err = state.GetCursor(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if cursor == nil || cursor.MessageID != "3" || cursor.FolderID != "200" {
				t.Fatalf("later arrival cursor: want UID 3 in generation 200, got %+v", cursor)
			}
		})
	}
}

// TestIMAPChannel_OnFirstConnect_Monitor verifies that in "monitor" mode on first
// connection existing emails are skipped.
func TestIMAPChannel_OnFirstConnect_Monitor(t *testing.T) {
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 6), // UidNext=6, UIDs 1-5 exist
	}

	cfg := IMAPChannelConfig{
		Username:       "user@work.com",
		Folders:        []string{"INBOX"},
		OnFirstConnect: "monitor",
	}
	state := newMockStateStore()

	w := newTestIMAPChannel(conn, cfg, state)

	var count int
	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, _ model.Message) error {
		count++
		return nil
	})

	if count != 0 {
		t.Errorf("in monitor mode want 0 handler calls, got %d", count)
	}

	// The cursor must point to the last existing UID (5 = UidNext-1).
	cursor, _ := state.GetCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"))
	if cursor == nil {
		t.Fatal("cursor was not saved")
	}
	if cursor.MessageID != "5" {
		t.Errorf("cursor MessageID: want %q (UidNext-1), got %q", "5", cursor.MessageID)
	}
	if cursor.FolderID != "100" {
		t.Errorf("cursor FolderID: want %q, got %q", "100", cursor.FolderID)
	}
}

// TestIMAPChannel_OnFirstConnect_Backfill verifies that in "backfill" mode on first
// connection all existing emails are processed.
func TestIMAPChannel_OnFirstConnect_Backfill(t *testing.T) {
	now := time.Now()
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 4),
		searchUIDs:   []uint32{1, 2, 3},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(1, "Первое", "a", "x.com", "A", now),
			makeTestIMAPMessage(2, "Второе", "b", "x.com", "B", now.Add(time.Minute)),
			makeTestIMAPMessage(3, "Третье", "c", "x.com", "C", now.Add(2*time.Minute)),
		},
	}

	cfg := IMAPChannelConfig{
		Username:       "user@work.com",
		Folders:        []string{"INBOX"},
		OnFirstConnect: "backfill",
	}
	state := newMockStateStore() // no cursor

	w := newTestIMAPChannel(conn, cfg, state)

	var received []model.Message
	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 3 {
		t.Fatalf("in backfill mode want 3 emails, got %d", len(received))
	}
}

// TestIMAPChannel_NoNewMessages verifies that the handler is not called when there are no new emails.
func TestIMAPChannel_NoNewMessages(t *testing.T) {
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 4), // UidNext=4
	}

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Folders:  []string{"INBOX"},
	}
	state := newMockStateStore()
	// Cursor points to UID=3 (= UidNext-1), no new emails.
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "3",
		FolderID:  "100",
	})

	w := newTestIMAPChannel(conn, cfg, state)

	var count int
	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, _ model.Message) error {
		count++
		return nil
	})

	if count != 0 {
		t.Errorf("want 0 handler calls, got %d", count)
	}
}

// TestIMAPChannel_FetchHistoryReturnsNil verifies that FetchHistory returns nil.
func TestIMAPChannel_FetchHistoryReturnsNil(t *testing.T) {
	w := NewIMAPChannel(nil, newMockStateStore())

	msgs, err := w.FetchHistory(context.Background(), model.Source{}, 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("want nil, got %v", msgs)
	}
}

// TestIMAPChannel_AuthorNameFallback verifies that when PersonalName is empty,
// the email address is used as the author name.
func TestIMAPChannel_AuthorNameFallback(t *testing.T) {
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 3),
		searchUIDs:   []uint32{2},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(2, "Тема", "noreply", "service.com", "" /* пустое имя */, time.Now()),
		},
	}

	cfg := IMAPChannelConfig{Username: "user@work.com", Folders: []string{"INBOX"}}
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "1",
		FolderID:  "100",
	})

	w := newTestIMAPChannel(conn, cfg, state)

	var received []model.Message
	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, msg model.Message) error {
		received = append(received, msg)
		return nil
	})

	if len(received) != 1 {
		t.Fatalf("want 1 email, got %d", len(received))
	}
	// AuthorName must equal the email address.
	if received[0].AuthorName != received[0].Author {
		t.Errorf("AuthorName must equal Author when PersonalName is empty: Author=%q, AuthorName=%q",
			received[0].Author, received[0].AuthorName)
	}
	if !strings.Contains(received[0].Author, "@") {
		t.Errorf("Author must contain @: %q", received[0].Author)
	}
}

// TestIMAPChannel_Watch_ContextCancellation verifies that Watch terminates cleanly
// when the context is cancelled.
func TestIMAPChannel_Watch_ContextCancellation(t *testing.T) {
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 1), // empty mailbox
		searchUIDs:   []uint32{},
	}

	cfg := IMAPChannelConfig{
		Username:     "user@work.com",
		Folders:      []string{"INBOX"},
		PollInterval: 100 * time.Millisecond,
	}
	state := newMockStateStore()
	// Set cursor so that pollOnce returns nil immediately (no new emails).
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "0",
		FolderID:  "100",
	})

	w := newTestIMAPChannel(conn, cfg, state)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })
	}()

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Watch did not finish after context cancellation timeout")
	}
}

// TestIMAPChannel_MultipleConfigs verifies that Watch starts goroutines
// for multiple configs in parallel.
func TestIMAPChannel_MultipleConfigs(t *testing.T) {
	makeConn := func() *mockImapConn {
		return &mockImapConn{
			selectStatus: makeTestStatus(100, 2),
			searchUIDs:   []uint32{1},
			fetchMessages: []*imap.Message{
				makeTestIMAPMessage(1, "Письмо", "a", "x.com", "A", time.Now()),
			},
		}
	}

	cfgs := []IMAPChannelConfig{
		{Host: "host1", Port: 993, Username: "user1@work.com", Folders: []string{"INBOX"}, PollInterval: 50 * time.Millisecond},
		{Host: "host2", Port: 993, Username: "user2@work.com", Folders: []string{"INBOX"}, PollInterval: 50 * time.Millisecond},
	}

	state := newMockStateStore()
	// Set cursor for both mailboxes — no new emails.
	for _, cfg := range cfgs {
		_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, cfg.Folders[0]), model.Cursor{
			MessageID: "0",
			FolderID:  "100",
		})
	}

	// Each config gets its own mock via a unique address — no shared state.
	connMap := map[string]*mockImapConn{
		"host1:993": makeConn(),
		"host2:993": makeConn(),
	}
	w := &IMAPChannel{
		configs: cfgs,
		state:   state,
		dial: func(addr string) (imapConn, error) {
			return connMap[addr], nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := w.Watch(ctx, func(_ context.Context, _ model.Message) error { return nil })

	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Errorf("want context deadline/canceled, got %v", err)
	}
}

// TestIMAPChannel_MultiFolderSeparateStateKeys verifies that each folder
// uses a separate key in StateStore.
func TestIMAPChannel_MultiFolderSeparateStateKeys(t *testing.T) {
	now := time.Now()
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 3),
		searchUIDs:   []uint32{1, 2},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(1, "Тест 1", "alice", "example.com", "Alice", now),
			makeTestIMAPMessage(2, "Тест 2", "bob", "example.com", "Bob", now.Add(time.Hour)),
		},
	}

	cfg := IMAPChannelConfig{
		Username: "user@test.com",
		Folders:  []string{"INBOX", "Sent"},
	}
	state := newMockStateStore()

	w := &IMAPChannel{
		configs: []IMAPChannelConfig{cfg},
		state:   state,
		dial:    func(_ string) (imapConn, error) { return conn, nil },
	}

	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, _ model.Message) error { return nil })
	_ = w.pollOnce(context.Background(), cfg, "Sent", func(_ context.Context, _ model.Message) error { return nil })

	inboxKey := imapStateKey(cfg.Username, "INBOX")
	sentKey := imapStateKey(cfg.Username, "Sent")

	inboxCursor, _ := state.GetCursor(context.Background(), inboxKey)
	sentCursor, _ := state.GetCursor(context.Background(), sentKey)

	if inboxCursor == nil {
		t.Error("no cursor for INBOX")
	}
	if sentCursor == nil {
		t.Error("no cursor for Sent")
	}
	if inboxKey == sentKey {
		t.Error("INBOX and Sent keys must be different")
	}
}

// TestIMAPChannel_MultiFolderBothFoldersPolled verifies that with Folders = ["INBOX", "Sent"]
// the channel calls the handler for emails from both folders.
func TestIMAPChannel_MultiFolderBothFoldersPolled(t *testing.T) {
	now := time.Now()

	cfg := IMAPChannelConfig{
		Username:     "user@test.com",
		Folders:      []string{"INBOX", "Sent"},
		PollInterval: time.Hour, // large interval — poll only once
	}
	state := newMockStateStore()

	handlerCh := make(chan string, 4) // buffer for Source.ID from both folders

	w := &IMAPChannel{
		configs: []IMAPChannelConfig{cfg},
		state:   state,
		// Each dial call returns a fresh mock — no shared state between folders.
		dial: func(_ string) (imapConn, error) {
			return &mockImapConn{
				selectStatus: makeTestStatus(100, 2),
				searchUIDs:   []uint32{1},
				fetchMessages: []*imap.Message{
					makeTestIMAPMessage(1, "Тест", "alice", "example.com", "Alice", now),
				},
			}, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = w.Watch(ctx, func(_ context.Context, msg model.Message) error {
			handlerCh <- msg.Source.ID
			return nil
		})
	}()

	// Wait for the handler to be called for both folders.
	seen := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case sourceID := <-handlerCh:
			seen[sourceID] = true
		case <-ctx.Done():
			t.Fatal("timeout: handler was not called for both folders")
		}
	}

	if !seen["user@test.com:INBOX"] {
		t.Errorf("handler was not called for INBOX; got: %v", seen)
	}
	if !seen["user@test.com:Sent"] {
		t.Errorf("handler was not called for Sent; got: %v", seen)
	}
}

// TestConvertIMAPMessage_LabelAsSourceName verifies that Label is used as Source.Name,
// and when Label is empty the current folder name is used as a fallback.
func TestConvertIMAPMessage_LabelAsSourceName(t *testing.T) {
	tests := []struct {
		name     string
		label    string
		folder   string
		wantName string
	}{
		{
			name:     "label задан — используется как Source.Name",
			label:    "Рабочая почта",
			folder:   "INBOX",
			wantName: "Рабочая почта",
		},
		{
			name:     "label пустой — fallback на folder",
			label:    "",
			folder:   "INBOX",
			wantName: "INBOX",
		},
		{
			name:     "label пустой, нестандартная папка",
			label:    "",
			folder:   "Work/Projects",
			wantName: "Work/Projects",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &IMAPChannel{}
			msg := makeTestIMAPMessage(1, "Тема", "alice", "example.com", "Alice", time.Now())
			cfg := IMAPChannelConfig{
				Username: "user@work.com",
				Label:    tt.label,
			}
			got := w.convertIMAPMessage(msg, cfg, tt.folder)
			if got == nil {
				t.Fatal("want non-nil")
			}
			if got.Source.Name != tt.wantName {
				t.Errorf("Source.Name: want %q, got %q", tt.wantName, got.Source.Name)
			}
		})
	}
}

// TestConvertIMAPMessage_SubjectAndTextSeparate verifies that the email subject is stored
// in Message.Subject and the body in Message.Text without concatenation.
func TestConvertIMAPMessage_SubjectAndTextSeparate(t *testing.T) {
	w := &IMAPChannel{}
	msg := makeTestIMAPMessage(1, "Важная тема письма", "alice", "example.com", "Alice", time.Now())
	cfg := IMAPChannelConfig{Username: "user@work.com"}

	got := w.convertIMAPMessage(msg, cfg, "INBOX")
	if got == nil {
		t.Fatal("want non-nil")
	}
	// Subject must store the email subject.
	if got.Subject != "Важная тема письма" {
		t.Errorf("Subject: want %q, got %q", "Важная тема письма", got.Subject)
	}
	// Text without a body must be empty (must not include Subject).
	if got.Text != "" {
		t.Errorf("Text without email body must be empty: got %q", got.Text)
	}
	// Source.Kind must be "imap".
	if got.Source.Kind != "imap" {
		t.Errorf("Source.Kind: want %q, got %q", "imap", got.Source.Kind)
	}
}

// TestConvertIMAPMessage_SentEmail_SplitsReplyAndQuote verifies that an email from the user
// themselves (from == cfg.Username) is split into reply and quote, bypassing the senders filter.
func TestConvertIMAPMessage_SentEmail_SplitsReplyAndQuote(t *testing.T) {
	w := &IMAPChannel{}

	rawBody := "Content-Type: text/plain; charset=utf-8\r\n\r\nда, сделаю\r\n\r\nOn Mon wrote:\r\n> Гриша, заведи аккаунт!"
	section := &imap.BodySectionName{}
	msg := makeTestIMAPMessage(1, "Re: Новый аккаунт", "user", "work.com", "User", time.Now())
	msg.Body = map[*imap.BodySectionName]imap.Literal{
		section: strings.NewReader(rawBody),
	}

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Senders:  []string{"boss@example.com"}, // sent email must bypass this filter
	}

	got := w.convertIMAPMessage(msg, cfg, "Sent")

	if got == nil {
		t.Fatal("want non-nil for sent email")
	}
	if got.Text != "да, сделаю" {
		t.Errorf("Text: want %q, got %q", "да, сделаю", got.Text)
	}
	if got.ReplyTo == nil {
		t.Fatal("ReplyTo: want non-nil for email with quote")
	}
	if !strings.Contains(got.ReplyTo.Text, "Гриша, заведи аккаунт!") {
		t.Errorf("ReplyTo.Text: want to contain %q, got %q", "Гриша, заведи аккаунт!", got.ReplyTo.Text)
	}
}

// TestConvertIMAPMessage_SentEmail_OnlyQuote verifies that a forwarded email with no own reply
// (reply == "") returns nil — nothing to process.
func TestConvertIMAPMessage_SentEmail_OnlyQuote(t *testing.T) {
	w := &IMAPChannel{}

	// Only a quote, no own text.
	rawBody := "Content-Type: text/plain; charset=utf-8\r\n\r\n> Исходное письмо\r\n> Продолжение"
	section := &imap.BodySectionName{}
	msg := makeTestIMAPMessage(1, "Fwd: Важное", "user", "work.com", "User", time.Now())
	msg.Body = map[*imap.BodySectionName]imap.Literal{
		section: strings.NewReader(rawBody),
	}

	cfg := IMAPChannelConfig{Username: "user@work.com"}

	got := w.convertIMAPMessage(msg, cfg, "Sent")

	if got != nil {
		t.Errorf("want nil for forwarded email with no reply, got %v", got.Text)
	}
}

// TestConvertIMAPMessage_SentEmail_NoQuote verifies that a sent email without a quote
// is returned with ReplyTo == nil and the full text in Text.
func TestConvertIMAPMessage_SentEmail_NoQuote(t *testing.T) {
	w := &IMAPChannel{}

	rawBody := "Content-Type: text/plain; charset=utf-8\r\n\r\nПривет, это я"
	section := &imap.BodySectionName{}
	msg := makeTestIMAPMessage(1, "Привет", "user", "work.com", "User", time.Now())
	msg.Body = map[*imap.BodySectionName]imap.Literal{
		section: strings.NewReader(rawBody),
	}

	cfg := IMAPChannelConfig{Username: "user@work.com"}

	got := w.convertIMAPMessage(msg, cfg, "Sent")

	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Text != "Привет, это я" {
		t.Errorf("Text: want %q, got %q", "Привет, это я", got.Text)
	}
	if got.ReplyTo != nil {
		t.Errorf("ReplyTo: want nil for email with no quote, got %v", got.ReplyTo)
	}
}

// TestConvertIMAPMessage_IncomingEmail_Unchanged verifies that an incoming email
// (from != username) is processed unchanged (legacy behaviour).
func TestConvertIMAPMessage_IncomingEmail_Unchanged(t *testing.T) {
	w := &IMAPChannel{}

	rawBody := "Content-Type: text/plain; charset=utf-8\r\n\r\nСделай отчёт"
	section := &imap.BodySectionName{}
	msg := makeTestIMAPMessage(1, "Задача", "alice", "example.com", "Alice", time.Now())
	msg.Body = map[*imap.BodySectionName]imap.Literal{
		section: strings.NewReader(rawBody),
	}

	cfg := IMAPChannelConfig{Username: "user@work.com"}

	got := w.convertIMAPMessage(msg, cfg, "INBOX")

	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Text != "Сделай отчёт" {
		t.Errorf("Text: want %q, got %q", "Сделай отчёт", got.Text)
	}
	if got.ReplyTo != nil {
		t.Errorf("ReplyTo: want nil for incoming email, got %v", got.ReplyTo)
	}
}

// TestConvertIMAPMessage_IncomingEmail_FilteredBySenders verifies that an incoming email
// from a sender not in the Senders list returns nil.
func TestConvertIMAPMessage_IncomingEmail_FilteredBySenders(t *testing.T) {
	w := &IMAPChannel{}

	msg := makeTestIMAPMessage(1, "Тема", "denied", "example.com", "Denied", time.Now())

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Senders:  []string{"allowed@example.com"},
	}

	got := w.convertIMAPMessage(msg, cfg, "INBOX")

	if got != nil {
		t.Errorf("want nil for sender not in Senders, got %v", got)
	}
}

// TestIMAPChannel_HandlerError_StopsCursorAdvance verifies the at-least-once guarantee:
// on handler error the cursor must not advance past the failed email,
// otherwise a subsequent poll will skip it forever.
func TestIMAPChannel_HandlerError_StopsCursorAdvance(t *testing.T) {
	now := time.Now()
	conn := &mockImapConn{
		selectStatus: makeTestStatus(100, 4),
		searchUIDs:   []uint32{2, 3},
		fetchMessages: []*imap.Message{
			makeTestIMAPMessage(2, "Упавшее", "alice", "example.com", "Alice", now),
			makeTestIMAPMessage(3, "Следующее", "bob", "example.com", "Bob", now.Add(time.Hour)),
		},
	}

	cfg := IMAPChannelConfig{
		Username: "user@work.com",
		Folders:  []string{"INBOX"},
	}
	state := newMockStateStore()
	_ = state.SaveCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"), model.Cursor{
		MessageID: "1",
		FolderID:  "100",
	})

	w := newTestIMAPChannel(conn, cfg, state)

	handlerErr := errors.New("ошибка обработки")
	var called int
	_ = w.pollOnce(context.Background(), cfg, "INBOX", func(_ context.Context, _ model.Message) error {
		called++
		return handlerErr // error on the first email (UID=2)
	})

	// Handler must be called exactly once — processing stopped after the error.
	if called != 1 {
		t.Errorf("handler must be called 1 time (before error), got %d", called)
	}

	// The cursor must not advance: the email with UID=2 will be re-fetched on the next poll.
	cursor, _ := state.GetCursor(context.Background(), imapStateKey(cfg.Username, "INBOX"))
	if cursor == nil {
		t.Fatal("cursor must not be nil")
	}
	if cursor.MessageID != "1" {
		t.Errorf("cursor MessageID: want %q (not advanced), got %q", "1", cursor.MessageID)
	}
}

// TestConvertIMAPMessage_InternalDatePreferredOverEnvelopeDate verifies that
// InternalDate (server reception date) is used as Timestamp instead of Envelope.Date.
func TestConvertIMAPMessage_InternalDatePreferredOverEnvelopeDate(t *testing.T) {
	w := &IMAPChannel{}
	envelopeDate := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	internalDate := time.Date(2026, 1, 10, 9, 30, 0, 0, time.UTC) // server reception time

	msg := makeTestIMAPMessage(1, "Тест", "alice", "example.com", "Alice", envelopeDate)
	msg.InternalDate = internalDate

	cfg := IMAPChannelConfig{Username: "user@work.com"}
	got := w.convertIMAPMessage(msg, cfg, "INBOX")
	if got == nil {
		t.Fatal("want non-nil")
	}
	if !got.Timestamp.Equal(internalDate) {
		t.Errorf("Timestamp = %v, want InternalDate %v", got.Timestamp, internalDate)
	}
}

// TestConvertIMAPMessage_FallsBackToEnvelopeDateWhenInternalDateZero verifies that
// when InternalDate is zero, Envelope.Date is used.
func TestConvertIMAPMessage_FallsBackToEnvelopeDateWhenInternalDateZero(t *testing.T) {
	w := &IMAPChannel{}
	envelopeDate := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)

	msg := makeTestIMAPMessage(1, "Тест", "alice", "example.com", "Alice", envelopeDate)
	// msg.InternalDate remains zero (imap.FetchInternalDate was not requested)

	cfg := IMAPChannelConfig{Username: "user@work.com"}
	got := w.convertIMAPMessage(msg, cfg, "INBOX")
	if got == nil {
		t.Fatal("want non-nil")
	}
	if !got.Timestamp.Equal(envelopeDate) {
		t.Errorf("Timestamp = %v, want Envelope.Date %v", got.Timestamp, envelopeDate)
	}
}

// TestIMAPChannel_ID verifies the ID() method.
func TestIMAPChannel_ID(t *testing.T) {
	w := NewIMAPChannel(nil, newMockStateStore())
	if w.ID() != "" {
		t.Errorf("ID: want empty string, got %q", w.ID())
	}
}

// TestConvertIMAPMessage_KindIsBatch verifies that an IMAP message gets Kind=Batch.
func TestConvertIMAPMessage_KindIsBatch(t *testing.T) {
	w := &IMAPChannel{}
	msg := makeTestIMAPMessage(1, "Тема", "alice", "example.com", "Alice", time.Now())
	cfg := IMAPChannelConfig{Username: "user@work.com"}

	got := w.convertIMAPMessage(msg, cfg, "INBOX")

	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Kind != model.MessageKindBatch {
		t.Errorf("Kind: want %q, got %q", model.MessageKindBatch, got.Kind)
	}
}

// TestConvertIMAPMessage_NilCallbacks verifies that an IMAP message leaves ReactFn and ReplyFn nil.
func TestConvertIMAPMessage_NilCallbacks(t *testing.T) {
	w := &IMAPChannel{}
	msg := makeTestIMAPMessage(1, "Тема", "alice", "example.com", "Alice", time.Now())
	cfg := IMAPChannelConfig{Username: "user@work.com"}

	got := w.convertIMAPMessage(msg, cfg, "INBOX")

	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.ReactFn != nil {
		t.Error("ReactFn must be nil for IMAP message")
	}
	if got.ReplyFn != nil {
		t.Error("ReplyFn must be nil for IMAP message")
	}
}

type imapIntakeStore struct {
	model.AutoTaskStore
	failUID string
	failure error
	lookups []string
	writes  []string
}

func (s *imapIntakeStore) Processed(ctx context.Context, key string) (bool, error) {
	s.lookups = append(s.lookups, key)
	return s.AutoTaskStore.Processed(ctx, key)
}

func (s *imapIntakeStore) RecordInputTx(ctx context.Context, tx *sql.Tx, key string, at time.Time) error {
	s.writes = append(s.writes, key)
	if s.failUID != "" && strings.Contains(key, fmt.Sprintf("/%q/", s.failUID)) {
		return s.failure
	}
	return s.AutoTaskStore.RecordInputTx(ctx, tx, key, at)
}

type imapIntakeInference struct {
	classified []string
	extracted  []string
}

func (i *imapIntakeInference) Classify(_ context.Context, msg model.Message) (model.Classification, error) {
	i.classified = append(i.classified, msg.ID)
	return model.ClassPromise, nil
}

func (i *imapIntakeInference) Extract(_ context.Context, msg model.Message, _ []model.HistoryEntry) ([]model.Task, error) {
	i.extracted = append(i.extracted, msg.ID)
	return []model.Task{{Summary: "Task " + msg.ID, Source: msg.Source}}, nil
}

type imapIntakeChecker struct{}

func (imapIntakeChecker) Compare(context.Context, model.CreateTaskRequest, string, []model.TaskReference) (model.TaskAssessment, error) {
	return model.TaskAssessment{Verdict: "different", Score: 0}, nil
}

type imapIntakeNotifier struct {
	tasks []model.Task
}

func (n *imapIntakeNotifier) Name() string { return "intake-test" }

func (n *imapIntakeNotifier) Notify(_ context.Context, tasks []model.Task) error {
	n.tasks = append(n.tasks, tasks...)
	return nil
}

type imapIntakeHarness struct {
	db        *sql.DB
	store     *imapIntakeStore
	inference *imapIntakeInference
	notifier  *imapIntakeNotifier
	queue     chan model.Message
	handler   func(context.Context, model.Message) error
}

func newIMAPIntakeHarness(t *testing.T) *imapIntakeHarness {
	t.Helper()
	db, err := storage.OpenDB(filepath.Join(t.TempDir(), "intake.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &imapIntakeStore{
		AutoTaskStore: storage.NewSQLiteAutoTaskStore(db),
		failure:       errors.New("receipt persistence failed"),
	}
	auto := usecase.NewAutoTaskService(usecase.AutoTaskServiceDeps{
		DB: db, Tasks: tasks, Store: store, Checker: imapIntakeChecker{},
		Events: events.NewSQLiteEventStore(db), Devices: devices.NewSQLiteDeviceStore(db),
		Queue: push.NewSQLitePushQueue(db), Broker: events.NewBroker(events.BrokerConfig{}),
		Logger: logger,
	})
	inference := &imapIntakeInference{}
	notifier := &imapIntakeNotifier{}
	pipe := pipeline.New(pipeline.Config{
		AutoTasks:   auto,
		Classifiers: map[model.MessageKind]model.Classifier{model.MessageKindBatch: inference},
		Extractors:  map[model.MessageKind]model.Extractor{model.MessageKindBatch: inference},
		Notifiers:   []model.Notifier{notifier}, Logger: logger,
	})
	queue := make(chan model.Message, 100)
	return &imapIntakeHarness{
		db: db, store: store, inference: inference, notifier: notifier,
		queue: queue, handler: pipe.WatchHandler(queue),
	}
}

func setIMAPIntakeResponses(conn *mockImapConn, validity uint32, uids ...uint32) {
	conn.searchUIDs = append([]uint32(nil), uids...)
	conn.fetchMessages = nil
	conn.fetchSent = 0
	var maxUID uint32
	for _, uid := range uids {
		if uid > maxUID {
			maxUID = uid
		}
		msg := makeTestIMAPMessage(uid, fmt.Sprintf("Email %d", uid), "sender", "example.com", "Sender",
			time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
		section := &imap.BodySectionName{}
		msg.Body = map[*imap.BodySectionName]imap.Literal{
			section: strings.NewReader(fmt.Sprintf("Content-Type: text/plain; charset=utf-8\r\n\r\nObligation %d", uid)),
		}
		conn.fetchMessages = append(conn.fetchMessages, msg)
	}
	conn.selectStatus = makeTestStatus(validity, maxUID+1)
}

func (h *imapIntakeHarness) assertCommitted(t *testing.T, uids ...string) {
	t.Helper()
	for _, table := range []string{"auto_task_inputs", "auto_task_candidates", "tasks"} {
		var count int
		if err := h.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != len(uids) {
			t.Errorf("%s rows = %d, want %d", table, count, len(uids))
		}
	}
	var summaries []string
	rows, err := h.db.Query("SELECT summary FROM tasks ORDER BY summary")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var summary string
		if err := rows.Scan(&summary); err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantSummaries := make([]string, 0, len(uids))
	for _, uid := range uids {
		wantSummaries = append(wantSummaries, "Task "+uid)
	}
	sort.Strings(wantSummaries)
	if !reflect.DeepEqual(summaries, wantSummaries) && len(uids) != 0 {
		t.Errorf("durable tasks = %v, want %v", summaries, wantSummaries)
	}
	var notified []string
	for _, task := range h.notifier.tasks {
		notified = append(notified, task.SourceMessage.ID)
	}
	if !reflect.DeepEqual(notified, uids) {
		t.Errorf("notified UIDs = %v, want %v", notified, uids)
	}
}

func TestIMAPChannel_AutoTaskHandlerFailureKeepsCursorAndRetries(t *testing.T) {
	ctx := context.Background()
	h := newIMAPIntakeHarness(t)
	h.store.failUID = "2"
	cfg := IMAPChannelConfig{Username: "owner@example.com", OnFirstConnect: "backfill"}
	state := newMockStateStore()
	key := imapStateKey(cfg.Username, "INBOX")
	if err := state.SaveCursor(ctx, key, model.Cursor{MessageID: "1", FolderID: "100"}); err != nil {
		t.Fatal(err)
	}
	conn := &mockImapConn{}
	setIMAPIntakeResponses(conn, 100, 2, 3)
	w := newTestIMAPChannel(conn, cfg, state)
	if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
		t.Fatalf("handler failure must only be logged: %v", err)
	}
	cursor, err := state.GetCursor(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.MessageID != "1" || state.saveCursorCalls != 1 || len(h.queue) != 0 {
		t.Fatalf("failed intake acknowledged: cursor=%+v saves=%d queued=%d; want UID 1, no save, empty queue",
			cursor, state.saveCursorCalls-1, len(h.queue))
	}
	if !reflect.DeepEqual(h.inference.classified, []string{"2"}) {
		t.Errorf("classified = %v, want only failed UID 2", h.inference.classified)
	}
	h.assertCommitted(t)

	h.store.failUID = ""
	setIMAPIntakeResponses(conn, 100, 2, 3)
	if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
		t.Fatal(err)
	}
	cursor, _ = state.GetCursor(ctx, key)
	if cursor.MessageID != "3" || state.saveCursorCalls != 2 || len(h.queue) != 0 {
		t.Errorf("retry cursor=%+v saves=%d queued=%d", cursor, state.saveCursorCalls-1, len(h.queue))
	}
	if !reflect.DeepEqual(h.inference.classified, []string{"2", "2", "3"}) ||
		!reflect.DeepEqual(h.inference.extracted, []string{"2", "2", "3"}) {
		t.Errorf("retry inference classified=%v extracted=%v", h.inference.classified, h.inference.extracted)
	}
	h.assertCommitted(t, "2", "3")
}

func TestIMAPChannel_AutoTaskSuccessBeforeFailureRetainsPollCursor(t *testing.T) {
	for _, tc := range []struct {
		name              string
		first             []uint32
		failed            string
		committed         string
		wantInference     []string
		wantNotifications []string
	}{
		{
			name: "ascending", first: []uint32{2, 3, 4}, failed: "3", committed: "2",
			wantInference:     []string{"2", "3", "3", "4"},
			wantNotifications: []string{"2", "3", "4"},
		},
		{
			name: "nonascending", first: []uint32{3, 2, 4}, failed: "2", committed: "3",
			wantInference:     []string{"3", "2", "2", "4"},
			wantNotifications: []string{"3", "2", "4"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newIMAPIntakeHarness(t)
			h.store.failUID = tc.failed
			cfg := IMAPChannelConfig{Username: "owner@example.com", OnFirstConnect: "backfill"}
			state := newMockStateStore()
			key := imapStateKey(cfg.Username, "INBOX")
			initial := model.Cursor{MessageID: "1", FolderID: "100", UpdatedAt: time.Now()}
			if err := state.SaveCursor(ctx, key, initial); err != nil {
				t.Fatal(err)
			}
			conn := &mockImapConn{}
			setIMAPIntakeResponses(conn, 100, tc.first...)
			w := newTestIMAPChannel(conn, cfg, state)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatalf("handler failure must only be logged: %v", err)
			}
			cursor, _ := state.GetCursor(ctx, key)
			if !reflect.DeepEqual(cursor, &initial) || state.saveCursorCalls != 1 {
				t.Errorf("failed poll changed cursor: got %+v, saves=%d; want untouched %+v and no save",
					cursor, state.saveCursorCalls-1, initial)
			}
			if len(h.queue) != 0 || len(h.store.lookups) != 2 ||
				!reflect.DeepEqual(h.inference.classified, []string{tc.committed, tc.failed}) {
				t.Fatalf("trailing UID 4 handled or queued: lookups=%v classified=%v queued=%d",
					h.store.lookups, h.inference.classified, len(h.queue))
			}
			h.assertCommitted(t, tc.committed)
			committedKey := h.store.lookups[0]
			processed, err := h.store.AutoTaskStore.Processed(ctx, committedKey)
			if err != nil || !processed {
				t.Fatalf("prior success has no durable receipt: processed=%v error=%v", processed, err)
			}
			failedKey := h.store.lookups[1]
			processed, err = h.store.AutoTaskStore.Processed(ctx, failedKey)
			if err != nil || processed {
				t.Fatalf("failed receipt transaction was not rolled back: processed=%v error=%v", processed, err)
			}

			h.store.failUID = ""
			// The mock search does not filter by cursor; explicitly return the retry set
			// and rebuild every MIME reader, reversing the initial nonascending order.
			setIMAPIntakeResponses(conn, 100, 2, 3, 4)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatal(err)
			}
			cursor, _ = state.GetCursor(ctx, key)
			if cursor.MessageID != "4" || cursor.FolderID != "100" || state.saveCursorCalls != 2 || len(h.queue) != 0 {
				t.Errorf("successful retry cursor=%+v saves=%d queued=%d; want UID 4, one save, empty queue",
					cursor, state.saveCursorCalls-1, len(h.queue))
			}
			if !reflect.DeepEqual(h.inference.classified, tc.wantInference) ||
				!reflect.DeepEqual(h.inference.extracted, tc.wantInference) {
				t.Errorf("committed replay repeated inference: classified=%v extracted=%v want=%v",
					h.inference.classified, h.inference.extracted, tc.wantInference)
			}
			if len(h.store.lookups) != 5 || len(h.store.writes) != 4 {
				t.Errorf("receipt lookup/write attempts = %d/%d, want 5/4", len(h.store.lookups), len(h.store.writes))
			}
			writesForCommitted := 0
			for _, key := range h.store.writes {
				if key == committedKey {
					writesForCommitted++
				}
			}
			if writesForCommitted != 1 {
				t.Errorf("committed UID %s repeated submission: receipt writes=%d", tc.committed, writesForCommitted)
			}
			h.assertCommitted(t, tc.wantNotifications...)
		})
	}
}

func TestIMAPChannel_AutoTaskFailureDrainsFetchAndPreservesFetchError(t *testing.T) {
	for _, fetchFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("fetch_error_%t", fetchFails), func(t *testing.T) {
			ctx := context.Background()
			h := newIMAPIntakeHarness(t)
			h.store.failUID = "2"
			cfg := IMAPChannelConfig{Username: "owner@example.com"}
			state := newMockStateStore()
			key := imapStateKey(cfg.Username, "INBOX")
			initial := model.Cursor{MessageID: "1", FolderID: "100"}
			if err := state.SaveCursor(ctx, key, initial); err != nil {
				t.Fatal(err)
			}
			uids := []uint32{3, 2}
			for uid := uint32(4); uid <= 30; uid++ {
				uids = append(uids, uid)
			}
			conn := &mockImapConn{}
			setIMAPIntakeResponses(conn, 100, uids...)
			fetchErr := errors.New("FETCH connection interrupted")
			if fetchFails {
				conn.fetchErr = fetchErr
			}
			w := newTestIMAPChannel(conn, cfg, state)
			done := make(chan error, 1)
			go func() { done <- w.pollOnce(ctx, cfg, "INBOX", h.handler) }()
			select {
			case err := <-done:
				if fetchFails && !errors.Is(err, fetchErr) {
					t.Errorf("FETCH error lost after handler failure: %v", err)
				}
				if !fetchFails && err != nil {
					t.Errorf("handler error unexpectedly returned: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("poll blocked instead of draining more than ten trailing FETCH responses")
			}
			if conn.fetchSent != len(uids) {
				t.Errorf("FETCH sent %d responses, want all %d drained", conn.fetchSent, len(uids))
			}
			cursor, _ := state.GetCursor(ctx, key)
			if !reflect.DeepEqual(cursor, &initial) || state.saveCursorCalls != 1 {
				t.Errorf("failure changed cursor: %+v, saves=%d; want untouched cursor and no save",
					cursor, state.saveCursorCalls-1)
			}
			if !reflect.DeepEqual(h.inference.classified, []string{"3", "2"}) ||
				len(h.store.lookups) != 2 || len(h.queue) != 0 {
				t.Errorf("trailing responses handled: classified=%v lookups=%v queued=%d",
					h.inference.classified, h.store.lookups, len(h.queue))
			}
			h.assertCommitted(t, "3")
		})
	}
}

func TestIMAPChannel_AutoTaskBackfillFailureDoesNotWriteCursor(t *testing.T) {
	for _, changedGeneration := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed_generation_%t", changedGeneration), func(t *testing.T) {
			ctx := context.Background()
			h := newIMAPIntakeHarness(t)
			h.store.failUID = "2"
			cfg := IMAPChannelConfig{Username: "owner@example.com", OnFirstConnect: "backfill"}
			state := newMockStateStore()
			key := imapStateKey(cfg.Username, "INBOX")
			var initial *model.Cursor
			if changedGeneration {
				initial = &model.Cursor{MessageID: "50", FolderID: "100", UpdatedAt: time.Now()}
				if err := state.SaveCursor(ctx, key, *initial); err != nil {
					t.Fatal(err)
				}
			}
			savesBefore := state.saveCursorCalls
			conn := &mockImapConn{}
			setIMAPIntakeResponses(conn, 200, 1, 2, 3)
			w := newTestIMAPChannel(conn, cfg, state)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatal(err)
			}
			cursor, _ := state.GetCursor(ctx, key)
			if !reflect.DeepEqual(cursor, initial) || state.saveCursorCalls != savesBefore {
				t.Errorf("failed backfill saved new generation: got %+v saves=%d, want %+v and no save",
					cursor, state.saveCursorCalls-savesBefore, initial)
			}
			if !reflect.DeepEqual(h.inference.classified, []string{"1", "2"}) ||
				len(h.store.lookups) != 2 || len(h.queue) != 0 {
				t.Errorf("backfill continued after failure: classified=%v lookups=%v queued=%d",
					h.inference.classified, h.store.lookups, len(h.queue))
			}
			h.assertCommitted(t, "1")
			if h.notifier.tasks[0].SourceMessage.IMAPUIDValidity != 200 {
				t.Error("backfill did not deliver the selected mailbox generation")
			}
			h.store.failUID = ""
			setIMAPIntakeResponses(conn, 200, 1, 2, 3)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatal(err)
			}
			cursor, _ = state.GetCursor(ctx, key)
			if cursor == nil || cursor.MessageID != "3" || cursor.FolderID != "200" ||
				state.saveCursorCalls != savesBefore+1 {
				t.Errorf("successful backfill did not save new generation once: cursor=%+v saves=%d",
					cursor, state.saveCursorCalls-savesBefore)
			}
			if !reflect.DeepEqual(h.inference.classified, []string{"1", "2", "2", "3"}) {
				t.Errorf("backfill repeated committed input inference: %v", h.inference.classified)
			}
			h.assertCommitted(t, "1", "2", "3")
		})
	}
}

func TestIMAPChannel_AutoTaskMonitorSkipsExistingBeforeDurableIntake(t *testing.T) {
	for _, changedGeneration := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed_generation_%t", changedGeneration), func(t *testing.T) {
			ctx := context.Background()
			h := newIMAPIntakeHarness(t)
			cfg := IMAPChannelConfig{Username: "owner@example.com", OnFirstConnect: "monitor"}
			state := newMockStateStore()
			key := imapStateKey(cfg.Username, "INBOX")
			if changedGeneration {
				if err := state.SaveCursor(ctx, key, model.Cursor{MessageID: "50", FolderID: "100"}); err != nil {
					t.Fatal(err)
				}
			}
			savesBefore := state.saveCursorCalls
			conn := &mockImapConn{}
			setIMAPIntakeResponses(conn, 200, 1, 2, 3)
			w := newTestIMAPChannel(conn, cfg, state)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatal(err)
			}
			cursor, _ := state.GetCursor(ctx, key)
			if cursor == nil || cursor.MessageID != "3" || cursor.FolderID != "200" ||
				state.saveCursorCalls != savesBefore+1 || conn.fetchSent != 0 || len(h.store.lookups) != 0 {
				t.Fatalf("monitor did not skip existing messages: cursor=%+v saves=%d fetched=%d lookups=%v",
					cursor, state.saveCursorCalls-savesBefore, conn.fetchSent, h.store.lookups)
			}
			h.assertCommitted(t)
			skipped := *cursor
			h.store.failUID = "4"
			setIMAPIntakeResponses(conn, 200, 4)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatal(err)
			}
			cursor, _ = state.GetCursor(ctx, key)
			if !reflect.DeepEqual(cursor, &skipped) || state.saveCursorCalls != savesBefore+1 {
				t.Errorf("failed new email changed monitor cursor: %+v saves=%d", cursor, state.saveCursorCalls-savesBefore)
			}
			h.assertCommitted(t)
			h.store.failUID = ""
			setIMAPIntakeResponses(conn, 200, 4)
			if err := w.pollOnce(ctx, cfg, "INBOX", h.handler); err != nil {
				t.Fatal(err)
			}
			cursor, _ = state.GetCursor(ctx, key)
			if cursor.MessageID != "4" || cursor.FolderID != "200" || state.saveCursorCalls != savesBefore+2 {
				t.Errorf("monitor retry cursor=%+v saves=%d", cursor, state.saveCursorCalls-savesBefore)
			}
			if !reflect.DeepEqual(h.inference.classified, []string{"4", "4"}) {
				t.Errorf("monitor classified old input or skipped retry: %v", h.inference.classified)
			}
			h.assertCommitted(t, "4")
		})
	}
}

func TestIMAPChannel_WithoutAutoTasksAcknowledgesQueuedEmail(t *testing.T) {
	ctx := context.Background()
	cfg := IMAPChannelConfig{Username: "owner@example.com"}
	state := newMockStateStore()
	key := imapStateKey(cfg.Username, "INBOX")
	if err := state.SaveCursor(ctx, key, model.Cursor{MessageID: "1", FolderID: "100"}); err != nil {
		t.Fatal(err)
	}
	conn := &mockImapConn{}
	setIMAPIntakeResponses(conn, 100, 2, 3)
	inference := &imapIntakeInference{}
	pipe := pipeline.New(pipeline.Config{
		Classifiers: map[model.MessageKind]model.Classifier{model.MessageKindBatch: inference},
		Extractors:  map[model.MessageKind]model.Extractor{model.MessageKindBatch: inference},
	})
	queue := make(chan model.Message, 2)
	w := newTestIMAPChannel(conn, cfg, state)
	if err := w.pollOnce(ctx, cfg, "INBOX", pipe.WatchHandler(queue)); err != nil {
		t.Fatal(err)
	}
	cursor, _ := state.GetCursor(ctx, key)
	if cursor.MessageID != "3" || state.saveCursorCalls != 2 || len(queue) != 2 || len(inference.classified) != 0 {
		t.Fatalf("no-service email did not retain queued acknowledgement: cursor=%+v saves=%d queued=%d classified=%v",
			cursor, state.saveCursorCalls-1, len(queue), inference.classified)
	}
	for _, wantUID := range []string{"2", "3"} {
		msg := <-queue
		if msg.ID != wantUID || msg.IMAPUIDValidity != 100 || msg.Kind != model.MessageKindBatch {
			t.Errorf("queued email lost identity: %+v", msg)
		}
	}
}
