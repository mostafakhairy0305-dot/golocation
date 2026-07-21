// Package port declares the fan-out feature's contract: delivery to waiting
// callers. That covers the shape of a subscription, the registry of live ones,
// the one-shot waiters a single Next call needs, and the drop policy applied
// when a subscriber cannot keep up.
//
// Broadcaster is the port; ../adapter/chanhub implements it over Go channels.
package port

import "github.com/mostafakhairy0305-dot/golocation/geo"

// DropPolicy defines subscriber behavior when its channel is full.
type DropPolicy uint8

const (
	// DropDefault inherits the locator's configured default policy.
	DropDefault DropPolicy = iota
	// DropOldest discards the queued old value and retains the newest value.
	DropOldest
	// DropNewest retains queued values and discards the incoming value.
	DropNewest
)

// SubscriptionConfig configures one independent real-time subscription.
type SubscriptionConfig struct {
	Buffer     int
	DropPolicy DropPolicy
	// ReplayLatest immediately sends the most recent accepted fix, when present.
	ReplayLatest bool
}

// Subscription contains independent channels for fixes, native errors, and
// status changes. All channels close when the subscription ends or the
// Broadcaster closes.
//
// This is the type callers receive, which is why it lives here rather than
// being mapped into one: the shape of a subscription is a fan-out decision,
// and a second near-identical struct elsewhere would only be able to drift.
type Subscription struct {
	Locations <-chan geo.Fix
	Errors    <-chan error
	Statuses  <-chan geo.Status
}

// Priming is what a new subscription receives before it sees any broadcast, so
// a subscriber learns the current state without waiting for it to change.
type Priming struct {
	Status geo.Status
	Fix    geo.Fix
	HasFix bool
}

// Event is what a one-shot waiter receives: the first fix admitted after it
// registered, or the first error broadcast in the meantime — whichever comes
// first.
type Event struct {
	Fix geo.Fix
	Err error
}

// Broadcaster is the subscriber registry. Every Broadcast call must be safe on
// a native callback thread, which means none of them may block on a slow
// subscriber.
type Broadcaster interface {
	// Add registers a subscription and primes it. cfg must already be
	// normalized: Buffer >= 1 and DropPolicy resolved to a concrete policy.
	// The returned id identifies the subscription for Remove.
	Add(cfg SubscriptionConfig, priming Priming) (uint64, Subscription, error)
	// Remove unregisters a subscription and closes its channels. It is a no-op
	// for an unknown id, so it is safe to call more than once.
	Remove(id uint64)

	// AddOnce registers a waiter for a single Event. It costs one channel and
	// no goroutine, which is why Next uses it instead of a whole subscription.
	// The channel closes if the Broadcaster closes first.
	AddOnce() (uint64, <-chan Event, error)
	// RemoveOnce unregisters a waiter. The waiter itself must call it, which
	// is what keeps a one-shot free of a supervising goroutine.
	RemoveOnce(id uint64)

	BroadcastFix(geo.Fix)
	BroadcastError(error)
	BroadcastStatus(geo.Status)

	// Done closes when the Broadcaster closes. Per-subscription goroutines
	// select on it so none of them outlive shutdown.
	Done() <-chan struct{}
	// Close unregisters everything and closes every channel it handed out.
	// Subsequent Add and AddOnce calls return geo.ErrClosed. Close is
	// idempotent.
	Close()
}
