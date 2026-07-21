package core

import (
	"context"
	"errors"
	"strings"
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
	stopped  int   `exhaustruct:"optional"`
	started  int   `exhaustruct:"optional"`
	stopErr  error `exhaustruct:"optional"`
	startErr error `exhaustruct:"optional"`
	platform string
}

func (p *fakeProvider) Start(context.Context) error {
	p.started++

	return p.startErr
}

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
	addErr     error `exhaustruct:"optional"`
	addOnceErr error `exhaustruct:"optional"`
	done       chan struct{}

	// Only errors are recorded: the drop-before-broadcast rules are the whole
	// reason this stub exists, and fixes and statuses have a real hub to be
	// observed through.
	mu   sync.Mutex `exhaustruct:"optional"`
	errs []error    `exhaustruct:"optional"`
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

func (h *stubHub) BroadcastFix(geo.Fix)       {}
func (h *stubHub) BroadcastStatus(geo.Status) {}

func (h *stubHub) BroadcastError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.errs = append(h.errs, err)
}

func (h *stubHub) Done() <-chan struct{} { return h.done }
func (h *stubHub) Close()                { close(h.done) }

// errorCount reports how many errors were broadcast.
func (h *stubHub) errorCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.errs)
}

var _ fanout.Broadcaster = (*stubHub)(nil)

// testConfig holds the fixed values this package's tests share. A function
// returns it rather than a package-level variable holding it, so the package
// carries no global state; the value is constant, so every call is equal.
type testConfig struct {
	epoch time.Time
}

