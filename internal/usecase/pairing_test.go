package usecase_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/pushproto"
	"github.com/anadale/huskwoot/internal/usecase"
)

// mockPairingStore is a mock of model.PairingStore for PairingService tests.
type mockPairingStore struct {
	created   *model.PendingPairing
	createErr error
	getResult *model.PendingPairing
	// getSeq: if non-empty, each Get call returns the next element;
	// after exhaustion returns the last one.
	getSeq       []*model.PendingPairing
	getCallCount int
	// onGetCall: if set, called before each Get return (after counter increment).
	onGetCall      func(callIndex int)
	getErr         error
	csrfID         string
	csrfHash       string
	confirmedID    string
	confirmedDevID string
	deleteCount    int64
	deleteErr      error
}

func (m *mockPairingStore) CreateTx(_ context.Context, _ *sql.Tx, p *model.PendingPairing) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.created = p
	return nil
}

func (m *mockPairingStore) Get(_ context.Context, _ string) (*model.PendingPairing, error) {
	if len(m.getSeq) > 0 {
		idx := m.getCallCount
		if idx >= len(m.getSeq) {
			idx = len(m.getSeq) - 1
		}
		m.getCallCount++
		if m.onGetCall != nil {
			m.onGetCall(m.getCallCount - 1)
		}
		return m.getSeq[idx], m.getErr
	}
	return m.getResult, m.getErr
}

func (m *mockPairingStore) SetCSRFTx(_ context.Context, _ *sql.Tx, id, csrfHash string) error {
	m.csrfID = id
	m.csrfHash = csrfHash
	return nil
}

func (m *mockPairingStore) MarkConfirmedTx(_ context.Context, _ *sql.Tx, id, deviceID string) error {
	m.confirmedID = id
	m.confirmedDevID = deviceID
	return nil
}

func (m *mockPairingStore) DeleteExpired(_ context.Context, _ time.Time) (int64, error) {
	return m.deleteCount, m.deleteErr
}

// mockPairingNotifier is a mock of usecase.PairingNotifier.
type mockPairingNotifier struct {
	called     bool
	chatID     int64
	deviceName string
	magicURL   string
	err        error
}

func (m *mockPairingNotifier) SendMagicLink(_ context.Context, chatID int64, deviceName, magicURL string) error {
	m.called = true
	m.chatID = chatID
	m.deviceName = deviceName
	m.magicURL = magicURL
	return m.err
}

// mockPairingBroadcaster is a mock of usecase.PairingBroadcaster.
type mockPairingBroadcaster struct {
	mu         sync.Mutex
	channels   map[string]chan model.PairingResult
	lastNotify *model.PairingResult
	subscribed chan struct{}
	once       sync.Once
}

func newMockPairingBroadcaster() *mockPairingBroadcaster {
	return &mockPairingBroadcaster{
		channels:   make(map[string]chan model.PairingResult),
		subscribed: make(chan struct{}),
	}
}

func (m *mockPairingBroadcaster) Subscribe(pairID string) (<-chan model.PairingResult, func()) {
	ch := make(chan model.PairingResult, 1)
	m.mu.Lock()
	m.channels[pairID] = ch
	m.mu.Unlock()
	m.once.Do(func() { close(m.subscribed) })
	cleanup := func() {
		m.mu.Lock()
		delete(m.channels, pairID)
		m.mu.Unlock()
	}
	return ch, cleanup
}

func (m *mockPairingBroadcaster) Notify(pairID string, result model.PairingResult) {
	m.mu.Lock()
	cp := result
	m.lastNotify = &cp
	ch, ok := m.channels[pairID]
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- result:
	default:
	}
}

var pairingTestTime = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

func pairingSHA256HexTest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newTestPairingSvc(
	t *testing.T,
	store *mockPairingStore,
	notifier *mockPairingNotifier,
	bc *mockPairingBroadcaster,
	longPoll time.Duration,
) model.PairingService {
	t.Helper()
	db := openTestDB(t)
	if longPoll <= 0 {
		longPoll = 60 * time.Second
	}
	return usecase.NewPairingService(usecase.PairingDeps{
		DB:              db,
		PairingStore:    store,
		Sender:          notifier,
		Broadcaster:     bc,
		OwnerChatID:     12345,
		LinkTTL:         5 * time.Minute,
		LongPoll:        longPoll,
		ExternalBaseURL: "https://huskwoot.example.com",
		Now:             func() time.Time { return pairingTestTime },
	})
}

