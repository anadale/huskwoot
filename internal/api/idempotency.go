package api

import (
	"bytes"
	"container/list"
	"net/http"
	"sync"
	"time"
)

// IdempotencyHeader is the HTTP header name the client uses to request
// cached replay of a repeated request.
const IdempotencyHeader = "Idempotency-Key"

const (
	defaultIdempotencyTTL        = time.Hour
	defaultIdempotencyMaxEntries = 1024
)

// IdempotencyConfig controls the middleware parameters. Any zero field
// receives a safe default.
type IdempotencyConfig struct {
	// TTL is how long to keep a cached response. Default: 1 hour.
	TTL time.Duration
	// MaxEntries is the LRU size limit. Oldest entries are evicted when
	// exceeded. Default: 1024.
	MaxEntries int
	// Now is the time extension point for tests that need to "shift" time.
	// Default: time.Now.
	Now func() time.Time
}

// IdempotencyMiddleware caches responses for mutating HTTP requests keyed on
// `device_id + Idempotency-Key`. A repeated request with the same key gets the
// previously stored status, headers, and body — the real handler is not called.
// Transparent for GET/HEAD/OPTIONS and requests without the header. Responses
// with 5xx status are not cached: the client must be able to retry after a
// transient error is resolved.
func IdempotencyMiddleware(cfg IdempotencyConfig) func(http.Handler) http.Handler {
	cache := newIdempotencyCache(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyHeader)
			if key == "" || !isIdempotencySensitive(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			cacheKey := DeviceIDFromContext(r.Context()) + ":" + key

			for {
				entry, existing := cache.reserve(cacheKey)
				if existing {
					select {
					case <-entry.ready:
					case <-r.Context().Done():
						WriteError(w, http.StatusRequestTimeout, ErrorCodeTimeout, "client disconnected before idempotent response was ready")
						return
					}
					if entry.response != nil {
						writeCachedResponse(w, entry.response)
						return
					}
					// Entry was removed as unsuccessful (5xx) — retry the loop;
					// this request will execute the handler itself.
					continue
				}
				rec := newIdempotencyRecorder(w)
				// Guarantee entry.ready is closed even on panic: otherwise
				// requests waiting on the same key block until their own
				// context expires, and the pending entry stays in the LRU for
				// the full TTL.
				fulfilled := false
				defer func() {
					if fulfilled {
						return
					}
					cache.fulfill(entry, nil, false)
				}()
				next.ServeHTTP(rec, r)
				resp := rec.snapshot()
				cache.fulfill(entry, resp, isCacheableStatus(resp.status))
				fulfilled = true
				return
			}
		})
	}
}

// isIdempotencySensitive decides whether a method's response should be cached.
// Safe methods (GET/HEAD/OPTIONS) are repeatable by definition; TRACE/CONNECT
// are filtered out on the same principle.
func isIdempotencySensitive(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isCacheableStatus records that transient 5xx responses may be retried by the
// client — so they must not be cached. 2xx/3xx/4xx are considered deterministic
// and are cached.
func isCacheableStatus(status int) bool {
	return status >= 200 && status < 500
}

// idempotencyEntry is an LRU record. Channel ready is closed when response
// is filled or the entry is removed as unsuccessful.
type idempotencyEntry struct {
	key       string
	createdAt time.Time
	ready     chan struct{}
	response  *cachedResponse
}

// cachedResponse is what is replayed for repeated requests.
type cachedResponse struct {
	status int
	header http.Header
	body   []byte
}

type idempotencyCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	entries    map[string]*list.Element
	order      *list.List
}

func newIdempotencyCache(cfg IdempotencyConfig) *idempotencyCache {
	c := &idempotencyCache{
		ttl:        cfg.TTL,
		maxEntries: cfg.MaxEntries,
		now:        cfg.Now,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
	if c.ttl <= 0 {
		c.ttl = defaultIdempotencyTTL
	}
	if c.maxEntries <= 0 {
		c.maxEntries = defaultIdempotencyMaxEntries
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// reserve looks up an entry by key. If found and not expired, returns
// (entry, true) — the caller waits on entry.ready and serves entry.response.
// If absent, creates a pending entry and returns (entry, false): the caller
// executes the real handler and then calls fulfill.
func (c *idempotencyCache) reserve(key string) (*idempotencyEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		entry := el.Value.(*idempotencyEntry)
		if !c.isExpiredLocked(entry) {
			c.order.MoveToFront(el)
			return entry, true
		}
		c.removeLocked(el)
	}
	entry := &idempotencyEntry{
		key:       key,
		createdAt: c.now(),
		ready:     make(chan struct{}),
	}
	el := c.order.PushFront(entry)
	c.entries[key] = el
	c.evictLocked()
	return entry, false
}

// fulfill closes a pending entry. success=true — response is stored and served
// to waiters. success=false — entry is removed; waiters retry the handler.
func (c *idempotencyCache) fulfill(entry *idempotencyEntry, resp *cachedResponse, success bool) {
	c.mu.Lock()
	if success {
		entry.response = resp
	} else if el, ok := c.entries[entry.key]; ok {
		c.removeLocked(el)
	}
	c.mu.Unlock()
	close(entry.ready)
}

func (c *idempotencyCache) isExpiredLocked(e *idempotencyEntry) bool {
	return c.now().Sub(e.createdAt) > c.ttl
}

func (c *idempotencyCache) removeLocked(el *list.Element) {
	entry := el.Value.(*idempotencyEntry)
	delete(c.entries, entry.key)
	c.order.Remove(el)
}

// evictLocked keeps the LRU within maxEntries by removing the oldest elements
// from the tail of the list.
func (c *idempotencyCache) evictLocked() {
	for c.order.Len() > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.removeLocked(oldest)
	}
}

// idempotencyRecorder proxies response writes to the real ResponseWriter while
// simultaneously accumulating status/headers/body for later caching.
type idempotencyRecorder struct {
	http.ResponseWriter
	status      int
	headerSnap  http.Header
	body        bytes.Buffer
	wroteHeader bool
}

func newIdempotencyRecorder(w http.ResponseWriter) *idempotencyRecorder {
	return &idempotencyRecorder{ResponseWriter: w}
}

func (r *idempotencyRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.headerSnap = r.ResponseWriter.Header().Clone()
	r.ResponseWriter.WriteHeader(code)
}

func (r *idempotencyRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}

func (r *idempotencyRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap is needed by http.NewResponseController to reach the underlying
// *http.response through the wrapper chain (idempotencyRecorder is only
// applied to mutating methods, so SSE never sees it, but handlers may use
// ResponseController for other purposes).
func (r *idempotencyRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *idempotencyRecorder) snapshot() *cachedResponse {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	header := r.headerSnap
	if header == nil {
		header = r.ResponseWriter.Header().Clone()
	}
	body := make([]byte, r.body.Len())
	copy(body, r.body.Bytes())
	return &cachedResponse{status: status, header: header, body: body}
}

// writeCachedResponse replays a previously stored response: copies headers,
// writes the status, and writes the body.
func writeCachedResponse(w http.ResponseWriter, resp *cachedResponse) {
	dst := w.Header()
	for k, v := range resp.header {
		vv := make([]string, len(v))
		copy(vv, v)
		dst[k] = vv
	}
	w.WriteHeader(resp.status)
	_, _ = w.Write(resp.body)
}
