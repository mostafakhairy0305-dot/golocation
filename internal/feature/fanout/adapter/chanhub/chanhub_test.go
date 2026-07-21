package chanhub_test

import (
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/adapter/chanhub"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
)

func fixAt(lat float64) geo.Fix {
	return geo.Fix{Latitude: lat, Timestamp: time.Now().UTC()}
}

// Failures the tests broadcast. Values rather than literals so an assertion
// can name the exact error it expects to come back out.
var (
	errLate           = errors.New("late")
	errProviderFailed = errors.New("provider failed")
)

// addStream registers a subscription the Hub is expected to accept. Every test
// below needs one before it can assert anything, and a refusal is never the
// thing under test — so it ends the test rather than returning an error.
func addStream(
	t *testing.T,
	hub *chanhub.Hub,
	cfg fanout.SubscriptionConfig,
	priming fanout.Priming,
) (uint64, fanout.Subscription) {
	t.Helper()

	id, stream, err := hub.Add(cfg, priming)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	return id, stream
}

// addWaiter registers a one-shot waiter the Hub is expected to accept.
func addWaiter(t *testing.T, hub *chanhub.Hub) (uint64, <-chan fanout.Event) {
	t.Helper()

	id, events, err := hub.AddOnce()
	if err != nil {
		t.Fatalf("AddOnce: %v", err)
	}

	return id, events
}

// expectStatus fails unless the named subscription already has want waiting.
func expectStatus(
	t *testing.T,
	name string,
	statuses <-chan geo.Status,
	want geo.Status,
) {
	t.Helper()

	select {
	case got := <-statuses:
		if got.Message != want.Message || got.State != want.State {
			t.Fatalf("%s subscription status = %+v, want %+v", name, got, want)
		}
	default:
		t.Fatalf("the status never reached the %s subscription", name)
	}
}

// expectNoEvent fails if the waiter was woken at all.
func expectNoEvent(t *testing.T, why string, events <-chan fanout.Event) {
	t.Helper()

	select {
	case event := <-events:
		t.Fatalf("%s: %+v", why, event)
	default:
	}
}

// expectClosed fails unless the channel is closed rather than merely empty.
func expectClosed[T any](t *testing.T, name string, ch <-chan T) {
	t.Helper()

	if _, ok := <-ch; ok {
		t.Fatalf("the %s channel stayed open after Close", name)
	}
}

// expectDone fails unless the Hub has signalled shutdown.
func expectDone(t *testing.T, hub *chanhub.Hub) {
	t.Helper()

	select {
	case <-hub.Done():
	default:
		t.Fatal("Done did not close")
	}
}

// expectClosedErr fails unless err reports a closed Hub.
func expectClosedErr(t *testing.T, op string, err error) {
	t.Helper()

	if !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("%s after Close = %v, want ErrClosed", op, err)
	}
}

func TestDropOldestKeepsTheNewestValue(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest},
		fanout.Priming{},
	)

	hub.BroadcastFix(fixAt(1))
	hub.BroadcastFix(fixAt(2))
	hub.BroadcastFix(fixAt(3))

	got := <-stream.Locations
	if got.Latitude != 3 {
		t.Fatalf("kept %v, want the newest (3)", got.Latitude)
	}
}

func TestDropNewestKeepsTheQueuedValue(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropNewest},
		fanout.Priming{},
	)

	hub.BroadcastFix(fixAt(1))
	hub.BroadcastFix(fixAt(2))
	hub.BroadcastFix(fixAt(3))

	got := <-stream.Locations
	if got.Latitude != 1 {
		t.Fatalf("kept %v, want the queued (1)", got.Latitude)
	}
}

// Priming is applied while the registration lock is held so that a broadcast
// racing Add can only land after it, never in front of it.
func TestPrimingArrivesBeforeAnyBroadcast(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	priming := fanout.Priming{
		Status: geo.Status{State: geo.StateStarting, Message: "primed"},
		Fix:    fixAt(7),
		HasFix: true,
	}

	_, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 4, DropPolicy: fanout.DropOldest, ReplayLatest: true},
		priming,
	)

	hub.BroadcastFix(fixAt(8))

	if first := <-stream.Locations; first.Latitude != 7 {
		t.Fatalf("first fix = %v, want the primed 7", first.Latitude)
	}

	if second := <-stream.Locations; second.Latitude != 8 {
		t.Fatalf("second fix = %v, want the broadcast 8", second.Latitude)
	}

	if status := <-stream.Statuses; status.Message != "primed" {
		t.Fatalf("status = %q, want the primed one", status.Message)
	}
}

func TestReplayLatestIsIgnoredWithoutAFix(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest, ReplayLatest: true},
		fanout.Priming{},
	)

	select {
	case fix := <-stream.Locations:
		t.Fatalf("replayed a fix that was never stored: %+v", fix)
	default:
	}
}

