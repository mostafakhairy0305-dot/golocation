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
type Options struct {
	MinimumInterval       time.Duration
	MinimumDistanceMeters float64
	MaximumAge            time.Duration
	DefaultChannelBuffer  int
	DefaultDropPolicy     fanout.DropPolicy
}

// Features are the ports Service drives. New fills in the default adapter for
// every field left nil, so production code passes a zero value and a test
// substitutes only the feature it is exercising.
type Features struct {
	Gate      admission.Gate
	Hub       fanout.Broadcaster
	Cache     fixcache.Cache
	Lifecycle lifecycle.Tracker
	Clock     clock.Clock
}

// Service is the application. It satisfies the public Locator for callers and
// provider.Sink for the adapter.
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
	native       provider.Provider
	platform     string
	capabilities geo.Capabilities

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ provider.Sink = (*Service)(nil)

func New(opts Options, features Features) *Service {
	if features.Clock == nil {
		features.Clock = systemclock.Clock{}
	}

	if features.Gate == nil {
		features.Gate = rules.New(admission.Rules{
			MinimumInterval:       opts.MinimumInterval,
			MinimumDistanceMeters: opts.MinimumDistanceMeters,
			MaximumAge:            opts.MaximumAge,
		})
	}

	if features.Hub == nil {
		features.Hub = chanhub.New()
	}

	if features.Cache == nil {
		features.Cache = atomiccache.New()
	}

	if features.Lifecycle == nil {
		features.Lifecycle = atomicstate.New(geo.Status{
			State:      geo.StateStarting,
			Permission: geo.PermissionUnknown,
		}, features.Clock)
	}

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
	if ctx == nil {
		return geo.Fix{}, fmt.Errorf("%w: nil context", geo.ErrInvalidConfig)
	}

	if s.closed.Load() {
		return geo.Fix{}, geo.ErrClosed
	}

	// A one-shot waiter, not a whole subscription. Next is what Current falls
	// back to, so it is the busiest path in the package, and a subscription
	// would cost three channels, a cancellable context, and a supervising
	// goroutine to deliver a single value. The waiter is one channel, and the
	// deferred RemoveOnce is the supervision.
	id, events, err := s.hub.AddOnce()
	if err != nil {
		return geo.Fix{}, err
	}
	defer s.hub.RemoveOnce(id)

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
		return geo.Fix{}, ctx.Err()
	}
}

// Last returns the most recent accepted fix without blocking.
func (s *Service) Last() (geo.Fix, bool) { return s.cache.Load() }

// Subscribe creates an independent real-time stream.
func (s *Service) Subscribe(
	ctx context.Context,
	config fanout.SubscriptionConfig,
) (fanout.Subscription, error) {
	if ctx == nil {
		return fanout.Subscription{}, fmt.Errorf(
			"%w: nil subscription context",
			geo.ErrInvalidConfig,
		)
	}

	if s.closed.Load() {
		return fanout.Subscription{}, geo.ErrClosed
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
		return fanout.Subscription{}, err
	}

	// One goroutine per subscription, ending at whichever comes first: the
	// caller's context or the hub's shutdown.
	go func() {
		select {
		case <-ctx.Done():
			s.hub.Remove(id)
		case <-s.hub.Done():
		}
	}()

	return subscription, nil
}

func (s *Service) Status() geo.Status { return s.lifecycle.Get() }

func (s *Service) Capabilities() geo.Capabilities { return s.capabilities }

func (s *Service) Close() error {
	var stopErr error

	s.closeOnce.Do(func() {
		s.closed.Store(true)

		if s.native != nil {
			stopErr = s.native.Stop()
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
	if fix.ReceivedAt.IsZero() {
		fix.ReceivedAt = now
	}

	if fix.Timestamp.IsZero() {
		fix.Timestamp = fix.ReceivedAt
	}

	fix.Timestamp = fix.Timestamp.UTC()
	fix.ReceivedAt = fix.ReceivedAt.UTC()

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
	// An atomic state read, so the steady state — every fix after the first —
	// never touches the status lock.
	if s.lifecycle.State() != geo.StateReady {
		if status, changed := s.lifecycle.MarkReady("receiving locations"); changed {
			s.hub.BroadcastStatus(status)
		}
	}

	s.hub.BroadcastFix(fix)
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
