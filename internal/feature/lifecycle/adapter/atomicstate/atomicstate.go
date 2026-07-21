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
	clock clock.Clock
	// New stores through these rather than initializing them in the literal.
	state  atomic.Uint32              `exhaustruct:"optional"`
	status atomic.Pointer[geo.Status] `exhaustruct:"optional"`

	mu sync.Mutex `exhaustruct:"optional"`
}

var _ lifecycle.Tracker = (*Tracker)(nil)

// New builds a Tracker holding initial, stamping it from clock when it carries
// no time of its own.
func New(initial geo.Status, clock clock.Clock) *Tracker {
	if initial.UpdatedAt.IsZero() {
		initial.UpdatedAt = clock.Now()
	}

	tracker := &Tracker{clock: clock}
	tracker.state.Store(uint32(initial.State))
	tracker.status.Store(&initial)

	return tracker
}

// stateMask is geo.State's width in the word State stores it in.
const stateMask = 0xFF

// State narrows the stored word back to geo.State's uint8. Every write goes
// through store, which only ever widens a geo.State, so the mask discards
// nothing — it just says so in a form the compiler and the linters can see.
func (t *Tracker) State() geo.State { return geo.State(t.state.Load() & stateMask) }

// Get returns the current status.
func (t *Tracker) Get() geo.Status { return *t.status.Load() }

// Set records status and returns what was stored alongside whether it says
// anything new. An unset time is stamped from the clock, and an unknown
// permission inherits whatever the platform last reported.
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

// MarkReady moves the tracker to geo.StateReady with message, and returns the
// stored status alongside whether it says anything new. Reaching ready proves
// access was granted, so an as-yet-unknown permission is resolved here.
func (t *Tracker) MarkReady(message string) (geo.Status, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	next := *t.status.Load()
	next.State = geo.StateReady
	next.Message = message

	next.UpdatedAt = t.clock.Now()
	// Readiness proves the platform let us through, so a permission we never
	// learned is now known to be granted. One the platform already decided —
	// granted, denied, restricted — is left exactly as reported.
	switch next.Permission {
	case geo.PermissionUnknown, geo.PermissionPromptRequired:
		next.Permission = geo.PermissionGranted
	case geo.PermissionGranted, geo.PermissionDenied, geo.PermissionRestricted:
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
