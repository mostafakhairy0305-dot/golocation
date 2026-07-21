package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/adapter/fixedclock"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/adapter/chanhub"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// fakeProvider stands in for an operating system. Attaching one is what gives
// the Service a platform name and capabilities; it never produces fixes of its
// own, because a test drives the Sink directly.
type fakeProvider struct {
	stopped  int
	stopErr  error
	platform string
}

func (p *fakeProvider) Start(context.Context) error { return nil }
func (p *fakeProvider) Stop() error {
	p.stopped++

	return p.stopErr
}
func (p *fakeProvider) Platform() string { return p.platform }
func (p *fakeProvider) Capabilities() geo.Capabilities {
	return geo.Capabilities{Altitude: true, Speed: true}
}

var _ provider.Provider = (*fakeProvider)(nil)

// stubHub fails registration on demand. A real closed chanhub cannot stand in
// here: Close sets s.closed, and every entry point short-circuits on that
// before it ever reaches the hub, so the registration-failed branches are
// unreachable through the real adapter.
type stubHub struct {
	addErr     error
	addOnceErr error
	done       chan struct{}

	mu       sync.Mutex
	fixes    []geo.Fix
	errs     []error
	statuses []geo.Status
}

func newStubHub() *stubHub { return &stubHub{done: make(chan struct{})} }

func (h *stubHub) Add(
	fanout.SubscriptionConfig,
	fanout.Priming,
) (uint64, fanout.Subscription, error) {
	if h.addErr != nil {
		return 0, fanout.Subscription{}, h.addErr
	}

	return 1, fanout.Subscription{}, nil
}
func (h *stubHub) Remove(uint64) {}
func (h *stubHub) AddOnce() (uint64, <-chan fanout.Event, error) {
	if h.addOnceErr != nil {
		return 0, nil, h.addOnceErr
	}

	return 1, make(chan fanout.Event, 1), nil
}
func (h *stubHub) RemoveOnce(uint64) {}

func (h *stubHub) BroadcastFix(fix geo.Fix) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.fixes = append(h.fixes, fix)
}

func (h *stubHub) BroadcastError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.errs = append(h.errs, err)
}

func (h *stubHub) BroadcastStatus(status geo.Status) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.statuses = append(h.statuses, status)
}

func (h *stubHub) Done() <-chan struct{} { return h.done }
func (h *stubHub) Close()                { close(h.done) }

func (h *stubHub) counts() (fixes, errs, statuses int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.fixes), len(h.errs), len(h.statuses)
}

var _ fanout.Broadcaster = (*stubHub)(nil)

var epoch = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// The service runs on a clock the test drives, so anything depending on
// elapsed time is decided by advancing it rather than by sleeping.
func newTestService(t *testing.T) (*Service, *fakeProvider, *fixedclock.Clock) {
	t.Helper()

	stopped := fixedclock.New(epoch)
	service := New(Options{
		MaximumAge:           time.Minute,
		DefaultChannelBuffer: 4,
		DefaultDropPolicy:    fanout.DropOldest,
	}, Features{Clock: stopped})
	native := &fakeProvider{platform: "test"}
	service.Attach(native)
	t.Cleanup(func() { _ = service.Close() })

	return service, native, stopped
}

func sampleFixAt(now time.Time, lat, lon float64) geo.Fix {
	return geo.Fix{
		Timestamp:      now,
		ReceivedAt:     now,
		Latitude:       lat,
		Longitude:      lon,
		AccuracyMeters: 10,
	}
}

