// Package port declares the admission feature's contract: whether a provider
// sample is worth publishing at all, which is the validation, staleness, rate,
// and distance checks a fix must pass before it reaches a subscriber.
//
// Gate is the port; ../adapter/rules implements it against configured
// thresholds. Importers alias this package to the feature name — admission —
// because six features each declare a package port and only the alias tells
// them apart at the call site.
package port

import (
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Rules are the admission thresholds. A zero value disables the corresponding
// check.
type Rules struct {
	MinimumInterval       time.Duration `exhaustruct:"optional"`
	MinimumDistanceMeters float64       `exhaustruct:"optional"`
	MaximumAge            time.Duration `exhaustruct:"optional"`
}

// Gate decides which provider samples reach subscribers. Implementations must
// be safe for concurrent use: a native callback thread can call Admit at any
// time, from any goroutine.
type Gate interface {
	// Admit reports whether a fix should be published, and records it as the
	// new baseline when it is.
	//
	// A false result with a nil error means the fix was correct but redundant
	// — it arrived too soon after the last one, or too close to it — which is
	// a routine outcome, not a failure. A non-nil error means the fix was
	// rejected as unusable, and the caller should surface it to subscribers.
	//
	// The error carries the cause only. Annotating it with the platform is the
	// caller's job, because only the caller knows which adapter produced the
	// fix.
	Admit(fix geo.Fix, now time.Time) (bool, error)
}
