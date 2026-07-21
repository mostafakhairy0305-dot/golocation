//go:build linux

package platform

import (
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/geoclue"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(opts provider.Options, sink provider.Sink) (provider.Provider, error) {
	return geoclue.New(geoclue.Options{
		Accuracy:              opts.Accuracy,
		DesiredAccuracyMeters: opts.DesiredAccuracyMeters,
		MinimumInterval:       opts.MinimumInterval,
		MinimumDistanceMeters: opts.MinimumDistanceMeters,
		DesktopID:             opts.Linux.DesktopID,
		Reconnect:             opts.Linux.Reconnect,
		ReconnectMin:          opts.Linux.ReconnectMin,
		ReconnectMax:          opts.Linux.ReconnectMax,
	}, sink)
}
