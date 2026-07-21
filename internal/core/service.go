// Package core is the application layer: it composes the features that turn
// the raw stream a Provider produces into the guarantees the public Locator
// promises. It knows nothing about any operating system, and holds no policy
// of its own — every decision belongs to a feature, and Service only sequences
// them.
package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	rules "github.com/mostafakhairy0305-dot/golocation/internal/feature/admission/adapter/rules"
	admission "github.com/mostafakhairy0305-dot/golocation/internal/feature/admission/port"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/adapter/systemclock"
	clock "github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/port"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/adapter/chanhub"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/fixcache/adapter/atomiccache"
	fixcache "github.com/mostafakhairy0305-dot/golocation/internal/feature/fixcache/port"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/lifecycle/adapter/atomicstate"
	lifecycle "github.com/mostafakhairy0305-dot/golocation/internal/feature/lifecycle/port"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// Options is what the core needs to run. It is deliberately narrower than the
// public Config: nothing platform-specific reaches this far in.
// Options mirrors the public Config in being all-optional: it arrives already
// normalized in production, and a test states only the knob it exercises.
type Options struct {
	MinimumInterval       time.Duration     `exhaustruct:"optional"`
	MinimumDistanceMeters float64           `exhaustruct:"optional"`
	MaximumAge            time.Duration     `exhaustruct:"optional"`
	DefaultChannelBuffer  int               `exhaustruct:"optional"`
	DefaultDropPolicy     fanout.DropPolicy `exhaustruct:"optional"`
}

// Features are the ports Service drives. New fills in the default adapter for
// every field left nil, so production code passes a zero value and a test
// substitutes only the feature it is exercising.
type Features struct {
	Gate      admission.Gate     `exhaustruct:"optional"`
	Hub       fanout.Broadcaster `exhaustruct:"optional"`
	Cache     fixcache.Cache     `exhaustruct:"optional"`
	Lifecycle lifecycle.Tracker  `exhaustruct:"optional"`
	Clock     clock.Clock        `exhaustruct:"optional"`
}

// withDefaults returns features with the default adapter filled in wherever the
// caller left one nil. It is split in two because the second half builds on the
// first: the lifecycle tracker needs a clock, so the clock must already be
// settled by the time we get there.
func (f Features) withDefaults(opts Options) Features {
	f = f.withPolicyDefaults(opts)

	return f.withPlumbingDefaults()
}

// withPolicyDefaults fills in the two features that answer to configuration:
// what time it is, and which samples are worth publishing.
func (f Features) withPolicyDefaults(opts Options) Features {
	if f.Clock == nil {
		f.Clock = systemclock.Clock{}
	}

	if f.Gate == nil {
		f.Gate = rules.New(admission.Rules{
			MinimumInterval:       opts.MinimumInterval,
			MinimumDistanceMeters: opts.MinimumDistanceMeters,
			MaximumAge:            opts.MaximumAge,
		})
	}

	return f
}

// withPlumbingDefaults fills in the three features that only hold state, and
// take no configuration beyond the clock already settled above.
func (f Features) withPlumbingDefaults() Features {
	if f.Hub == nil {
		f.Hub = chanhub.New()
	}

	if f.Cache == nil {
		f.Cache = atomiccache.New()
	}

	if f.Lifecycle == nil {
		f.Lifecycle = atomicstate.New(geo.Status{
			State:      geo.StateStarting,
			Permission: geo.PermissionUnknown,
		}, f.Clock)
	}

	return f
}

// Service is the application. It satisfies the public Locator for callers and
// provider.Host for the adapter.
type Service struct {
	maximumAge           time.Duration
	defaultChannelBuffer int
	defaultDropPolicy    fanout.DropPolicy

	gate      admission.Gate
	hub       fanout.Broadcaster
	cache     fixcache.Cache
	lifecycle lifecycle.Tracker
	clock     clock.Clock

	// Set once by Attach, before the provider starts, and only read after.
	native       provider.Provider `exhaustruct:"optional"`
	platform     string            `exhaustruct:"optional"`
	capabilities geo.Capabilities  `exhaustruct:"optional"`

	// Zero values are the usable ones.
	closed    atomic.Bool `exhaustruct:"optional"`
	closeOnce sync.Once   `exhaustruct:"optional"`
}

var _ provider.Host = (*Service)(nil)

