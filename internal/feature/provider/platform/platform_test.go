package platform

import (
	"testing"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// discardSink satisfies provider.Sink without recording anything: this test
// never starts a provider, so nothing is ever published through it.
type discardSink struct{}

func (discardSink) PublishFix(geo.Fix)       {}
func (discardSink) PublishStatus(geo.Status) {}
func (discardSink) PublishError(error)       {}

// This file carries no build tags on purpose: it runs on every OS, so it may
// only assert what is true everywhere. Which adapter got selected is a build
// decision, but the shape of the result is a contract — a caller that gets
// neither a Provider nor an error has nothing to act on, and one that gets
// both cannot tell whether it succeeded.
func TestNewReturnsExactlyOneOfAProviderOrAnError(t *testing.T) {
	var factory provider.Factory = Factory{}

	// PermissionDoNotRequest keeps this from raising an OS permission prompt
	// on the platforms that have one; construction alone must not ask.
	native, err := factory.New(
		provider.Options{Permission: provider.PermissionDoNotRequest},
		discardSink{},
	)
	if native != nil {
		t.Cleanup(func() { _ = native.Stop() })
	}

	switch {
	case native == nil && err == nil:
		t.Fatal("New returned neither a Provider nor an error")
	case native != nil && err != nil:
		t.Fatalf("New returned both a Provider (%T) and an error (%v)", native, err)
	case native != nil && native.Platform() == "":
		t.Fatal("Platform is empty, so provider errors would carry no attribution")
	}
}
