// Package port declares the lifecycle feature's contract: service state — the
// current Status, the permission carried forward across updates, and the
// judgement of whether a change is worth telling subscribers about.
//
// Tracker is the port; ../adapter/atomicstate implements it.
package port

import "github.com/mostafakhairy0305-dot/golocation/geo"

// Tracker records service status. Implementations must be safe for concurrent
// use: a native callback thread reports status changes while callers read them.
type Tracker interface {
	// Set records status and returns it as recorded, together with whether it
	// differs from the previous one. Only a changed status is worth
	// broadcasting, and only the recorded value is worth broadcasting — the
	// tracker fills in fields the reporter left blank.
	Set(status geo.Status) (geo.Status, bool)

	// MarkReady records that fixes are flowing, preserving the rest of the
	// current status. It is separate from Set because becoming ready also
	// settles the permission question: a fix cannot arrive without access,
	// whatever the last authorization callback happened to say.
	MarkReady(message string) (geo.Status, bool)

	// Get returns the current status.
	Get() geo.Status

	// State returns the current state alone. It exists for the fix hot path,
	// where the only question is whether readiness has already been announced
	// and copying a whole Status to find out would be wasteful.
	State() geo.State
}
