// Package port declares the clock feature's contract: the reading of the
// current time. Wall-clock time is an outside dependency like any other —
// freshness, admission, and every status timestamp are decided against it — so
// it is reached through a port rather than by calling time.Now wherever the
// answer happens to be needed.
//
// Clock is the port; ../adapter/systemclock is the real one, and
// ../adapter/fixedclock is the one a test drives by hand.
package port

import "time"

// Clock reports the current time. Implementations must be safe for concurrent
// use.
type Clock interface {
	// Now returns the current time in UTC, so nothing downstream has to
	// normalize it before comparing or recording.
	Now() time.Time
}
