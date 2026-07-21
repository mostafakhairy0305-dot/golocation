//go:build darwin && (amd64 || arm64)

package platform

import (
	"github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/adapter/corelocation"
	provider "github.com/mostafakhairy0305-dot/golocation/internal/feature/provider/port"
)

func newProvider(opts provider.Options, sink provider.Sink) (provider.Provider, error) {
	return corelocation.New(corelocation.Options{
		Accuracy:              opts.Accuracy,
		DesiredAccuracyMeters: opts.DesiredAccuracyMeters,
		MinimumDistanceMeters: opts.MinimumDistanceMeters,
		Permission:            opts.Permission,
	}, sink)
}
