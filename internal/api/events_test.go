package api_test

import (
	"bufio"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

// sseTestHarness assembles the infrastructure for SSE integration tests:
// real stores, TaskService, broker, and httptest.Server.
type sseTestHarness struct {
	t          *testing.T
	db         *sql.DB
	server     *httptest.Server
	token      string
	device     *model.Device
	broker     *events.Broker
	eventStore *events.SQLiteEventStore
	taskSvc    model.TaskService
	projectSvc model.ProjectService
}

func newSSEHarness(t *testing.T, heartbeat time.Duration) *sseTestHarness {
	t.Helper()
	db := openTestDB(t)

	sqliteTasks, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	tasks := storage.NewCachedTaskStore(sqliteTasks)
	meta := storage.NewSQLiteMetaStore(db)
	eventStore := events.NewSQLiteEventStore(db)
	pushQueue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})
	deviceStore := devices.NewSQLiteDeviceStore(db)

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: db, Tasks: tasks, Meta: meta, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: db, Tasks: tasks, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: broker,
	})

	token := "sse-test-token"
	device := createTestDevice(t, db, "sse-device", token)

	srv := api.New(api.Config{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:                   db,
		Devices:              deviceStore,
		Projects:             projectSvc,
		Tasks:                taskSvc,
		Events:               eventStore,
		Broker:               broker,
		SSEHeartbeatInterval: heartbeat,
	})

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &sseTestHarness{
		t: t, db: db, server: ts, token: token, device: device,
		broker: broker, eventStore: eventStore, taskSvc: taskSvc, projectSvc: projectSvc,
	}
}

// openSSE opens a connection to /v1/events with the given Last-Event-ID.
// An empty lastEventID means the header is not sent.
func (h *sseTestHarness) openSSE(ctx context.Context, lastEventID string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.URL+"/v1/events", nil)
	if err != nil {
		h.t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		h.t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	return resp
}

// readSSEEvents parses an SSE stream and sends events to a channel until body
// is closed. Each event is a set of id/event/data fields.
type sseEvent struct {
	id      string
	kind    string
	data    string
	comment string
}

func readSSEEvents(body io.Reader) <-chan sseEvent {
	out := make(chan sseEvent, 32)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		var cur sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if cur.id != "" || cur.kind != "" || cur.data != "" || cur.comment != "" {
					out <- cur
					cur = sseEvent{}
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				cur.comment = strings.TrimPrefix(line, ":")
				// Comments are a standalone "event" that does not block the parser: emit immediately
				out <- cur
				cur = sseEvent{}
				continue
			}
			switch {
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

// waitBrokerActive waits until a subscriber appears for the device. Needed to
// avoid publishing events before the SSE handler has subscribed.
func waitBrokerActive(t *testing.T, broker *events.Broker, deviceID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if broker.IsActive(deviceID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("broker.IsActive(%s) остался false после %s", deviceID, timeout)
}

// nextSSE reads the next meaningful event (not a heartbeat comment) or fails on
// timeout. Comments/keepalives are ignored.
func nextSSE(t *testing.T, ch <-chan sseEvent, timeout time.Duration) sseEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("SSE stream closed without expected event")
			}
			if ev.kind == "" && ev.id == "" && ev.data == "" {
				// pure keepalive (comment) — skip
				continue
			}
			return ev
		case <-deadline:
			t.Fatalf("timeout waiting for SSE event (%s)", timeout)
		}
	}
}

// ---- tests ----

func TestSSEReceivesLiveEvent(t *testing.T) {
	h := newSSEHarness(t, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()

	events := readSSEEvents(resp.Body)
	waitBrokerActive(t, h.broker, h.device.ID, 2*time.Second)

	go func() {
		_, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "live"})
		if err != nil {
			t.Errorf("CreateTask: %v", err)
		}
	}()

	ev := nextSSE(t, events, 3*time.Second)
	if ev.kind != string(model.EventTaskCreated) {
		t.Fatalf("kind=%q, want %q", ev.kind, model.EventTaskCreated)
	}
	if ev.id == "" {
		t.Fatal("id is empty")
	}
	if !strings.Contains(ev.data, `"summary":"live"`) {
		t.Fatalf("data does not contain summary: %q", ev.data)
	}
}