// New builds the Service, substituting the default adapter for every feature
// left nil. It does not touch the operating system: the provider arrives
// separately through Attach.
func New(opts Options, features Features) *Service {
	features = features.withDefaults(opts)

	return &Service{
		maximumAge:           opts.MaximumAge,
		defaultChannelBuffer: opts.DefaultChannelBuffer,
		defaultDropPolicy:    opts.DefaultDropPolicy,
		gate:                 features.Gate,
		hub:                  features.Hub,
		cache:                features.Cache,
		lifecycle:            features.Lifecycle,
		clock:                features.Clock,
	}
}

// Attach binds the provider whose fixes this Service publishes. It must be
// called before Start and exactly once. The platform name and capabilities are
// read here rather than per call, so nothing on the hot path touches the
// adapter.
func (s *Service) Attach(native provider.Provider) {
	s.native = native
	s.platform = native.Platform()
	s.capabilities = native.Capabilities()
}

// Start starts the attached provider, bounded by ctx. The Service owns the
// provider's lifecycle from Attach onwards, which is what lets the factory that
// built it hand it over and forget it.
func (s *Service) Start(ctx context.Context) error {
	if s.native == nil {
		return fmt.Errorf("%w: no provider attached", geo.ErrInvalidConfig)
	}

	err := s.native.Start(ctx)
	if err != nil {
		return fmt.Errorf("start the %s provider: %w", s.platform, err)
	}

	return nil
}

// Current returns a sufficiently fresh cached fix, or waits for the next fix.
func (s *Service) Current(ctx context.Context) (geo.Fix, error) {
	if ctx == nil {
		return geo.Fix{}, fmt.Errorf("%w: nil context", geo.ErrInvalidConfig)
	}

	if s.closed.Load() {
		return geo.Fix{}, geo.ErrClosed
	}

	if fix, ok := s.cache.Load(); ok && geo.IsFresh(fix, s.maximumAge, s.clock.Now()) {
		return fix, nil
	}

	return s.Next(ctx)
}

// Next always waits for a fix accepted after the call starts.
func (s *Service) Next(ctx context.Context) (geo.Fix, error) {
	err := s.checkReady(ctx, "nil context")
	if err != nil {
		return geo.Fix{}, err
	}

	// A one-shot waiter, not a whole subscription. Next is what Current falls
	// back to, so it is the busiest path in the package, and a subscription
	// would cost three channels, a cancellable context, and a supervising
	// goroutine to deliver a single value. The waiter is one channel, and the
	// deferred RemoveOnce is the supervision.
	id, events, err := s.hub.AddOnce()
	if err != nil {
		return geo.Fix{}, fmt.Errorf("wait for the next fix: %w", err)
	}

	defer s.hub.RemoveOnce(id)

	return awaitEvent(ctx, events)
}

// awaitEvent resolves a one-shot waiter: the first fix, the first error, a
// closed channel because the locator shut down, or the caller giving up.
func awaitEvent(ctx context.Context, events <-chan fanout.Event) (geo.Fix, error) {
	select {
	case event, ok := <-events:
		if !ok {
			return geo.Fix{}, geo.ErrClosed
		}

		if event.Err != nil {
			return geo.Fix{}, event.Err
		}

		return event.Fix, nil
	case <-ctx.Done():
		return geo.Fix{}, fmt.Errorf("wait for the next fix: %w", ctx.Err())
	}
}

// Last returns the most recent accepted fix without blocking.
func (s *Service) Last() (geo.Fix, bool) { return s.cache.Load() }

// Subscribe creates an independent real-time stream.
func (s *Service) Subscribe(
	ctx context.Context,
	config fanout.SubscriptionConfig,
) (fanout.Subscription, error) {
	err := s.checkReady(ctx, "nil subscription context")
	if err != nil {
		return fanout.Subscription{}, err
	}

	cfg, err := s.normalizeSubscription(config)
	if err != nil {
		return fanout.Subscription{}, err
	}

	priming := fanout.Priming{Status: s.lifecycle.Get()}
	if cfg.ReplayLatest {
		priming.Fix, priming.HasFix = s.cache.Load()
	}

	id, subscription, err := s.hub.Add(cfg, priming)
	if err != nil {
		return fanout.Subscription{}, fmt.Errorf("register the subscription: %w", err)
	}

	s.watchSubscription(ctx, id)

	return subscription, nil
}

// Status returns the latest service and permission snapshot.
func (s *Service) Status() geo.Status { return s.lifecycle.Get() }

// Capabilities reports the optional fields the attached provider can supply.
// It is fixed at Attach, so it is safe to call from any goroutine.
func (s *Service) Capabilities() geo.Capabilities { return s.capabilities }

