//go:build darwin && (amd64 || arm64)

package platform

import (
	"errors"
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/corelocation"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// errCoreLocationMissing stands in for the one thing that can go wrong while
// building the darwin provider: CoreLocation not loading.
var errCoreLocationMissing = errors.New("CoreLocation is missing")

// silentHost records nothing and publishes nowhere. The failing case must not
// attach anything, so "was Attach called" is all this test needs to know.
type silentHost struct {
	attached bool `exhaustruct:"optional"`
}

func (*silentHost) PublishFix(geo.Fix)       {}
func (*silentHost) PublishStatus(geo.Status) {}
func (*silentHost) PublishError(error)       {}

func (h *silentHost) Attach(provider.Provider) { h.attached = true }

// A host handed neither a provider nor an error has nothing to act on, so a
// constructor failure has to travel out with the platform named and the host
// left untouched.
func TestNewProviderReportsAConstructorFailure(t *testing.T) {
	t.Parallel()

	host := &silentHost{}
	build := func(corelocation.Options, provider.Sink) (*corelocation.Backend, error) {
		return nil, errCoreLocationMissing
	}

	err := newProviderWith(
		build,
		provider.Options{Permission: provider.PermissionDoNotRequest},
		host,
	)
	if !errors.Is(err, errCoreLocationMissing) {
		t.Fatalf("newProviderWith = %v, want %v", err, errCoreLocationMissing)
	}

	if host.attached {
		t.Fatal("a failed constructor still attached a provider")
	}
}