// awaitWaiter blocks until a one-shot waiter is registered, so a test can
// publish exactly once and know the waiter will see it. Polling the registry
// beats sleeping and hoping the goroutine got there.
func awaitWaiter(t *testing.T, service *Service) {
	t.Helper()

	hub, ok := service.hub.(*chanhub.Hub)
	if !ok {
		t.Fatalf("default hub is %T, want *chanhub.Hub", service.hub)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, waiters := hub.Counts(); waiters > 0 {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("no waiter registered")
}

func TestNextReturnsAFixPublishedAfterTheCall(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Published before Next registers, so Next must not return it.
	service.PublishFix(sampleFixAt(epoch, 1, 1))

	result := make(chan geo.Fix, 1)

	go func() {
		fix, err := service.Next(ctx)
		if err != nil {
			t.Errorf("Next: %v", err)
			close(result)

			return
		}

		result <- fix
	}()

	awaitWaiter(t, service)
	service.PublishFix(sampleFixAt(epoch, 2, 2))

	select {
	case fix, ok := <-result:
		if !ok {
			t.Fatal("Next failed")
		}

		if fix.Latitude != 2 {
			t.Fatalf("Next returned a fix from before the call: %+v", fix)
		}
	case <-ctx.Done():
		t.Fatal("Next did not return")
	}
}

func TestNextReturnsABroadcastError(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	want := errors.New("provider exploded")
	result := make(chan error, 1)

	go func() {
		_, err := service.Next(ctx)
		result <- err
	}()

	awaitWaiter(t, service)
	service.PublishError(want)

	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("Next error = %v, want %v", err, want)
		}
	case <-ctx.Done():
		t.Fatal("Next did not return")
	}
}

func TestNextHonoursItsContext(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := service.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next error = %v, want DeadlineExceeded", err)
	}
}

// A one-shot waiter that is not unregistered would accumulate in the hub for
// the life of the locator, which is what makes Next's deferred RemoveOnce load
// bearing rather than tidy.
func TestNextLeavesNoWaiterBehind(t *testing.T) {
	service, _, _ := newTestService(t)

	for range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		_, _ = service.Next(ctx)

		cancel()
	}

	hub, ok := service.hub.(*chanhub.Hub)
	if !ok {
		t.Fatalf("default hub is %T, want *chanhub.Hub", service.hub)
	}

	if _, waiters := hub.Counts(); waiters != 0 {
		t.Fatalf("waiters left registered = %d, want 0", waiters)
	}
}

func TestCurrentServesTheCachedFixWhileItIsFresh(t *testing.T) {
	service, _, stopped := newTestService(t)
	want := sampleFixAt(epoch, 51.5, -0.12)
	service.PublishFix(want)

	// Still inside MaximumAge. No publisher is running, so a cache miss would
	// block until the context expires rather than return.
	stopped.Advance(30 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := service.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	if got.Latitude != want.Latitude || got.Longitude != want.Longitude {
		t.Fatalf(
			"Current = %v,%v want %v,%v",
			got.Latitude,
			got.Longitude,
			want.Latitude,
			want.Longitude,
		)
	}
}

func TestCurrentWaitsOnceTheCachedFixGoesStale(t *testing.T) {
	service, _, stopped := newTestService(t)
	service.PublishFix(sampleFixAt(epoch, 51.5, -0.12))

	// Past MaximumAge, so the cached fix is no longer servable.
	stopped.Advance(2 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if _, err := service.Current(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Current error = %v, want DeadlineExceeded", err)
	}
}

func TestAStaleSampleIsRejectedRatherThanCached(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx := t.Context()

	sub, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 2})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	service.PublishFix(sampleFixAt(epoch.Add(-time.Hour), 51.5, -0.12))

	select {
	case err := <-sub.Errors:
		if !errors.Is(err, geo.ErrStaleFix) {
			t.Fatalf("error = %v, want ErrStaleFix", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no error reached the subscriber")
	}

	if fix, ok := service.Last(); ok {
		t.Fatalf("a stale fix was cached: %+v", fix)
	}
}

func TestPublishFixRejectsAnUnusableSampleWithThePlatformAnnotated(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx := t.Context()

	sub, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 2})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	service.PublishFix(sampleFixAt(epoch, 91, 0)) // latitude out of range

	select {
	case err := <-sub.Errors:
		var annotated *geo.Error
		if !errors.As(err, &annotated) {
			t.Fatalf("error = %v, want a *geo.Error", err)
		}

		if annotated.Platform != "test" {
			t.Fatalf("platform = %q, want %q", annotated.Platform, "test")
		}
	case <-time.After(time.Second):
		t.Fatal("no error reached the subscriber")
	}

	select {
	case fix := <-sub.Locations:
		t.Fatalf("an invalid fix reached the subscriber: %+v", fix)
	default:
	}
}

