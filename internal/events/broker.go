package events

import (
	"sync"

	"github.com/anadale/huskwoot/internal/model"
)

const defaultBrokerBufferSize = 64

// BrokerConfig holds parameters for the in-memory event broker.
type BrokerConfig struct {
	// BufferSize is the per-subscriber channel buffer size. If <= 0,
	// defaultBrokerBufferSize is used. A subscriber with a full buffer is
	// considered slow: the broker closes its channel and removes it from
	// subscriptions to avoid blocking delivery; the client will reconnect
	// and catch up via Last-Event-ID.
	BufferSize int
}

// Broker is an in-memory fan-out of events to active SSE subscribers. No
// database interaction: the broker receives an already-persisted event and
// delivers it to the subscriber channels of each device.
type Broker struct {
	bufferSize int

	mu   sync.RWMutex
	subs map[string]map[*subscription]struct{}
}

// subscription represents a single subscriber. The pointer is used as a unique
// key in subs so that multiple subscriptions from the same device don't collide.
type subscription struct {
	ch chan model.Event
}

// NewBroker creates a new broker. Safe for concurrent use.
func NewBroker(cfg BrokerConfig) *Broker {
	buf := cfg.BufferSize
	if buf <= 0 {
		buf = defaultBrokerBufferSize
	}
	return &Broker{
		bufferSize: buf,
		subs:       make(map[string]map[*subscription]struct{}),
	}
}

// Subscribe registers a new subscriber for deviceID and returns a read-only
// event channel and an unsubscribe function.
func (b *Broker) Subscribe(deviceID string) (<-chan model.Event, func()) {
	sub := &subscription{ch: make(chan model.Event, b.bufferSize)}

	b.mu.Lock()
	set, ok := b.subs[deviceID]
	if !ok {
		set = make(map[*subscription]struct{})
		b.subs[deviceID] = set
	}
	set[sub] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() { b.remove(deviceID, sub) })
	}
	return sub.ch, cancel
}

// IsActive returns true if the device has at least one active subscriber.
func (b *Broker) IsActive(deviceID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[deviceID]) > 0
}

// Notify delivers an event to all subscribers of all devices. Subscribers
// with a full buffer are removed and their channels closed — the client will
// reconnect and perform a replay.
func (b *Broker) Notify(ev model.Event) {
	b.mu.RLock()
	var slow []slowSub
	for deviceID, set := range b.subs {
		for sub := range set {
			select {
			case sub.ch <- ev:
			default:
				slow = append(slow, slowSub{deviceID: deviceID, sub: sub})
			}
		}
	}
	b.mu.RUnlock()

	for _, s := range slow {
		b.remove(s.deviceID, s.sub)
	}
}

type slowSub struct {
	deviceID string
	sub      *subscription
}

// remove deletes a subscription from the index and closes its channel. Idempotent.
func (b *Broker) remove(deviceID string, sub *subscription) {
	b.mu.Lock()
	set, ok := b.subs[deviceID]
	if !ok {
		b.mu.Unlock()
		return
	}
	if _, exists := set[sub]; !exists {
		b.mu.Unlock()
		return
	}
	delete(set, sub)
	if len(set) == 0 {
		delete(b.subs, deviceID)
	}
	b.mu.Unlock()

	close(sub.ch)
}

// Static check: *Broker satisfies model.Broker.
var _ model.Broker = (*Broker)(nil)
