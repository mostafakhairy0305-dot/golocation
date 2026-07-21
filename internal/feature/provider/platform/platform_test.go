package platform_test

import (
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/platform"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// recordingHost satisfies provider.Host without publishing anywhere: this test
// never starts a provider, so nothing is ever produced through it. What it does
// record is the one thing under test — whether a provider was attached.
type recordingHost struct {
	attached provider.Provider `exhaustruct:"optional"`
}

func (*recordingHost) PublishFix(geo.Fix)       {}
func (*recordingHost) PublishStatus(geo.Status) {}
func (*recordingHost) PublishError(error)       {}

func (h *recordingHost) Attach(native provider.Provider) { h.attached = native }

// This file carries no build tags on purpose: it runs on every OS, so it may
// only assert what is true everywhere. Which adapter got selected is a build
// decision, but the shape of the result is a contract — a host left with
// neither a Provider nor an error has nothing to act on, and one given both
// cannot tell whether it succeeded.
func TestNewAttachesExactlyOneProviderOrFails(t *testing.T) {
	t.Parallel()

	host := &recordingHost{}

	// PermissionDoNotRequest keeps this from raising an OS permission prompt
	// on the platforms that have one; construction alone must not ask.
	err := platform.New(
		provider.Options{Permission: provider.PermissionDoNotRequest},
		host,
	)
	if host.attached != nil {
		t.Cleanup(func() { _ = host.attached.Stop() })
	}

	expectExactlyOne(t, host.attached, err)

	if host.attached != nil && host.attached.Platform() == "" {
		t.Fatal("Platform is empty, so provider errors would carry no attribution")
	}
}

// expectExactlyOne fails unless the factory attached a Provider or reported an
// error — never both, and never neither.
func expectExactlyOne(t *testing.T, native provider.Provider, err error) {
	t.Helper()

	if native == nil && err == nil {
		t.Fatal("New attached no Provider and reported no error")
	}

	if native != nil && err != nil {
		t.Fatalf("New attached a Provider (%T) and reported an error (%v)", native, err)
	}
}
