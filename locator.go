package location

import (
	"context"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// Locator is the same public API on Windows, macOS, and Linux.
//
// It is declared here, by the package that consumes it, rather than beside the
// implementation: this is the one contract the outside world is on the far end
// of, so it belongs with the façade the outside world calls. Every other
// contract in the module is a driven port and lives with the feature that owns
// it.
type Locator interface {
	// Current returns a sufficiently fresh cached fix, or waits for the next fix.
	Current(ctx context.Context) (geo.Fix, error)
	// Next always waits for a fix accepted after the call starts.
	Next(ctx context.Context) (geo.Fix, error)
	// Last returns the most recent accepted fix without blocking.
	Last() (geo.Fix, bool)
	// Subscribe creates an independent real-time stream.
	Subscribe(ctx context.Context, config SubscriptionConfig) (Subscription, error)
	// Status reports the current service and permission state.
	Status() geo.Status
	// Capabilities reports which optional Fix fields the active provider can supply.
	Capabilities() geo.Capabilities
	// Close stops the provider and closes every subscription. It is idempotent.
	Close() error
}
