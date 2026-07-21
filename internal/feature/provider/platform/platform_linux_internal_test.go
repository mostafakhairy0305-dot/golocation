//go:build linux

package platform

import (
	"errors"
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/geoclue"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// errGeoClueUnavailable stands in for whatever building the GeoClue adapter
// could one day fail at.
var errGeoClueUnavailable = errors.New("GeoClue is unavailable")

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
	build := func(geoclue.Options, provider.Sink) (*geoclue.Backend, error) {
		return nil, errGeoClueUnavailable
	}

	err := newProviderWith(
		build,
		provider.Options{Permission: provider.PermissionDoNotRequest},
		host,
	)
	if !errors.Is(err, errGeoClueUnavailable) {
		t.Fatalf("newProviderWith = %v, want %v", err, errGeoClueUnavailable)
	}

	if host.attached {
		t.Fatal("a failed constructor still attached a provider")
	}
}
