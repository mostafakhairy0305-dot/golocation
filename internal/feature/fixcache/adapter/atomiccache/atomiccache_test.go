package atomiccache_test

import (
	"sync"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/fixcache/adapter/atomiccache"
)

func TestEmptyCacheReportsNothing(t *testing.T) {
	t.Parallel()

	if fix, ok := atomiccache.New().Load(); ok {
		t.Fatalf("an empty cache returned %+v", fix)
	}
}

func TestLoadReturnsTheNewestStoredFix(t *testing.T) {
	t.Parallel()

	cache := atomiccache.New()
	cache.Store(geo.Fix{Latitude: 1})
	cache.Store(geo.Fix{Latitude: 2})

	fix, ok := cache.Load()
	if !ok || fix.Latitude != 2 {
		t.Fatalf("Load = %v, %v; want the newest (2)", fix.Latitude, ok)
	}
}

// The published pointer is shared with every reader, so a caller mutating what
// it stored — or what it loaded — must not be able to reach the cached value.
func TestTheCachedFixCannotBeMutatedThroughACopy(t *testing.T) {
	t.Parallel()

	cache := atomiccache.New()
	stored := geo.Fix{Latitude: 1, Timestamp: time.Now().UTC()}
	cache.Store(stored)

	stored.Latitude = 99
	loaded, _ := cache.Load()
	loaded.Latitude = 42

	again, _ := cache.Load()
	if again.Latitude != 1 {
		t.Fatalf("cached latitude = %v, want 1", again.Latitude)
	}
}

func TestConcurrentStoresAndLoads(t *testing.T) {
	t.Parallel()

	cache := atomiccache.New()
	cache.Store(geo.Fix{Latitude: 1})

	const (
		goroutines = 4
		rounds     = 500
	)

	var workers sync.WaitGroup

	for writer := range goroutines {
		workers.Go(func() { storeRepeatedly(cache, writer, rounds) })
	}

	for range goroutines {
		workers.Go(func() { loadRepeatedly(t, cache, rounds) })
	}

	workers.Wait()
}

// storeRepeatedly publishes distinct fixes, so a racing Load has something to
// tear rather than reading the same value back every time.
func storeRepeatedly(cache *atomiccache.Cache, writer, rounds int) {
	for i := range rounds {
		cache.Store(geo.Fix{Latitude: float64(writer), Longitude: float64(i)})
	}
}

// loadRepeatedly fails if the cache ever reports empty once a fix is in it. It
// reports through t.Error because it runs off the test's goroutine.
func loadRepeatedly(t *testing.T, cache *atomiccache.Cache, rounds int) {
	t.Helper()

	for range rounds {
		if _, ok := cache.Load(); !ok {
			t.Error("Load reported an empty cache after a Store")

			return
		}
	}
}

func BenchmarkLoad(b *testing.B) {
	cache := atomiccache.New()
	cache.Store(geo.Fix{Latitude: 51.5, Longitude: -0.12})
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, ok := cache.Load(); !ok {
				b.Fatal("empty cache")
			}
		}
	})
}
