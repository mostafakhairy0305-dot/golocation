//go:build windows && (amd64 || arm64)

package platform

import (
	"fmt"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/winrt"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(opts provider.Options, host provider.Host) error {
	native, err := winrt.New(winrt.Options{
		Accuracy:              opts.Accuracy,
		DesiredAccuracyMeters: opts.DesiredAccuracyMeters,
		MinimumInterval:       opts.MinimumInterval,
		MaximumAge:            opts.MaximumAge,
		StartTimeout:          opts.StartTimeout,
		Permission:            opts.Permission,
	}, host)
	if err != nil {
		return fmt.Errorf("select the windows provider: %w", err)
	}

	host.Attach(native)

	return nil
}
