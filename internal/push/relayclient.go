package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

// ErrRelayUnavailable is returned when the relay is unreachable (network error,
// 5xx response, or context cancellation).
var ErrRelayUnavailable = errors.New("relay unavailable")

// RelayClient is the interface for instance-to-push-relay communication.
type RelayClient interface {
	Push(ctx context.Context, req pushproto.PushRequest) (pushproto.PushResponse, error)
	UpsertRegistration(ctx context.Context, deviceID string, r pushproto.RegistrationRequest) error
	DeleteRegistration(ctx context.Context, deviceID string) error
}

// HTTPRelayClientConfig holds parameters for the relay HTTP client.
type HTTPRelayClientConfig struct {
	BaseURL    string
	InstanceID string
	Secret     []byte
	Timeout    time.Duration
	Clock      func() time.Time
	Logger     *slog.Logger
}

type httpRelayClient struct {
	cfg        HTTPRelayClientConfig
	httpClient *http.Client
}

// NewHTTPRelayClient creates an HTTP implementation of RelayClient.
func NewHTTPRelayClient(cfg HTTPRelayClientConfig) RelayClient {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &httpRelayClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *httpRelayClient) Push(ctx context.Context, req pushproto.PushRequest) (pushproto.PushResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return pushproto.PushResponse{}, fmt.Errorf("serializing push request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/push", body)
	if err != nil {
		return pushproto.PushResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return pushproto.PushResponse{}, fmt.Errorf("%w: status %d", ErrRelayUnavailable, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return pushproto.PushResponse{}, fmt.Errorf("%w: status %d (authorization error)", ErrRelayUnavailable, resp.StatusCode)
	}

	var pr pushproto.PushResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return pushproto.PushResponse{}, fmt.Errorf("parsing relay push response: %w", err)
	}
	return pr, nil
}

func (c *httpRelayClient) UpsertRegistration(ctx context.Context, deviceID string, r pushproto.RegistrationRequest) error {
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("serializing upsert registration: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPut, "/v1/registrations/"+deviceID, body)
	if err != nil {
		return err
	}
	defer readAndClose(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: upsert registration status %d", ErrRelayUnavailable, resp.StatusCode)
	}
	return nil
}

func (c *httpRelayClient) DeleteRegistration(ctx context.Context, deviceID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, "/v1/registrations/"+deviceID, nil)
	if err != nil {
		return err
	}
	defer readAndClose(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: delete registration status %d", ErrRelayUnavailable, resp.StatusCode)
	}
	return nil
}

func (c *httpRelayClient) doRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if body == nil {
		body = []byte{}
	}

	ts := strconv.FormatInt(c.cfg.Clock().Unix(), 10)
	sig := pushproto.Sign(c.cfg.Secret, method, path, ts, body)

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating relay request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Huskwoot-Instance", c.cfg.InstanceID)
	req.Header.Set("X-Huskwoot-Timestamp", ts)
	req.Header.Set("X-Huskwoot-Signature", sig)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrRelayUnavailable, ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrRelayUnavailable, err)
	}
	return resp, nil
}

// NilRelayClient is a no-op implementation used when the [push] section is not configured.
type NilRelayClient struct{}

func (NilRelayClient) Push(_ context.Context, _ pushproto.PushRequest) (pushproto.PushResponse, error) {
	return pushproto.PushResponse{}, nil
}

func (NilRelayClient) UpsertRegistration(_ context.Context, _ string, _ pushproto.RegistrationRequest) error {
	return nil
}

func (NilRelayClient) DeleteRegistration(_ context.Context, _ string) error {
	return nil
}

// readAndClose drains and closes the body, ignoring read errors.
func readAndClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, rc)
	rc.Close()
}