func TestPairingService_RequestPairing_PersistsAndSendsDM(t *testing.T) {
	store := &mockPairingStore{}
	notifier := &mockPairingNotifier{}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, notifier, bc, 0)

	req := model.PairingRequest{
		DeviceName:  "iPhone 17",
		Platform:    "ios",
		ClientNonce: "test-nonce-value",
	}
	p, err := svc.RequestPairing(context.Background(), req)
	if err != nil {
		t.Fatalf("RequestPairing: %v", err)
	}

	if store.created == nil {
		t.Fatal("expected CreateTx to be called, but it was not")
	}
	if store.created.DeviceName != "iPhone 17" {
		t.Errorf("DeviceName = %q, want %q", store.created.DeviceName, "iPhone 17")
	}
	if store.created.Platform != "ios" {
		t.Errorf("Platform = %q, want %q", store.created.Platform, "ios")
	}
	wantNonceHash := pairingSHA256HexTest("test-nonce-value")
	if store.created.NonceHash != wantNonceHash {
		t.Errorf("NonceHash = %q, want %q", store.created.NonceHash, wantNonceHash)
	}

	if !notifier.called {
		t.Fatal("expected SendMagicLink to be called, but it was not")
	}
	if notifier.chatID != 12345 {
		t.Errorf("chatID = %d, want %d", notifier.chatID, 12345)
	}
	if notifier.deviceName != "iPhone 17" {
		t.Errorf("deviceName = %q, want %q", notifier.deviceName, "iPhone 17")
	}

	if p == nil {
		t.Fatal("expected non-nil PendingPairing")
	}
	if p.ID == "" {
		t.Error("ID should not be empty")
	}
}

func TestPairingService_RequestPairing_ReturnsPendingDTO(t *testing.T) {
	store := &mockPairingStore{}
	notifier := &mockPairingNotifier{}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, notifier, bc, 0)

	req := model.PairingRequest{
		DeviceName:  "Android Test",
		Platform:    "android",
		ClientNonce: "nonce123",
	}
	p, err := svc.RequestPairing(context.Background(), req)
	if err != nil {
		t.Fatalf("RequestPairing: %v", err)
	}

	wantExpiry := pairingTestTime.Add(5 * time.Minute)
	if !p.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", p.ExpiresAt, wantExpiry)
	}

	wantMagicURL := "https://huskwoot.example.com/pair/confirm/" + p.ID
	if notifier.magicURL != wantMagicURL {
		t.Errorf("magicURL = %q, want %q", notifier.magicURL, wantMagicURL)
	}
}

func TestPairingService_PollStatus_NonceMismatch_Returns403Equivalent(t *testing.T) {
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:        "pair-1",
			NonceHash: pairingSHA256HexTest("correct-nonce"),
			ExpiresAt: pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, &mockPairingNotifier{}, bc, 0)

	_, err := svc.PollStatus(context.Background(), "pair-1", "wrong-nonce")
	if !errors.Is(err, usecase.ErrNonceMismatch) {
		t.Errorf("expected ErrNonceMismatch, got %v", err)
	}
}

func TestPairingService_PollStatus_TimeoutReturnsPending(t *testing.T) {
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:        "pair-2",
			NonceHash: pairingSHA256HexTest("my-nonce"),
			ExpiresAt: pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, &mockPairingNotifier{}, bc, 50*time.Millisecond)

	res, err := svc.PollStatus(context.Background(), "pair-2", "my-nonce")
	if err != nil {
		t.Fatalf("PollStatus: %v", err)
	}
	if res.Status != model.PairingStatusPending {
		t.Errorf("Status = %q, want %q", res.Status, model.PairingStatusPending)
	}
}

func TestPairingService_PollStatus_ReceivesConfirmedFromBroadcaster(t *testing.T) {
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:        "pair-3",
			NonceHash: pairingSHA256HexTest("nonce-abc"),
			ExpiresAt: pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, &mockPairingNotifier{}, bc, 5*time.Second)

	want := model.PairingResult{
		PairID:      "pair-3",
		Status:      model.PairingStatusConfirmed,
		DeviceID:    "dev-uuid",
		BearerToken: "token-xyz",
	}

	resultCh := make(chan *model.PairingResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := svc.PollStatus(context.Background(), "pair-3", "nonce-abc")
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- r
	}()

	// Wait for subscription registration deterministically.
	select {
	case <-bc.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for broadcaster subscription")
	}
	bc.Notify("pair-3", want)

	select {
	case res := <-resultCh:
		if res.Status != model.PairingStatusConfirmed {
			t.Errorf("Status = %q, want %q", res.Status, model.PairingStatusConfirmed)
		}
		if res.DeviceID != "dev-uuid" {
			t.Errorf("DeviceID = %q, want %q", res.DeviceID, "dev-uuid")
		}
		if res.BearerToken != "token-xyz" {
			t.Errorf("BearerToken = %q, want %q", res.BearerToken, "token-xyz")
		}
	case err := <-errCh:
		t.Fatalf("PollStatus: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for broadcaster result")
	}
}

