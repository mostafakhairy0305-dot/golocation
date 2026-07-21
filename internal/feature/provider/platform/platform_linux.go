//go:build linux

package platform

import (
	"fmt"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/geoclue"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(opts provider.Options, host provider.Host) error {
	native, err := geoclue.New(geoclue.Options{
		Accuracy:              opts.Accuracy,
		DesiredAccuracyMeters: opts.DesiredAccuracyMeters,
		MinimumInterval:       opts.MinimumInterval,
		MinimumDistanceMeters: opts.MinimumDistanceMeters,
		DesktopID:             opts.Linux.DesktopID,
		Reconnect:             opts.Linux.Reconnect,
		ReconnectMin:          opts.Linux.ReconnectMin,
		ReconnectMax:          opts.Linux.ReconnectMax,
	}, host)
	if err != nil {
		return fmt.Errorf("select the linux provider: %w", err)
	}

	host.Attach(native)

	return nil
}
