package api

import (
	"context"
	"net/http"

	"github.com/anadale/huskwoot/internal/model"
)

// SetSSEReplayBatchSizeForTest replaces sseReplayBatchSize and returns a
// restore function. The _test.go suffix means this helper is only compiled
// during testing and is invisible to the production binary, yet visible to
// the external package api_test.
func SetSSEReplayBatchSizeForTest(n int) func() {
	old := sseReplayBatchSize
	sseReplayBatchSize = n
	return func() { sseReplayBatchSize = old }
}

// DrainSSEThroughForTest exposes the cursor result for deterministic write-error tests.
func DrainSSEThroughForTest(ctx context.Context, store model.EventStore, w http.ResponseWriter, cursor, target int64) (int64, bool) {
	return newEventsHandler(store, nil, 0, nil).drainThrough(ctx, w, w.(http.Flusher), cursor, target)
}

// ParseLastEventIDForTest exposes the explicit-cursor boundary to external tests.
func ParseLastEventIDForTest(header string) (int64, bool) {
	return parseLastEventID(header)
}
