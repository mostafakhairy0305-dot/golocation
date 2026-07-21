package chanhub

import (
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
)

func fixAt(lat float64) geo.Fix {
	return geo.Fix{Latitude: lat, Timestamp: time.Now().UTC()}
}

func TestDropOldestKeepsTheNewestValue(t *testing.T) {
	hub := New()
	defer hub.Close()

	_, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	hub.BroadcastFix(fixAt(1))
	hub.BroadcastFix(fixAt(2))
	hub.BroadcastFix(fixAt(3))

	got := <-stream.Locations
	if got.Latitude != 3 {
		t.Fatalf("kept %v, want the newest (3)", got.Latitude)
	}
}

func TestDropNewestKeepsTheQueuedValue(t *testing.T) {
	hub := New()
	defer hub.Close()

	_, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropNewest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

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
	hub := New()
	defer hub.Close()

	priming := fanout.Priming{
		Status: geo.Status{State: geo.StateStarting, Message: "primed"},
		Fix:    fixAt(7),
		HasFix: true,
	}
	_, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 4, DropPolicy: fanout.DropOldest, ReplayLatest: true}, priming)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
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
	hub := New()
	defer hub.Close()

	_, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest, ReplayLatest: true}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	select {
	case fix := <-stream.Locations:
		t.Fatalf("replayed a fix that was never stored: %+v", fix)
	default:
	}
}

func TestWaiterTakesTheFirstEventAndIgnoresTheRest(t *testing.T) {
	hub := New()
	defer hub.Close()

	_, events, err := hub.AddOnce()
	if err != nil {
		t.Fatalf("AddOnce: %v", err)
	}
	hub.BroadcastFix(fixAt(1))
	hub.BroadcastFix(fixAt(2))
	hub.BroadcastError(errors.New("late"))

	event := <-events
	if event.Err != nil || event.Fix.Latitude != 1 {
		t.Fatalf("event = %+v, want the first fix", event)
	}
	select {
	case extra := <-events:
		t.Fatalf("a one-shot waiter delivered twice: %+v", extra)
	default:
	}
}

func TestWaiterReceivesAnError(t *testing.T) {
	hub := New()
	defer hub.Close()

	_, events, err := hub.AddOnce()
	if err != nil {
		t.Fatalf("AddOnce: %v", err)
	}
	want := errors.New("provider failed")
	hub.BroadcastError(want)

	if event := <-events; !errors.Is(event.Err, want) {
		t.Fatalf("event.Err = %v, want %v", event.Err, want)
	}
}

// BroadcastError reaches subscriptions as well as waiters. The waiter half is
// covered above; this pins the half a subscriber actually observes.
func TestBroadcastErrorReachesSubscriptions(t *testing.T) {
	hub := New()
	defer hub.Close()

	_, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := errors.New("provider failed")
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
	hub := New()
	defer hub.Close()

	_, first, err := hub.Add(fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, second, err := hub.Add(fanout.SubscriptionConfig{Buffer: 2, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, events, err := hub.AddOnce()
	if err != nil {
		t.Fatalf("AddOnce: %v", err)
	}

	// Drain the priming status each subscription received at registration.
	<-first.Statuses
	<-second.Statuses

	want := geo.Status{State: geo.StateReady, Message: "receiving locations"}
	hub.BroadcastStatus(want)

	for name, statuses := range map[string]<-chan geo.Status{"first": first.Statuses, "second": second.Statuses} {
		select {
		case got := <-statuses:
			if got.Message != want.Message || got.State != want.State {
				t.Fatalf("%s subscription status = %+v, want %+v", name, got, want)
			}
		default:
			t.Fatalf("the status never reached the %s subscription", name)
		}
	}

	select {
	case event := <-events:
		t.Fatalf("a status woke a one-shot waiter: %+v", event)
	default:
	}
}

func TestDropOldestKeepsTheNewestStatus(t *testing.T) {
	hub := New()
	defer hub.Close()

	_, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
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
	hub := New()
	defer hub.Close()

	id, events, err := hub.AddOnce()
	if err != nil {
		t.Fatalf("AddOnce: %v", err)
	}
	hub.RemoveOnce(id)
	hub.RemoveOnce(id) // idempotent
	hub.BroadcastFix(fixAt(1))

	select {
	case event := <-events:
		t.Fatalf("an unregistered waiter received %+v", event)
	default:
	}
	if _, waiters := hub.Counts(); waiters != 0 {
		t.Fatalf("waiters = %d, want 0", waiters)
	}
}

func TestCloseClosesEveryChannelAndRejectsNewRegistrations(t *testing.T) {
	hub := New()

	id, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, events, err := hub.AddOnce()
	if err != nil {
		t.Fatalf("AddOnce: %v", err)
	}

	hub.Close()
	hub.Close() // idempotent

	for range stream.Locations {
	}
	for range stream.Errors {
	}
	if _, ok := <-events; ok {
		t.Fatal("a waiter channel stayed open after Close")
	}
	select {
	case <-hub.Done():
	default:
		t.Fatal("Done did not close")
	}

	hub.Remove(id) // must not double-close
	if _, _, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1}, fanout.Priming{}); !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("Add after Close = %v, want ErrClosed", err)
	}
	if _, _, err := hub.AddOnce(); !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("AddOnce after Close = %v, want ErrClosed", err)
	}
	if streams, waiters := hub.Counts(); streams != 0 || waiters != 0 {
		t.Fatalf("counts after Close = %d/%d, want 0/0", streams, waiters)
	}
}

func TestRemoveClosesTheStreamAndIsIdempotent(t *testing.T) {
	hub := New()
	defer hub.Close()

	id, stream, err := hub.Add(fanout.SubscriptionConfig{Buffer: 1, DropPolicy: fanout.DropOldest}, fanout.Priming{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	hub.Remove(id)
	hub.Remove(id)

	if _, ok := <-stream.Errors; ok {
		t.Fatal("a removed stream stayed open")
	}
	hub.BroadcastFix(fixAt(1)) // must not send on a closed channel
	if streams, _ := hub.Counts(); streams != 0 {
		t.Fatalf("streams = %d, want 0", streams)
	}
}

func BenchmarkBroadcastFixToWaiters(b *testing.B) {
	hub := New()
	defer hub.Close()
	for range 8 {
		if _, _, err := hub.AddOnce(); err != nil {
			b.Fatalf("AddOnce: %v", err)
		}
	}
	fix := fixAt(1)
	b.ReportAllocs()
	for b.Loop() {
		hub.BroadcastFix(fix)
	}
}
