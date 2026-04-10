package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

const (
	// defaultSSEHeartbeatInterval is the keepalive comment interval when
	// Config.SSEHeartbeatInterval <= 0. 15 s prevents intermediate proxies and
	// mobile radio stacks from closing an "idle" TCP connection.
	defaultSSEHeartbeatInterval = 15 * time.Second
	// lastEventIDHeader is the standard SSE header carrying the client cursor.
	lastEventIDHeader = "Last-Event-ID"
)

// sseReplayBatchSize is the upper limit for a single SinceSeq call during replay.
// Declared as var so tests can substitute a smaller value without creating hundreds of events.
var sseReplayBatchSize = 500

type eventsHandler struct {
	events    model.EventStore
	broker    model.Broker
	heartbeat time.Duration
	logger    *slog.Logger
}

func newEventsHandler(eventStore model.EventStore, broker model.Broker, heartbeat time.Duration, logger *slog.Logger) *eventsHandler {
	return &eventsHandler{events: eventStore, broker: broker, heartbeat: heartbeat, logger: logger}
}

// stream handles GET /v1/events: performs replay from Last-Event-ID,
// subscribes to the broker, and streams live events with periodic keepalive.
// Always unsubscribes from the broker before returning.
func (h *eventsHandler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "streaming not supported")
		return
	}

	// SSE is a long-lived stream; the global http.Server.WriteTimeout would close
	// the connection after RequestTimeout. Clear the write deadline for this handler only.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	ctx := r.Context()
	deviceID := DeviceIDFromContext(ctx)
	lastID := parseLastEventID(r.Header.Get(lastEventIDHeader))

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := h.broker.Subscribe(deviceID)
	defer cancel()

	// replayedSeq is the highest seq delivered during the replay phase. An event
	// with the same seq may already be in the broker channel (Broker.Notify fired
	// between Subscribe and replay) — the live loop must drop it to avoid the
	// client seeing a duplicate seq.
	var replayedSeq int64
	if lastID > 0 {
		seq, okReplay := h.replay(ctx, w, flusher, lastID)
		if !okReplay {
			return
		}
		replayedSeq = seq
	}

	heartbeat := h.heartbeat
	if heartbeat <= 0 {
		heartbeat = defaultSSEHeartbeatInterval
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				// Broker disconnected a slow subscriber. The client will reconnect
				// and replay from Last-Event-ID.
				return
			}
			if ev.Seq <= replayedSeq {
				continue
			}
			if !writeSSEEvent(w, flusher, ev) {
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ":keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// replay page-fetches events with seq > lastID. Returns the last actually
// delivered seq and a continuation flag: false means close the connection
// (reset or error), true means proceed in live mode.
// The upper bound is fixed by a MaxSeq snapshot on entry: new events
// (seq > maxSeq) will arrive via the broker channel opened before replay.
func (h *eventsHandler) replay(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, lastID int64) (int64, bool) {
	maxSeq, err := h.events.MaxSeq(ctx)
	if err != nil {
		h.logError(ctx, "replay MaxSeq", err)
		return 0, false
	}

	// Client cursor is ahead of the server: retention wipe or instance was recreated.
	if lastID > maxSeq {
		writeResetEvent(w, flusher, maxSeq)
		return 0, false
	}

	// Retention deletes events from the head of the sequence by created_at, and seq
	// is monotonically increasing with time, so retention always leaves a contiguous
	// tail starting from some MinSeq. Natural AUTOINCREMENT gaps arise after
	// transaction rollbacks and do not indicate retention loss. The check
	// "MinSeq > lastID+1" reliably distinguishes a retention loss (the client
	// genuinely missed events) from a rollback gap (nothing was lost).
	minSeq, err := h.events.MinSeq(ctx)
	if err != nil {
		h.logError(ctx, "replay MinSeq", err)
		return 0, false
	}
	if minSeq > 0 && minSeq > lastID+1 {
		writeResetEvent(w, flusher, maxSeq)
		return 0, false
	}

	cursor := lastID
	for cursor < maxSeq {
		if err := ctx.Err(); err != nil {
			return cursor, false
		}
		batch, err := h.events.SinceSeq(ctx, cursor, sseReplayBatchSize)
		if err != nil {
			h.logError(ctx, "replay SinceSeq", err)
			return cursor, false
		}
		if len(batch) == 0 {
			// No data in the range (cursor, maxSeq]. Retention loss was already
			// ruled out by the MinSeq check above, so we get here only when all
			// seq values between cursor and maxSeq are AUTOINCREMENT gaps. Just
			// finish replay.
			return cursor, true
		}
		for _, ev := range batch {
			if !writeSSEEvent(w, flusher, ev) {
				return cursor, false
			}
		}
		cursor = batch[len(batch)-1].Seq
	}
	return cursor, true
}

func (h *eventsHandler) logError(ctx context.Context, op string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "api/events: "+op,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("device_id", DeviceIDFromContext(ctx)),
		slog.String("error", err.Error()),
	)
}

// writeSSEEvent serialises a single event in SSE format and flushes the buffer.
// Returns false on a write error — the connection is considered broken.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, ev model.Event) bool {
	payload := ev.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// writeResetEvent sends the special reset event with the current max seq and
// flushes the buffer. The client must then perform a cold re-sync via
// GET /v1/sync/snapshot.
func writeResetEvent(w http.ResponseWriter, flusher http.Flusher, maxSeq int64) {
	payload, _ := json.Marshal(map[string]int64{"last_seq": maxSeq})
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", maxSeq, model.EventReset, payload)
	flusher.Flush()
}

// parseLastEventID parses the Last-Event-ID header. An empty or invalid
// string is treated as 0 (client connecting for the first time).
func parseLastEventID(header string) int64 {
	if header == "" {
		return 0
	}
	n, err := strconv.ParseInt(header, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