func TestAnUnstampedFixIsStampedFromTheClock(t *testing.T) {
	service, _, _ := newTestService(t)
	service.PublishFix(geo.Fix{Latitude: 1, Longitude: 2, AccuracyMeters: 5})

	fix, ok := service.Last()
	if !ok {
		t.Fatal("an unstamped fix was not admitted")
	}

	if !fix.Timestamp.Equal(epoch) || !fix.ReceivedAt.Equal(epoch) {
		t.Fatalf("stamps = %v/%v, want both %v", fix.Timestamp, fix.ReceivedAt, epoch)
	}
}

func TestFirstAdmittedFixAnnouncesReadinessOnce(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx := t.Context()

	sub, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 8})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// Drain the priming status so only broadcasts remain.
	<-sub.Statuses

	service.PublishFix(sampleFixAt(epoch, 10, 10))

	select {
	case status := <-sub.Statuses:
		if status.State != geo.StateReady {
			t.Fatalf("state = %v, want StateReady", status.State)
		}

		if status.Permission != geo.PermissionGranted {
			t.Fatalf("permission = %v, want PermissionGranted", status.Permission)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness was never announced")
	}

	service.PublishFix(sampleFixAt(epoch, 20, 20))

	select {
	case status := <-sub.Statuses:
		t.Fatalf("readiness was announced twice: %+v", status)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSubscribeReplaysTheLatestFixOnlyWhenAsked(t *testing.T) {
	service, _, _ := newTestService(t)

	ctx := t.Context()

	service.PublishFix(sampleFixAt(epoch, 1, 2))

	replaying, err := service.Subscribe(
		ctx,
		fanout.SubscriptionConfig{Buffer: 2, ReplayLatest: true},
	)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	select {
	case fix := <-replaying.Locations:
		if fix.Latitude != 1 {
			t.Fatalf("replayed %v, want 1", fix.Latitude)
		}
	default:
		t.Fatal("ReplayLatest delivered nothing")
	}

	plain, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 2})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	select {
	case fix := <-plain.Locations:
		t.Fatalf("a plain subscription replayed %+v", fix)
	default:
	}
}

func TestEndingASubscriptionContextClosesItAndFreesTheRegistration(t *testing.T) {
	service, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())

	sub, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 2})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	cancel()

	for range sub.Locations {
		t.Fatal("a cancelled subscription yielded a fix")
	}

	hub := service.hub.(*chanhub.Hub)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if subscriptions, _ := hub.Counts(); subscriptions == 0 {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("the subscription stayed registered after its context ended")
}

func TestCloseStopsTheProviderOnceAndRejectsLaterCalls(t *testing.T) {
	service, native, _ := newTestService(t)

	ctx := t.Context()

	sub, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 2})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := service.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if native.stopped != 1 {
		t.Fatalf("provider stopped %d times, want 1", native.stopped)
	}

	for range sub.Locations {
		t.Fatal("a closed subscription yielded a fix")
	}

	if _, err := service.Next(context.Background()); !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("Next after Close = %v, want ErrClosed", err)
	}

	if _, err := service.Subscribe(
		context.Background(),
		fanout.SubscriptionConfig{},
	); !errors.Is(
		err,
		geo.ErrClosed,
	) {
		t.Fatalf("Subscribe after Close = %v, want ErrClosed", err)
	}

	if service.Status().State != geo.StateClosed {
		t.Fatalf("state after Close = %v, want StateClosed", service.Status().State)
	}
}

// Close reports the provider's failure rather than swallowing it, so a caller
// deferring Close still learns that teardown went wrong.
func TestCloseReportsTheProviderStopError(t *testing.T) {
	stopped := fixedclock.New(epoch)
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: stopped},
	)
	want := errors.New("stop failed")
	service.Attach(&fakeProvider{platform: "test", stopErr: want})

	err := service.Close()
	if !errors.Is(err, want) {
		t.Fatalf("Close = %v, want %v", err, want)
	}
}