// Close stops the provider and tears down every subscription. It is idempotent
// and returns the provider's stop error, if any.
func (s *Service) Close() error {
	var stopErr error

	s.closeOnce.Do(func() {
		s.closed.Store(true)

		if s.native != nil {
			err := s.native.Stop()
			if err != nil {
				stopErr = fmt.Errorf("stop the %s provider: %w", s.platform, err)
			}
		}
		// The tracker carries the permission forward, so the closing status
		// still reports whatever access the session had.
		s.PublishStatus(geo.Status{State: geo.StateClosed, Message: "closed"})
		s.hub.Close()
	})

	return stopErr
}

// PublishFix admits a provider sample and fans it out. It is the adapter's
// entry point for location data, and runs on whichever thread the operating
// system calls back on.
func (s *Service) PublishFix(fix geo.Fix) {
	if s.closed.Load() {
		return
	}

	now := s.clock.Now()
	fix = stamp(fix, now)

	admitted, err := s.gate.Admit(fix, now)
	if err != nil {
		// The gate reports the cause; only here is the platform known.
		s.PublishError(geo.Wrap(s.platform, "admit fix", err, true))

		return
	}

	if !admitted {
		return
	}

	s.cache.Store(fix)
	s.announceReady()
	s.hub.BroadcastFix(fix)
}

// stamp fills the two times a provider may leave unset and normalises both to
// UTC. A fix with no timestamp of its own is taken as having been produced
// when it arrived.
func stamp(fix geo.Fix, now time.Time) geo.Fix {
	if fix.ReceivedAt.IsZero() {
		fix.ReceivedAt = now
	}

	if fix.Timestamp.IsZero() {
		fix.Timestamp = fix.ReceivedAt
	}

	fix.Timestamp = fix.Timestamp.UTC()
	fix.ReceivedAt = fix.ReceivedAt.UTC()

	return fix
}

// PublishError fans a native error out to every subscriber, and to any caller
// blocked in Next.
func (s *Service) PublishError(err error) {
	if err == nil || s.closed.Load() {
		return
	}

	s.hub.BroadcastError(err)
}

// PublishStatus records a status change and fans it out. Repeated identical
// statuses are recorded but not broadcast.
func (s *Service) PublishStatus(status geo.Status) {
	if s.closed.Load() && status.State != geo.StateClosed {
		return
	}

	if recorded, changed := s.lifecycle.Set(status); changed {
		s.hub.BroadcastStatus(recorded)
	}
}

// checkReady rejects the two conditions every entry point shares: a nil
// Context, which would panic the moment anything selected on its Done, and a
// locator that has already been closed. what names the context so the error
// says which argument was nil.
func (s *Service) checkReady(ctx context.Context, what string) error {
	if ctx == nil {
		return fmt.Errorf("%w: %s", geo.ErrInvalidConfig, what)
	}

	if s.closed.Load() {
		return geo.ErrClosed
	}

	return nil
}

// watchSubscription is one goroutine per subscription, ending at whichever
// comes first: the caller's context or the hub's shutdown. Only the former
// needs to unregister — a closing hub tears every subscription down itself.
func (s *Service) watchSubscription(ctx context.Context, id uint64) {
	go func() {
		select {
		case <-ctx.Done():
			s.hub.Remove(id)
		case <-s.hub.Done():
		}
	}()
}

// announceReady reports the first admitted fix as readiness, once. The state
// is read atomically, so the steady state — every fix after the first — never
// touches the status lock.
func (s *Service) announceReady() {
	if s.lifecycle.State() == geo.StateReady {
		return
	}

	status, changed := s.lifecycle.MarkReady("receiving locations")
	if changed {
		s.hub.BroadcastStatus(status)
	}
}

func (s *Service) normalizeSubscription(
	in fanout.SubscriptionConfig,
) (fanout.SubscriptionConfig, error) {
	out := in
	if out.Buffer == 0 {
		out.Buffer = s.defaultChannelBuffer
	}

	if out.Buffer < 1 {
		return fanout.SubscriptionConfig{}, fmt.Errorf(
			"%w: subscription buffer must be at least 1",
			geo.ErrInvalidConfig,
		)
	}

	if out.DropPolicy == fanout.DropDefault {
		out.DropPolicy = s.defaultDropPolicy
	}

	if out.DropPolicy > fanout.DropNewest {
		return fanout.SubscriptionConfig{}, fmt.Errorf(
			"%w: unknown subscription drop policy %d",
			geo.ErrInvalidConfig,
			out.DropPolicy,
		)
	}

	return out, nil
}
