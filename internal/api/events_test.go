package api_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	return newSSEHarnessWithHooks(t, heartbeat, nil, nil)
}

func newSSEHarnessWithHooks(t *testing.T, heartbeat time.Duration, wrapStore func(model.EventStore) model.EventStore, wrapBroker func(model.Broker) model.Broker) *sseTestHarness {
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
	var source model.EventStore = eventStore
	if wrapStore != nil {
		source = wrapStore(source)
	}
	var notifications model.Broker = broker
	if wrapBroker != nil {
		notifications = wrapBroker(notifications)
	}

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB: db, Tasks: tasks, Meta: meta, Events: eventStore,
		AutoTasks: storage.NewSQLiteAutoTaskStore(db),
		Devices:   deviceStore, Queue: pushQueue, Broker: notifications,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB: db, Tasks: tasks, Events: eventStore,
		Devices: deviceStore, Queue: pushQueue, Broker: notifications,
	})

	token := "sse-test-token"
	device := createTestDevice(t, db, "sse-device", token)

	srv := api.New(api.Config{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:                   db,
		Devices:              deviceStore,
		Projects:             projectSvc,
		Tasks:                taskSvc,
		TaskStore:            sqliteTasks,
		Events:               source,
		Broker:               notifications,
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

// assertBrokerActive relies on the HTTP response barrier: subscription must
// already exist before stream commits its 200 response.
func assertBrokerActive(t *testing.T, broker *events.Broker, deviceID string) {
	t.Helper()
	if !broker.IsActive(deviceID) {
		t.Fatalf("broker.IsActive(%s) is false after HTTP 200", deviceID)
	}
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
	assertBrokerActive(t, h.broker, h.device.ID)

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
	assertBrokerActive(t, h.broker, h.device.ID)

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

func TestSSEKeepaliveCarriesMaxSeq(t *testing.T) {
	h := newSSEHarness(t, 30*time.Millisecond)

	// Three events before connecting establish MaxSeq == 3.
	for i := 0; i < 3; i++ {
		if _, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{
			Summary: "pre-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A missing cursor skips replay but immediately reports the initial mark.
	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	events := readSSEEvents(resp.Body)
	assertBrokerActive(t, h.broker, h.device.ID)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed before keepalive")
			}
			if !strings.Contains(ev.comment, "keepalive") {
				continue
			}
			fields := strings.Fields(ev.comment)
			if len(fields) != 2 {
				t.Fatalf("keepalive comment = %q, want \"keepalive <maxSeq>\"", ev.comment)
			}
			seq, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatalf("keepalive maxSeq is not numeric: %q", ev.comment)
			}
			if seq != 3 {
				t.Fatalf("keepalive maxSeq = %d, want 3", seq)
			}
			return
		case <-deadline:
			t.Fatal("keepalive with maxSeq did not arrive within 2 seconds")
		}
	}
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
	restore := api.SetSSEReplayBatchSizeForTest(1)
	defer restore()
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

func TestSSEExplicitZeroReplaysSnapshotZeroEventBeforeConnect(t *testing.T) {
	h := newSSEHarness(t, time.Hour)
	resp := sseRequest(t, h, http.MethodGet, "/v1/sync/snapshot", "")
	var snapshot struct {
		LastSeq int64 `json:"lastSeq"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if snapshot.LastSeq != 0 {
		t.Fatalf("snapshot cursor = %d, want 0", snapshot.LastSeq)
	}
	task, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "synthetic zero replay"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := h.openSSE(ctx, "0")
	defer stream.Body.Close()
	frame := nextSSE(t, readSSEEvents(stream.Body), 2*time.Second)
	if frame.id != "1" || frame.kind != string(model.EventTaskCreated) || !strings.Contains(frame.data, task.ID) {
		t.Fatalf("unexpected replay: %+v", frame)
	}
	logSSEFrame(t, frame)
}

func sseRequest(t *testing.T, h *sseTestHarness, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d: %s", method, path, resp.StatusCode, data)
	}
	return resp
}

func logSSEFrame(t *testing.T, frame sseEvent) {
	t.Helper()
	t.Logf("Actual synthetic HTTP SSE frame:\nid: %s\nevent: %s\ndata: %s\n", frame.id, frame.kind, frame.data)
}

// sseStoreProbe places barriers around real SQLite reads without synthesizing rows.
type sseStoreProbe struct {
	model.EventStore
	max   func(context.Context) (int64, error)
	min   func(context.Context) (int64, error)
	since func(context.Context, int64, int) ([]model.Event, error)
}

func (p *sseStoreProbe) MaxSeq(ctx context.Context) (int64, error) {
	if p.max != nil {
		return p.max(ctx)
	}
	return p.EventStore.MaxSeq(ctx)
}

func (p *sseStoreProbe) MinSeq(ctx context.Context) (int64, error) {
	if p.min != nil {
		return p.min(ctx)
	}
	return p.EventStore.MinSeq(ctx)
}

func (p *sseStoreProbe) SinceSeq(ctx context.Context, cursor int64, limit int) ([]model.Event, error) {
	if p.since != nil {
		return p.since(ctx, cursor, limit)
	}
	return p.EventStore.SinceSeq(ctx, cursor, limit)
}

type sseBrokerProbe struct {
	model.Broker
	subscribe func(string) (<-chan model.Event, func())
	notify    func(model.Event)
}

func (p *sseBrokerProbe) Subscribe(id string) (<-chan model.Event, func()) {
	if p.subscribe != nil {
		return p.subscribe(id)
	}
	return p.Broker.Subscribe(id)
}

func (p *sseBrokerProbe) Notify(ev model.Event) {
	if p.notify != nil {
		p.notify(ev)
		return
	}
	p.Broker.Notify(ev)
}

func insertSSEDurable(t *testing.T, h *sseTestHarness, kind model.EventKind) model.Event {
	t.Helper()
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ev := model.Event{Kind: kind, EntityID: "synthetic", Payload: json.RawMessage(`{"synthetic":true}`)}
	ev.Seq, err = h.eventStore.Insert(context.Background(), tx, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ev
}

func waitSSEBarrier(t *testing.T, barrier <-chan struct{}) {
	t.Helper()
	select {
	case <-barrier:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE barrier")
	}
}

func awaitSSEClose(t *testing.T, frames <-chan sseEvent) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame, open := <-frames:
			if !open {
				return
			}
			if frame.kind != "" || frame.id != "" {
				t.Fatalf("unexpected frame before close: %+v", frame)
			}
		case <-deadline:
			t.Fatal("SSE stream did not close")
		}
	}
}

func waitSSEReady(t *testing.T, frames <-chan sseEvent, mark int64) {
	t.Helper()
	select {
	case frame, open := <-frames:
		if !open || frame.comment != fmt.Sprintf("keepalive %d", mark) || frame.id != "" {
			t.Fatalf("initial ready frame = %+v, open=%v", frame, open)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE initial keepalive did not arrive")
	}
}

func TestSSELastEventIDParsing(t *testing.T) {
	for _, tc := range []struct {
		header string
		seq    int64
		valid  bool
	}{
		{"0", 0, true}, {"01", 1, true}, {"42", 42, true},
		{"9223372036854775807", 9223372036854775807, true},
		{"", 0, false}, {"invalid", 0, false}, {"-1", 0, false}, {"-0", 0, false},
		{"+1", 0, false}, {" 0", 0, false}, {"0 ", 0, false}, {"1.0", 0, false},
		{"9223372036854775808", 0, false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			seq, valid := api.ParseLastEventIDForTest(tc.header)
			if seq != tc.seq || valid != tc.valid {
				t.Fatalf("parse(%q)=(%d,%v), want (%d,%v)", tc.header, seq, valid, tc.seq, tc.valid)
			}
		})
	}
}

func TestSSEInvalidHeadersEstablishMarkAfterSubscriptionBeforeResponse(t *testing.T) {
	for _, header := range []string{"", "invalid", "-1", "-0", "+1", "0 1", "1.0", "9223372036854775808"} {
		t.Run(header, func(t *testing.T) {
			var h *sseTestHarness
			marked, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			var markOnce sync.Once
			h = newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
				return &sseStoreProbe{EventStore: base, max: func(ctx context.Context) (int64, error) {
					seq, err := base.MaxSeq(ctx)
					markOnce.Do(func() {
						close(marked)
						<-release
					})
					return seq, err
				}}
			}, func(base model.Broker) model.Broker {
				return &sseBrokerProbe{Broker: base, subscribe: func(id string) (<-chan model.Event, func()) {
					ch, cancel := base.Subscribe(id)
					insertSSEDurable(t, h, model.EventProjectUpdated)
					return ch, cancel
				}}
			})
			defer releaseOnce.Do(func() { close(release) })
			insertSSEDurable(t, h, model.EventProjectCreated)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan *http.Response, 1)
			go func() { result <- h.openSSE(ctx, header) }()
			waitSSEBarrier(t, marked)
			select {
			case resp := <-result:
				resp.Body.Close()
				t.Fatal("HTTP 200 escaped before initial durable mark")
			default:
			}
			releaseOnce.Do(func() { close(release) })
			resp := <-result
			defer resp.Body.Close()
			frames := readSSEEvents(resp.Body)
			waitSSEReady(t, frames, 2)
			ev := insertSSEDurable(t, h, model.EventProjectDeleted)
			h.broker.Notify(model.Event{Seq: 999, Kind: model.EventTaskDeleted, Payload: json.RawMessage(`{"forged":true}`)})
			frame := nextSSE(t, frames, 2*time.Second)
			if frame.id != strconv.FormatInt(ev.Seq, 10) || frame.kind != string(ev.Kind) || strings.Contains(frame.data, "forged") {
				t.Fatalf("new-only durable frame = %+v", frame)
			}
		})
	}
}

func TestSSEExplicitZeroResetsPastRetention(t *testing.T) {
	h := newSSEHarness(t, time.Hour)
	insertSSEDurable(t, h, model.EventProjectCreated)
	insertSSEDurable(t, h, model.EventProjectUpdated)
	if _, err := h.db.Exec("DELETE FROM events WHERE seq = 1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "0")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	frame := nextSSE(t, frames, 2*time.Second)
	if frame.kind != "reset" || frame.id != "2" || frame.data != `{"last_seq":2}` {
		t.Fatalf("reset = %+v", frame)
	}
	logSSEFrame(t, frame)
	awaitSSEClose(t, frames)
}

func TestSSEHeartbeatDrainsCommitWithoutNotify(t *testing.T) {
	h := newSSEHarness(t, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	waitSSEReady(t, frames, 0)
	ev := insertSSEDurable(t, h, model.EventProjectUpdated)
	frame := nextSSE(t, frames, 2*time.Second)
	if frame.id != strconv.FormatInt(ev.Seq, 10) || frame.kind != string(ev.Kind) {
		t.Fatalf("heartbeat frame = %+v", frame)
	}
}

func sseAsyncRequest(h *sseTestHarness, method, path, body string) <-chan error {
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("HTTP status %d: %s", resp.StatusCode, data)
		}
		done <- err
	}()
	return done
}

func waitSSERequest(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP mutation did not complete")
	}
}

func holdSSENotify(kind model.EventKind, held chan<- struct{}, release <-chan struct{}) func(model.Broker) model.Broker {
	return func(base model.Broker) model.Broker {
		var once sync.Once
		return &sseBrokerProbe{Broker: base, notify: func(ev model.Event) {
			if ev.Kind == kind {
				once.Do(func() {
					close(held)
					<-release
				})
			}
			base.Notify(ev)
		}}
	}
}

func assertSSESequence(t *testing.T, frames <-chan sseEvent, start int64, kinds ...model.EventKind) {
	t.Helper()
	for i, kind := range kinds {
		frame := nextSSE(t, frames, 2*time.Second)
		if frame.id != strconv.FormatInt(start+int64(i), 10) || frame.kind != string(kind) {
			t.Fatalf("frame %d = %+v, want seq=%d kind=%s", i, frame, start+int64(i), kind)
		}
		logSSEFrame(t, frame)
	}
}

func TestSSEDelayedPATCHNotifyThenDELETEUsesDurableOrder(t *testing.T) {
	held, release := make(chan struct{}), make(chan struct{})
	h := newSSEHarnessWithHooks(t, time.Hour, nil, holdSSENotify(model.EventProjectUpdated, held, release))
	var once sync.Once
	defer once.Do(func() { close(release) })
	project, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Synthetic delayed patch"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	waitSSEReady(t, frames, 1)
	patch := sseAsyncRequest(h, http.MethodPatch, "/v1/projects/"+project.ID, `{"color":"purple"}`)
	waitSSEBarrier(t, held)
	sseRequest(t, h, http.MethodDelete, "/v1/projects/"+project.ID, "").Body.Close()
	assertSSESequence(t, frames, 2, model.EventProjectUpdated, model.EventProjectDeleted)
	once.Do(func() { close(release) })
	waitSSERequest(t, patch)
	last := insertSSEDurable(t, h, model.EventProjectCreated)
	h.broker.Notify(last)
	assertSSESequence(t, frames, 4, model.EventProjectCreated)
}

func TestSSEHeldMoveDeleteThenCompletionUsesDurableOrder(t *testing.T) {
	held, release := make(chan struct{}), make(chan struct{})
	h := newSSEHarnessWithHooks(t, time.Hour, nil, holdSSENotify(model.EventTaskMoved, held, release))
	var once sync.Once
	defer once.Do(func() { close(release) })
	project, err := h.projectSvc.CreateProject(context.Background(), model.CreateProjectRequest{Name: "Synthetic held move"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{ProjectID: project.ID, Summary: "Synthetic moved task"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	waitSSEReady(t, frames, 2)
	deletion := sseAsyncRequest(h, http.MethodDelete, "/v1/projects/"+project.ID, "")
	waitSSEBarrier(t, held)
	if _, err := h.taskSvc.CompleteTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	assertSSESequence(t, frames, 3, model.EventTaskMoved, model.EventProjectDeleted, model.EventTaskCompleted)
	once.Do(func() { close(release) })
	waitSSERequest(t, deletion)
	last := insertSSEDurable(t, h, model.EventProjectCreated)
	h.broker.Notify(last)
	assertSSESequence(t, frames, 6, model.EventProjectCreated)
}

func TestSSECommitDuringReplayAndAfterTargetWaitsForNextWake(t *testing.T) {
	restore := api.SetSSEReplayBatchSizeForTest(1)
	defer restore()
	pageLoaded, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
		return &sseStoreProbe{EventStore: base, since: func(ctx context.Context, cursor int64, limit int) ([]model.Event, error) {
			page, err := base.SinceSeq(ctx, cursor, limit)
			if cursor == 1 {
				once.Do(func() {
					close(pageLoaded)
					<-release
				})
			}
			return page, err
		}}
	}, nil)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	insertSSEDurable(t, h, model.EventProjectCreated)
	insertSSEDurable(t, h, model.EventProjectUpdated)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "0")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	assertSSESequence(t, frames, 1, model.EventProjectCreated)
	waitSSEBarrier(t, pageLoaded)
	later := insertSSEDurable(t, h, model.EventProjectDeleted)
	releaseOnce.Do(func() { close(release) })
	assertSSESequence(t, frames, 2, model.EventProjectUpdated)
	waitSSEReady(t, frames, 2)
	h.broker.Notify(later)
	assertSSESequence(t, frames, 3, model.EventProjectDeleted)
}

func TestSSERetentionAfterPageMaterializationResetsBeforeFrames(t *testing.T) {
	for _, pageCursor := range []int64{0, 1} {
		t.Run(strconv.FormatInt(pageCursor, 10), func(t *testing.T) {
			restore := api.SetSSEReplayBatchSizeForTest(1)
			defer restore()
			loaded, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			h := newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
				return &sseStoreProbe{EventStore: base, since: func(ctx context.Context, cursor int64, limit int) ([]model.Event, error) {
					page, err := base.SinceSeq(ctx, cursor, limit)
					if cursor == pageCursor {
						once.Do(func() {
							close(loaded)
							<-release
						})
					}
					return page, err
				}}
			}, nil)
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			for range 4 {
				insertSSEDurable(t, h, model.EventProjectUpdated)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp := h.openSSE(ctx, "0")
			defer resp.Body.Close()
			frames := readSSEEvents(resp.Body)
			if pageCursor == 1 {
				assertSSESequence(t, frames, 1, model.EventProjectUpdated)
			}
			waitSSEBarrier(t, loaded)
			if _, err := h.db.Exec("DELETE FROM events WHERE seq <= ?", pageCursor+1); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			frame := nextSSE(t, frames, 2*time.Second)
			if frame.kind != "reset" || frame.id != "4" {
				t.Fatalf("materialized stale page leaked: %+v", frame)
			}
			awaitSSEClose(t, frames)
		})
	}
}

func TestSSEPageRowsAboveTargetWaitForNextWake(t *testing.T) {
	var h *sseTestHarness
	later := make(chan model.Event, 1)
	var once sync.Once
	h = newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
		return &sseStoreProbe{EventStore: base, since: func(ctx context.Context, cursor int64, limit int) ([]model.Event, error) {
			once.Do(func() { later <- insertSSEDurable(t, h, model.EventProjectDeleted) })
			return base.SinceSeq(ctx, cursor, limit)
		}}
	}, nil)
	insertSSEDurable(t, h, model.EventProjectCreated)
	insertSSEDurable(t, h, model.EventProjectUpdated)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "0")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	assertSSESequence(t, frames, 1, model.EventProjectCreated, model.EventProjectUpdated)
	waitSSEReady(t, frames, 2)
	h.broker.Notify(<-later)
	assertSSESequence(t, frames, 3, model.EventProjectDeleted)
}

func TestSSEEmptyPageBeforeTargetClosesWithoutAcknowledgement(t *testing.T) {
	for _, detectable := range []bool{false, true} {
		t.Run(strconv.FormatBool(detectable), func(t *testing.T) {
			var h *sseTestHarness
			h = newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
				return &sseStoreProbe{EventStore: base, since: func(ctx context.Context, cursor int64, limit int) ([]model.Event, error) {
					query := "DELETE FROM events WHERE seq > 1"
					if detectable {
						query = "DELETE FROM events"
					}
					if _, err := h.db.Exec(query); err != nil {
						return nil, err
					}
					page, err := base.SinceSeq(ctx, cursor, limit)
					// A commit after the empty SQL read makes the loss detectable
					// only when the handler checks bounds after materialization.
					if detectable {
						insertSSEDurable(t, h, model.EventProjectDeleted)
					}
					return page, err
				}}
			}, nil)
			for range 3 {
				insertSSEDurable(t, h, model.EventProjectUpdated)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			resp := h.openSSE(ctx, "1")
			defer resp.Body.Close()
			frames := readSSEEvents(resp.Body)
			if detectable {
				frame := nextSSE(t, frames, 2*time.Second)
				if frame.kind != "reset" || frame.id != "4" {
					t.Fatalf("detectable loss = %+v", frame)
				}
			}
			awaitSSEClose(t, frames)
		})
	}
}

type sseFaultWriter struct {
	*httptest.ResponseRecorder
	failAt     int
	writes     int
	afterFlush func()
	onHeader   func(int)
}

func (w *sseFaultWriter) WriteHeader(status int) {
	if w.onHeader != nil {
		w.onHeader(status)
	}
	w.ResponseRecorder.WriteHeader(status)
}

func (w *sseFaultWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.failAt > 0 && w.writes == w.failAt {
		return 0, errors.New("synthetic response write failure")
	}
	return w.ResponseRecorder.Write(data)
}

func (w *sseFaultWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.afterFlush != nil {
		w.afterFlush()
	}
}

func TestSSEDrainErrorsNeverAdvancePastSuccessfulFrames(t *testing.T) {
	for _, failure := range []string{"since", "min", "max", "write-first", "write-second", "cancel-before", "cancel-after-first", "second-page", "empty-page"} {
		t.Run(failure, func(t *testing.T) {
			restore := api.SetSSEReplayBatchSizeForTest(1)
			defer restore()
			h := newSSEHarness(t, time.Hour)
			for range 2 {
				insertSSEDurable(t, h, model.EventProjectUpdated)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &sseStoreProbe{EventStore: h.eventStore}
			writer := &sseFaultWriter{ResponseRecorder: httptest.NewRecorder()}
			wantCursor := int64(0)
			boom := errors.New("synthetic SQL read failure")
			switch failure {
			case "since":
				source.since = func(context.Context, int64, int) ([]model.Event, error) { return nil, boom }
			case "min":
				source.min = func(context.Context) (int64, error) { return 0, boom }
			case "max":
				source.max = func(context.Context) (int64, error) { return 0, boom }
			case "write-first":
				writer.failAt = 1
			case "write-second":
				writer.failAt, wantCursor = 2, 1
			case "cancel-before":
				cancel()
			case "cancel-after-first":
				writer.afterFlush = cancel
				wantCursor = 1
			case "second-page":
				source.since = func(ctx context.Context, cursor int64, limit int) ([]model.Event, error) {
					if cursor == 1 {
						return nil, boom
					}
					return h.eventStore.SinceSeq(ctx, cursor, limit)
				}
				wantCursor = 1
			case "empty-page":
				if _, err := h.db.Exec("DELETE FROM events WHERE seq = 2"); err != nil {
					t.Fatal(err)
				}
				wantCursor = 1
			}
			cursor, keepGoing := api.DrainSSEThroughForTest(ctx, source, writer, 0, 2)
			if cursor != wantCursor || keepGoing {
				t.Fatalf("cursor=%d continue=%v, want cursor=%d close; frames=%s", cursor, keepGoing, wantCursor, writer.Body)
			}
			if strings.Contains(writer.Body.String(), "id: 2\n") {
				t.Fatalf("failed frame was acknowledged: %s", writer.Body)
			}
		})
	}
}

func TestSSEBrokerCloseAndOverflowTerminateWithoutPayloadFallback(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(strconv.FormatBool(overflow), func(t *testing.T) {
			wakeEntered, release := make(chan struct{}), make(chan struct{})
			subscription := make(chan func(), 1)
			calls := 0
			h := newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
				return &sseStoreProbe{EventStore: base, max: func(ctx context.Context) (int64, error) {
					calls++
					seq, err := base.MaxSeq(ctx)
					if calls == 2 {
						close(wakeEntered)
						<-release
					}
					return seq, err
				}}
			}, func(base model.Broker) model.Broker {
				return &sseBrokerProbe{Broker: base, subscribe: func(id string) (<-chan model.Event, func()) {
					ch, cancel := base.Subscribe(id)
					subscription <- cancel
					return ch, cancel
				}}
			})
			var once sync.Once
			defer once.Do(func() { close(release) })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp := h.openSSE(ctx, "")
			defer resp.Body.Close()
			frames := readSSEEvents(resp.Body)
			waitSSEReady(t, frames, 0)
			closeSubscription := <-subscription
			ev := insertSSEDurable(t, h, model.EventProjectCreated)
			h.broker.Notify(ev)
			waitSSEBarrier(t, wakeEntered)
			if overflow {
				for range 65 {
					h.broker.Notify(model.Event{Seq: 999, Kind: model.EventTaskDeleted, Payload: json.RawMessage(`{"forged":true}`)})
				}
			} else {
				closeSubscription()
			}
			if h.broker.IsActive(h.device.ID) {
				t.Fatal("broker did not remove the closed or overflowed subscriber")
			}
			once.Do(func() { close(release) })
			awaitSSEClose(t, frames)
		})
	}
}

func TestSSECancellationUnsubscribesUsingChannelBarrier(t *testing.T) {
	unsubscribed := make(chan struct{})
	h := newSSEHarnessWithHooks(t, time.Hour, nil, func(base model.Broker) model.Broker {
		return &sseBrokerProbe{Broker: base, subscribe: func(id string) (<-chan model.Event, func()) {
			ch, cancel := base.Subscribe(id)
			return ch, func() {
				cancel()
				close(unsubscribed)
			}
		}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	resp := h.openSSE(ctx, "")
	cancel()
	resp.Body.Close()
	waitSSEBarrier(t, unsubscribed)
	if h.broker.IsActive(h.device.ID) {
		t.Fatal("cancelled stream still subscribed")
	}
}

func TestSSEDatabaseFailuresTerminateWithoutBrokerPayloadFallback(t *testing.T) {
	for _, operation := range []string{"initial-max", "wake-max", "since", "min", "page-max"} {
		t.Run(operation, func(t *testing.T) {
			h := newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
				maxCalls := 0
				boom := errors.New("synthetic database failure")
				probe := &sseStoreProbe{EventStore: base}
				probe.max = func(ctx context.Context) (int64, error) {
					maxCalls++
					if operation == "initial-max" || (operation == "wake-max" && maxCalls == 2) || (operation == "page-max" && maxCalls == 3) {
						return 0, boom
					}
					return base.MaxSeq(ctx)
				}
				if operation == "since" {
					probe.since = func(context.Context, int64, int) ([]model.Event, error) { return nil, boom }
				}
				if operation == "min" {
					probe.min = func(context.Context) (int64, error) { return 0, boom }
				}
				return probe
			}, nil)
			if operation == "initial-max" {
				req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/events", nil)
				req.Header.Set("Authorization", "Bearer "+h.token)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusInternalServerError {
					t.Fatalf("initial mark failure status = %d", resp.StatusCode)
				}
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			resp := h.openSSE(ctx, "")
			defer resp.Body.Close()
			frames := readSSEEvents(resp.Body)
			waitSSEReady(t, frames, 0)
			insertSSEDurable(t, h, model.EventProjectCreated)
			h.broker.Notify(model.Event{Seq: 999, Kind: model.EventTaskDeleted, Payload: json.RawMessage(`{"forged":true}`)})
			awaitSSEClose(t, frames)
		})
	}
}

func TestSSESoftDeleteGETCancelledThenPurgeGET404AndWrappedFrame(t *testing.T) {
	h := newSSEHarness(t, time.Hour)
	task, err := h.taskSvc.CreateTask(context.Background(), model.CreateTaskRequest{Summary: "Synthetic retained task"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	waitSSEReady(t, frames, 1)
	sseRequest(t, h, http.MethodDelete, "/v1/tasks/"+task.ID, "").Body.Close()
	get := sseRequest(t, h, http.MethodGet, "/v1/tasks/"+task.ID, "")
	var cancelled struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(get.Body).Decode(&cancelled); err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	if cancelled.ID != task.ID || cancelled.Status != "cancelled" {
		t.Fatalf("soft-deleted task = %+v", cancelled)
	}
	soft := nextSSE(t, frames, 2*time.Second)
	if soft.kind != string(model.EventTaskUpdated) {
		t.Fatalf("soft delete frame = %+v", soft)
	}
	purged, err := h.taskSvc.PurgeClosedOlderThan(context.Background(), time.Now().Add(time.Hour))
	if err != nil || purged != 1 {
		t.Fatalf("purge count=%d err=%v", purged, err)
	}
	frame := nextSSE(t, frames, 2*time.Second)
	var payload struct {
		ID   string `json:"id"`
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatal(err)
	}
	if frame.id != "3" || frame.kind != string(model.EventTaskDeleted) || payload.ID != "" || payload.Task.ID != task.ID || payload.Task.Status != "cancelled" {
		t.Fatalf("purge frame = %+v, payload=%+v", frame, payload)
	}
	stored, err := h.eventStore.GetBySeq(context.Background(), 3)
	if err != nil || stored == nil || string(stored.Payload) != frame.data {
		t.Fatalf("HTTP frame differs from durable purge event: stored=%+v err=%v", stored, err)
	}
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/tasks/"+task.ID, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	gone, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("purged GET status = %d, want 404", gone.StatusCode)
	}
	logSSEFrame(t, frame)
}

func TestSSECommitDuringSubscriptionReplaysOnce(t *testing.T) {
	var h *sseTestHarness
	h = newSSEHarnessWithHooks(t, time.Hour, nil, func(base model.Broker) model.Broker {
		return &sseBrokerProbe{Broker: base, subscribe: func(id string) (<-chan model.Event, func()) {
			ch, cancel := base.Subscribe(id)
			ev := insertSSEDurable(t, h, model.EventProjectCreated)
			base.Notify(ev)
			return ch, cancel
		}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "0")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	assertSSESequence(t, frames, 1, model.EventProjectCreated)
	waitSSEReady(t, frames, 1)
	ev := insertSSEDurable(t, h, model.EventProjectUpdated)
	h.broker.Notify(ev)
	assertSSESequence(t, frames, 2, model.EventProjectUpdated)
}

func TestSSEClosedDatabaseTerminatesWithoutBrokerPayloadFallback(t *testing.T) {
	h := newSSEHarness(t, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp := h.openSSE(ctx, "")
	defer resp.Body.Close()
	frames := readSSEEvents(resp.Body)
	waitSSEReady(t, frames, 0)
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	h.broker.Notify(model.Event{Seq: 999, Kind: model.EventTaskDeleted, Payload: json.RawMessage(`{"forged":true}`)})
	awaitSSEClose(t, frames)
}

func TestSSEHTTPWriteFailureUnsubscribesWithoutFurtherFrames(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			h := newSSEHarness(t, time.Hour)
			for range 3 {
				insertSSEDurable(t, h, model.EventProjectUpdated)
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
			req.Header.Set("Authorization", "Bearer "+h.token)
			req.Header.Set("Last-Event-ID", "0")
			writer := &sseFaultWriter{ResponseRecorder: httptest.NewRecorder(), failAt: failAt}
			h.server.Config.Handler.ServeHTTP(writer, req)
			if writer.writes != failAt || h.broker.IsActive(h.device.ID) {
				t.Fatalf("write failure did not stop and unsubscribe: writes=%d active=%v", writer.writes, h.broker.IsActive(h.device.ID))
			}
			if strings.Contains(writer.Body.String(), fmt.Sprintf("id: %d\n", failAt)) {
				t.Fatalf("failed frame appeared in response: %s", writer.Body)
			}
		})
	}
}

func TestSSEInitialSubscriptionAndMarkPrecedeHeadersAndFlush(t *testing.T) {
	var order []string
	h := newSSEHarnessWithHooks(t, time.Hour, func(base model.EventStore) model.EventStore {
		return &sseStoreProbe{EventStore: base, max: func(ctx context.Context) (int64, error) {
			order = append(order, "mark")
			return base.MaxSeq(ctx)
		}}
	}, func(base model.Broker) model.Broker {
		return &sseBrokerProbe{Broker: base, subscribe: func(id string) (<-chan model.Event, func()) {
			order = append(order, "subscribe")
			return base.Subscribe(id)
		}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+h.token)
	writer := &sseFaultWriter{
		ResponseRecorder: httptest.NewRecorder(),
		onHeader: func(status int) {
			if status != http.StatusOK {
				t.Errorf("initial stream status=%d", status)
			}
			order = append(order, "headers")
		},
		afterFlush: func() {
			order = append(order, "flush")
			cancel()
		},
	}
	h.server.Config.Handler.ServeHTTP(writer, req)
	if len(order) < 4 || strings.Join(order[:4], ",") != "subscribe,mark,headers,flush" {
		t.Fatalf("stream initialization order=%v", order)
	}
}
