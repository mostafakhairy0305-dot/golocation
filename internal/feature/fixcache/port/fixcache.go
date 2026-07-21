// Package port declares the fix-cache feature's contract: the most recent
// admitted fix — the value Last returns outright, Current serves while it is
// still fresh, and a replaying subscription is primed with.
//
// Cache is the port; ../adapter/atomiccache implements it.
package port

import "github.com/mostafakhairy0305-dot/golocation/geo"

// Cache holds the newest admitted fix. Implementations must be safe for
// concurrent use, and must not make a reader wait on a writer: Last is
// documented as non-blocking, and every subscriber may call it at once.
type Cache interface {
	Store(fix geo.Fix)
	// Load reports the newest stored fix, or false when nothing has been
	// stored yet.
	Load() (geo.Fix, bool)
}
