package push

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pushproto"
)

// --- mocks ---

type mockPushQueue struct {
	mu             sync.Mutex
	nextBatch      []model.PushJob
	nextBatchErr   error
	delivered      []int64
	failed         []markFailedCall
	dropped        []dropCall
	batchCallCount int
}

type markFailedCall struct {
	id          int64
	errText     string
	nextAttempt time.Time
}

type dropCall struct {
	id     int64
	reason string
}

func (m *mockPushQueue) Enqueue(_ context.Context, _ *sql.Tx, _ string, _ int64) error {
	return nil
}

func (m *mockPushQueue) NextBatch(_ context.Context, _ int) ([]model.PushJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchCallCount++
	if m.nextBatchErr != nil {
		return nil, m.nextBatchErr
	}
	return m.nextBatch, nil
}

func (m *mockPushQueue) MarkDelivered(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered = append(m.delivered, id)
	return nil
}

func (m *mockPushQueue) MarkFailed(_ context.Context, id int64, errText string, next time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = append(m.failed, markFailedCall{id, errText, next})
	return nil
}

func (m *mockPushQueue) Drop(_ context.Context, id int64, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped = append(m.dropped, dropCall{id, reason})
	return nil
}

func (m *mockPushQueue) DeleteDelivered(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

type mockEventStore struct {
	events map[int64]*model.Event
}

func (m *mockEventStore) Insert(_ context.Context, _ *sql.Tx, _ model.Event) (int64, error) {
	return 0, nil
}
func (m *mockEventStore) SinceSeq(_ context.Context, _ int64, _ int) ([]model.Event, error) {
	return nil, nil
}
func (m *mockEventStore) MaxSeq(_ context.Context) (int64, error) { return 0, nil }
func (m *mockEventStore) MinSeq(_ context.Context) (int64, error) { return 0, nil }
func (m *mockEventStore) DeleteOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (m *mockEventStore) GetBySeq(_ context.Context, seq int64) (*model.Event, error) {
	ev, ok := m.events[seq]
	if !ok {
		return nil, nil
	}
	return ev, nil
}

type mockDeviceStore struct {
	devices           map[string]*model.Device
	updatedPushTokens []string
}

func (m *mockDeviceStore) Create(_ context.Context, _ *sql.Tx, _ *model.Device) error { return nil }
func (m *mockDeviceStore) FindByTokenHash(_ context.Context, _ string) (*model.Device, error) {
	return nil, nil
}
func (m *mockDeviceStore) UpdateLastSeen(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (m *mockDeviceStore) Revoke(_ context.Context, _ string) error          { return nil }
func (m *mockDeviceStore) List(_ context.Context) ([]model.Device, error)    { return nil, nil }
func (m *mockDeviceStore) ListActiveIDs(_ context.Context) ([]string, error) { return nil, nil }
func (m *mockDeviceStore) UpdatePushTokens(_ context.Context, id string, _ *string, _ *string) error {
	m.updatedPushTokens = append(m.updatedPushTokens, id)
	return nil
}
func (m *mockDeviceStore) Get(_ context.Context, id string) (*model.Device, error) {
	d, ok := m.devices[id]
	if !ok {
		return nil, nil
	}
	return d, nil
}
func (m *mockDeviceStore) ListInactive(_ context.Context, _ time.Time) ([]model.Device, error) {
	return nil, nil
}
func (m *mockDeviceStore) DeleteRevokedOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

type mockRelayClient struct {
	pushResponse pushproto.PushResponse
	pushErr      error
	pushCalls    []pushproto.PushRequest
}

func (m *mockRelayClient) Push(_ context.Context, req pushproto.PushRequest) (pushproto.PushResponse, error) {
	m.pushCalls = append(m.pushCalls, req)
	return m.pushResponse, m.pushErr
}
func (m *mockRelayClient) UpsertRegistration(_ context.Context, _ string, _ pushproto.RegistrationRequest) error {
	return nil
}
func (m *mockRelayClient) DeleteRegistration(_ context.Context, _ string) error { return nil }

type mockTemplates struct {
	req *pushproto.PushRequest
	ok  bool
	err error
}

func (m *mockTemplates) Resolve(_ context.Context, _ *model.Event) (*pushproto.PushRequest, bool, error) {
	return m.req, m.ok, m.err
}

// --- helpers ---

func fixedClock() func() time.Time {
	t := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func apnsPtr(s string) *string { return &s }

func makeTaskCreatedEvent(seq int64) *model.Event {
	payload, _ := json.Marshal(map[string]any{
		"task": map[string]any{
			"id":           "task-1",
			"number":       1,
			"project_id":   "proj-1",
			"project_slug": "inbox",
			"summary":      "Тестовая задача",
		},
	})
	return &model.Event{
		Seq:       seq,
		Kind:      model.EventTaskCreated,
		EntityID:  "task-1",
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

func makeJob(id int64, deviceID string, seq int64, attempts int) model.PushJob {
	return model.PushJob{
		ID:            id,
		DeviceID:      deviceID,
		EventSeq:      seq,
		Attempts:      attempts,
		CreatedAt:     time.Now(),
		NextAttemptAt: time.Now().Add(-time.Second),
	}
}

func makeActiveDevice(id string) *model.Device {
	apns := "apns-token-abc"
	return &model.Device{
		ID:        id,
		Name:      "iPhone",
		Platform:  "ios",
		APNSToken: &apns,
	}
}

func makeDispatcher(
	queue model.PushQueue,
	events model.EventStore,
	devices model.DeviceStore,
	relay RelayClient,
	tmpl TemplateResolver,
) *Dispatcher {
	return NewDispatcher(DispatcherDeps{
		Queue:       queue,
		Events:      events,
		Devices:     devices,
		Relay:       relay,
		Templates:   tmpl,
		Clock:       fixedClock(),
		Logger:      silentLogger(),
		Interval:    10 * time.Millisecond,
		BatchSize:   32,
		MaxAttempts: 4,
	})
}

// --- processOne tests ---

func TestDispatcher_ProcessOne_SendsAndMarksDelivered(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0)
	dev := makeActiveDevice("dev-1")

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{pushResponse: pushproto.PushResponse{Status: pushproto.StatusSent}}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.delivered) != 1 || queue.delivered[0] != 10 {
		t.Errorf("want MarkDelivered(10), got: %v", queue.delivered)
	}
	if len(relay.pushCalls) != 1 {
		t.Errorf("want 1 Push call, got %d", len(relay.pushCalls))
	}
	if relay.pushCalls[0].DeviceID != "dev-1" {
		t.Errorf("wrong DeviceID in Push: %q", relay.pushCalls[0].DeviceID)
	}
}

func TestDispatcher_ProcessOne_MissingEvent_Drops(t *testing.T) {
	job := makeJob(10, "dev-1", 99, 0)

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{}} // seq 99 absent
	devices := &mockDeviceStore{devices: map[string]*model.Device{}}
	relay := &mockRelayClient{}
	tmpl := &mockTemplates{}

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "event_missing" {
		t.Errorf("want Drop(event_missing), got: %v", queue.dropped)
	}
	if len(relay.pushCalls) != 0 {
		t.Error("relay.Push must not be called when event is absent")
	}
}

func TestDispatcher_ProcessOne_NotPushable_Drops(t *testing.T) {
	ev := &model.Event{
		Seq:      1,
		Kind:     model.EventTaskCompleted, // not push-worthy
		EntityID: "task-1",
		Payload:  json.RawMessage(`{"task":{"id":"task-1","number":1,"project_id":"p","project_slug":"inbox","summary":"x"}}`),
	}
	job := makeJob(10, "dev-1", 1, 0)

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{}}
	relay := &mockRelayClient{}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "not_pushable" {
		t.Errorf("want Drop(not_pushable), got: %v", queue.dropped)
	}
}

func TestDispatcher_ProcessOne_MissingDevice_Drops(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-missing", 1, 0)

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{}} // device absent
	relay := &mockRelayClient{}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "device_revoked" {
		t.Errorf("want Drop(device_revoked), got: %v", queue.dropped)
	}
}