func TestCloseUnblocksNext(t *testing.T) {
	service, _, _ := newTestService(t)
	result := make(chan error, 1)

	go func() {
		_, err := service.Next(context.Background())
		result <- err
	}()

	awaitWaiter(t, service)

	_ = service.Close()

	select {
	case err := <-result:
		if !errors.Is(err, geo.ErrClosed) {
			t.Fatalf("Next = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}
}

func TestSubscribeRejectsAnInvalidConfig(t *testing.T) {
	service, _, _ := newTestService(t)

	cases := map[string]fanout.SubscriptionConfig{
		"negative buffer":     {Buffer: -1},
		"unknown drop policy": {DropPolicy: fanout.DropNewest + 1},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Subscribe(
				context.Background(),
				cfg,
			); !errors.Is(
				err,
				geo.ErrInvalidConfig,
			) {
				t.Fatalf("Subscribe = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestSubscribeAppliesTheConfiguredDefaults(t *testing.T) {
	service, _, _ := newTestService(t)

	cfg, err := service.normalizeSubscription(fanout.SubscriptionConfig{})
	if err != nil {
		t.Fatalf("normalizeSubscription: %v", err)
	}

	if cfg.Buffer != 4 {
		t.Errorf("Buffer = %d, want the configured default 4", cfg.Buffer)
	}

	if cfg.DropPolicy != fanout.DropOldest {
		t.Errorf("DropPolicy = %v, want the configured default DropOldest", cfg.DropPolicy)
	}
}

func TestNilContextIsRejectedRatherThanPanicking(t *testing.T) {
	service, _, _ := newTestService(t)

	// A nil Context is exactly what is under test, but it is also what every
	// linter forbids writing inline. Holding it in a variable keeps the call
	// sites honest without a suppression on each one.
	var nilCtx context.Context

	if _, err := service.Current(nilCtx); !errors.Is(err, geo.ErrInvalidConfig) {
		t.Errorf("Current(nil) = %v, want ErrInvalidConfig", err)
	}

	if _, err := service.Next(nilCtx); !errors.Is(err, geo.ErrInvalidConfig) {
		t.Errorf("Next(nil) = %v, want ErrInvalidConfig", err)
	}

	if _, err := service.Subscribe(
		nilCtx,
		fanout.SubscriptionConfig{},
	); !errors.Is(
		err,
		geo.ErrInvalidConfig,
	) {
		t.Errorf("Subscribe(nil) = %v, want ErrInvalidConfig", err)
	}
}

// Production passes a zero Features and relies on New to fill in every
// adapter. A nil left behind would not fail until the first publish, on a
// native callback thread, as a panic.
func TestAZeroFeaturesGetsEveryDefaultAdapter(t *testing.T) {
	service := New(Options{}, Features{})

	t.Cleanup(func() { _ = service.Close() })

	if service.clock == nil {
		t.Error("clock was left nil")
	}

	if service.gate == nil {
		t.Error("gate was left nil")
	}

	if service.hub == nil {
		t.Error("hub was left nil")
	}

	if service.cache == nil {
		t.Error("cache was left nil")
	}

	if service.lifecycle == nil {
		t.Error("lifecycle was left nil")
	}

	status := service.Status()
	if status.State != geo.StateStarting {
		t.Errorf("state = %v, want StateStarting", status.State)
	}

	if status.Permission != geo.PermissionUnknown {
		t.Errorf("permission = %v, want PermissionUnknown", status.Permission)
	}

	if status.UpdatedAt.IsZero() {
		t.Error("the initial status was not stamped from the default clock")
	}
}

func TestCurrentAfterCloseReturnsErrClosed(t *testing.T) {
	service, _, _ := newTestService(t)
	service.PublishFix(sampleFixAt(epoch, 1, 2))

	err := service.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A cached fix is present and fresh, so this proves the closed check runs
	// ahead of the cache rather than serving a fix from a dead locator.
	if _, err := service.Current(context.Background()); !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("Current after Close = %v, want ErrClosed", err)
	}
}

func TestCapabilitiesComeFromTheAttachedProvider(t *testing.T) {
	service, native, _ := newTestService(t)

	got := service.Capabilities()

	want := native.Capabilities()
	if got != want {
		t.Fatalf("Capabilities = %+v, want %+v", got, want)
	}
}

// Registration can fail independently of the Service being closed — a hub
// shared with a torn-down peer, say — and Next must surface that rather than
// block on a channel nobody will ever send to.
func TestNextReportsAFailedWaiterRegistration(t *testing.T) {
	hub := newStubHub()
	want := errors.New("hub refused the waiter")
	hub.addOnceErr = want
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(epoch), Hub: hub},
	)

	if _, err := service.Next(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Next = %v, want %v", err, want)
	}
}

func TestSubscribeReportsAFailedRegistration(t *testing.T) {
	hub := newStubHub()
	want := errors.New("hub refused the subscription")
	hub.addErr = want
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(epoch), Hub: hub},
	)

	if _, err := service.Subscribe(
		context.Background(),
		fanout.SubscriptionConfig{Buffer: 1},
	); !errors.Is(
		err,
		want,
	) {
		t.Fatalf("Subscribe = %v, want %v", err, want)
	}
}

// A provider can call back one more time after Stop returns, so a publish
// arriving after Close must not disturb what the last live session cached.
func TestPublishFixAfterCloseLeavesTheCacheAlone(t *testing.T) {
	service, _, _ := newTestService(t)
	want := sampleFixAt(epoch, 51.5, -0.12)
	service.PublishFix(want)

	err := service.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	service.PublishFix(sampleFixAt(epoch, 40, 40))

	got, ok := service.Last()
	if !ok {
		t.Fatal("the cache was emptied by a publish after Close")
	}

	if got.Latitude != want.Latitude {
		t.Fatalf("cached latitude = %v, want the pre-Close %v", got.Latitude, want.Latitude)
	}
}

// A fix the gate declines without an error is a rate-limit decision, not a
// fault: nothing is broadcast, nothing is reported, and the previous fix stays
// servable. That silence is what distinguishes it from a rejection with an
// error.
func TestAFixDeclinedByTheGateLeavesThePreviousOneCached(t *testing.T) {
	stopped := fixedclock.New(epoch)
	service := New(Options{
		MinimumInterval:      time.Minute,
		MaximumAge:           time.Hour,
		DefaultChannelBuffer: 4,
		DefaultDropPolicy:    fanout.DropOldest,
	}, Features{Clock: stopped})
	service.Attach(&fakeProvider{platform: "test"})
	t.Cleanup(func() { _ = service.Close() })

	ctx := t.Context()

	sub, err := service.Subscribe(ctx, fanout.SubscriptionConfig{Buffer: 4})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	first := sampleFixAt(epoch, 51.5, -0.12)
	service.PublishFix(first)
	<-sub.Locations

	// One second later: valid, fresh, and well inside MinimumInterval.
	service.PublishFix(sampleFixAt(epoch.Add(time.Second), 48.85, 2.35))

	got, ok := service.Last()
	if !ok {
		t.Fatal("nothing is cached")
	}

	if got.Latitude != first.Latitude {
		t.Fatalf("cached latitude = %v, want the admitted %v", got.Latitude, first.Latitude)
	}

	select {
	case fix := <-sub.Locations:
		t.Fatalf("a declined fix was broadcast: %+v", fix)
	default:
	}

	select {
	case err := <-sub.Errors:
		t.Fatalf("a declined fix reported an error: %v", err)
	default:
	}
}

func TestPublishErrorDropsNilAndAnythingAfterClose(t *testing.T) {
	hub := newStubHub()
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(epoch), Hub: hub},
	)

	service.PublishError(nil)

	if _, errs, _ := hub.counts(); errs != 0 {
		t.Fatalf("a nil error was broadcast %d times, want 0", errs)
	}

	err := service.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	service.PublishError(errors.New("late failure"))

	if _, errs, _ := hub.counts(); errs != 0 {
		t.Fatalf("an error after Close was broadcast %d times, want 0", errs)
	}
}

// The closing status is published from inside Close, after s.closed is set, so
// StateClosed has to be exempt from the drop — otherwise the locator's own
// shutdown would be the one status nobody hears.
func TestPublishStatusAfterCloseIsDroppedExceptForTheClosingOne(t *testing.T) {
	service, _, _ := newTestService(t)

	err := service.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	service.PublishStatus(geo.Status{State: geo.StateReady, Message: "back from the dead"})

	if got := service.Status(); got.State != geo.StateClosed {
		t.Fatalf("state = %v, want it to stay StateClosed", got.State)
	}

	service.PublishStatus(geo.Status{State: geo.StateClosed, Message: "closed again"})

	if got := service.Status(); got.Message != "closed again" {
		t.Fatalf("message = %q, want the closing status to still be recorded", got.Message)
	}
}
