// Package chanhub implements fanout.Broadcaster over buffered Go channels.
package chanhub

import (
	"sync"
	"sync/atomic"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
)

type subscriber struct {
	fixes    chan geo.Fix
	errors   chan error
	statuses chan geo.Status
	policy   fanout.DropPolicy
}

// Hub is the subscriber registry. The zero value is not usable; call New.
//
// Subscriptions and waiters are kept in separate maps because they are
// unregistered on different schedules — a subscription when its context ends,
// a waiter the moment its single Next returns — and a broadcast walks both
// without caring which is which.
type Hub struct {
	mu      sync.RWMutex
	subs    map[uint64]*subscriber
	waiters map[uint64]chan fanout.Event
	closed  bool

	nextID atomic.Uint64
	done   chan struct{}
}

var _ fanout.Broadcaster = (*Hub)(nil)

func New() *Hub {
	return &Hub{
		subs:    make(map[uint64]*subscriber),
		waiters: make(map[uint64]chan fanout.Event),
		done:    make(chan struct{}),
	}
}

func (h *Hub) Done() <-chan struct{} { return h.done }

func (h *Hub) Add(
	cfg fanout.SubscriptionConfig,
	priming fanout.Priming,
) (uint64, fanout.Subscription, error) {
	sub := &subscriber{
		fixes:    make(chan geo.Fix, cfg.Buffer),
		errors:   make(chan error, cfg.Buffer),
		statuses: make(chan geo.Status, cfg.Buffer),
		policy:   cfg.DropPolicy,
	}
	id := h.nextID.Add(1)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		sub.close()

		return 0, fanout.Subscription{}, geo.ErrClosed
	}
	// Priming happens under the write lock, on channels nobody else can reach
	// yet, so it cannot block. Doing it after unlocking would let a broadcast
	// racing this registration land first and leave the subscriber holding a
	// newer fix behind an older primed one.
	offer(sub.statuses, priming.Status, sub.policy)

	if cfg.ReplayLatest && priming.HasFix {
		offer(sub.fixes, priming.Fix, sub.policy)
	}

	h.subs[id] = sub
	h.mu.Unlock()

	return id, fanout.Subscription{
		Locations: sub.fixes,
		Errors:    sub.errors,
		Statuses:  sub.statuses,
	}, nil
}

func (h *Hub) Remove(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if sub, ok := h.subs[id]; ok {
		delete(h.subs, id)
		sub.close()
	}
}

func (h *Hub) AddOnce() (uint64, <-chan fanout.Event, error) {
	// Buffer one, so a broadcast can deposit the event and move on whether or
	// not the waiter has reached its receive yet.
	events := make(chan fanout.Event, 1)
	id := h.nextID.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		close(events)

		return 0, events, geo.ErrClosed
	}

	h.waiters[id] = events

	return id, events, nil
}

// RemoveOnce drops a waiter without closing its channel: the waiter is the
// only reader and has already stopped reading, and leaving the channel open
// keeps Close the single place that ever closes one.
func (h *Hub) RemoveOnce(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.waiters, id)
}

// Counts reports how many subscriptions and waiters are registered. It is not
// part of fanout.Broadcaster: nothing in the application needs it, but a
// leaked registration is invisible from the outside and worth asserting on.
func (h *Hub) Counts() (subscriptions, waiters int) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.subs), len(h.waiters)
}

func (h *Hub) BroadcastFix(fix geo.Fix) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		offer(sub.fixes, fix, sub.policy)
	}

	for _, events := range h.waiters {
		wake(events, fanout.Event{Fix: fix})
	}
}

// BroadcastError reaches waiters as well as subscriptions: a caller in Next
// wants to hear that the provider failed, not wait out its context for a fix
// that is not coming.
func (h *Hub) BroadcastError(err error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		offer(sub.errors, err, sub.policy)
	}

	for _, events := range h.waiters {
		wake(events, fanout.Event{Err: err})
	}
}

func (h *Hub) BroadcastStatus(status geo.Status) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		offer(sub.statuses, status, sub.policy)
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.closed = true
	close(h.done)

	for id, sub := range h.subs {
		delete(h.subs, id)
		sub.close()
	}

	for id, events := range h.waiters {
		delete(h.waiters, id)
		close(events)
	}
}

func (s *subscriber) close() {
	close(s.fixes)
	close(s.errors)
	close(s.statuses)
}

// offer delivers value without ever blocking the caller, because the caller
// may be an operating system callback thread that must return promptly.
func offer[T any](ch chan T, value T, policy fanout.DropPolicy) {
	select {
	case ch <- value:
		return
	default:
	}

	if policy == fanout.DropNewest {
		return
	}
	// Make room by discarding the queued oldest value, then retry. Both steps
	// stay non-blocking: the subscriber may drain or refill the channel in
	// between, and either outcome is acceptable — one value is lost either
	// way, which is exactly what a drop policy is for.
	select {
	case <-ch:
	default:
	}

	select {
	case ch <- value:
	default:
	}
}

// wake delivers to a one-shot waiter. A full channel means the waiter already
// has an event it has not read yet, and one is all it will ever use.
func wake(events chan fanout.Event, event fanout.Event) {
	select {
	case events <- event:
	default:
	}
}