func TestSSEReplaysSinceLastEventID(t *testing.T) {
	h := newSSEHarness(t, 0)

	// Create 3 events before connecting.
	for i := 0; i < 3; i++ {
		if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{
			Summary: "pre-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := h.openSSE(ctx, "1")
	defer resp.Body.Close()
	events := readSSEEvents(resp.Body)

	// Expect replay of the two remaining events (seq 2 and 3).
	ev1 := nextSSE(t, events, 2*time.Second)
	if ev1.id != "2" {
		t.Fatalf("first event: id=%q, want 2", ev1.id)
	}
	ev2 := nextSSE(t, events, 2*time.Second)
	if ev2.id != "3" {
		t.Fatalf("second event: id=%q, want 3", ev2.id)
	}
}

func TestSSEReplayPaginatesBeyondBatchSize(t *testing.T) {
	// Verify that when the client is more than one SinceSeq batch behind,
	// replay still loads all events without losing the tail.
	restore := api.SetSSEReplayBatchSizeForTest(3)
	defer restore()

	h := newSSEHarness(t, 0)

	const total = 7
	for i := 0; i < total; i++ {
		if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{
			Summary: "pre-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Last-Event-ID=1 → replay must deliver events 2..7 (six events), which
	// exceeds one batch (batch=3) and requires two SinceSeq iterations.
	resp := h.openSSE(ctx, "1")
	defer resp.Body.Close()
	events := readSSEEvents(resp.Body)

	for i := 2; i <= total; i++ {
		ev := nextSSE(t, events, 2*time.Second)
		if ev.id != strconv.Itoa(i) {
			t.Fatalf("event %d: id=%q, want %d", i, ev.id, i)
		}
	}
}

func TestSSEResetWhenLastEventIDTooOld(t *testing.T) {
	h := newSSEHarness(t, 0)

	// Create events and immediately delete them: simulating a retention wipe.
	for i := 0; i < 2; i++ {
		if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{
			Summary: "gone",
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	if _, err := h.db.Exec("DELETE FROM push_queue"); err != nil {
		t.Fatalf("DELETE push_queue: %v", err)
	}
	if _, err := h.db.Exec("DELETE FROM events"); err != nil {
		t.Fatalf("DELETE events: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Client connects with Last-Event-ID > MaxSeq (data has been lost).
	resp := h.openSSE(ctx, "100")
	defer resp.Body.Close()
	events := readSSEEvents(resp.Body)

	ev := nextSSE(t, events, 2*time.Second)
	if ev.kind != string(model.EventReset) {
		t.Fatalf("kind=%q, want %q", ev.kind, model.EventReset)
	}
}

func TestSSEHeartbeat(t *testing.T) {
	h := newSSEHarness(t, 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	events := readSSEEvents(resp.Body)
	waitBrokerActive(t, h.broker, h.device.ID, 2*time.Second)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed before heartbeat")
			}
			if ev.comment != "" && strings.Contains(ev.comment, "keepalive") {
				return
			}
		case <-deadline:
			t.Fatal("heartbeat did not arrive within 2 seconds")
		}
	}
}

func TestSSEClientDisconnectUnsubscribes(t *testing.T) {
	h := newSSEHarness(t, 0)

	ctx, cancel := context.WithCancel(context.Background())
	resp := h.openSSE(ctx, "")

	waitBrokerActive(t, h.broker, h.device.ID, 2*time.Second)

	cancel()
	_ = resp.Body.Close()

	// After context cancellation, the handler must unsubscribe from the broker.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.broker.IsActive(h.device.ID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscriber not removed from broker after client disconnect")
}

func TestSSEUnauthenticatedReturns401(t *testing.T) {
	h := newSSEHarness(t, 0)

	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}

// TestSSEReplaySkipsAutoIncrementGapWithoutReset verifies that a natural gap in
// seq (AUTOINCREMENT loses a number after a rolled-back transaction) is not
// treated as a retention loss by replay. Previously the handler saw
// `batch[0].Seq > cursor+1` and forced an expensive cold-re-sync. After the fix
// retention loss is distinguished via MinSeq, and ordinary gaps are skipped.
func TestSSEReplaySkipsAutoIncrementGapWithoutReset(t *testing.T) {
	h := newSSEHarness(t, 0)

	// Create 3 events normally — seq 1, 2, 3.
	for i := 0; i < 3; i++ {
		if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{
			Summary: "real-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	// Simulate an AUTOINCREMENT gap (e.g. rolled-back transaction): seq 4 is
	// skipped, the next event gets seq 5. We delete seq=4 AFTER insertion —
	// this reproduces "seq was lost, not absent from both retention and the
	// logical event sequence".
	if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "will-be-gap"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.db.Exec("DELETE FROM push_queue WHERE event_seq = 4"); err != nil {
		t.Fatalf("DELETE push_queue: %v", err)
	}
	if _, err := h.db.Exec("DELETE FROM events WHERE seq = 4"); err != nil {
		t.Fatalf("DELETE event 4: %v", err)
	}
	// Insert one more event — it will receive seq=5 (the real nature of an
	// AUTOINCREMENT gap: seq is strictly monotone).
	if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "after-gap"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Client connects with Last-Event-ID=2 — not a retention loss, but a natural
	// AUTOINCREMENT gap ahead (seq 3, 5; seq 4 missing).
	resp := h.openSSE(ctx, "2")
	defer resp.Body.Close()
	eventsCh := readSSEEvents(resp.Body)

	ev1 := nextSSE(t, eventsCh, 2*time.Second)
	if ev1.kind == string(model.EventReset) {
		t.Fatalf("replay sent reset on AUTOINCREMENT gap, expected normal event: data=%s", ev1.data)
	}
	if ev1.id != "3" {
		t.Fatalf("first event: id=%q, want 3", ev1.id)
	}
	ev2 := nextSSE(t, eventsCh, 2*time.Second)
	if ev2.kind == string(model.EventReset) {
		t.Fatalf("second event: reset instead of seq=5")
	}
	if ev2.id != "5" {
		t.Fatalf("second event: id=%q, want 5 (seq 4 — AUTOINCREMENT gap)", ev2.id)
	}
}

// TestSSEReplayResetsOnRetentionGap — the complementary test: when retention
// has actually deleted events from the head of the sequence, replay must send
// a reset rather than silently ignoring the loss.
func TestSSEReplayResetsOnRetentionGap(t *testing.T) {
	h := newSSEHarness(t, 0)

	for i := 0; i < 4; i++ {
		if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{
			Summary: "e-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	// Retention deleted the first two events (seq 1,2): remaining seq — 3, 4.
	if _, err := h.db.Exec("DELETE FROM push_queue WHERE event_seq IN (1,2)"); err != nil {
		t.Fatalf("DELETE push_queue: %v", err)
	}
	if _, err := h.db.Exec("DELETE FROM events WHERE seq IN (1,2)"); err != nil {
		t.Fatalf("DELETE events 1,2: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Client with Last-Event-ID=1 has fallen past the retention boundary (MinSeq=3 > 1+1=2).
	resp := h.openSSE(ctx, "1")
	defer resp.Body.Close()
	eventsCh := readSSEEvents(resp.Body)

	ev := nextSSE(t, eventsCh, 2*time.Second)
	if ev.kind != string(model.EventReset) {
		t.Fatalf("kind=%q, want %q — retention loss must trigger reset", ev.kind, model.EventReset)
	}
}
