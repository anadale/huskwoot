package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/pushproto"
	"github.com/google/uuid"
)

// Sentinel errors for the pairing flow.
var (
	ErrPairingNotFound  = errors.New("pairing request not found")
	ErrPairingExpired   = errors.New("pairing request expired")
	ErrNonceMismatch    = errors.New("nonce mismatch")
	ErrSenderFailed     = errors.New("sending magic-link failed")
	ErrAlreadyConfirmed = errors.New("pairing request already confirmed")
	ErrCSRFMismatch     = errors.New("csrf token mismatch")
)

// PairingNotifier sends a magic-link DM to the instance owner.
type PairingNotifier interface {
	SendMagicLink(ctx context.Context, chatID int64, deviceName, magicURL string) error
}

// PairingBroadcaster delivers the pairing confirmation result to long-poll subscribers.
type PairingBroadcaster interface {
	Subscribe(pairID string) (<-chan model.PairingResult, func())
	Notify(pairID string, result model.PairingResult)
}

// PairingDeps collects the dependencies for PairingService.
type PairingDeps struct {
	DB              *sql.DB
	PairingStore    model.PairingStore
	DeviceStore     model.DeviceStore
	Sender          PairingNotifier
	Broadcaster     PairingBroadcaster
	OwnerChatID     int64
	LinkTTL         time.Duration
	LongPoll        time.Duration
	ExternalBaseURL string
	Now             func() time.Time
	// Rand is used for generating bearer tokens.
	// Defaults to crypto/rand.Reader when nil.
	Rand   io.Reader
	Logger *slog.Logger
	// Relay is the push relay client. Defaults to NilRelayClient (push disabled) when nil.
	Relay push.RelayClient
}

type pairingService struct {
	db              *sql.DB
	pairingStore    model.PairingStore
	deviceStore     model.DeviceStore
	sender          PairingNotifier
	broadcaster     PairingBroadcaster
	ownerChatID     int64
	linkTTL         time.Duration
	longPoll        time.Duration
	externalBaseURL string
	now             func() time.Time
	rand            io.Reader
	relay           push.RelayClient
	logger          *slog.Logger
}

