package location

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// errStopFailed is the teardown failure a fake provider reports when a test
// wants Close to carry an error back out of the public API.
var errStopFailed = errors.New("stop failed")

// openSession is the whole public entry point in one line: every Session
// method below needs a live locator wired to a fake operating system, and the
// provider comes back so a test can publish through its sink.
func openSession(t *testing.T) (*Session, *fakeProvider) {
	t.Helper()

	native := &fakeProvider{}

	session, err := open(context.Background(), Config{}, factoryFor(native, nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = session.Close() })

	return session, native
}

// A fix published while Current is not waiting still satisfies it: the cache is
// what makes Current cheap, and DefaultConfig's maximum age is minutes wide.
func TestSessionCurrentServesTheCachedFix(t *testing.T) {
	t.Parallel()

	session, native := openSession(t)

	native.sink.PublishFix(geo.Fix{Latitude: 51.5, Longitude: -0.12, AccuracyMeters: 10})

	fix, err := session.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	if fix.Latitude != 51.5 {
		t.Fatalf("Current = %+v, want the published fix", fix)
	}
}

func TestSessionCurrentReportsAClosedSession(t *testing.T) {
	t.Parallel()

	session, _ := openSession(t)

	err := session.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = session.Current(context.Background())
	if !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("Current after Close = %v, want ErrClosed", err)
	}
}

// Next ignores the cache, so the fix it returns has to be published after the
// call is already waiting.
func TestSessionNextWaitsForAFreshFix(t *testing.T) {
	t.Parallel()

	session, native := openSession(t)

	// The cached fix Next must not settle for.
	native.sink.PublishFix(geo.Fix{Latitude: 1, Longitude: 1, AccuracyMeters: 10})

	done := make(chan struct{})
	defer close(done)

	go publishUntil(done, native)

	fix, err := session.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	if fix.Latitude != 51.5 {
		t.Fatalf("Next = %+v, want the fix published after the call", fix)
	}
}

// publishUntil republishes the same fix until done closes. Publishing once
// would be a race: the hub drops a fix that lands before the waiter is
// registered, and repeating is harmless because Next takes the first one it
// sees and ignores the rest.
func publishUntil(done <-chan struct{}, native *fakeProvider) {
	for {
		select {
		case <-done:
			return
		default:
			native.sink.PublishFix(
				geo.Fix{Latitude: 51.5, Longitude: -0.12, AccuracyMeters: 10},
			)
			time.Sleep(time.Millisecond)
		}
	}
}

func TestSessionNextReportsACancelledContext(t *testing.T) {
	t.Parallel()

	session, _ := openSession(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := session.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Next with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestSessionSubscribeStreamsFixes(t *testing.T) {
	t.Parallel()

	session, native := openSession(t)

	subscription, err := session.Subscribe(t.Context(), SubscriptionConfig{Buffer: 4})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	native.sink.PublishFix(geo.Fix{Latitude: 51.5, Longitude: -0.12, AccuracyMeters: 10})

	fix := <-subscription.Locations
	if fix.Latitude != 51.5 {
		t.Fatalf("subscription delivered %+v, want the published fix", fix)
	}
}

func TestSessionSubscribeRejectsAnInvalidConfig(t *testing.T) {
	t.Parallel()

	session, _ := openSession(t)

	_, err := session.Subscribe(context.Background(), SubscriptionConfig{Buffer: -1})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Subscribe with a negative buffer = %v, want ErrInvalidConfig", err)
	}
}

// Close reports what the provider said on the way down rather than swallowing
// it, so a caller can log a stuck native service.
func TestSessionCloseReportsAFailedProviderStop(t *testing.T) {
	t.Parallel()

	native := &fakeProvider{stopErr: errStopFailed}

	session, err := open(context.Background(), Config{}, factoryFor(native, nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	err = session.Close()
	if !errors.Is(err, errStopFailed) {
		t.Fatalf("Close = %v, want the provider's stop error", err)
	}

	// The teardown runs once, but its outcome is cached: a second Close reports
	// the same stop error rather than swallowing it, so a caller that only
	// deferred Close still learns the native service went down badly.
	err = session.Close()
	if !errors.Is(err, errStopFailed) {
		t.Fatalf("second Close = %v, want the same stop error", err)
	}
}
