package location

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/core"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// fakeProvider is an operating system that does whatever the test says. The
// provider.Factory port is what lets one reach Open at all.
type fakeProvider struct {
	startErr error
	sink     provider.Sink

	mu       sync.Mutex
	started  int
	stopped  int
	startCtx context.Context
}

func (p *fakeProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	p.started++
	p.startCtx = ctx
	p.mu.Unlock()

	return p.startErr
}

func (p *fakeProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopped++

	return nil
}

func (p *fakeProvider) Platform() string { return "fake" }
func (p *fakeProvider) Capabilities() geo.Capabilities {
	return geo.Capabilities{Altitude: true, Speed: true, Heading: true}
}

func (p *fakeProvider) counts() (started, stopped int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.started, p.stopped
}

func factoryFor(p *fakeProvider, err error) provider.Factory {
	return provider.FactoryFunc(
		func(_ provider.Options, sink provider.Sink) (provider.Provider, error) {
			if err != nil {
				return nil, err
			}

			p.sink = sink

			return p, nil
		},
	)
}

func TestOpenStartsTheProviderAndReturnsAWorkingLocator(t *testing.T) {
	native := &fakeProvider{}

	loc, err := open(context.Background(), Config{}, factoryFor(native, nil), core.Features{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = loc.Close() })

	if started, _ := native.counts(); started != 1 {
		t.Fatalf("provider started %d times, want 1", started)
	}

	if !loc.Capabilities().Heading {
		t.Error("capabilities were not read from the attached provider")
	}

	if loc.Status().State != geo.StateStarting {
		t.Errorf("state = %v, want StateStarting before any fix", loc.Status().State)
	}

	// The locator is wired to the provider's sink, so a fix published by the
	// operating system comes back out of the public API.
	native.sink.PublishFix(geo.Fix{Latitude: 51.5, Longitude: -0.12, AccuracyMeters: 10})

	fix, ok := loc.Last()
	if !ok || fix.Latitude != 51.5 {
		t.Fatalf("Last = %+v, %v; want the published fix", fix, ok)
	}

	if loc.Status().State != geo.StateReady {
		t.Errorf("state = %v, want StateReady after a fix", loc.Status().State)
	}
}

// A provider that starts and then fails must not leak: Open tears the whole
// service down rather than handing back a half-built locator.
func TestOpenClosesTheServiceWhenStartFails(t *testing.T) {
	want := errors.New("no location service")
	native := &fakeProvider{startErr: want}

	loc, err := open(context.Background(), Config{}, factoryFor(native, nil), core.Features{})
	if !errors.Is(err, want) {
		t.Fatalf("open error = %v, want %v", err, want)
	}

	if loc != nil {
		t.Fatal("open returned a locator alongside an error")
	}

	if _, stopped := native.counts(); stopped != 1 {
		t.Fatalf("provider stopped %d times, want 1", stopped)
	}
}

func TestOpenReportsAFactoryFailure(t *testing.T) {
	want := geo.Wrap("fake", "open", geo.ErrUnsupported, false)

	loc, err := open(context.Background(), Config{}, factoryFor(nil, want), core.Features{})
	if !errors.Is(err, geo.ErrUnsupported) {
		t.Fatalf("open error = %v, want ErrUnsupported", err)
	}

	if loc != nil {
		t.Fatal("open returned a locator alongside an error")
	}
}

func TestOpenBoundsStartupWithStartTimeout(t *testing.T) {
	native := &fakeProvider{}

	loc, err := open(
		context.Background(),
		Config{StartTimeout: time.Minute},
		factoryFor(native, nil),
		core.Features{},
	)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = loc.Close() })

	native.mu.Lock()
	startCtx := native.startCtx
	native.mu.Unlock()

	deadline, ok := startCtx.Deadline()
	if !ok {
		t.Fatal("Start received a context with no deadline")
	}

	if remaining := time.Until(deadline); remaining > time.Minute {
		t.Fatalf("deadline is %v away, want at most the configured minute", remaining)
	}
}

func TestOpenRejectsAnInvalidConfigBeforeTouchingTheProvider(t *testing.T) {
	native := &fakeProvider{}

	_, err := open(
		context.Background(),
		Config{MinimumDistanceMeters: -1},
		factoryFor(native, nil),
		core.Features{},
	)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("open error = %v, want ErrInvalidConfig", err)
	}

	if started, _ := native.counts(); started != 0 {
		t.Fatal("an invalid config still reached the provider")
	}
}

func TestOpenRejectsANilContext(t *testing.T) {
	// A nil Context is exactly what is under test, but it is also what every
	// linter forbids writing inline.
	var nilCtx context.Context

	if _, err := Open(nilCtx, DefaultConfig()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Open(nil) = %v, want ErrInvalidConfig", err)
	}
}

// The re-exported names must stay wired to the features that define them,
// otherwise a caller's DropOldest and the hub's DropOldest could drift apart.
func TestReExportedConstantsMatchTheirFeature(t *testing.T) {
	var (
		_ Locator = (*core.Service)(nil)
		_         = Subscription{}
		_         = SubscriptionConfig{DropPolicy: DropOldest, Buffer: 1}
		_         = AccuracyNavigation
		_         = PermissionDoNotRequest
	)
	if DropDefault == DropOldest || DropOldest == DropNewest {
		t.Fatal("the drop policies collapsed onto one value")
	}
}
