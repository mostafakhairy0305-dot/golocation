package port

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
)

// stubProvider is only ever compared by identity: the point of the test below
// is that FactoryFunc hands back exactly what the function returned.
type stubProvider struct{}

func (stubProvider) Start(context.Context) error    { return nil }
func (stubProvider) Stop() error                    { return nil }
func (stubProvider) Capabilities() geo.Capabilities { return geo.Capabilities{} }
func (stubProvider) Platform() string               { return "stub" }

type stubSink struct{}

func (stubSink) PublishFix(geo.Fix)       {}
func (stubSink) PublishStatus(geo.Status) {}
func (stubSink) PublishError(error)       {}

// The adapter is a one-line forwarder, and the failure it could hide is
// silent: dropping the Sink or substituting default Options would leave a
// provider that starts but never publishes anywhere the caller can see.
func TestFactoryFuncForwardsBothArgumentsAndBothResults(t *testing.T) {
	wantOpts := Options{
		Accuracy:              AccuracyNavigation,
		DesiredAccuracyMeters: 5,
		MinimumInterval:       2 * time.Second,
		MinimumDistanceMeters: 25,
		MaximumAge:            time.Minute,
		StartTimeout:          10 * time.Second,
		Permission:            PermissionDoNotRequest,
		Linux:                 LinuxOptions{DesktopID: "golocation", Reconnect: true},
	}
	wantSink := stubSink{}
	wantProvider := stubProvider{}
	wantErr := errors.New("factory failed")

	var (
		gotOpts Options
		gotSink Sink
		factory Factory = FactoryFunc(func(opts Options, sink Sink) (Provider, error) {
			gotOpts, gotSink = opts, sink

			return wantProvider, wantErr
		})
	)

	provider, err := factory.New(wantOpts, wantSink)

	if gotOpts != wantOpts {
		t.Errorf("Options = %+v, want %+v", gotOpts, wantOpts)
	}

	if gotSink != Sink(wantSink) {
		t.Errorf("Sink = %v, want %v", gotSink, wantSink)
	}

	if provider != Provider(wantProvider) {
		t.Errorf("Provider = %v, want %v", provider, wantProvider)
	}

	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}
