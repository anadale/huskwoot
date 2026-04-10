package relay_test

import (
	"sync"
	"testing"

	"github.com/anadale/huskwoot/internal/relay"
)

func TestInstanceLoader_Secret_ReturnsNilForMissing(t *testing.T) {
	loader := relay.NewInstanceLoader([]relay.InstanceSpec{
		{ID: "known", Secret: "s3cr3t"},
	})

	if got := loader.Secret("unknown"); got != nil {
		t.Errorf("Secret(\"unknown\") = %v, want nil", got)
	}
	if got := loader.Secret("known"); string(got) != "s3cr3t" {
		t.Errorf("Secret(\"known\") = %q, want %q", string(got), "s3cr3t")
	}
}

func TestInstanceLoader_Swap_IsRaceFree(t *testing.T) {
	loader := relay.NewInstanceLoader([]relay.InstanceSpec{
		{ID: "a", Secret: "initial"},
	})

	const readers = 10
	const writers = 5
	const iters = 1000

	var wg sync.WaitGroup

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iters {
				_ = loader.Secret("a")
			}
		}()
	}

	for i := range writers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for range iters {
				loader.Swap(map[string][]byte{
					"a": []byte("updated"),
				})
				_ = loader.InstanceIDs()
				_ = idx
			}
		}(i)
	}

	wg.Wait()
}

func TestInstanceLoader_InstanceIDs_ReturnsKnownIDs(t *testing.T) {
	loader := relay.NewInstanceLoader([]relay.InstanceSpec{
		{ID: "x", Secret: "s1"},
		{ID: "y", Secret: "s2"},
	})

	ids := loader.InstanceIDs()
	if len(ids) != 2 {
		t.Errorf("InstanceIDs() returned %d IDs, want 2", len(ids))
	}

	seen := make(map[string]bool)
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["x"] || !seen["y"] {
		t.Errorf("InstanceIDs() = %v, want [x y]", ids)
	}
}

func TestInstanceLoader_Swap_UpdatesSecrets(t *testing.T) {
	loader := relay.NewInstanceLoader([]relay.InstanceSpec{
		{ID: "inst", Secret: "old-secret"},
	})

	if got := string(loader.Secret("inst")); got != "old-secret" {
		t.Fatalf("before Swap: Secret(\"inst\") = %q, want %q", got, "old-secret")
	}

	loader.Swap(map[string][]byte{
		"inst": []byte("new-secret"),
	})

	if got := string(loader.Secret("inst")); got != "new-secret" {
		t.Errorf("after Swap: Secret(\"inst\") = %q, want %q", got, "new-secret")
	}
}
