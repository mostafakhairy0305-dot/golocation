// Package rules implements admission.Gate with the interval, distance, and
// staleness thresholds the caller configured.
package rules

import (
	"fmt"
	"sync"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	admission "github.com/mostafakhairy0305-dot/golocation/internal/feature/admission/port"
)

// Gate is the default admission.Gate. It is safe for concurrent use.
type Gate struct {
	rules admission.Rules

	// The zero values are the usable ones: no lock held, and nothing admitted
	// yet.
	mu           sync.Mutex `exhaustruct:"optional"`
	last         geo.Fix    `exhaustruct:"optional"`
	hasPublished bool       `exhaustruct:"optional"`
}

var _ admission.Gate = (*Gate)(nil)

// New builds a Gate enforcing rules. A zero field disables its check.
func New(rules admission.Rules) *Gate { return &Gate{rules: rules} }

// Admit applies the checks cheapest-first, and holds the lock only for the two
// that compare against the previous fix. Validation and staleness depend on
// nothing but the sample itself, so a malformed or stale fix — the common case
// when a provider is struggling — never contends for the lock at all.
func (g *Gate) Admit(fix geo.Fix, now time.Time) (bool, error) {
	err := geo.Validate(fix)
	if err != nil {
		return false, fmt.Errorf("admit fix: %w", err)
	}

	if g.stale(fix, now) {
		return false, geo.ErrStaleFix
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.hasPublished && g.redundant(fix) {
		return false, nil
	}

	g.last = fix
	g.hasPublished = true

	return true, nil
}

// stale reports whether the sample is older than the configured maximum age.
// A zero maximum disables the check.
func (g *Gate) stale(fix geo.Fix, now time.Time) bool {
	return g.rules.MaximumAge > 0 && !geo.IsFresh(fix, g.rules.MaximumAge, now)
}

// redundant reports whether the sample arrived too soon after, or too close
// to, the last admitted one. Callers hold the lock: it reads g.last.
func (g *Gate) redundant(fix geo.Fix) bool {
	if g.rules.MinimumInterval > 0 &&
		fix.ReceivedAt.Sub(g.last.ReceivedAt) < g.rules.MinimumInterval {
		return true
	}

	if g.rules.MinimumDistanceMeters > 0 &&
		geo.Distance(g.last, fix) < g.rules.MinimumDistanceMeters {
		return true
	}

	return false
}
