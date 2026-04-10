package events_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/events"
	"github.com/anadale/huskwoot/internal/model"
)

func makeEvent(seq int64, kind model.EventKind) model.Event {
	return model.Event{
		Seq:       seq,
		Kind:      kind,
		EntityID:  "entity-1",
		Payload:   []byte(`{"id":"entity-1"}`),
		CreatedAt: time.Now().UTC(),
	}
}

func TestBrokerSubscribeReceivesNotifications(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	ch, cancel := b.Subscribe("device-1")
	defer cancel()

	ev := makeEvent(1, model.EventTaskCreated)
	b.Notify(ev)

	select {
	case got := <-ch:
		if got.Seq != ev.Seq || got.Kind != ev.Kind {
			t.Fatalf("got %+v, want %+v", got, ev)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event within one second")
	}
}

func TestBrokerIsActiveReflectsSubscriptions(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	if b.IsActive("device-1") {
		t.Fatal("IsActive=true before subscribing")
	}

	_, cancel := b.Subscribe("device-1")
	if !b.IsActive("device-1") {
		t.Fatal("IsActive=false after subscribing")
	}

	cancel()
	if b.IsActive("device-1") {
		t.Fatal("IsActive=true after unsubscribing")
	}
}

func TestBrokerMultipleSubscribersForSameDevice(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	ch1, cancel1 := b.Subscribe("device-1")
	defer cancel1()
	ch2, cancel2 := b.Subscribe("device-1")
	defer cancel2()

	ev := makeEvent(1, model.EventTaskCreated)
	b.Notify(ev)

	for _, ch := range []<-chan model.Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Seq != ev.Seq {
				t.Fatalf("seq=%d, want %d", got.Seq, ev.Seq)
			}
		case <-time.After(time.Second):
			t.Fatal("one of the subscribers did not receive the event")
		}
	}
}

func TestBrokerNotifyReachesAllDevices(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	ch1, cancel1 := b.Subscribe("device-1")
	defer cancel1()
	ch2, cancel2 := b.Subscribe("device-2")
	defer cancel2()

	ev := makeEvent(42, model.EventProjectUpdated)
	b.Notify(ev)

	for i, ch := range []<-chan model.Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Seq != 42 {
				t.Fatalf("subscriber %d: seq=%d", i, got.Seq)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive event", i)
		}
	}
}

func TestBrokerUnsubscribeClosesChannel(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	ch, cancel := b.Subscribe("device-1")

	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel not closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed within one second")
	}
}

func TestBrokerUnsubscribeIdempotent(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	_, cancel := b.Subscribe("device-1")
	cancel()
	cancel()

	if b.IsActive("device-1") {
		t.Fatal("IsActive=true after double cancel")
	}
}

func TestBrokerNotifyNoSubscribersIsNoop(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{})
	b.Notify(makeEvent(1, model.EventTaskCreated))
}

func TestBrokerFullBufferDropsSubscriber(t *testing.T) {
	b := events.NewBroker(events.BrokerConfig{BufferSize: 2})
	ch, cancel := b.Subscribe("device-slow")
	defer cancel()

	for i := 1; i <= 5; i++ {
		b.Notify(makeEvent(int64(i), model.EventTaskCreated))
	}

	if b.IsActive("device-slow") {
		t.Fatal("slow subscriber was not removed from the broker")
	}

	received := make([]model.Event, 0, 5)
	timeout := time.After(time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			received = append(received, ev)
		case <-timeout:
			t.Fatal("channel not closed within one second after overflow")
		}
	}
	if len(received) == 0 {
		t.Fatal("received no events before overflow")
	}
}

func TestBrokerConcurrentSubscribeNotifyRace(t *testing.T) {
	t.Parallel()

	b := events.NewBroker(events.BrokerConfig{BufferSize: 128})

	var wg sync.WaitGroup
	var delivered atomic.Int64

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(devNum int) {
			defer wg.Done()
			ch, cancel := b.Subscribe("device")
			defer cancel()
			done := time.After(200 * time.Millisecond)
			for {
				select {
				case <-ch:
					delivered.Add(1)
				case <-done:
					return
				}
			}
		}(i)
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			deadline := time.Now().Add(150 * time.Millisecond)
			for j := int64(0); time.Now().Before(deadline); j++ {
				b.Notify(makeEvent(seq*1000+j, model.EventTaskCreated))
			}
		}(int64(i))
	}

	wg.Wait()
	// The race detector will catch incorrect accesses; delivered count is a sanity-check.
	if delivered.Load() == 0 {
		t.Fatal("no events were delivered")
	}
}
