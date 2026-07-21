// Package atomicstate implements lifecycle.Tracker with lock-free reads over
// a mutex-guarded update.
package atomicstate

import (
	"sync"
	"sync/atomic"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	clock "github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/port"
	lifecycle "github.com/mostafakhairy0305-dot/golocation/internal/feature/lifecycle/port"
)

// Tracker is the default lifecycle.Tracker.
//
// The read and write paths are wildly asymmetric: status changes a handful of
// times in a session, while Status can be called from every subscriber at
// once. So readers take no lock at all — Get is one atomic pointer load, State
// one atomic word load — and the mutex serializes only the read-modify-write
// that updating requires, where contention is nil.
//
// State is kept separately from the Status it belongs to because the fix hot
// path asks for it and nothing else, and one word beats a pointer chase plus a
// struct copy. It is written under the same lock, so it never disagrees with
// the Status it was taken from.
type Tracker struct {
	clock  clock.Clock
	state  atomic.Uint32
	status atomic.Pointer[geo.Status]

	mu sync.Mutex
}

var _ lifecycle.Tracker = (*Tracker)(nil)

func New(initial geo.Status, clock clock.Clock) *Tracker {
	if initial.UpdatedAt.IsZero() {
		initial.UpdatedAt = clock.Now()
	}

	tracker := &Tracker{clock: clock}
	tracker.state.Store(uint32(initial.State))
	tracker.status.Store(&initial)

	return tracker
}

// State narrows the stored word back to geo.State's uint8. Every write goes
// through store, which only ever widens a geo.State, so the mask discards
// nothing — it just says so in a form the compiler and the linters can see.
func (t *Tracker) State() geo.State { return geo.State(t.state.Load() & 0xFF) }

func (t *Tracker) Get() geo.Status { return *t.status.Load() }

func (t *Tracker) Set(status geo.Status) (geo.Status, bool) {
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = t.clock.Now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// A backend reporting only a state change leaves permission unknown. It
	// inherits what we already learned rather than erasing it.
	if status.Permission == geo.PermissionUnknown {
		status.Permission = t.status.Load().Permission
	}

	return t.store(status)
}

func (t *Tracker) MarkReady(message string) (geo.Status, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	next := *t.status.Load()
	next.State = geo.StateReady
	next.Message = message

	next.UpdatedAt = t.clock.Now()
	switch next.Permission {
	case geo.PermissionUnknown, geo.PermissionPromptRequired:
		next.Permission = geo.PermissionGranted
	}

	return t.store(next)
}

// store commits next and reports whether it says anything new. It must be
// called with the mutex held: the compare-then-publish is what the mutex is
// for, and two concurrent updates could otherwise both see themselves as the
// change. UpdatedAt is excluded from the comparison on purpose — a restated
// status is not a change, however recently it was restated.
func (t *Tracker) store(next geo.Status) (geo.Status, bool) {
	previous := t.status.Load()
	changed := previous.State != next.State ||
		previous.Permission != next.Permission ||
		previous.Message != next.Message

	// The two stores are not one atomic step, so a reader can catch the pair
	// mid-update and see a State from one version with a Status from another.
	// Both orderings of that skew are harmless: the only caller of State uses
	// it to decide whether to bother calling MarkReady, and MarkReady rechecks
	// under the lock and reports changed=false when there is nothing new.
	t.state.Store(uint32(next.State))
	t.status.Store(&next)

	return next, changed
}
