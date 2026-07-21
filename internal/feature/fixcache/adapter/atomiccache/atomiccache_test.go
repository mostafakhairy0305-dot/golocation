package atomiccache

import (
	"sync"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

func TestEmptyCacheReportsNothing(t *testing.T) {
	if fix, ok := New().Load(); ok {
		t.Fatalf("an empty cache returned %+v", fix)
	}
}

func TestLoadReturnsTheNewestStoredFix(t *testing.T) {
	cache := New()
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
	cache := New()
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
	cache := New()
	cache.Store(geo.Fix{Latitude: 1})

	var wg sync.WaitGroup
	for writer := range 4 {
		wg.Go(func() {
			for i := range 500 {
				cache.Store(geo.Fix{Latitude: float64(writer), Longitude: float64(i)})
			}
		})
	}

	for range 4 {
		wg.Go(func() {
			for range 500 {
				if _, ok := cache.Load(); !ok {
					t.Error("Load reported an empty cache after a Store")

					return
				}
			}
		})
	}

	wg.Wait()
}

func BenchmarkLoad(b *testing.B) {
	cache := New()
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
