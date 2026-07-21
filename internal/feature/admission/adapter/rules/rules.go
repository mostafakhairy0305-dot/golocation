// Package rules implements admission.Gate with the interval, distance, and
// staleness thresholds the caller configured.
package rules

import (
	"sync"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	admission "github.com/mostafakhairy0305-dot/golocation/internal/feature/admission/port"
)

// Gate is the default admission.Gate. It is safe for concurrent use.
type Gate struct {
	rules admission.Rules

	mu           sync.Mutex
	last         geo.Fix
	hasPublished bool
}

var _ admission.Gate = (*Gate)(nil)

func New(rules admission.Rules) *Gate { return &Gate{rules: rules} }

// Admit applies the checks cheapest-first, and holds the lock only for the two
// that compare against the previous fix. Validation and staleness depend on
// nothing but the sample itself, so a malformed or stale fix — the common case
// when a provider is struggling — never contends for the lock at all.
func (g *Gate) Admit(fix geo.Fix, now time.Time) (bool, error) {
	if err := geo.Validate(fix); err != nil {
		return false, err
	}
	if g.rules.MaximumAge > 0 && !geo.IsFresh(fix, g.rules.MaximumAge, now) {
		return false, geo.ErrStaleFix
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.hasPublished {
		if g.rules.MinimumInterval > 0 && fix.ReceivedAt.Sub(g.last.ReceivedAt) < g.rules.MinimumInterval {
			return false, nil
		}
		if g.rules.MinimumDistanceMeters > 0 && geo.Distance(g.last, fix) < g.rules.MinimumDistanceMeters {
			return false, nil
		}
	}

	g.last = fix
	g.hasPublished = true
	return true, nil
}
