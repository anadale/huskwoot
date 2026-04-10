package pairing

import (
	"sync"

	"github.com/anadale/huskwoot/internal/model"
)

// Broadcaster is a thread-safe in-memory pub/sub for pairing results.
// Each pairID may have at most one subscriber (long-poll client).
type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]chan model.PairingResult
}

// NewBroadcaster creates a new Broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[string]chan model.PairingResult)}
}

// Subscribe registers a subscriber for the given pairID.
// Returns a buffered channel (size 1) and an unsubscribe function.
// If a subscriber for pairID already exists, it is replaced.
func (b *Broadcaster) Subscribe(pairID string) (<-chan model.PairingResult, func()) {
	ch := make(chan model.PairingResult, 1)
	b.mu.Lock()
	b.subs[pairID] = ch
	b.mu.Unlock()

	cleanup := func() {
		b.mu.Lock()
		if b.subs[pairID] == ch {
			delete(b.subs, pairID)
		}
		b.mu.Unlock()
	}
	return ch, cleanup
}

// Notify sends a result to the subscriber for pairID without blocking.
// If there is no subscriber or the channel buffer is full, the call returns immediately.
func (b *Broadcaster) Notify(pairID string, result model.PairingResult) {
	b.mu.Lock()
	ch, ok := b.subs[pairID]
	b.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- result:
	default:
	}
}