func TestPairingService_PollStatus_ExpiredPairing_ReturnsExpired(t *testing.T) {
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:        "pair-4",
			NonceHash: pairingSHA256HexTest("nonce"),
			ExpiresAt: pairingTestTime.Add(-1 * time.Hour),
		},
	}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, &mockPairingNotifier{}, bc, 0)

	res, err := svc.PollStatus(context.Background(), "pair-4", "nonce")
	if err != nil {
		t.Fatalf("PollStatus: %v", err)
	}
	if res.Status != model.PairingStatusExpired {
		t.Errorf("Status = %q, want %q", res.Status, model.PairingStatusExpired)
	}
}

// TestPairingService_PollStatus_ConfirmedBetweenSubscribeAndSelect — TOCTOU fix:
// ConfirmWithCSRF calls Notify after Subscribe but before the second Get.
// The second Get returns ConfirmedAt != nil; the token is already in the ch buffer.
// PollStatus must return the token via a non-blocking receive, not ErrAlreadyConfirmed.
func TestPairingService_PollStatus_ConfirmedBetweenSubscribeAndSelect(t *testing.T) {
	now := pairingTestTime
	basePairing := &model.PendingPairing{
		ID:        "pair-toctou",
		NonceHash: pairingSHA256HexTest("nonce"),
		ExpiresAt: now.Add(5 * time.Minute),
	}
	confirmedAt := now.Add(1 * time.Second)
	confirmedPairing := &model.PendingPairing{
		ID:          "pair-toctou",
		NonceHash:   pairingSHA256HexTest("nonce"),
		ExpiresAt:   now.Add(5 * time.Minute),
		ConfirmedAt: &confirmedAt,
	}
	bc := newMockPairingBroadcaster()

	want := model.PairingResult{
		PairID:      "pair-toctou",
		Status:      model.PairingStatusConfirmed,
		DeviceID:    "dev-toctou",
		BearerToken: "token-toctou",
	}

	// First Get → not confirmed; on the second Get (after Subscribe)
	// we simulate Notify (ConfirmWithCSRF finished between Subscribe and the second Get).
	store := &mockPairingStore{
		getSeq: []*model.PendingPairing{basePairing, confirmedPairing},
		onGetCall: func(callIndex int) {
			// callIndex 1 = second Get; Subscribe has already happened, ch is registered.
			// Notify puts the token into the ch buffer before the second Get returns.
			if callIndex == 1 {
				bc.Notify("pair-toctou", want)
			}
		},
	}
	svc := newTestPairingSvc(t, store, &mockPairingNotifier{}, bc, 5*time.Second)

	res, err := svc.PollStatus(context.Background(), "pair-toctou", "nonce")
	if err != nil {
		t.Fatalf("PollStatus returned error: %v", err)
	}
	if res.BearerToken != want.BearerToken {
		t.Errorf("BearerToken = %q, want %q", res.BearerToken, want.BearerToken)
	}
	if res.Status != model.PairingStatusConfirmed {
		t.Errorf("Status = %q, want %q", res.Status, model.PairingStatusConfirmed)
	}
}

// TestPairingService_PollStatus_AlreadyConfirmedNoBroadcastToken verifies that
// when ConfirmedAt is already set on the second read and the broadcaster is empty,
// ErrAlreadyConfirmed is returned (the token was irrecoverably lost before Subscribe).
func TestPairingService_PollStatus_AlreadyConfirmedNoBroadcastToken(t *testing.T) {
	now := pairingTestTime
	confirmedAt := now.Add(-30 * time.Second)
	pairing := &model.PendingPairing{
		ID:          "pair-already",
		NonceHash:   pairingSHA256HexTest("nonce"),
		ExpiresAt:   now.Add(5 * time.Minute),
		ConfirmedAt: &confirmedAt,
	}
	store := &mockPairingStore{getResult: pairing}
	bc := newMockPairingBroadcaster()
	svc := newTestPairingSvc(t, store, &mockPairingNotifier{}, bc, 5*time.Second)

	_, err := svc.PollStatus(context.Background(), "pair-already", "nonce")
	if !errors.Is(err, usecase.ErrAlreadyConfirmed) {
		t.Errorf("expected ErrAlreadyConfirmed, got %v", err)
	}
}