func TestDispatcher_ProcessOne_RevokedDevice_Drops(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0)
	revokedAt := time.Now()
	dev := &model.Device{ID: "dev-1", RevokedAt: &revokedAt}

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "device_revoked" {
		t.Errorf("want Drop(device_revoked), got: %v", queue.dropped)
	}
}

func TestDispatcher_ProcessOne_NoTokens_Drops(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0)
	dev := &model.Device{ID: "dev-1", Platform: "ios"} // APNSToken and FCMToken both nil

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "no_tokens" {
		t.Errorf("want Drop(no_tokens), got: %v", queue.dropped)
	}
}

func TestDispatcher_ProcessOne_InvalidToken_ClearsTokensAndDrops(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0)
	dev := makeActiveDevice("dev-1")

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{
		pushResponse: pushproto.PushResponse{Status: pushproto.StatusInvalidToken},
	}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(devices.updatedPushTokens) != 1 || devices.updatedPushTokens[0] != "dev-1" {
		t.Errorf("want UpdatePushTokens(dev-1), got: %v", devices.updatedPushTokens)
	}
	if len(queue.dropped) != 1 || queue.dropped[0].reason != "invalid_token" {
		t.Errorf("want Drop(invalid_token), got: %v", queue.dropped)
	}
}

func TestDispatcher_ProcessOne_UpstreamError_SchedulesBackoff(t *testing.T) {
	tests := []struct {
		name        string
		attempts    int
		wantDropped bool
		wantBackoff time.Duration
	}{
		{"attempts=0 → +5s", 0, false, 5 * time.Second},
		{"attempts=1 → +30s", 1, false, 30 * time.Second},
		{"attempts=2 → +5m", 2, false, 5 * time.Minute},
		{"attempts=3 → drop max_attempts", 3, true, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := makeTaskCreatedEvent(1)
			job := makeJob(10, "dev-1", 1, tc.attempts)
			dev := makeActiveDevice("dev-1")

			queue := &mockPushQueue{}
			events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
			devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
			relay := &mockRelayClient{
				pushResponse: pushproto.PushResponse{Status: pushproto.StatusUpstreamError},
			}
			tmpl := NewTemplates("")

			d := makeDispatcher(queue, events, devices, relay, tmpl)
			d.processOne(context.Background(), job)

			if tc.wantDropped {
				if len(queue.dropped) != 1 || queue.dropped[0].reason != "max_attempts" {
					t.Errorf("want Drop(max_attempts), got: %v", queue.dropped)
				}
			} else {
				if len(queue.failed) != 1 {
					t.Fatalf("want 1 MarkFailed, got %d", len(queue.failed))
				}
				wantNext := fixedClock()().Add(tc.wantBackoff)
				if !queue.failed[0].nextAttempt.Equal(wantNext) {
					t.Errorf("wrong next_attempt: want %v, got %v",
						wantNext, queue.failed[0].nextAttempt)
				}
			}
		})
	}
}

