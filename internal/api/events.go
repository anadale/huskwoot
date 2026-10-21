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

// sseReplayBatchSize bounds each materialized durable event page.
// Tests can substitute a smaller value without creating hundreds of events.
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

// stream subscribes before establishing its initial durable mark. Broker payloads
// are wakeups only; SQL supplies every frame, in durable sequence order.
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
	cursor, replay := parseLastEventID(r.Header.Get(lastEventIDHeader))
	ch, cancel := h.broker.Subscribe(deviceID)
	defer cancel()
	target, err := h.events.MaxSeq(ctx)
	if err != nil {
		h.logError(ctx, "initial MaxSeq", err)
		WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "failed to read event cursor")
		return
	}
	if !replay {
		cursor = target
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if replay {
		var keepGoing bool
		cursor, keepGoing = h.drainThrough(ctx, w, flusher, cursor, target)
		if !keepGoing {
			return
		}
	}

	heartbeat := h.heartbeat
	if heartbeat <= 0 {
		heartbeat = defaultSSEHeartbeatInterval
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	if !writeKeepalive(w, flusher, target) {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-ch:
			if !open || !h.broker.IsActive(deviceID) {
				return
			}
		case <-ticker.C:
			if !h.broker.IsActive(deviceID) {
				return
			}
		}
		target, err = h.events.MaxSeq(ctx)
		if err != nil {
			h.logError(ctx, "wake MaxSeq", err)
			return
		}
		if ctx.Err() != nil || !h.broker.IsActive(deviceID) {
			return
		}
		var keepGoing bool
		cursor, keepGoing = h.drainThrough(ctx, w, flusher, cursor, target)
		if !keepGoing || !writeKeepalive(w, flusher, target) {
			return
		}
	}
}

// writeKeepalive reports a server mark, not a client acknowledgement.
func writeKeepalive(w http.ResponseWriter, flusher http.Flusher, maxSeq int64) bool {
	if _, err := fmt.Fprintf(w, ":keepalive %d\n\n", maxSeq); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// drainThrough delivers retained SQL rows through a fixed target. Bounds are read
// after each page is materialized, before any of that page's frames are written.
// Retention by source timestamp can leave non-prefix holes invisible to MinSeq;
// numeric gaps alone therefore never imply lost events.
func (h *eventsHandler) drainThrough(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, cursor, target int64) (int64, bool) {
	for {
		if ctx.Err() != nil {
			return cursor, false
		}
		page, err := h.events.SinceSeq(ctx, cursor, sseReplayBatchSize)
		if err != nil {
			h.logError(ctx, "drain SinceSeq", err)
			return cursor, false
		}
		minSeq, err := h.events.MinSeq(ctx)
		if err != nil {
			h.logError(ctx, "drain MinSeq", err)
			return cursor, false
		}
		maxSeq, err := h.events.MaxSeq(ctx)
		if err != nil {
			h.logError(ctx, "drain MaxSeq", err)
			return cursor, false
		}
		if cursor > maxSeq || (minSeq > cursor && minSeq-cursor > 1) {
			writeResetEvent(w, flusher, maxSeq)
			return cursor, false
		}
		if ctx.Err() != nil || (h.broker != nil && !h.broker.IsActive(DeviceIDFromContext(ctx))) {
			return cursor, false
		}
		if cursor >= target {
			return cursor, true
		}
		if len(page) == 0 {
			// Bounds were rechecked after the empty read. Without detectable
			// loss there is still no safe acknowledgement of the missing tail.
			return cursor, false
		}
		advanced := false
		for _, ev := range page {
			if ev.Seq > target {
				return cursor, true
			}
			if ev.Seq <= cursor {
				continue
			}
			if ctx.Err() != nil || !writeSSEEvent(w, flusher, ev) {
				return cursor, false
			}
			cursor = ev.Seq
			advanced = true
			if cursor == target {
				return cursor, true
			}
		}
		if !advanced {
			return cursor, false
		}
	}
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

// parseLastEventID distinguishes explicit zero (replay) from a missing or invalid
// cursor (new events only). Only nonnegative decimal int64 values are accepted.
func parseLastEventID(header string) (int64, bool) {
	if header == "" {
		return 0, false
	}
	for _, digit := range header {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
