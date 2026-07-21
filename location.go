package location

import (
	"context"
	"fmt"

	"github.com/mostafakhairy0305-dot/golocation/geo"
	"github.com/mostafakhairy0305-dot/golocation/internal/core"
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/platform"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// Open starts the operating system's native location provider.
func Open(ctx context.Context, config Config) (*Session, error) {
	return open(ctx, config, platform.New)
}

// open is Open with its one dependency made explicit: which operating system
// to ask. Open supplies the real one; a test supplies a fake, which is the
// only way to exercise start-up without a live location service. The feature
// adapters are always the defaults — core.New fills in a zero Features — and
// a test that needs to substitute one drives core.Service directly.
func open(
	ctx context.Context,
	config Config,
	factory provider.Factory,
) (*Session, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", geo.ErrInvalidConfig)
	}

	cfg, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	service := core.New(core.Options{
		MinimumInterval:       cfg.MinimumInterval,
		MinimumDistanceMeters: cfg.MinimumDistanceMeters,
		MaximumAge:            cfg.MaximumAge,
		DefaultChannelBuffer:  cfg.DefaultChannelBuffer,
		DefaultDropPolicy:     cfg.DefaultDropPolicy,
	}, core.Features{})

	err = factory(provider.Options{
		Accuracy:              cfg.Accuracy,
		DesiredAccuracyMeters: cfg.DesiredAccuracyMeters,
		MinimumInterval:       cfg.MinimumInterval,
		MinimumDistanceMeters: cfg.MinimumDistanceMeters,
		MaximumAge:            cfg.MaximumAge,
		StartTimeout:          cfg.StartTimeout,
		Permission:            cfg.Permission,
		Linux: provider.LinuxOptions{
			DesktopID:    cfg.Linux.DesktopID,
			Reconnect:    cfg.Linux.Reconnect,
			ReconnectMin: cfg.Linux.ReconnectMin,
			ReconnectMax: cfg.Linux.ReconnectMax,
		},
	}, service)
	if err != nil {
		return nil, fmt.Errorf("open the native provider: %w", err)
	}

	err = startNative(ctx, cfg, service)
	if err != nil {
		// Close runs the same teardown a caller would get, rather than a
		// hand-rolled subset that drifts. It is safe here because closeOnce
		// guards it and every adapter's Stop is idempotent.
		_ = service.Close()

		return nil, err
	}

	return &Session{service: service}, nil
}

// startNative bounds provider startup — and the permission prompt it may put
// on screen — with the configured timeout. A zero timeout means the caller's
// own context is the only deadline.
func startNative(ctx context.Context, cfg Config, service *core.Service) error {
	startCtx := ctx

	cancel := func() {}
	if cfg.StartTimeout > 0 {
		startCtx, cancel = context.WithTimeout(ctx, cfg.StartTimeout)
	}

	defer cancel()

	err := service.Start(startCtx)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	return nil
}
