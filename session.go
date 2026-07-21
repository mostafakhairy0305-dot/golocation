package location

import (
	"context"
	"fmt"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/core"
	fanout "github.com/mostafakhairy0305-dot/golocation/internal/feature/fanout/port"
)

// Session is one open locator: the value Open returns, and the only
// implementation of [Locator] this package ships.
//
// Open returns this concrete type rather than the interface so that callers get
// something they can name, embed, and extend; Locator stays for the callers who
// want to accept any locator, including a fake of their own.
type Session struct {
	service *core.Service
}

var _ Locator = (*Session)(nil)

// Current returns a sufficiently fresh cached fix, or waits for the next fix.
func (s *Session) Current(ctx context.Context) (Fix, error) {
	fix, err := s.service.Current(ctx)
	if err != nil {
		return Fix{}, fmt.Errorf("current: %w", err)
	}

	return fix, nil
}

// Next always waits for a fix accepted after the call starts.
func (s *Session) Next(ctx context.Context) (Fix, error) {
	fix, err := s.service.Next(ctx)
	if err != nil {
		return Fix{}, fmt.Errorf("next: %w", err)
	}

	return fix, nil
}

// Last returns the most recent accepted fix without blocking.
func (s *Session) Last() (Fix, bool) { return s.service.Last() }

// Subscribe creates an independent real-time stream.
func (s *Session) Subscribe(
	ctx context.Context,
	config SubscriptionConfig,
) (Subscription, error) {
	subscription, err := s.service.Subscribe(ctx, config)
	if err != nil {
		return fanout.Subscription{}, fmt.Errorf("subscribe: %w", err)
	}

	return subscription, nil
}

// Status reports the current service and permission state.
func (s *Session) Status() geo.Status { return s.service.Status() }

// Capabilities reports which optional Fix fields the active provider can supply.
func (s *Session) Capabilities() geo.Capabilities { return s.service.Capabilities() }

// Close stops the provider and closes every subscription. The teardown runs
// once; Close reports the provider's stop error, if any, on every call.
func (s *Session) Close() error {
	err := s.service.Close()
	if err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return nil
}
