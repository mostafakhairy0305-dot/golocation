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

// errNoLocationService is the startup failure a fake provider reports when a
// test wants Open to fail after the provider was already built.
var errNoLocationService = errors.New("no location service")

// fakeProvider is an operating system that does whatever the test says. The
// provider.Factory port is what lets one reach open at all.
type fakeProvider struct {
	startErr error         `exhaustruct:"optional"`
	stopErr  error         `exhaustruct:"optional"`
	sink     provider.Sink `exhaustruct:"optional"`

	mu      sync.Mutex `exhaustruct:"optional"`
	started int        `exhaustruct:"optional"`
	stopped int        `exhaustruct:"optional"`
	// What Start's context said about its deadline, rather than the context
	// itself: a Context outlives the call it was made for, and holding one in
	// a struct is what lets it be used past that point.
	startDeadline    time.Time `exhaustruct:"optional"`
	startHasDeadline bool      `exhaustruct:"optional"`
}

func (p *fakeProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	p.started++
	p.startDeadline, p.startHasDeadline = ctx.Deadline()
	p.mu.Unlock()

	return p.startErr
}

func (p *fakeProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopped++

	return p.stopErr
}

func (p *fakeProvider) Platform() string { return "fake" }
func (p *fakeProvider) Capabilities() geo.Capabilities {
	return geo.Capabilities{Altitude: true, Speed: true, Heading: true}
}

// counts reports how many times Start and Stop were called, in that order.
func (p *fakeProvider) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.started, p.stopped
}

// startedWithDeadline reports the deadline Start's context carried, and
// whether it had one at all.
func (p *fakeProvider) startedWithDeadline() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.startDeadline, p.startHasDeadline
}

// factoryFor is the operating system this package's tests are opened against:
// either one that attaches native, or one that refuses with err.
func factoryFor(native *fakeProvider, err error) provider.Factory {
	return func(_ provider.Options, host provider.Host) error {
		if err != nil {
			return err
		}

		native.sink = host
		host.Attach(native)

		return nil
	}
}

func TestOpenStartsTheProviderAndReturnsAWorkingLocator(t *testing.T) {
	t.Parallel()

	native := &fakeProvider{}

	loc, err := open(context.Background(), Config{}, factoryFor(native, nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = loc.Close() })

	expectFreshLocator(t, loc, native)

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

// expectFreshLocator fails unless the locator reflects a provider that has
// started but has not yet produced anything.
func expectFreshLocator(t *testing.T, loc Locator, native *fakeProvider) {
	t.Helper()

	if started, _ := native.counts(); started != 1 {
		t.Fatalf("provider started %d times, want 1", started)
	}

	if !loc.Capabilities().Heading {
		t.Error("capabilities were not read from the attached provider")
	}

	if loc.Status().State != geo.StateStarting {
		t.Errorf("state = %v, want StateStarting before any fix", loc.Status().State)
	}
}

// A provider that starts and then fails must not leak: Open tears the whole
// service down rather than handing back a half-built locator.
func TestOpenClosesTheServiceWhenStartFails(t *testing.T) {
	t.Parallel()

	want := errNoLocationService
	native := &fakeProvider{startErr: want}

	loc, err := open(context.Background(), Config{}, factoryFor(native, nil))
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
	t.Parallel()

	want := geo.Wrap("fake", "open", geo.ErrUnsupported, false)

	loc, err := open(context.Background(), Config{}, factoryFor(nil, want))
	if !errors.Is(err, geo.ErrUnsupported) {
		t.Fatalf("open error = %v, want ErrUnsupported", err)
	}

	if loc != nil {
		t.Fatal("open returned a locator alongside an error")
	}
}

func TestOpenBoundsStartupWithStartTimeout(t *testing.T) {
	t.Parallel()

	native := &fakeProvider{}

	loc, err := open(
		context.Background(),
		Config{StartTimeout: time.Minute},
		factoryFor(native, nil),
	)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = loc.Close() })

	deadline, ok := native.startedWithDeadline()
	if !ok {
		t.Fatal("Start received a context with no deadline")
	}

	if remaining := time.Until(deadline); remaining > time.Minute {
		t.Fatalf("deadline is %v away, want at most the configured minute", remaining)
	}
}

func TestOpenRejectsAnInvalidConfigBeforeTouchingTheProvider(t *testing.T) {
	t.Parallel()

	native := &fakeProvider{}

	_, err := open(
		context.Background(),
		Config{MinimumDistanceMeters: -1},
		factoryFor(native, nil),
	)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("open error = %v, want ErrInvalidConfig", err)
	}

	if started, _ := native.counts(); started != 0 {
		t.Fatal("an invalid config still reached the provider")
	}
}

func TestOpenRejectsANilContext(t *testing.T) {
	t.Parallel()

	// A nil Context is exactly what is under test, but it is also what every
	// linter forbids writing inline.
	var nilCtx context.Context

	_, err := Open(nilCtx, DefaultConfig())
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Open(nil) = %v, want ErrInvalidConfig", err)
	}
}

// The re-exported names must stay wired to the features that define them,
// otherwise a caller's DropOldest and the hub's DropOldest could drift apart.
func TestReExportedConstantsMatchTheirFeature(t *testing.T) {
	t.Parallel()

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