func config() *testConfig {
	return &testConfig{epoch: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
}

// testPlatform is the platform name the fake provider reports, and what an
// annotated error is expected to carry through.
const testPlatform = "test"

// Failures the fakes are told to return. They are values rather than literals
// so a test can assert the exact error travelled through, not just that some
// error did.
var (
	errProviderExploded = errors.New("provider exploded")
	errStopFailed       = errors.New("stop failed")
	errStartFailed      = errors.New("start failed")
	errHubRefusedWaiter = errors.New("hub refused the waiter")
	errHubRefusedSub    = errors.New("hub refused the subscription")
	errLateFailure      = errors.New("late failure")
)

// The service runs on a clock the test drives, so anything depending on
// elapsed time is decided by advancing it rather than by sleeping.
func newTestService(t *testing.T) (*Service, *fakeProvider, *fixedclock.Clock) {
	t.Helper()

	epoch := config().epoch
	stopped := fixedclock.New(epoch)
	service := New(Options{
		MaximumAge:           time.Minute,
		DefaultChannelBuffer: 4,
		DefaultDropPolicy:    fanout.DropOldest,
	}, Features{Clock: stopped})
	native := &fakeProvider{platform: testPlatform}
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

// testHub returns the default hub. The registration assertions below have to
// see inside it, and the field is typed as the port.
func testHub(t *testing.T, service *Service) *chanhub.Hub {
	t.Helper()

	hub, ok := service.hub.(*chanhub.Hub)
	if !ok {
		t.Fatalf("default hub is %T, want *chanhub.Hub", service.hub)
	}

	return hub
}

// awaitWaiter blocks until a one-shot waiter is registered, so a test can
// publish exactly once and know the waiter will see it. Polling the registry
// beats sleeping and hoping the goroutine got there.
func awaitWaiter(t *testing.T, service *Service) {
	t.Helper()

	hub := testHub(t, service)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, waiters := hub.Counts(); waiters > 0 {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("no waiter registered")
}

// awaitNoSubscriptions blocks until the hub has let go of every subscription.
func awaitNoSubscriptions(t *testing.T, service *Service) {
	t.Helper()

	hub := testHub(t, service)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if subscriptions, _ := hub.Counts(); subscriptions == 0 {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("the subscription stayed registered after its context ended")
}

// subscribe registers a subscription the Service is expected to accept, scoped
// to the test's own context. A refusal is never the thing under test here — the
// two tests that do exercise one call Subscribe directly.
func subscribe(
	t *testing.T,
	service *Service,
	cfg fanout.SubscriptionConfig,
) fanout.Subscription {
	t.Helper()

	sub, err := service.Subscribe(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	return sub
}

// closeService closes the Service, failing the test if teardown reports one.
func closeService(t *testing.T, what string, service *Service) {
	t.Helper()

	err := service.Close()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// expectClosedErr fails unless err reports a closed Service.
func expectClosedErr(t *testing.T, op string, err error) {
	t.Helper()

	if !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("%s after Close = %v, want ErrClosed", op, err)
	}
}

// nextResult is what a background Next returned.
type nextResult struct {
	fix geo.Fix
	err error
}

// nextInBackground calls Next on its own goroutine, so the test can publish
// while it is blocked. The single-slot channel means the goroutine never
// outlives the test waiting to hand its result over.
func nextInBackground(
	ctx context.Context,
	t *testing.T,
	service *Service,
) <-chan nextResult {
	t.Helper()

	results := make(chan nextResult, 1)

	go func() {
		fix, err := service.Next(ctx)
		results <- nextResult{fix: fix, err: err}
	}()

	return results
}

// awaitNext waits for a background Next to come back.
func awaitNext(t *testing.T, results <-chan nextResult) nextResult {
	t.Helper()

	// t.Fatal ends the test, so the value below is never really returned; it
	// exists because the compiler cannot know that.
	var none nextResult

	select {
	case got := <-results:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not return")

		return none
	}
}

// awaitError waits for the subscriber's next error.
func awaitError(t *testing.T, errs <-chan error) error {
	t.Helper()

	select {
	case err := <-errs:
		return err
	case <-time.After(time.Second):
		t.Fatal("no error reached the subscriber")

		return nil
	}
}

// awaitStatus waits for the subscriber's next status.
func awaitStatus(t *testing.T, statuses <-chan geo.Status) geo.Status {
	t.Helper()

	var none geo.Status

	select {
	case status := <-statuses:
		return status
	case <-time.After(time.Second):
		t.Fatal("no status reached the subscriber")

		return none
	}
}

// takeFix returns the fix already waiting on the subscription, rather than
// waiting for one to arrive: the tests using it assert on delivery that has
// already happened by the time they look.
func takeFix(t *testing.T, why string, fixes <-chan geo.Fix) geo.Fix {
	t.Helper()

	select {
	case fix := <-fixes:
		return fix
	default:
		t.Fatal(why)

		return geo.Fix{}
	}
}

// expectNoFix fails if the subscription has a fix waiting.
func expectNoFix(t *testing.T, why string, fixes <-chan geo.Fix) {
	t.Helper()

	select {
	case fix := <-fixes:
		t.Fatalf("%s: %+v", why, fix)
	default:
	}
}

// expectNoError fails if the subscription has an error waiting.
func expectNoError(t *testing.T, why string, errs <-chan error) {
	t.Helper()

	select {
	case err := <-errs:
		t.Fatalf("%s: %v", why, err)
	default:
	}
}

// expectNoStatus fails if another status arrives while we watch for one.
func expectNoStatus(t *testing.T, why string, statuses <-chan geo.Status) {
	t.Helper()

	select {
	case status := <-statuses:
		t.Fatalf("%s: %+v", why, status)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNextReturnsAFixPublishedAfterTheCall(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	service, _, _ := newTestService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Published before Next registers, so Next must not return it.
	service.PublishFix(sampleFixAt(epoch, 1, 1))

	results := nextInBackground(ctx, t, service)

	awaitWaiter(t, service)
	service.PublishFix(sampleFixAt(epoch, 2, 2))

	got := awaitNext(t, results)
	if got.err != nil {
		t.Fatalf("Next: %v", got.err)
	}

	if got.fix.Latitude != 2 {
		t.Fatalf("Next returned a fix from before the call: %+v", got.fix)
	}
}

func TestNextReturnsABroadcastError(t *testing.T) {
	t.Parallel()

	service, _, _ := newTestService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	want := errProviderExploded
	results := nextInBackground(ctx, t, service)

	awaitWaiter(t, service)
	service.PublishError(want)

	if got := awaitNext(t, results); !errors.Is(got.err, want) {
		t.Fatalf("Next error = %v, want %v", got.err, want)
	}
}

func TestNextHonoursItsContext(t *testing.T) {
	t.Parallel()

	service, _, _ := newTestService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := service.Next(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next error = %v, want DeadlineExceeded", err)
	}
}

// A one-shot waiter that is not unregistered would accumulate in the hub for
// the life of the locator, which is what makes Next's deferred RemoveOnce load
// bearing rather than tidy.
func TestNextLeavesNoWaiterBehind(t *testing.T) {
	t.Parallel()

	service, _, _ := newTestService(t)

	for range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		_, _ = service.Next(ctx)

		cancel()
	}

	if _, waiters := testHub(t, service).Counts(); waiters != 0 {
		t.Fatalf("waiters left registered = %d, want 0", waiters)
	}
}

func TestCurrentServesTheCachedFixWhileItIsFresh(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
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
	t.Parallel()

	epoch := config().epoch
	service, _, stopped := newTestService(t)
	service.PublishFix(sampleFixAt(epoch, 51.5, -0.12))

	// Past MaximumAge, so the cached fix is no longer servable.
	stopped.Advance(2 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := service.Current(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Current error = %v, want DeadlineExceeded", err)
	}
}

func TestAStaleSampleIsRejectedRatherThanCached(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	service, _, _ := newTestService(t)

	sub := subscribe(t, service, fanout.SubscriptionConfig{Buffer: 2})

	service.PublishFix(sampleFixAt(epoch.Add(-time.Hour), 51.5, -0.12))

	err := awaitError(t, sub.Errors)
	if !errors.Is(err, geo.ErrStaleFix) {
		t.Fatalf("error = %v, want ErrStaleFix", err)
	}

	if fix, ok := service.Last(); ok {
		t.Fatalf("a stale fix was cached: %+v", fix)
	}
}

func TestPublishFixRejectsAnUnusableSampleWithThePlatformAnnotated(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	service, _, _ := newTestService(t)

	sub := subscribe(t, service, fanout.SubscriptionConfig{Buffer: 2})

	service.PublishFix(sampleFixAt(epoch, 91, 0)) // latitude out of range

	err := awaitError(t, sub.Errors)

	var annotated *geo.Error
	if !errors.As(err, &annotated) {
		t.Fatalf("error = %v, want a *geo.Error", err)
	}

	if annotated.Platform != testPlatform {
		t.Fatalf("platform = %q, want %q", annotated.Platform, testPlatform)
	}

	expectNoFix(t, "an invalid fix reached the subscriber", sub.Locations)
}

func TestAnUnstampedFixIsStampedFromTheClock(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
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
	t.Parallel()

	epoch := config().epoch
	service, _, _ := newTestService(t)

	sub := subscribe(t, service, fanout.SubscriptionConfig{Buffer: 8})
	// Drain the priming status so only broadcasts remain.
	<-sub.Statuses

	service.PublishFix(sampleFixAt(epoch, 10, 10))

	status := awaitStatus(t, sub.Statuses)
	if status.State != geo.StateReady {
		t.Fatalf("state = %v, want StateReady", status.State)
	}

	if status.Permission != geo.PermissionGranted {
		t.Fatalf("permission = %v, want PermissionGranted", status.Permission)
	}

	service.PublishFix(sampleFixAt(epoch, 20, 20))
	expectNoStatus(t, "readiness was announced twice", sub.Statuses)
}

func TestSubscribeReplaysTheLatestFixOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
	service, _, _ := newTestService(t)

	service.PublishFix(sampleFixAt(epoch, 1, 2))

	replaying := subscribe(
		t, service,
		fanout.SubscriptionConfig{Buffer: 2, ReplayLatest: true},
	)

	replayed := takeFix(t, "ReplayLatest delivered nothing", replaying.Locations)
	if replayed.Latitude != 1 {
		t.Fatalf("replayed %v, want 1", replayed.Latitude)
	}

	plain := subscribe(t, service, fanout.SubscriptionConfig{Buffer: 2})
	expectNoFix(t, "a plain subscription replayed a fix", plain.Locations)
}

func TestEndingASubscriptionContextClosesItAndFreesTheRegistration(t *testing.T) {
	t.Parallel()

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

	awaitNoSubscriptions(t, service)
}

func TestCloseStopsTheProviderOnceAndRejectsLaterCalls(t *testing.T) {
	t.Parallel()

	service, native, _ := newTestService(t)

	sub := subscribe(t, service, fanout.SubscriptionConfig{Buffer: 2})

	closeService(t, "Close", service)
	closeService(t, "second Close", service)

	if native.stopped != 1 {
		t.Fatalf("provider stopped %d times, want 1", native.stopped)
	}

	for range sub.Locations {
		t.Fatal("a closed subscription yielded a fix")
	}

	_, err := service.Next(context.Background())
	expectClosedErr(t, "Next", err)

	_, err = service.Subscribe(context.Background(), fanout.SubscriptionConfig{})
	expectClosedErr(t, "Subscribe", err)

	if service.Status().State != geo.StateClosed {
		t.Fatalf("state after Close = %v, want StateClosed", service.Status().State)
	}
}

// Close reports the provider's failure rather than swallowing it, so a caller
// deferring Close still learns that teardown went wrong.
func TestCloseReportsTheProviderStopError(t *testing.T) {
	t.Parallel()

	stopped := fixedclock.New(config().epoch)
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: stopped},
	)
	want := errStopFailed
	service.Attach(&fakeProvider{platform: testPlatform, stopErr: want})

	err := service.Close()
	if !errors.Is(err, want) {
		t.Fatalf("Close = %v, want %v", err, want)
	}
}

func TestCloseUnblocksNext(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	service, _, _ := newTestService(t)

	cases := map[string]fanout.SubscriptionConfig{
		"negative buffer":     {Buffer: -1},
		"unknown drop policy": {DropPolicy: fanout.DropNewest + 1},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := service.Subscribe(context.Background(), cfg)
			if !errors.Is(err, geo.ErrInvalidConfig) {
				t.Fatalf("Subscribe = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestSubscribeAppliesTheConfiguredDefaults(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	service, _, _ := newTestService(t)

	// A nil Context is exactly what is under test, but it is also what every
	// linter forbids writing inline. Holding it in a variable keeps the call
	// sites honest without a suppression on each one.
	var nilCtx context.Context

	_, err := service.Current(nilCtx)
	if !errors.Is(err, geo.ErrInvalidConfig) {
		t.Errorf("Current(nil) = %v, want ErrInvalidConfig", err)
	}

	_, err = service.Next(nilCtx)
	if !errors.Is(err, geo.ErrInvalidConfig) {
		t.Errorf("Next(nil) = %v, want ErrInvalidConfig", err)
	}

	_, err = service.Subscribe(nilCtx, fanout.SubscriptionConfig{})
	if !errors.Is(err, geo.ErrInvalidConfig) {
		t.Errorf("Subscribe(nil) = %v, want ErrInvalidConfig", err)
	}
}

// Production passes a zero Features and relies on New to fill in every
// adapter. A nil left behind would not fail until the first publish, on a
// native callback thread, as a panic.
func TestAZeroFeaturesGetsEveryDefaultAdapter(t *testing.T) {
	t.Parallel()

	service := New(Options{}, Features{})

	t.Cleanup(func() { _ = service.Close() })

	for name, feature := range map[string]any{
		"clock":     service.clock,
		"gate":      service.gate,
		"hub":       service.hub,
		"cache":     service.cache,
		"lifecycle": service.lifecycle,
	} {
		if feature == nil {
			t.Errorf("%s was left nil", name)
		}
	}

	expectInitialStatus(t, service.Status())
}

// expectInitialStatus fails unless status is the one New starts a Service on.
func expectInitialStatus(t *testing.T, status geo.Status) {
	t.Helper()

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
	t.Parallel()

	service, _, _ := newTestService(t)
	service.PublishFix(sampleFixAt(config().epoch, 1, 2))

	err := service.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A cached fix is present and fresh, so this proves the closed check runs
	// ahead of the cache rather than serving a fix from a dead locator.
	_, err = service.Current(context.Background())
	if !errors.Is(err, geo.ErrClosed) {
		t.Fatalf("Current after Close = %v, want ErrClosed", err)
	}
}

func TestCapabilitiesComeFromTheAttachedProvider(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	hub := newStubHub()
	want := errHubRefusedWaiter
	hub.addOnceErr = want
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(config().epoch), Hub: hub},
	)

	_, err := service.Next(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("Next = %v, want %v", err, want)
	}
}

func TestSubscribeReportsAFailedRegistration(t *testing.T) {
	t.Parallel()

	hub := newStubHub()
	want := errHubRefusedSub
	hub.addErr = want
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(config().epoch), Hub: hub},
	)

	_, err := service.Subscribe(context.Background(), fanout.SubscriptionConfig{Buffer: 1})
	if !errors.Is(err, want) {
		t.Fatalf("Subscribe = %v, want %v", err, want)
	}
}

// A provider can call back one more time after Stop returns, so a publish
// arriving after Close must not disturb what the last live session cached.
func TestPublishFixAfterCloseLeavesTheCacheAlone(t *testing.T) {
	t.Parallel()

	epoch := config().epoch
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
	t.Parallel()

	epoch := config().epoch
	stopped := fixedclock.New(epoch)
	service := New(Options{
		MinimumInterval:      time.Minute,
		MaximumAge:           time.Hour,
		DefaultChannelBuffer: 4,
		DefaultDropPolicy:    fanout.DropOldest,
	}, Features{Clock: stopped})
	service.Attach(&fakeProvider{platform: testPlatform})
	t.Cleanup(func() { _ = service.Close() })

	sub := subscribe(t, service, fanout.SubscriptionConfig{Buffer: 4})

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

	expectNoFix(t, "a declined fix was broadcast", sub.Locations)
	expectNoError(t, "a declined fix reported an error", sub.Errors)
}

func TestPublishErrorDropsNilAndAnythingAfterClose(t *testing.T) {
	t.Parallel()

	hub := newStubHub()
	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(config().epoch), Hub: hub},
	)

	service.PublishError(nil)

	if errs := hub.errorCount(); errs != 0 {
		t.Fatalf("a nil error was broadcast %d times, want 0", errs)
	}

	err := service.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	service.PublishError(errLateFailure)

	if errs := hub.errorCount(); errs != 0 {
		t.Fatalf("an error after Close was broadcast %d times, want 0", errs)
	}
}

// The closing status is published from inside Close, after s.closed is set, so
// StateClosed has to be exempt from the drop — otherwise the locator's own
// shutdown would be the one status nobody hears.
func TestPublishStatusAfterCloseIsDroppedExceptForTheClosingOne(t *testing.T) {
	t.Parallel()

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

func TestStartStartsTheAttachedProvider(t *testing.T) {
	t.Parallel()

	service, native, _ := newTestService(t)

	err := service.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if native.started != 1 {
		t.Fatalf("provider started %d times, want 1", native.started)
	}
}

// Attach is the only thing that gives the Service something to start, so a
// Service that was never attached has to say so rather than panic on a nil
// provider.
func TestStartWithoutAProviderReportsAnInvalidConfig(t *testing.T) {
	t.Parallel()

	service := New(
		Options{MaximumAge: time.Minute, DefaultChannelBuffer: 1},
		Features{Clock: fixedclock.New(config().epoch)},
	)

	t.Cleanup(func() { _ = service.Close() })

	err := service.Start(context.Background())
	if !errors.Is(err, geo.ErrInvalidConfig) {
		t.Fatalf("Start with no provider = %v, want ErrInvalidConfig", err)
	}
}

// The platform name is read at Attach precisely so a startup failure can be
// attributed without touching the adapter again.
func TestStartAnnotatesTheProviderFailureWithThePlatform(t *testing.T) {
	t.Parallel()

	service, native, _ := newTestService(t)
	native.startErr = errStartFailed

	err := service.Start(context.Background())
	if !errors.Is(err, errStartFailed) {
		t.Fatalf("Start = %v, want %v", err, errStartFailed)
	}

	if !strings.Contains(err.Error(), testPlatform) {
		t.Fatalf("Start error %q does not name the %q platform", err, testPlatform)
	}
}
