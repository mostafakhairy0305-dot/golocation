package fixedclock

import (
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// Every consumer compares against UTC timestamps the core produced, so a
// clock handed a wall time in some other zone must normalise rather than make
// the caller's Equal comparisons depend on the test machine's locale.
func TestNewNormalisesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+7", 7*60*60)
	clock := New(epoch.In(zone))

	if got := clock.Now(); !got.Equal(epoch) {
		t.Fatalf("Now = %v, want %v", got, epoch)
	}
	if got := clock.Now().Location(); got != time.UTC {
		t.Fatalf("Location = %v, want UTC", got)
	}
}

func TestSetJumpsToAnAbsoluteTime(t *testing.T) {
	clock := New(epoch)
	zone := time.FixedZone("UTC-5", -5*60*60)
	want := epoch.Add(48 * time.Hour)

	clock.Set(want.In(zone))

	if got := clock.Now(); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
	if got := clock.Now().Location(); got != time.UTC {
		t.Fatalf("Location = %v, want UTC", got)
	}
}

// A negative advance is not an abuse of the API: it is how a test reproduces a
// provider timestamp arriving from the future, which the freshness rules
// deliberately tolerate.
func TestAdvanceMovesInEitherDirection(t *testing.T) {
	cases := map[string]struct {
		by   time.Duration
		want time.Time
	}{
		"forward": {by: 90 * time.Second, want: epoch.Add(90 * time.Second)},
		"nowhere": {by: 0, want: epoch},
		"back":    {by: -30 * time.Second, want: epoch.Add(-30 * time.Second)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clock := New(epoch)
			clock.Advance(tc.by)
			if got := clock.Now(); !got.Equal(tc.want) {
				t.Fatalf("Now = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAdvanceAccumulates(t *testing.T) {
	clock := New(epoch)
	clock.Advance(time.Minute)
	clock.Advance(time.Minute)

	if want := epoch.Add(2 * time.Minute); !clock.Now().Equal(want) {
		t.Fatalf("Now = %v, want %v", clock.Now(), want)
	}
}

// The mutex exists because the code under test reads the clock from a
// background goroutine while the test advances it. Under -race, this is what
// proves it.
func TestNowAndAdvanceAreSafeConcurrently(t *testing.T) {
	clock := New(epoch)
	const readers = 8
	const advances = 200

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if clock.Now().Before(epoch) {
						t.Error("Now went backwards while only advancing forward")
						return
					}
				}
			}
		}()
	}

	for range advances {
		clock.Advance(time.Millisecond)
	}
	close(stop)
	wg.Wait()

	if want := epoch.Add(advances * time.Millisecond); !clock.Now().Equal(want) {
		t.Fatalf("Now = %v, want %v", clock.Now(), want)
	}
}