// mockDeviceStoreForPairing is a minimal mock of model.DeviceStore for ConfirmWithCSRF tests.
type mockDeviceStoreForPairing struct {
	created   *model.Device
	createErr error
}

func (m *mockDeviceStoreForPairing) Create(_ context.Context, _ *sql.Tx, d *model.Device) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.created = d
	return nil
}

func (m *mockDeviceStoreForPairing) FindByTokenHash(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}

func (m *mockDeviceStoreForPairing) UpdateLastSeen(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockDeviceStoreForPairing) Revoke(_ context.Context, _ string) error {
	return nil
}

func (m *mockDeviceStoreForPairing) List(_ context.Context) ([]model.Device, error) {
	return nil, nil
}

func (m *mockDeviceStoreForPairing) ListActiveIDs(_ context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockDeviceStoreForPairing) UpdatePushTokens(_ context.Context, _ string, _, _ *string) error {
	return nil
}

func (m *mockDeviceStoreForPairing) Get(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}

func (m *mockDeviceStoreForPairing) ListInactive(_ context.Context, _ time.Time) ([]model.Device, error) {
	return nil, nil
}

func (m *mockDeviceStoreForPairing) DeleteRevokedOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func newTestPairingSvcFull(
	t *testing.T,
	store *mockPairingStore,
	notifier *mockPairingNotifier,
	bc *mockPairingBroadcaster,
	deviceStore model.DeviceStore,
) model.PairingService {
	t.Helper()
	db := openTestDB(t)
	return usecase.NewPairingService(usecase.PairingDeps{
		DB:              db,
		PairingStore:    store,
		DeviceStore:     deviceStore,
		Sender:          notifier,
		Broadcaster:     bc,
		OwnerChatID:     12345,
		LinkTTL:         5 * time.Minute,
		LongPoll:        60 * time.Second,
		ExternalBaseURL: "https://huskwoot.example.com",
		Now:             func() time.Time { return pairingTestTime },
		Rand:            bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)),
	})
}

func TestPairingService_PrepareConfirm_StoresCSRFHash(t *testing.T) {
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:        "pair-csrf",
			NonceHash: pairingSHA256HexTest("nonce"),
			ExpiresAt: pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	ds := &mockDeviceStoreForPairing{}
	svc := newTestPairingSvcFull(t, store, &mockPairingNotifier{}, bc, ds)

	csrfToken := "csrf-token-value"
	p, err := svc.PrepareConfirm(context.Background(), "pair-csrf", csrfToken)
	if err != nil {
		t.Fatalf("PrepareConfirm: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil PendingPairing")
	}
	wantCSRFHash := pairingSHA256HexTest(csrfToken)
	if store.csrfID != "pair-csrf" {
		t.Errorf("SetCSRFTx: id = %q, want %q", store.csrfID, "pair-csrf")
	}
	if store.csrfHash != wantCSRFHash {
		t.Errorf("SetCSRFTx: csrfHash = %q, want %q", store.csrfHash, wantCSRFHash)
	}
}

