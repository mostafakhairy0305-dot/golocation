// Package unsupported is the adapter used where no native provider exists.
// It stands in for what would otherwise be three near-identical per-platform
// stubs; the build-tagged selector supplies the platform name.
package unsupported

import (
	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// New reports that the platform has no location provider. It returns nothing
// but the refusal: there is no provider to attach and nothing to start, so the
// failure belongs at Open rather than at the first call.
func New(platform string) error {
	return geo.Wrap(platform, "open", geo.ErrUnsupported, false)
}
