package pairing_test

import (
	"sync"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pairing"
)

func TestBroadcaster_Subscribe_NotifyDeliversResult(t *testing.T) {
	b := pairing.NewBroadcaster()

	ch, cleanup := b.Subscribe("pair-1")
	defer cleanup()

	want := model.PairingResult{
		PairID:      "pair-1",
		Status:      model.PairingStatusConfirmed,
		DeviceID:    "dev-1",
		BearerToken: "tok-1",
	}

	go func() {
		time.Sleep(5 * time.Millisecond)
		b.Notify("pair-1", want)
	}()

	select {
	case got := <-ch:
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("result not received within 50ms")
	}
}

func TestBroadcaster_Notify_NoSubscribers_DoesNotBlock(t *testing.T) {
	b := pairing.NewBroadcaster()

	done := make(chan struct{})
	go func() {
		b.Notify("pair-no-sub", model.PairingResult{PairID: "pair-no-sub"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Notify blocked with no subscribers")
	}
}

func TestBroadcaster_Subscribe_CleanupRemovesEntry(t *testing.T) {
	b := pairing.NewBroadcaster()

	_, cleanup := b.Subscribe("pair-clean")
	cleanup()

	done := make(chan struct{})
	go func() {
		b.Notify("pair-clean", model.PairingResult{PairID: "pair-clean"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Notify blocked after cleanup")
	}
}

func TestBroadcaster_ConcurrentSubscribeNotify_NoRace(t *testing.T) {
	b := pairing.NewBroadcaster()

	const n = 20
	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pairID := "pair-race-" + string(rune('a'+i))
			ch, cleanup := b.Subscribe(pairID)
			defer cleanup()

			go func() {
				time.Sleep(2 * time.Millisecond)
				b.Notify(pairID, model.PairingResult{PairID: pairID, Status: model.PairingStatusConfirmed})
			}()

			select {
			case <-ch:
			case <-time.After(100 * time.Millisecond):
				t.Errorf("goroutine %d: result not received within 100ms", i)
			}
		}(i)
	}

	wg.Wait()
}
