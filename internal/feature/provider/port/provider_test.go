package port_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// errFactoryFailed is what the stub factory returns when a test asks it to
// fail, so the assertion can name the exact error it expects back.
var errFactoryFailed = errors.New("factory failed")

// stubProvider is only ever compared by identity: the point of the tests below
// is that a Factory attaches exactly the Provider it built.
type stubProvider struct{}

func (stubProvider) Start(context.Context) error    { return nil }
func (stubProvider) Stop() error                    { return nil }
func (stubProvider) Capabilities() geo.Capabilities { return geo.Capabilities{} }
func (stubProvider) Platform() string               { return "stub" }

// stubHost records what a Factory attached to it, standing in for the core.
type stubHost struct {
	attached provider.Provider `exhaustruct:"optional"`
}

func (*stubHost) PublishFix(geo.Fix)       {}
func (*stubHost) PublishStatus(geo.Status) {}
func (*stubHost) PublishError(error)       {}

func (h *stubHost) Attach(native provider.Provider) { h.attached = native }

var _ provider.Host = (*stubHost)(nil)

// A Factory is the seam the composition root reaches the operating system
// through, and the failure it could hide is silent: dropping the Options or
// attaching nothing would leave a locator that starts but never publishes
// anywhere the caller can see.
func TestAFactoryReachesItsHostWithBothArguments(t *testing.T) {
	t.Parallel()

	wantOpts := provider.Options{
		Accuracy:              provider.AccuracyNavigation,
		DesiredAccuracyMeters: 5,
		MinimumInterval:       2 * time.Second,
		MinimumDistanceMeters: 25,
		MaximumAge:            time.Minute,
		StartTimeout:          10 * time.Second,
		Permission:            provider.PermissionDoNotRequest,
		Linux: provider.LinuxOptions{
			DesktopID: "golocation",
			Reconnect: true,
		},
	}
	wantProvider := stubProvider{}
	host := &stubHost{}

	var gotOpts provider.Options

	var factory provider.Factory = func(
		opts provider.Options,
		host provider.Host,
	) error {
		gotOpts = opts

		host.Attach(wantProvider)

		return nil
	}

	err := factory(wantOpts, host)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	if gotOpts != wantOpts {
		t.Errorf("Options = %+v, want %+v", gotOpts, wantOpts)
	}

	if host.attached != provider.Provider(wantProvider) {
		t.Errorf("attached = %v, want %v", host.attached, wantProvider)
	}
}

// A Factory that fails must leave the host with nothing: a Provider attached
// alongside an error is one the caller would go on to Start.
func TestAFailedFactoryAttachesNothing(t *testing.T) {
	t.Parallel()

	host := &stubHost{}

	var factory provider.Factory = func(provider.Options, provider.Host) error {
		return errFactoryFailed
	}

	err := factory(provider.Options{}, host)
	if !errors.Is(err, errFactoryFailed) {
		t.Fatalf("error = %v, want %v", err, errFactoryFailed)
	}

	if host.attached != nil {
		t.Errorf("attached = %v, want nothing", host.attached)
	}
}
