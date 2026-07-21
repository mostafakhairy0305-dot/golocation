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
func Open(ctx context.Context, config Config) (Locator, error) {
	return open(ctx, config, platform.Factory{}, core.Features{})
}

// open is Open with its two dependencies made explicit: which operating system
// to ask, and which feature adapters to run on. Open supplies the real ones;
// a test supplies fakes, which is the only way to exercise start-up without a
// live location service.
func open(ctx context.Context, config Config, factory provider.Factory, features core.Features) (Locator, error) {
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
	}, features)

	native, err := factory.New(provider.Options{
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
		return nil, err
	}
	service.Attach(native)

	startCtx := ctx
	cancel := func() {}
	if cfg.StartTimeout > 0 {
		startCtx, cancel = context.WithTimeout(ctx, cfg.StartTimeout)
	}
	defer cancel()

	if err := native.Start(startCtx); err != nil {
		// Close runs the same teardown a caller would get, rather than a
		// hand-rolled subset that drifts. It is safe here because closeOnce
		// guards it and every adapter's Stop is idempotent.
		_ = service.Close()
		return nil, err
	}

	return service, nil
}
