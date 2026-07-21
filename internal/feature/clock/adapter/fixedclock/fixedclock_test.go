package fixedclock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/adapter/fixedclock"
)

// testConfig holds the fixed values this package's tests share. A function
// returns it rather than a package-level variable holding it, so the package
// carries no global state; the value is constant, so every call is equal.
type testConfig struct {
	epoch time.Time
}

func config() *testConfig {
	return &testConfig{epoch: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
}

// Every consumer compares against UTC timestamps the core produced, so a
// clock handed a wall time in some other zone must normalise rather than make
// the caller's Equal comparisons depend on the test machine's locale.
func TestNewNormalisesToUTC(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	zone := time.FixedZone("UTC+7", 7*60*60)
	clock := fixedclock.New(epoch.In(zone))

	if got := clock.Now(); !got.Equal(epoch) {
		t.Fatalf("Now = %v, want %v", got, epoch)
	}

	if got := clock.Now().Location(); got != time.UTC {
		t.Fatalf("Location = %v, want UTC", got)
	}
}

func TestSetJumpsToAnAbsoluteTime(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	clock := fixedclock.New(epoch)
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
	t.Parallel()

	epoch := config().epoch

	cases := map[string]struct {
		by   time.Duration
		want time.Time
	}{
		"forward": {by: 90 * time.Second, want: epoch.Add(90 * time.Second)},
		"nowhere": {by: 0, want: epoch},
		"back":    {by: -30 * time.Second, want: epoch.Add(-30 * time.Second)},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clock := fixedclock.New(epoch)
			clock.Advance(testCase.by)

			if got := clock.Now(); !got.Equal(testCase.want) {
				t.Fatalf("Now = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestAdvanceAccumulates(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	clock := fixedclock.New(epoch)
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
	t.Parallel()

	epoch := config().epoch
	clock := fixedclock.New(epoch)

	const (
		readers  = 8
		advances = 200
	)

	var readersDone sync.WaitGroup

	stop := make(chan struct{})

	for range readers {
		readersDone.Go(func() { readUntilStopped(t, clock, epoch, stop) })
	}

	for range advances {
		clock.Advance(time.Millisecond)
	}

	close(stop)
	readersDone.Wait()

	if want := epoch.Add(advances * time.Millisecond); !clock.Now().Equal(want) {
		t.Fatalf("Now = %v, want %v", clock.Now(), want)
	}
}

// readUntilStopped hammers Now until stop closes, failing if the clock is ever
// seen behind the epoch it started at. It reports through t.Error rather than
// t.Fatal because it runs off the test's goroutine.
func readUntilStopped(
	t *testing.T,
	clock *fixedclock.Clock,
	epoch time.Time,
	stop <-chan struct{},
) {
	t.Helper()

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
}
