//go:build windows && (amd64 || arm64)

package platform

import (
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/winrt"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(opts provider.Options, sink provider.Sink) (provider.Provider, error) {
	return winrt.New(winrt.Options{
		Accuracy:              opts.Accuracy,
		DesiredAccuracyMeters: opts.DesiredAccuracyMeters,
		MinimumInterval:       opts.MinimumInterval,
		MaximumAge:            opts.MaximumAge,
		StartTimeout:          opts.StartTimeout,
		Permission:            opts.Permission,
	}, sink)
}