func TestDispatcher_ProcessOne_UpstreamError_HonoursRetryAfter(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0) // attempts=0 → backoff[0]=5s
	dev := makeActiveDevice("dev-1")

	// RetryAfter=120 > 5s → RetryAfter must be used
	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{
		pushResponse: pushproto.PushResponse{
			Status:     pushproto.StatusUpstreamError,
			RetryAfter: 120,
			Message:    "сервис временно недоступен",
		},
	}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.failed) != 1 {
		t.Fatalf("want MarkFailed, got failed=%v dropped=%v", queue.failed, queue.dropped)
	}
	wantNext := fixedClock()().Add(120 * time.Second)
	if !queue.failed[0].nextAttempt.Equal(wantNext) {
		t.Errorf("wrong next_attempt: want %v, got %v", wantNext, queue.failed[0].nextAttempt)
	}
}

func TestDispatcher_ProcessOne_NetworkError_BackoffAndDrop(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	dev := makeActiveDevice("dev-1")
	netErr := fmt.Errorf("%w: connection refused", ErrRelayUnavailable)

	tests := []struct {
		attempts    int
		wantDropped bool
	}{
		{0, false},
		{3, true},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempts=%d", tc.attempts), func(t *testing.T) {
			job := makeJob(10, "dev-1", 1, tc.attempts)

			queue := &mockPushQueue{}
			events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
			devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
			relay := &mockRelayClient{pushErr: netErr}
			tmpl := NewTemplates("")

			d := makeDispatcher(queue, events, devices, relay, tmpl)
			d.processOne(context.Background(), job)

			if tc.wantDropped {
				if len(queue.dropped) != 1 || queue.dropped[0].reason != "max_attempts" {
					t.Errorf("want Drop(max_attempts), got: %v", queue.dropped)
				}
			} else {
				if len(queue.failed) != 1 {
					t.Errorf("want MarkFailed, got: %v", queue.failed)
				}
			}
		})
	}
}