func TestPairingService_PrepareConfirm_ExpiredPairing_ReturnsErrExpired(t *testing.T) {
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:        "pair-expired",
			NonceHash: pairingSHA256HexTest("nonce"),
			ExpiresAt: pairingTestTime.Add(-1 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	ds := &mockDeviceStoreForPairing{}
	svc := newTestPairingSvcFull(t, store, &mockPairingNotifier{}, bc, ds)

	_, err := svc.PrepareConfirm(context.Background(), "pair-expired", "csrf")
	if !errors.Is(err, usecase.ErrPairingExpired) {
		t.Errorf("expected ErrPairingExpired, got %v", err)
	}
}

func TestPairingService_ConfirmWithCSRF_ValidatesAndCreatesDevice(t *testing.T) {
	csrfToken := "good-csrf-token"
	csrfHash := pairingSHA256HexTest(csrfToken)

	tests := []struct {
		name        string
		pairing     *model.PendingPairing
		csrfToken   string
		wantErr     error
		wantCreated bool
	}{
		{
			name: "success",
			pairing: &model.PendingPairing{
				ID:         "pair-ok",
				DeviceName: "iPhone 17",
				Platform:   "ios",
				CSRFHash:   csrfHash,
				ExpiresAt:  pairingTestTime.Add(5 * time.Minute),
			},
			csrfToken:   csrfToken,
			wantCreated: true,
		},
		{
			name: "csrf mismatch",
			pairing: &model.PendingPairing{
				ID:        "pair-mismatch",
				CSRFHash:  csrfHash,
				ExpiresAt: pairingTestTime.Add(5 * time.Minute),
			},
			csrfToken: "wrong-csrf",
			wantErr:   usecase.ErrCSRFMismatch,
		},
		{
			name: "expired",
			pairing: &model.PendingPairing{
				ID:        "pair-exp",
				CSRFHash:  csrfHash,
				ExpiresAt: pairingTestTime.Add(-1 * time.Minute),
			},
			csrfToken: csrfToken,
			wantErr:   usecase.ErrPairingExpired,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &mockPairingStore{getResult: tc.pairing}
			bc := newMockPairingBroadcaster()
			ds := &mockDeviceStoreForPairing{}
			svc := newTestPairingSvcFull(t, store, &mockPairingNotifier{}, bc, ds)

			dev, err := svc.ConfirmWithCSRF(context.Background(), tc.pairing.ID, tc.csrfToken)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfirmWithCSRF: %v", err)
			}
			if dev == nil {
				t.Fatal("expected non-nil Device")
			}
			if tc.wantCreated && ds.created == nil {
				t.Error("expected DeviceStore.Create to be called")
			}
			if ds.created != nil {
				if ds.created.Name != tc.pairing.DeviceName {
					t.Errorf("Device.Name = %q, want %q", ds.created.Name, tc.pairing.DeviceName)
				}
				if ds.created.TokenHash == "" {
					t.Error("Device.TokenHash must not be empty")
				}
			}
			// broadcaster must have called Notify with the correct result.
			bc.mu.Lock()
			notified := bc.lastNotify
			bc.mu.Unlock()
			if notified == nil {
				t.Fatal("broadcaster.Notify was not called")
			}
			if notified.Status != model.PairingStatusConfirmed {
				t.Errorf("Notify Status = %q, want %q", notified.Status, model.PairingStatusConfirmed)
			}
			if notified.DeviceID == "" {
				t.Error("Notify DeviceID is empty")
			}
			if notified.BearerToken == "" {
				t.Error("Notify BearerToken is empty")
			}
			if ds.created != nil && notified.BearerToken == ds.created.TokenHash {
				t.Error("Notify BearerToken must not be the token hash")
			}
		})
	}
}

// mockRelayClientForPairing is a mock of push.RelayClient for PairingService tests.
type mockRelayClientForPairing struct {
	upsertCalled   bool
	upsertDeviceID string
	upsertReq      pushproto.RegistrationRequest
	upsertErr      error
}

func (m *mockRelayClientForPairing) Push(_ context.Context, _ pushproto.PushRequest) (pushproto.PushResponse, error) {
	return pushproto.PushResponse{}, nil
}

func (m *mockRelayClientForPairing) UpsertRegistration(_ context.Context, deviceID string, r pushproto.RegistrationRequest) error {
	m.upsertCalled = true
	m.upsertDeviceID = deviceID
	m.upsertReq = r
	return m.upsertErr
}

func (m *mockRelayClientForPairing) DeleteRegistration(_ context.Context, _ string) error {
	return nil
}

func newTestPairingSvcWithRelay(
	t *testing.T,
	store *mockPairingStore,
	notifier *mockPairingNotifier,
	bc *mockPairingBroadcaster,
	deviceStore model.DeviceStore,
	relay push.RelayClient,
) model.PairingService {
	t.Helper()
	db := openTestDB(t)
	return usecase.NewPairingService(usecase.PairingDeps{
		DB:              db,
		PairingStore:    store,
		DeviceStore:     deviceStore,
		Sender:          notifier,
		Broadcaster:     bc,
		OwnerChatID:     12345,
		LinkTTL:         5 * time.Minute,
		LongPoll:        60 * time.Second,
		ExternalBaseURL: "https://huskwoot.example.com",
		Now:             func() time.Time { return pairingTestTime },
		Rand:            bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)),
		Relay:           relay,
	})
}

