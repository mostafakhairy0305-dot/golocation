//go:build linux

package platform

import (
	"fmt"

	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/geoclue"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

// linuxConstructor is geoclue.New, named so a test can pass one that fails.
// Building the GeoClue adapter only records the options — nothing it can fail
// at yet — so the failure path is not a state a test can arrange for real.
type linuxConstructor func(geoclue.Options, provider.Sink) (*geoclue.Backend, error)

func newProvider(opts provider.Options, host provider.Host) error {
	return newProviderWith(geoclue.New, opts, host)
}

func newProviderWith(build linuxConstructor, opts provider.Options, host provider.Host) error {
	native, err := build(geoclue.Options{
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