// NewPairingService creates an implementation of model.PairingService.
func NewPairingService(deps PairingDeps) model.PairingService {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	linkTTL := deps.LinkTTL
	if linkTTL <= 0 {
		linkTTL = 5 * time.Minute
	}
	longPoll := deps.LongPoll
	if longPoll <= 0 {
		longPoll = 60 * time.Second
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	relay := deps.Relay
	if relay == nil {
		relay = push.NilRelayClient{}
	}
	return &pairingService{
		db:              deps.DB,
		pairingStore:    deps.PairingStore,
		deviceStore:     deps.DeviceStore,
		sender:          deps.Sender,
		broadcaster:     deps.Broadcaster,
		ownerChatID:     deps.OwnerChatID,
		linkTTL:         linkTTL,
		longPoll:        longPoll,
		externalBaseURL: deps.ExternalBaseURL,
		now:             now,
		rand:            deps.Rand,
		relay:           relay,
		logger:          logger,
	}
}

// RequestPairing creates a pairing request, saves it to the database, and sends a magic-link to the owner.
func (s *pairingService) RequestPairing(ctx context.Context, req model.PairingRequest) (*model.PendingPairing, error) {
	now := s.now()
	pairID := uuid.NewString()

	p := &model.PendingPairing{
		ID:         pairID,
		DeviceName: req.DeviceName,
		Platform:   req.Platform,
		APNSToken:  req.APNSToken,
		FCMToken:   req.FCMToken,
		NonceHash:  pairingSHA256Hex(req.ClientNonce),
		ExpiresAt:  now.Add(s.linkTTL),
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning pairing transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := s.pairingStore.CreateTx(ctx, tx, p); err != nil {
		return nil, fmt.Errorf("saving pairing request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing pairing transaction: %w", err)
	}

	magicURL := s.externalBaseURL + "/pair/confirm/" + pairID
	if err := s.sender.SendMagicLink(ctx, s.ownerChatID, req.DeviceName, magicURL); err != nil {
		s.logger.Warn("pairing: failed to send magic-link",
			"pair_id", pairID,
			"device_name", req.DeviceName,
			"error", err,
		)
		return nil, fmt.Errorf("%w: %v", ErrSenderFailed, err)
	}

	return p, nil
}

// PollStatus waits for the result of a pairing request (long-poll up to longPoll TTL).
func (s *pairingService) PollStatus(ctx context.Context, pairID, clientNonce string) (*model.PairingResult, error) {
	p, err := s.pairingStore.Get(ctx, pairID)
	if err != nil {
		return nil, fmt.Errorf("getting pairing request %q: %w", pairID, err)
	}
	if p == nil {
		return nil, ErrPairingNotFound
	}

	if p.ExpiresAt.Before(s.now()) {
		return &model.PairingResult{PairID: pairID, Status: model.PairingStatusExpired}, nil
	}

	gotHash := pairingSHA256Hex(clientNonce)
	if subtle.ConstantTimeCompare([]byte(gotHash), []byte(p.NonceHash)) != 1 {
		return nil, ErrNonceMismatch
	}

	// Subscribe BEFORE checking ConfirmedAt to avoid missing a Notify
	// that could arrive between the DB read and the subscribe call.
	ch, cleanup := s.broadcaster.Subscribe(pairID)
	defer cleanup()

	// Re-read state after subscribing: if ConfirmWithCSRF completed between
	// the first Get and Subscribe, the token is already in ch's buffer or ConfirmedAt is set.
	p2, err := s.pairingStore.Get(ctx, pairID)
	if err != nil {
		return nil, fmt.Errorf("re-fetching pairing request %q: %w", pairID, err)
	}
	if p2 == nil {
		return nil, ErrPairingNotFound
	}
	if p2.ConfirmedAt != nil {
		select {
		case result := <-ch:
			return &result, nil
		default:
			return nil, ErrAlreadyConfirmed
		}
	}

	timer := time.NewTimer(s.longPoll)
	defer timer.Stop()

	select {
	case result := <-ch:
		return &result, nil
	case <-timer.C:
		return &model.PairingResult{PairID: pairID, Status: model.PairingStatusPending}, nil
	case <-ctx.Done():
		return &model.PairingResult{PairID: pairID, Status: model.PairingStatusPending}, nil
	}
}

// PrepareConfirm stores SHA256(csrfToken) in the pairing record.
func (s *pairingService) PrepareConfirm(ctx context.Context, pairID, csrfToken string) (*model.PendingPairing, error) {
	p, err := s.pairingStore.Get(ctx, pairID)
	if err != nil {
		return nil, fmt.Errorf("getting pairing request: %w", err)
	}
	if p == nil {
		return nil, ErrPairingNotFound
	}
	if p.ExpiresAt.Before(s.now()) {
		return nil, ErrPairingExpired
	}

	csrfHash := pairingSHA256Hex(csrfToken)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning PrepareConfirm transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := s.pairingStore.SetCSRFTx(ctx, tx, pairID, csrfHash); err != nil {
		return nil, fmt.Errorf("saving CSRF hash: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing PrepareConfirm transaction: %w", err)
	}

	p.CSRFHash = csrfHash
	return p, nil
}

// ConfirmWithCSRF validates the CSRF token, creates the device, and publishes the result to the broadcaster.
func (s *pairingService) ConfirmWithCSRF(ctx context.Context, pairID, csrfToken string) (*model.Device, error) {
	p, err := s.pairingStore.Get(ctx, pairID)
	if err != nil {
		return nil, fmt.Errorf("getting pairing request: %w", err)
	}
	if p == nil {
		return nil, ErrPairingNotFound
	}
	if p.ExpiresAt.Before(s.now()) {
		return nil, ErrPairingExpired
	}
	if p.ConfirmedAt != nil {
		return nil, ErrAlreadyConfirmed
	}

	csrfHash := pairingSHA256Hex(csrfToken)
	if subtle.ConstantTimeCompare([]byte(csrfHash), []byte(p.CSRFHash)) != 1 {
		return nil, ErrCSRFMismatch
	}

	r := s.rand
	if r == nil {
		r = rand.Reader
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(r, tokenBytes); err != nil {
		return nil, fmt.Errorf("generating bearer token: %w", err)
	}
	bearerToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := pairingSHA256Hex(bearerToken)

	device := &model.Device{
		ID:        uuid.NewString(),
		Name:      p.DeviceName,
		Platform:  p.Platform,
		TokenHash: tokenHash,
		APNSToken: p.APNSToken,
		FCMToken:  p.FCMToken,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("начало транзакции ConfirmWithCSRF: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := s.deviceStore.Create(ctx, tx, device); err != nil {
		return nil, fmt.Errorf("создание устройства: %w", err)
	}
	if err := s.pairingStore.MarkConfirmedTx(ctx, tx, pairID, device.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAlreadyConfirmed
		}
		return nil, fmt.Errorf("пометка pairing подтверждённым: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("коммит транзакции ConfirmWithCSRF: %w", err)
	}

	if device.APNSToken != nil || device.FCMToken != nil {
		reg := pushproto.RegistrationRequest{
			APNSToken: device.APNSToken,
			FCMToken:  device.FCMToken,
			Platform:  device.Platform,
		}
		if err := s.relay.UpsertRegistration(ctx, device.ID, reg); err != nil {
			s.logger.Warn("pairing: relay upsert failed",
				"device_id", device.ID,
				"error", err,
			)
		}
	}

	s.broadcaster.Notify(pairID, model.PairingResult{
		PairID:      pairID,
		Status:      model.PairingStatusConfirmed,
		DeviceID:    device.ID,
		BearerToken: bearerToken,
	})

	return device, nil
}

func pairingSHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
