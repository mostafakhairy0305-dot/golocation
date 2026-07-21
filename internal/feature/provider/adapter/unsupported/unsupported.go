// Package unsupported is the adapter used where no native provider exists.
// It stands in for what would otherwise be three near-identical per-platform
// stubs; the build-tagged selector supplies the platform name.
package unsupported

import (
	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// New reports that the platform has no location provider. It never returns a
// Provider: there is nothing to start, so the failure belongs at Open rather
// than at the first call.
func New(platform string) (provider.Provider, error) {
	return nil, geo.Wrap(platform, "open", geo.ErrUnsupported, false)
}