func TestPairingService_ConfirmWithCSRF_CallsRelayUpsert(t *testing.T) {
	csrfToken := "csrf-relay-test"
	csrfHash := pairingSHA256HexTest(csrfToken)
	apns := "apns-token-test"

	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:         "pair-relay",
			DeviceName: "iPhone Test",
			Platform:   "ios",
			APNSToken:  &apns,
			CSRFHash:   csrfHash,
			ExpiresAt:  pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	ds := &mockDeviceStoreForPairing{}
	relay := &mockRelayClientForPairing{}
	svc := newTestPairingSvcWithRelay(t, store, &mockPairingNotifier{}, bc, ds, relay)

	dev, err := svc.ConfirmWithCSRF(context.Background(), "pair-relay", csrfToken)
	if err != nil {
		t.Fatalf("ConfirmWithCSRF: %v", err)
	}
	if dev == nil {
		t.Fatal("expected non-nil Device")
	}
	if !relay.upsertCalled {
		t.Fatal("expected relay.UpsertRegistration to be called, but it was not")
	}
	if relay.upsertReq.APNSToken == nil || *relay.upsertReq.APNSToken != apns {
		t.Errorf("upsertReq.APNSToken = %v, want %q", relay.upsertReq.APNSToken, apns)
	}
	if relay.upsertReq.Platform != "ios" {
		t.Errorf("upsertReq.Platform = %q, want ios", relay.upsertReq.Platform)
	}
}

func TestPairingService_ConfirmWithCSRF_RelayErrorDoesNotFailConfirm(t *testing.T) {
	csrfToken := "csrf-relay-err"
	csrfHash := pairingSHA256HexTest(csrfToken)
	apns := "apns-token"

	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:         "pair-relay-err",
			DeviceName: "Test Device",
			Platform:   "ios",
			APNSToken:  &apns,
			CSRFHash:   csrfHash,
			ExpiresAt:  pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	ds := &mockDeviceStoreForPairing{}
	relay := &mockRelayClientForPairing{upsertErr: errors.New("relay недоступен")}
	svc := newTestPairingSvcWithRelay(t, store, &mockPairingNotifier{}, bc, ds, relay)

	dev, err := svc.ConfirmWithCSRF(context.Background(), "pair-relay-err", csrfToken)
	if err != nil {
		t.Fatalf("ConfirmWithCSRF should succeed even on relay error: %v", err)
	}
	if dev == nil {
		t.Fatal("expected non-nil Device")
	}
}

func TestPairingService_ConfirmWithCSRF_NoTokens_SkipsRelay(t *testing.T) {
	csrfToken := "csrf-no-tokens"
	csrfHash := pairingSHA256HexTest(csrfToken)

	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:         "pair-no-tokens",
			DeviceName: "Device Without Tokens",
			Platform:   "android",
			APNSToken:  nil,
			FCMToken:   nil,
			CSRFHash:   csrfHash,
			ExpiresAt:  pairingTestTime.Add(5 * time.Minute),
		},
	}
	bc := newMockPairingBroadcaster()
	ds := &mockDeviceStoreForPairing{}
	relay := &mockRelayClientForPairing{}
	svc := newTestPairingSvcWithRelay(t, store, &mockPairingNotifier{}, bc, ds, relay)

	dev, err := svc.ConfirmWithCSRF(context.Background(), "pair-no-tokens", csrfToken)
	if err != nil {
		t.Fatalf("ConfirmWithCSRF: %v", err)
	}
	if dev == nil {
		t.Fatal("expected non-nil Device")
	}
	if relay.upsertCalled {
		t.Error("relay.UpsertRegistration must not be called without tokens")
	}
}

func TestPairingService_ConfirmWithCSRF_DoubleConfirm_ReturnsErrAlreadyConfirmed(t *testing.T) {
	confirmedAt := pairingTestTime.Add(-1 * time.Minute)
	csrfToken := "csrf"
	csrfHash := pairingSHA256HexTest(csrfToken)
	store := &mockPairingStore{
		getResult: &model.PendingPairing{
			ID:          "pair-double",
			CSRFHash:    csrfHash,
			ExpiresAt:   pairingTestTime.Add(5 * time.Minute),
			ConfirmedAt: &confirmedAt,
		},
	}
	bc := newMockPairingBroadcaster()
	ds := &mockDeviceStoreForPairing{}
	svc := newTestPairingSvcFull(t, store, &mockPairingNotifier{}, bc, ds)

	_, err := svc.ConfirmWithCSRF(context.Background(), "pair-double", csrfToken)
	if !errors.Is(err, usecase.ErrAlreadyConfirmed) {
		t.Errorf("expected ErrAlreadyConfirmed, got %v", err)
	}
}