func TestDispatcher_ProcessOne_BadPayload_Drops(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0)
	dev := makeActiveDevice("dev-1")

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{
		pushResponse: pushproto.PushResponse{
			Status:  pushproto.StatusBadPayload,
			Message: "пустой заголовок",
		},
	}
	tmpl := NewTemplates("")

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "bad_payload" {
		t.Errorf("want Drop(bad_payload), got: %v", queue.dropped)
	}
}

func TestDispatcher_ProcessOne_TemplateError_Drops(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	job := makeJob(10, "dev-1", 1, 0)

	queue := &mockPushQueue{}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{}}
	relay := &mockRelayClient{}
	tmpl := &mockTemplates{err: errors.New("шаблон сломан")}

	d := makeDispatcher(queue, events, devices, relay, tmpl)
	d.processOne(context.Background(), job)

	if len(queue.dropped) != 1 || queue.dropped[0].reason != "bad_payload" {
		t.Errorf("want Drop(bad_payload) on template error, got: %v", queue.dropped)
	}
}

// --- Run tests ---

func TestDispatcher_Run_TerminatesOnContextCancel(t *testing.T) {
	queue := &mockPushQueue{nextBatch: nil}
	events := &mockEventStore{events: map[int64]*model.Event{}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{}}
	relay := &mockRelayClient{}
	tmpl := &mockTemplates{}

	d := makeDispatcher(queue, events, devices, relay, tmpl)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Run did not finish after context cancellation")
	}
}

func TestDispatcher_Run_ProcessesBatch_AndContinues(t *testing.T) {
	ev := makeTaskCreatedEvent(1)
	dev := makeActiveDevice("dev-1")

	queue := &mockPushQueue{
		nextBatch: []model.PushJob{makeJob(10, "dev-1", 1, 0)},
	}
	events := &mockEventStore{events: map[int64]*model.Event{1: ev}}
	devices := &mockDeviceStore{devices: map[string]*model.Device{"dev-1": dev}}
	relay := &mockRelayClient{
		pushResponse: pushproto.PushResponse{Status: pushproto.StatusSent},
	}
	tmpl := NewTemplates("")

	d := NewDispatcher(DispatcherDeps{
		Queue:       queue,
		Events:      events,
		Devices:     devices,
		Relay:       relay,
		Templates:   tmpl,
		Clock:       fixedClock(),
		Logger:      silentLogger(),
		Interval:    20 * time.Millisecond,
		BatchSize:   32,
		MaxAttempts: 4,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = d.Run(ctx)

	queue.mu.Lock()
	calls := queue.batchCallCount
	delivered := len(queue.delivered)
	queue.mu.Unlock()

	if calls < 2 {
		t.Errorf("want at least 2 ticks, got %d", calls)
	}
	if delivered < 1 {
		t.Errorf("want at least 1 delivery, got %d", delivered)
	}
}

func TestDispatcher_Defaults(t *testing.T) {
	d := NewDispatcher(DispatcherDeps{
		Queue:     &mockPushQueue{},
		Events:    &mockEventStore{events: map[int64]*model.Event{}},
		Devices:   &mockDeviceStore{devices: map[string]*model.Device{}},
		Relay:     NilRelayClient{},
		Templates: &mockTemplates{},
	})

	if d.interval != 2*time.Second {
		t.Errorf("default interval: want 2s, got %v", d.interval)
	}
	if d.batchSize != 32 {
		t.Errorf("default batchSize: want 32, got %d", d.batchSize)
	}
	if d.maxAttempts != 4 {
		t.Errorf("default maxAttempts: want 4, got %d", d.maxAttempts)
	}
}