func TestWaiterTakesTheFirstEventAndIgnoresTheRest(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, events := addWaiter(t, hub)

	hub.BroadcastFix(fixAt(1))
	hub.BroadcastFix(fixAt(2))
	hub.BroadcastError(errLate)

	event := <-events
	if event.Err != nil || event.Fix.Latitude != 1 {
		t.Fatalf("event = %+v, want the first fix", event)
	}

	expectNoEvent(t, "a one-shot waiter delivered twice", events)
}

func TestWaiterReceivesAnError(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, events := addWaiter(t, hub)

	want := errProviderFailed
	hub.BroadcastError(want)

	if event := <-events; !errors.Is(event.Err, want) {
		t.Fatalf("event.Err = %v, want %v", event.Err, want)
	}
}

// BroadcastError reaches subscriptions as well as waiters. The waiter half is
// covered above; this pins the half a subscriber actually observes.
func TestBroadcastErrorReachesSubscriptions(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest},
		fanout.Priming{},
	)

	want := errProviderFailed
	hub.BroadcastError(want)

	select {
	case got := <-stream.Errors:
		if !errors.Is(got, want) {
			t.Fatalf("error = %v, want %v", got, want)
		}
	default:
		t.Fatal("the error never reached the subscription")
	}
}

// A status must not wake a caller blocked in Next: Next promises a fix or a
// failure, and "still starting" is neither. That asymmetry against
// BroadcastError is the behaviour worth pinning here.
func TestBroadcastStatusReachesSubscriptionsButNeverWaiters(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	cfg := fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest}
	_, first := addStream(t, hub, cfg, fanout.Priming{})
	_, second := addStream(t, hub, cfg, fanout.Priming{})
	_, events := addWaiter(t, hub)

	// Drain the priming status each subscription received at registration.
	<-first.Statuses
	<-second.Statuses

	want := geo.Status{State: geo.StateReady, Message: "receiving locations"}
	hub.BroadcastStatus(want)

	expectStatus(t, "first", first.Statuses, want)
	expectStatus(t, "second", second.Statuses, want)
	expectNoEvent(t, "a status woke a one-shot waiter", events)
}

func TestDropOldestKeepsTheNewestStatus(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	_, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest},
		fanout.Priming{},
	)
	// The priming status already fills the single slot, so every broadcast
	// below walks offer's discard branch.
	hub.BroadcastStatus(geo.Status{State: geo.StateStarting, Message: "one"})
	hub.BroadcastStatus(geo.Status{State: geo.StateReconnecting, Message: "two"})
	hub.BroadcastStatus(geo.Status{State: geo.StateReady, Message: "three"})

	if got := <-stream.Statuses; got.Message != "three" {
		t.Fatalf("kept %q, want the newest (%q)", got.Message, "three")
	}
}

func TestRemoveOnceStopsDelivery(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	id, events := addWaiter(t, hub)

	hub.RemoveOnce(id)
	hub.RemoveOnce(id) // idempotent
	hub.BroadcastFix(fixAt(1))

	expectNoEvent(t, "an unregistered waiter received an event", events)

	if _, waiters := hub.Counts(); waiters != 0 {
		t.Fatalf("waiters = %d, want 0", waiters)
	}
}

func TestCloseClosesEveryChannelAndRejectsNewRegistrations(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()

	id, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest},
		fanout.Priming{},
	)
	_, events := addWaiter(t, hub)

	hub.Close()
	hub.Close() // idempotent

	// Neither channel was primed, so a receive that yields nothing is proof
	// Close closed it rather than proof it is merely empty.
	expectClosed(t, "fix", stream.Locations)
	expectClosed(t, "error", stream.Errors)
	expectClosed(t, "waiter", events)
	expectDone(t, hub)

	hub.Remove(id) // must not double-close

	_, _, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1}, fanout.Priming{})
	expectClosedErr(t, "Add", err)

	_, _, err = hub.AddOnce()
	expectClosedErr(t, "AddOnce", err)

	if streams, waiters := hub.Counts(); streams != 0 || waiters != 0 {
		t.Fatalf("counts after Close = %d/%d, want 0/0", streams, waiters)
	}
}

func TestRemoveClosesTheStreamAndIsIdempotent(t *testing.T) {
	t.Parallel()

	hub := chanhub.New()
	defer hub.Close()

	id, stream := addStream(
		t, hub,
		fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest},
		fanout.Priming{},
	)

	hub.Remove(id)
	hub.Remove(id)

	expectClosed(t, "error", stream.Errors)

	hub.BroadcastFix(fixAt(1)) // must not send on a closed channel

	if streams, _ := hub.Counts(); streams != 0 {
		t.Fatalf("streams = %d, want 0", streams)
	}
}

func BenchmarkBroadcastFixToWaiters(b *testing.B) {
	hub := chanhub.New()
	defer hub.Close()

	for range 8 {
		_, _, err := hub.AddOnce()
		if err != nil {
			b.Fatalf("AddOnce: %v", err)
		}
	}

	fix := fixAt(1)

	b.ReportAllocs()

	for b.Loop() {
		hub.BroadcastFix(fix)
	}
}
